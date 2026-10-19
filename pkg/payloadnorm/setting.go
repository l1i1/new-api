package payloadnorm

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
)

// OptionKey is the single option row that carries the whole setting document.
//
// One row instead of one row per field is deliberate. The setting is a scope boundary: a
// half-applied scope - an old allowlist left in place while the new one failed to parse -
// rewrites requests the operator did not intend to touch. As one document it is parsed,
// validated and installed as a unit, so it is either fully live or not live at all.
const OptionKey = "payload_normalize.setting"

// Setting is the operator-facing document. Zero value means disabled.
//
// Measurements behind the rewrite (智谱 glm-5.3-flash, 315 KB document, 300 requests,
// concurrency 20, replaying the customer's own captured bytes): the document in the last
// user message hits the upstream prompt cache 40-60% of the time, the same content in the
// system message hits 99%+. The cache is prefix based, so the placement decides the hit
// rate, and this setting is the switch for moving it.
type Setting struct {
	Enabled bool `json:"enabled"`
	// TokenIDs and ChannelIDs are allowlists. Both empty means no request matches, and an
	// enabled document without any scope is rejected rather than silently inert.
	TokenIDs   []int `json:"token_ids,omitempty"`
	ChannelIDs []int `json:"channel_ids,omitempty"`
	// MinUserChars is the size floor of the user message in characters (default 100000).
	MinUserChars int `json:"min_user_chars,omitempty"`
	// FingerprintChars is how many leading characters of the user message the fingerprint
	// covers (default 4096). FingerprintSHA256 is the hex sha256 of that prefix; an empty
	// digest disables the check and leaves the allowlists as the only guard.
	FingerprintChars  int    `json:"fingerprint_chars,omitempty"`
	FingerprintSHA256 string `json:"fingerprint_sha256,omitempty"`
	// DocumentChars marks where the stable block ends inside the user message, counted in
	// characters. Zero moves the whole user message into the system message; a positive
	// value must leave both the moved block and the user tail non-empty.
	DocumentChars int `json:"document_chars,omitempty"`
}

// Config mirrors the setting into the rewrite scope the pure Apply consumes.
func (s Setting) Config() Config {
	return Config{
		Enabled:           s.Enabled,
		TokenIDs:          s.TokenIDs,
		ChannelIDs:        s.ChannelIDs,
		MinUserChars:      s.MinUserChars,
		FingerprintChars:  s.FingerprintChars,
		FingerprintSHA256: s.FingerprintSHA256,
		DocumentChars:     s.DocumentChars,
	}
}

// ParseSetting decodes and validates a setting document without installing it, so the write
// path can reject a malformed scope before it reaches the database.
//
// Unknown fields are rejected: a misspelled field would otherwise be silently ignored and the
// operator would be left with a document that does not mean what it says.
func ParseSetting(raw []byte) (Setting, error) {
	var setting Setting
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&setting); err != nil {
		return Setting{}, fmt.Errorf("payload normalize setting: %w", err)
	}
	if err := decoder.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		return Setting{}, errors.New("payload normalize setting: trailing data after the document")
	}
	if err := setting.validate(); err != nil {
		return Setting{}, err
	}
	return setting, nil
}

func (s Setting) validate() error {
	if s.MinUserChars < 0 {
		return errors.New("payload normalize setting: min_user_chars must not be negative")
	}
	if s.FingerprintChars < 0 {
		return errors.New("payload normalize setting: fingerprint_chars must not be negative")
	}
	if s.DocumentChars < 0 {
		// Negative used to mean "whole message" in an earlier draft. It is now rejected:
		// only an explicit zero may choose the more aggressive whole-message rewrite.
		return errors.New("payload normalize setting: document_chars must not be negative")
	}
	if s.FingerprintSHA256 != "" {
		digest, err := hex.DecodeString(s.FingerprintSHA256)
		if err != nil {
			return fmt.Errorf("payload normalize setting: fingerprint_sha256 is not hex: %w", err)
		}
		if len(digest) != sha256.Size {
			return fmt.Errorf("payload normalize setting: fingerprint_sha256 must be %d hex characters", sha256.Size*2)
		}
	}
	for _, id := range s.TokenIDs {
		if id <= 0 {
			return fmt.Errorf("payload normalize setting: token id %d must be positive", id)
		}
	}
	for _, id := range s.ChannelIDs {
		if id <= 0 {
			return fmt.Errorf("payload normalize setting: channel id %d must be positive", id)
		}
	}
	if s.Enabled && len(s.TokenIDs) == 0 && len(s.ChannelIDs) == 0 {
		// An enabled document with no scope matches nobody. Reject it instead of letting the
		// operator believe the rewrite is on: this exact confusion is what "empty list means
		// everybody" bugs are made of, in the safe direction.
		return errors.New("payload normalize setting: enabled requires token_ids or channel_ids, an empty scope matches nobody")
	}
	return nil
}

// current is the atomically swapped snapshot. A request loads it once, so a concurrent
// reload never changes the rules mid-request, and a reload is a single pointer store - it
// cannot be observed half-applied.
var current atomic.Pointer[Setting]

// Current returns the installed document. Never nil after the first install; the zero value
// (disabled) governs an unconfigured process and an unparseable document alike.
func Current() Setting {
	if snapshot := current.Load(); snapshot != nil {
		return *snapshot
	}
	return Setting{}
}

// CurrentConfig is the rewrite scope implied by the installed document.
func CurrentConfig() Config {
	return Current().Config()
}

// ValidateSettingJSON checks a document before it is persisted. A blank value is the
// documented disable lever, so it validates.
func ValidateSettingJSON(value string) error {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	_, err := ParseSetting([]byte(value))
	return err
}

// InstallSettingJSON parses and publishes a document. A parse or validation failure
// publishes the disabled document and returns the error: a scope the process cannot read
// must not keep rewriting traffic, and a stale older scope must not stay live either
// (fail-closed). The write path rejects such documents before they reach the database, so
// this branch is a cold-start safety net for a hand-edited row.
func InstallSettingJSON(value string) error {
	if strings.TrimSpace(value) == "" {
		InstallDisabled()
		return nil
	}
	setting, err := ParseSetting([]byte(value))
	if err != nil {
		InstallDisabled()
		return err
	}
	InstallSetting(setting)
	return nil
}

// InstallSetting publishes a document that was already parsed and validated.
func InstallSetting(setting Setting) {
	current.Store(&setting)
}

// InstallDisabled takes the rewrite out of service.
func InstallDisabled() {
	disabled := Setting{}
	current.Store(&disabled)
}
