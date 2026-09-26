package model

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/fitpolicy"
)

// Fit-policy option plumbing.
//
// The policy snapshot is refreshed from the option-apply path rather than from a
// config-epoch reload hook. loadOptionsFromDatabase runs at startup, on every
// epoch advance and on the periodic SYNC_FREQUENCY sync, so a single call site
// covers all three. A reload-hook registration would only cover the epoch path,
// which means a node that lost Redis would keep serving a stale policy forever.

// ValidateFitPolicyOption compiles a policy document before it is persisted.
// An empty value is allowed: it removes the option, which drops the layer back
// to no opinion.
func ValidateFitPolicyOption(value string) error {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	_, err := fitpolicy.CompileJSON([]byte(value))
	return err
}

// refreshFitPolicySnapshot installs the policy currently present in the option
// map. A failure keeps the previous snapshot (last-known-good) and is returned
// so the caller can surface it.
func refreshFitPolicySnapshot() error {
	return fitpolicy.Reload(currentFitPolicyLoader)
}

// currentFitPolicyLoader reads the raw policy document from the option map.
// Absence is not an error: an unwritten option means "no opinion", which keeps
// the legacy routing path fully in charge.
func currentFitPolicyLoader() ([]byte, bool, error) {
	common.OptionMapRWMutex.RLock()
	raw, present := common.OptionMap[fitpolicy.OptionKey]
	common.OptionMapRWMutex.RUnlock()
	if !present || strings.TrimSpace(raw) == "" {
		return nil, false, nil
	}
	return []byte(raw), true, nil
}
