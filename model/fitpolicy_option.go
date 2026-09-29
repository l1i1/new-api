package model

import (
	"strings"
	"sync"
	"time"

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

// defaultFitPolicyDocument is the shipped default, encoded once.
//
// Lazy rather than a package-level initialiser so the encoding cost and any
// failure stay inside the option path, and so importing this package cannot fail
// on a document the process may never need.
var defaultFitPolicyDocument = sync.OnceValue(func() []byte {
	encoded, err := common.Marshal(fitpolicy.DefaultPolicy())
	if err != nil {
		// The document is a compiled-in literal, so this is a programming error,
		// not an operational one. Falling back to "no default" degrades to the
		// old behaviour (no opinion) instead of taking the process down.
		common.SysError("fitpolicy: the shipped default policy cannot be encoded: " + err.Error())
		return nil
	}
	return encoded
})

// Drift and seed notices ride a budget because this path runs on every sync and
// every config epoch, so an unbounded line would be written once a minute
// forever. The first lines are what a diagnosis needs; after that one line per
// window keeps the state visible without flooding.
var (
	fitPolicySeededLog     = common.NewLogBudget(2, time.Hour)
	fitPolicyDivergenceLog = common.NewLogBudget(2, 10*time.Minute)
	fitPolicyDisabledLog   = common.NewLogBudget(1, 10*time.Minute)
)

// ValidateFitPolicyOption compiles a policy document before it is persisted.
// An empty value is allowed: it clears the option, which stops the shipped
// default from applying and drops the layer back to no opinion. That is the
// documented uninstall lever — an empty value is *not* the same as an absent
// one, which installs the shipped default.
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
	if err := fitpolicy.Reload(currentFitPolicyLoader); err != nil {
		return err
	}
	reportFitPolicyDocumentState()
	return nil
}

// reportFitPolicyDocumentState makes the live document's identity visible.
//
// Which document is installed decides whether any request is pinned at all, and
// every way of getting it wrong is silent: an absent option used to install
// nothing, a shadow document decides but acts on nothing, and a disabled
// document answers "no opinion" for every request. All three look identical from
// the outside, so the state is logged rather than inferred from routing.
func reportFitPolicyDocumentState() {
	raw, present := fitPolicyOptionValue()
	switch {
	case !present:
		if fitPolicySeededLog.Allow() {
			common.SysLog("fitpolicy: option " + fitpolicy.OptionKey +
				" has never been written; the shipped default policy is in force")
		}
	case strings.TrimSpace(raw) == "":
		if fitPolicyDisabledLog.Allow() {
			common.SysLog("fitpolicy: option " + fitpolicy.OptionKey +
				" is empty; the official-fit policy layer is installed with no opinion")
		}
	default:
		policy, err := fitpolicy.ParsePolicy([]byte(raw))
		if err != nil {
			// Reload already reported the failure and kept the last good
			// snapshot; parsing it again here would only duplicate that.
			return
		}
		if diff := fitpolicy.DivergenceFromBuiltin(policy); diff != "" && fitPolicyDivergenceLog.Allow() {
			common.SysLog("fitpolicy: the live document differs from the shipped default (allowed, shown so it is not silent): " + diff)
		}
	}
}

// currentFitPolicyLoader reads the raw policy document from the option map.
//
// Absence has two distinct meanings and they must not be collapsed:
//   - the option has never been written: the shipped default is installed, so a
//     node that was never configured runs the rules the equivalence test proves
//     equal to the retired predicates instead of running nothing;
//   - the option exists but is blank: the operator cleared it, which is the
//     documented way to take the layer out, so no opinion is installed and the
//     legacy routing path is fully in charge.
func currentFitPolicyLoader() ([]byte, bool, error) {
	raw, present := fitPolicyOptionValue()
	if !present {
		document := defaultFitPolicyDocument()
		return document, document != nil, nil
	}
	if strings.TrimSpace(raw) == "" {
		return nil, false, nil
	}
	return []byte(raw), true, nil
}

// fitPolicyOptionValue reads the option without applying the default fallback,
// so callers can still tell an unwritten option from a cleared one.
func fitPolicyOptionValue() (string, bool) {
	common.OptionMapRWMutex.RLock()
	raw, present := common.OptionMap[fitpolicy.OptionKey]
	common.OptionMapRWMutex.RUnlock()
	return raw, present
}
