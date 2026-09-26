package fitpolicy

import (
	"strings"
	"sync/atomic"

	"github.com/QuantumNous/new-api/officialfit"
)

// OptionKey is the option row holding the policy document. It lives here so the
// host's write path and this package cannot disagree about the key.
const OptionKey = "official_fit.policy"

// current is the atomically-swapped compiled policy. A request loads it once,
// so a concurrent reload never changes the rules mid-request.
var current atomic.Pointer[Snapshot]

// Current returns the installed snapshot. It is never nil once a policy has
// been installed; before that it returns nil, which Decide treats as
// "no opinion" so the legacy path stays in charge.
func Current() *Snapshot {
	return current.Load()
}

// Install atomically replaces the live snapshot. Callers must only pass a
// snapshot that compiled successfully; a failed compile keeps the previous
// snapshot in place.
func Install(snapshot *Snapshot) {
	current.Store(snapshot)
}

// CompileJSON parses and compiles a policy document without installing it. The
// write path uses this before the database commit so an invalid policy is
// rejected by its author instead of becoming the live snapshot.
func CompileJSON(raw []byte) (*Snapshot, error) {
	policy, err := ParsePolicy(raw)
	if err != nil {
		return nil, err
	}
	return Compile(policy)
}

// InstallJSON parses and compiles a policy document and, on success, installs
// it. A parse or compile error leaves the live snapshot untouched and returns
// the error so the write path can reject it before the database commit.
func InstallJSON(raw []byte) (*Snapshot, error) {
	snapshot, err := CompileJSON(raw)
	if err != nil {
		return nil, err
	}
	Install(snapshot)
	return snapshot, nil
}

// IsRegisteredFamily reports whether id is a family the mechanism registry
// knows. A policy may only reference registered families: family knowledge
// (model prefixes, official channel type, wire shape) stays in code, so a new
// family still needs a registry entry and its consumers, never just JSON.
func IsRegisteredFamily(id string) bool {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return false
	}
	for i := range officialfit.Families {
		if officialfit.Families[i].ID == trimmed {
			return true
		}
	}
	return false
}

// RegisteredFamilyIDs returns the canonical family ids a policy may reference,
// in registry order.
func RegisteredFamilyIDs() []string {
	families := officialfit.Families
	ids := make([]string, 0, len(families))
	for i := range families {
		ids = append(ids, families[i].ID)
	}
	return ids
}

// Loader reads the current policy document. present=false means the option has
// never been written, which is not an error: the live snapshot stays as it is
// and the legacy path keeps serving.
type Loader func() (raw []byte, present bool, err error)

var (
	// lastError holds the most recent load/compile failure so the reload path
	// can expose why the snapshot is stale instead of silently freezing.
	lastError atomic.Pointer[string]
)

// Reload re-reads the policy and installs it. Any failure keeps the previous
// snapshot (last-known-good): a broken policy degrades to the last good one,
// never to a partially compiled state.
//
// The host calls this from its option-apply path, which is shared by startup,
// the config-epoch reload and the periodic SYNC_FREQUENCY sync. That is
// deliberate: a reload-hook registration would only cover the epoch path, so a
// node that lost Redis would keep serving a stale policy indefinitely.
func Reload(load Loader) error {
	if load == nil {
		return nil
	}
	raw, present, err := load()
	if err != nil {
		recordError(err)
		return err
	}
	if !present {
		// The option was removed: drop back to no opinion so the legacy route
		// logic takes over completely.
		Install(nil)
		clearError()
		return nil
	}
	if _, err := InstallJSON(raw); err != nil {
		recordError(err)
		return err
	}
	clearError()
	return nil
}

// LastError returns the most recent reload failure, or empty when the live
// snapshot is up to date.
func LastError() string {
	if value := lastError.Load(); value != nil {
		return *value
	}
	return ""
}

func recordError(err error) {
	if err == nil {
		return
	}
	message := err.Error()
	lastError.Store(&message)
}

func clearError() {
	lastError.Store(nil)
}
