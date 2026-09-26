// Package fitpolicy turns the official-fit route policy into hot-reloadable
// data.
//
// Today the per-family pin predicates live in Go (middleware/distributor.go)
// and the channel whitelist is a binary per-model mark. This package moves the
// predicate composition and the required-behaviour set into versioned JSON that
// compiles to expr programs and is swapped atomically, so changing what a family
// considers "must be official" no longer needs a release.
//
// Scope discipline (see docs/fitpolicy-tech-spec.md):
//   - the package is pure policy. It answers "which behaviours does this
//     request require" and nothing else; candidate narrowing, retry and channel
//     selection stay in their existing owners;
//   - family knowledge stays in the officialfit registry: this package looks a
//     family up by canonical id and never carries its own prefix table;
//   - every primitive below is an exact port of the shipped Go predicate, so a
//     built-in policy reproduces today's routing decision mark-for-mark.
package fitpolicy

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
)

// RequestView is the slice of a chat request the policy primitives read.
//
// It deliberately holds the raw fields rather than a parsed request so the
// primitives keep the exact tolerance of the shipped predicates: an
// unparseable thinking/tool_choice/response_format value is classified, not
// rejected — local validation answers those with the official 400 before any
// upstream call, so over-selecting costs nothing.
type RequestView struct {
	Model           string
	LogProbs        *bool
	ReasoningEffort string
	Thinking        json.RawMessage
	ToolChoice      json.RawMessage
	ResponseFormat  json.RawMessage
	Messages        []dto.Message
}

// ThinkingDisabled mirrors officialFitThinkingDisabled: thinking.type=disabled,
// or reasoning_effort=none when the thinking object does not explicitly
// re-enable thinking. Effort values other than none map to thinking output.
//
// Both selective families read it, in opposite directions: DeepSeek V4 treats
// disabled thinking as the class the pool serves faithfully, kimi-k3 the class
// it does not (the pool reports the thinking-on prompt count).
func (v RequestView) ThinkingDisabled() bool {
	thinkingEnabled := false
	thinkingDisabled := false
	if raw := trimJSON(v.Thinking); len(raw) > 0 {
		var fields struct {
			Type string `json:"type"`
		}
		if err := common.Unmarshal(raw, &fields); err == nil {
			switch strings.ToLower(strings.TrimSpace(fields.Type)) {
			case "disabled":
				thinkingDisabled = true
			case "enabled", "adaptive":
				thinkingEnabled = true
			}
		}
		// Unparseable thinking shapes stay thinking-expected.
		if thinkingEnabled {
			return false
		}
	}
	if thinkingDisabled {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(v.ReasoningEffort), "none")
}

// ReasoningEffortIs reports whether reasoning_effort equals s, case-insensitively.
func (v RequestView) ReasoningEffortIs(s string) bool {
	return strings.EqualFold(strings.TrimSpace(v.ReasoningEffort), s)
}

// LogprobsRequested mirrors the DeepSeek V4 logprobs clause: only an explicit
// true pins, an absent/false field stays servable.
func (v RequestView) LogprobsRequested() bool {
	return v.LogProbs != nil && *v.LogProbs
}

// HasImagePart mirrors messagesContainImagePart: any image_url content part.
func (v RequestView) HasImagePart() bool {
	for i := range v.Messages {
		for _, part := range v.Messages[i].ParseContent() {
			if strings.EqualFold(strings.TrimSpace(part.Type), "image_url") {
				return true
			}
		}
	}
	return false
}

// HistoryBeginsWithUserTurn mirrors historyBeginsWithUserTurn: the first
// non-system message is a user turn. No messages at all counts as "begins with
// user" so a request local validation will reject is not classified as a
// divergence it can never exercise.
func (v RequestView) HistoryBeginsWithUserTurn() bool {
	if len(v.Messages) == 0 {
		return true
	}
	for i := range v.Messages {
		role := strings.ToLower(strings.TrimSpace(v.Messages[i].Role))
		if role == "system" {
			continue
		}
		return role == "user"
	}
	return false
}

// MessagesCarryDynamicTools mirrors messagesCarryDynamicTools: any message
// declares a tools array (how K3 loads a tool set mid-conversation).
func (v RequestView) MessagesCarryDynamicTools() bool {
	for i := range v.Messages {
		raw := trimJSON(v.Messages[i].Tools)
		if len(raw) > 0 {
			return true
		}
	}
	return false
}

// ResponseFormatNotText mirrors kimiK3ResponseFormatNeedsOfficial: an absent
// field and type=text stay servable; json_object, json_schema and an
// unparseable value do not.
func (v RequestView) ResponseFormatNotText() bool {
	raw := trimJSON(v.ResponseFormat)
	if len(raw) == 0 {
		return false
	}
	var format struct {
		Type string `json:"type"`
	}
	if err := common.Unmarshal(raw, &format); err != nil {
		return true
	}
	return !strings.EqualFold(strings.TrimSpace(format.Type), "text")
}

// ToolChoiceForcesOfficial mirrors kimiK3ToolChoiceNeedsOfficial: "auto" and an
// absent field stay servable; "required", "none", the named-function object and
// any unparseable value do not.
func (v RequestView) ToolChoiceForcesOfficial() bool {
	raw := trimJSON(v.ToolChoice)
	if len(raw) == 0 {
		return false
	}
	var strategy string
	if err := common.Unmarshal(raw, &strategy); err == nil {
		return !strings.EqualFold(strings.TrimSpace(strategy), "auto")
	}
	// Not a string: the named-function object (or a malformed value the local
	// validator rejects). Both are forcing shapes.
	return true
}

// WholeFamily always selects, encoding a family with no measured selective
// predicate (glm-5.3 today keeps whole-family pinning).
func (v RequestView) WholeFamily() bool {
	return true
}

// trimJSON returns raw with surrounding whitespace removed, treating an absent
// value and a JSON null as empty. Centralizing this keeps every clause's
// "absent means servable" rule identical to the shipped predicates.
func trimJSON(raw json.RawMessage) json.RawMessage {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	return trimmed
}
