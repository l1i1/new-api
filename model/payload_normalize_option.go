package model

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/payloadnorm"
)

// Payload-normalize option plumbing.
//
// The setting is one JSON document in one option row, and the request path reads it from an
// atomically swapped immutable snapshot. That combination is the point: a document is parsed
// and validated as a unit before it becomes live, so the rewrite can never run against a
// half-applied scope (an old allowlist with a new mode, or a scope whose sibling field failed
// to parse). A document that does not parse installs "disabled", never the previous scope.
//
// The snapshot is refreshed from the option-apply path rather than from a config-epoch
// reload hook. loadOptionsFromDatabase runs at startup, on every epoch advance and on the
// periodic SYNC_FREQUENCY sync, so that one call site covers all three - a node that lost
// Redis would otherwise keep a stale scope indefinitely.

// ValidatePayloadNormalizeOption validates a document before it is persisted, so a malformed
// scope is rejected by its author instead of becoming live on the next reload.
func ValidatePayloadNormalizeOption(value string) error {
	return payloadnorm.ValidateSettingJSON(value)
}

// refreshPayloadNormalizeSnapshot installs the document currently present in the option map.
//
// Absence and blank mean the same thing here: disabled. There is no shipped default policy
// for this feature - it rewrites what the model sees, so it only ever acts when an operator
// has written a scope on purpose.
func refreshPayloadNormalizeSnapshot() {
	raw, present := payloadNormalizeOptionValue()
	if !present || strings.TrimSpace(raw) == "" {
		payloadnorm.InstallDisabled()
		return
	}
	if err := payloadnorm.InstallSettingJSON(raw); err != nil {
		// InstallSettingJSON already failed closed; this line is the only trace of why the
		// feature went dark, so it must name the row.
		common.SysError("payload normalize: option " + payloadnorm.OptionKey +
			" is invalid and the feature was disabled: " + err.Error())
	}
}

func payloadNormalizeOptionValue() (string, bool) {
	common.OptionMapRWMutex.RLock()
	raw, present := common.OptionMap[payloadnorm.OptionKey]
	common.OptionMapRWMutex.RUnlock()
	return raw, present
}
