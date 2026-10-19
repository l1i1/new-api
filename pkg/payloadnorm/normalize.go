// Package payloadnorm moves a client's stable long block out of the last user message and
// into the system message before the request is relayed upstream.
//
// It exists because of a measured upstream behaviour, not a style preference: with a 315 KB
// document in the last user message, the same bytes hit the provider's prompt cache only
// 40-60% of the time (智谱 glm-5.3-flash, 300 requests / concurrency 20, measured on both
// the direct endpoint and through this relay), while the same content placed in the system
// message hits 99%+. The upstream cache is prefix based, so the fix is to make the stable
// part of the payload the prefix - which is what this package does.
//
// The rewrite is deliberately conservative and fail-closed:
//
//   - disabled unless an operator scopes it to explicit token IDs and/or channel IDs;
//   - fires only for a single-turn request whose two messages are [system, user] and whose
//     contents are plain strings (no tool calls, no multimodal parts);
//   - the user message must be at least MinUserChars long;
//   - an optional sha256 fingerprint of the user message's leading characters must match,
//     so a rule written for one workload cannot silently rewrite a different one.
//
// Moving content between roles is not a formatting change: system and user carry different
// authority, so the rewrite can change model output. That is why the scope is opt-in and
// narrow, and why the setting is called a prototype rather than a default.
package payloadnorm

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/dto"
)

const (
	// DefaultMinUserChars keeps the rewrite away from ordinary chat turns.
	DefaultMinUserChars = 100_000
	// DefaultFingerprintChars is how much of the user message the fingerprint covers.
	DefaultFingerprintChars = 4_096

	// systemSeparator joins the original system prompt and the moved block. Two newlines
	// keep the moved document from being glued onto the instruction's last line.
	systemSeparator = "\n\n"
)

// Config is the runtime scope of the rewrite. The zero value is disabled.
type Config struct {
	Enabled bool

	// TokenIDs and ChannelIDs are allowlists. If both are empty the rewrite never fires; a
	// non-empty list requires membership.
	TokenIDs   []int
	ChannelIDs []int

	// MinUserChars is the minimum length of the user message.
	MinUserChars int
	// FingerprintChars and FingerprintSHA256 identify the workload by the leading characters
	// of the user message. An empty digest disables the fingerprint check.
	FingerprintChars  int
	FingerprintSHA256 string
	// DocumentChars is where the stable block ends inside the user message. Zero (or a value
	// outside the message) moves the whole user message and keeps the original system text
	// as the user turn.
	DocumentChars int
}

// Decision reports whether the request was rewritten and why. Reason is meant for logs: the
// non-rewritten reasons explain a missing hit better than a bare boolean.
type Decision struct {
	Apply  bool
	Reason string
}

// Apply returns the request that should be relayed. When the rewrite does not apply it
// returns the input request unchanged, so callers can always use the result.
//
// The returned request is a shallow copy with a new Messages slice; the input is never
// mutated, because the caller may still need the client's original layout for logging.
func Apply(cfg Config, tokenID, channelID int, req *dto.GeneralOpenAIRequest) (*dto.GeneralOpenAIRequest, Decision) {
	reject := func(reason string) (*dto.GeneralOpenAIRequest, Decision) {
		return req, Decision{Reason: reason}
	}
	if req == nil {
		return nil, Decision{Reason: "nil request"}
	}
	if !cfg.Enabled {
		return reject("disabled")
	}
	if !cfg.scoped(tokenID, channelID) {
		return reject("token/channel not in scope")
	}
	if len(req.Messages) != 2 {
		return reject("not a single-turn request")
	}
	system, user := req.Messages[0], req.Messages[1]
	if system.Role != "system" || user.Role != "user" {
		return reject("unexpected role layout")
	}
	if !system.IsStringContent() || !user.IsStringContent() {
		return reject("non-string content")
	}
	if len(req.Tools) > 0 || req.ToolChoice != nil || req.FunctionCall != nil {
		return reject("tool request")
	}
	if len(system.ToolCalls) > 0 || len(user.ToolCalls) > 0 || user.ToolCallId != "" {
		return reject("tool message")
	}
	systemText, _ := system.Content.(string)
	userText, _ := user.Content.(string)
	if len(userText) < cfg.minUserChars() {
		return reject("user message below the size floor")
	}
	if !cfg.fingerprintMatches(userText) {
		return reject("fingerprint mismatch")
	}

	split := cfg.DocumentChars
	if split <= 0 || split >= len(userText) {
		// Whole-content mode: the entire user message becomes the system body and the
		// original system text becomes the user turn. No knowledge of where the stable
		// block ends is needed, which is what makes this mode usable for a first cut.
		out := cloneRequest(req)
		out.Messages[0].SetStringContent(joinSystem(systemText, userText))
		out.Messages[1].SetStringContent(systemText)
		return &out, Decision{Apply: true, Reason: "moved whole user message into system"}
	}

	// Split mode: the configured prefix is the stable block, the remainder stays the user
	// turn. This preserves the client's instruction/content order, which is the least
	// surprising layout for the model.
	out := cloneRequest(req)
	out.Messages[0].SetStringContent(joinSystem(systemText, userText[:split]))
	out.Messages[1].SetStringContent(userText[split:])
	return &out, Decision{Apply: true, Reason: "moved stable block into system"}
}

// Fingerprint is the hex sha256 of the leading chars the fingerprint covers. Operators build
// the value in the setting from this function so the setting and the server cannot disagree.
func Fingerprint(userText string, fingerprintChars int) string {
	if fingerprintChars <= 0 {
		fingerprintChars = DefaultFingerprintChars
	}
	if fingerprintChars < len(userText) {
		userText = userText[:fingerprintChars]
	}
	sum := sha256.Sum256([]byte(userText))
	return hex.EncodeToString(sum[:])
}

func (cfg Config) minUserChars() int {
	if cfg.MinUserChars > 0 {
		return cfg.MinUserChars
	}
	return DefaultMinUserChars
}

func (cfg Config) scoped(tokenID, channelID int) bool {
	if len(cfg.TokenIDs) == 0 && len(cfg.ChannelIDs) == 0 {
		return false
	}
	if len(cfg.TokenIDs) > 0 && !containsInt(cfg.TokenIDs, tokenID) {
		return false
	}
	if len(cfg.ChannelIDs) > 0 && !containsInt(cfg.ChannelIDs, channelID) {
		return false
	}
	return true
}

func (cfg Config) fingerprintMatches(userText string) bool {
	want := strings.ToLower(strings.TrimSpace(cfg.FingerprintSHA256))
	if want == "" {
		return true
	}
	return Fingerprint(userText, cfg.FingerprintChars) == want
}

// joinSystem appends the moved block to the original system prompt, tolerating an empty
// prompt so the result never starts with stray separators.
func joinSystem(systemText, moved string) string {
	switch {
	case systemText == "":
		return moved
	case moved == "":
		return systemText
	default:
		return systemText + systemSeparator + moved
	}
}

func cloneRequest(req *dto.GeneralOpenAIRequest) dto.GeneralOpenAIRequest {
	out := *req
	out.Messages = make([]dto.Message, len(req.Messages))
	copy(out.Messages, req.Messages)
	return out
}

func containsInt(values []int, want int) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
