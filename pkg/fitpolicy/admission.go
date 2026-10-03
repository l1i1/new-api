package fitpolicy

import (
	"github.com/QuantumNous/new-api/officialfit"
)

// Admission is a family's answer to "which channels count as official-behaving
// for this model", bound to the snapshot that produced it.
//
// It is carried on the Requirement and re-derived from the current snapshot for
// callers outside a request (the relay-path dialect exemption, the affinity
// gate). Both shapes exist because the answer is a property of the policy, not
// of the request: a request-bound copy keeps a retry under the admission rules
// it started with, exactly the way the requirement's mark bindings already do.
type Admission struct {
	// Family is the canonical family id the admission governs.
	Family string
	// Source is AdmissionSourceDeclared or AdmissionSourceMeasured.
	Source string
	// Battery lists the behaviours a non-official-type channel must carry
	// fresh passing marks for. It is only consulted when Source is measured;
	// under declared it is the shadow-admission window's data.
	Battery []string
	// PolicyHash and BaselineHash are the bindings the battery is evaluated
	// against — the same binding rule the phase-1 marks use, so a policy or
	// baseline change invalidates admission measurements rather than letting
	// them silently re-validate.
	PolicyHash   string
	BaselineHash string
}

// SourceOrDefault resolves an empty source to the declared default. A zero
// Admission — the value every Requirement carried before the field existed —
// therefore answers "declared", which keeps that path's behaviour unchanged.
func (a Admission) SourceOrDefault() string {
	if a.Source == AdmissionSourceMeasured {
		return AdmissionSourceMeasured
	}
	return AdmissionSourceDeclared
}

// Measured reports whether the battery, not the channel's declaration, decides
// admission for non-official-type channels.
func (a Admission) Measured() bool {
	return a.SourceOrDefault() == AdmissionSourceMeasured
}

// WithSourceMeasured returns the same admission under the measured source. It
// exists for the shadow report: the equivalence window asks "what would the
// measured set have been for these candidates", which is the declared
// admission's battery evaluated under the other source.
func (a Admission) WithSourceMeasured() Admission {
	measured := a
	measured.Source = AdmissionSourceMeasured
	return measured
}

// AdmissionFor returns the family's admission for a model, resolved from this
// snapshot. ok is false when the snapshot has nothing to say about the model's
// family; the caller then falls back to the declared default, which is the
// behaviour that predates the field.
func (s *Snapshot) AdmissionFor(model string) (Admission, bool) {
	if s == nil {
		return Admission{}, false
	}
	familyID := officialfit.FamilyOf(model)
	if familyID == "" {
		return Admission{}, false
	}
	family, known := s.families[familyID]
	if !known || family == nil {
		return Admission{}, false
	}
	return Admission{
		Family:       familyID,
		Source:       family.admissionSource,
		Battery:      append([]string(nil), family.admissionBattery...),
		PolicyHash:   s.hash,
		BaselineHash: s.baseline,
	}, true
}
