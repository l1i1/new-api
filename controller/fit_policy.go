package controller

import (
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/fitpolicy"
	"github.com/gin-gonic/gin"
)

// Fit policy administration read surface.
//
// The document itself is written through PUT /api/option/, which is already the
// single validated write path for the option it lives in: validateOptionValue
// compiles the document before the database commit, so an uncompilable rule set
// can never be persisted. These endpoints add what that generic path cannot
// answer, and they are read-only on purpose:
//
//   - which document is in force — the shipped default, a cleared option, or an
//     administrator document — because all three answer "no opinion" to some
//     requests and look identical from the outside;
//   - whether the stored document still compiles (a document can be stored and
//     still be unreadable after a registry change);
//   - how it diverges from the shipped default;
//   - whether saving a candidate would leave the layer quietly pinning nothing.
//
// The last point is the reason this file exists. A malformed or disabled
// document does not fail loudly on its own: selection simply answers "no
// opinion" for every request, which is indistinguishable from a healthy policy.
// The validation endpoint turns that into an explicit, translatable verdict the
// editor must satisfy before the save button unlocks.

// Which document is in force. These are the three states the loader
// distinguishes, and they must not be collapsed: an unwritten option installs
// the shipped default, while a written-but-blank option takes the layer out
// entirely.
const (
	FitPolicySourceDefault  = "default"
	FitPolicySourceCleared  = "cleared"
	FitPolicySourceDocument = "document"
)

// Warning codes for a compilable document that still does less than it looks
// like it does. Codes rather than sentences on purpose: the reader is a
// localized UI, and a sentence assembled here could not be translated.
const (
	// FitPolicyWarningDisabled is enabled=false: no request is pinned at all.
	FitPolicyWarningDisabled = "disabled"
	// FitPolicyWarningShadow is shadow=true: requires are decided and traced but
	// never acted on.
	FitPolicyWarningShadow = "shadow"
	// FitPolicyWarningNoFamily is a document with no family, so no request can
	// require anything.
	FitPolicyWarningNoFamily = "no_family"
	// FitPolicyWarningNoRules is families without a single rule.
	FitPolicyWarningNoRules = "no_rules"
	// FitPolicyWarningBaselineUnset means measurements are not bound to an
	// official baseline, so a change in the official upstream cannot invalidate
	// them. Informational, not a defect: the shipped default does not set one.
	FitPolicyWarningBaselineUnset = "baseline_unset"
	// FitPolicyWarningCleared is an empty document: the documented uninstall
	// lever, which drops the layer back to no opinion.
	FitPolicyWarningCleared = "cleared"
)

// fitPolicyWarning is one thing the operator must know before saving.
type fitPolicyWarning struct {
	Code string `json:"code"`
	// Count carries the number of offending families for no_rules; zero is
	// omitted.
	Count int `json:"count,omitempty"`
}

// fitPolicySummary describes a document that compiled.
type fitPolicySummary struct {
	Version   int      `json:"version"`
	Enabled   bool     `json:"enabled"`
	Shadow    bool     `json:"shadow"`
	Baseline  string   `json:"baseline"`
	Families  []string `json:"families"`
	Rules     int      `json:"rules"`
	Behaviors int      `json:"behaviors"`
	// Hash is the content hash this document would install, the same value a
	// capability report is bound to.
	Hash string `json:"hash"`
}

// fitPolicyLiveView describes the snapshot actually installed in this process.
//
// It is reported separately from the stored document because the two can differ:
// a document that fails to load keeps the previous snapshot (last-known-good),
// and that divergence is precisely what an operator must be able to see.
type fitPolicyLiveView struct {
	Installed bool   `json:"installed"`
	Version   int    `json:"version"`
	Enabled   bool   `json:"enabled"`
	Shadow    bool   `json:"shadow"`
	Baseline  string `json:"baseline"`
	Hash      string `json:"hash"`
	LastError string `json:"last_error"`
}

// fitPolicyView is the whole administration view of the policy.
type fitPolicyView struct {
	OptionPresent bool   `json:"option_present"`
	Source        string `json:"source"`
	// Document is the raw stored value, exactly as written. Empty when the
	// option has never been written.
	Document string `json:"document"`
	// EffectiveDocument is the document that is actually in force, indented for
	// display. Empty only when the option was cleared.
	EffectiveDocument string `json:"effective_document"`
	// ParseError is non-empty when the in-force document no longer parses or
	// compiles. That is not a normal state: the write path rejects such a
	// document, so it means the registry changed underneath a stored document.
	ParseError string `json:"parse_error"`
	// Divergence describes how the in-force document differs from the shipped
	// default ("" when identical).
	Divergence string             `json:"divergence"`
	Warnings   []fitPolicyWarning `json:"warnings"`
	Live       fitPolicyLiveView  `json:"live"`
	// DefaultDocument is the shipped default, indented, so the editor can offer
	// "restore the shipped default" without guessing its bytes.
	DefaultDocument string `json:"default_document"`
}

// GetFitPolicy returns the administration view of official_fit.policy.
func GetFitPolicy(c *gin.Context) {
	common.ApiSuccess(c, buildFitPolicyView())
}

// fitPolicyValidateRequest carries a candidate document.
type fitPolicyValidateRequest struct {
	Document string `json:"document"`
}

// fitPolicyValidation is the answer to "may I save this, and what would it do".
type fitPolicyValidation struct {
	Valid bool `json:"valid"`
	// Error is the compiler's rejection message, verbatim.
	Error string `json:"error"`
	// Divergence is how the candidate differs from the shipped default.
	Divergence string             `json:"divergence"`
	Warnings   []fitPolicyWarning `json:"warnings"`
	Summary    *fitPolicySummary  `json:"summary"`
	// LiveHash is the hash currently installed, so the editor can tell a real
	// change from a no-op rewrite.
	LiveHash string `json:"live_hash"`
}

// ValidateFitPolicyDocument compiles a candidate policy document without
// storing or installing it.
//
// It is the mandatory pre-flight for the editor: the frontend will not enable
// the save button for a document this endpoint has not accepted. The write path
// compiles again on PUT /api/option/, so a client that skips this step still
// cannot persist an uncompilable document — this endpoint exists to make the
// rejection visible *before* the save, together with what the document would do
// to the live layer.
//
// An invalid document is a successful validation request, so the response is
// 200 with valid=false rather than an error status.
func ValidateFitPolicyDocument(c *gin.Context) {
	var request fitPolicyValidateRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid request body"})
		return
	}
	common.ApiSuccess(c, validateFitPolicyDocument(request.Document))
}

// buildFitPolicyView assembles the view from the option map and the installed
// snapshot. It reads no database row: the option map is the same source the
// policy loader uses, so what is shown is what the process is running from.
func buildFitPolicyView() fitPolicyView {
	raw, present := model.FitPolicyOptionValue()
	defaultDocument := model.FitPolicyDefaultDocument()

	view := fitPolicyView{
		OptionPresent:   present,
		Document:        raw,
		DefaultDocument: defaultDocument,
		Live:            currentFitPolicyLiveView(),
	}

	effective := ""
	switch {
	case !present:
		view.Source = FitPolicySourceDefault
		effective = defaultDocument
	case strings.TrimSpace(raw) == "":
		view.Source = FitPolicySourceCleared
	default:
		view.Source = FitPolicySourceDocument
		effective = raw
	}
	view.EffectiveDocument = indentFitPolicyDocument(effective)

	if strings.TrimSpace(effective) == "" {
		// A cleared option is a deliberate state, not a defect, but it is also
		// indistinguishable from a broken one at request time, so it is reported
		// with the same machinery.
		if view.Source == FitPolicySourceCleared {
			view.Warnings = []fitPolicyWarning{{Code: FitPolicyWarningCleared}}
		}
		return view
	}

	policy, err := fitpolicy.ParsePolicy([]byte(effective))
	if err != nil {
		view.ParseError = err.Error()
		return view
	}
	view.Divergence = fitpolicy.DivergenceFromBuiltin(policy)
	view.Warnings = fitPolicyDocumentWarnings(policy)
	return view
}

// validateFitPolicyDocument compiles one candidate document. A blank document is
// valid: it is the documented rollback lever, which clears the option and drops
// the layer back to no opinion.
func validateFitPolicyDocument(document string) fitPolicyValidation {
	validation := fitPolicyValidation{LiveHash: currentFitPolicyHash()}
	if strings.TrimSpace(document) == "" {
		validation.Valid = true
		validation.Warnings = []fitPolicyWarning{{Code: FitPolicyWarningCleared}}
		return validation
	}

	policy, err := fitpolicy.ParsePolicy([]byte(document))
	if err != nil {
		validation.Error = err.Error()
		return validation
	}
	// Compile, not just Parse: expr type-checks each rule against the evaluation
	// environment, so a typo inside `when` is rejected here rather than silently
	// evaluating false on a live request.
	snapshot, err := fitpolicy.Compile(policy)
	if err != nil {
		validation.Error = err.Error()
		return validation
	}

	validation.Valid = true
	validation.Divergence = fitpolicy.DivergenceFromBuiltin(policy)
	validation.Warnings = fitPolicyDocumentWarnings(policy)
	validation.Summary = fitPolicySummaryOf(policy, snapshot.Hash())
	return validation
}

// fitPolicyDocumentWarnings names every way a compilable document can still
// leave the layer doing nothing.
//
// Each of these is legal and each is a real operational choice — a shadow window
// and a deliberate rollback both look like this — so none of them is an error.
// They are reported so that the choice is visible at the moment it is made
// instead of being discovered later from the absence of pinned traffic.
func fitPolicyDocumentWarnings(policy fitpolicy.Policy) []fitPolicyWarning {
	warnings := make([]fitPolicyWarning, 0, 4)
	if !policy.Enabled {
		warnings = append(warnings, fitPolicyWarning{Code: FitPolicyWarningDisabled})
	}
	if policy.Shadow {
		warnings = append(warnings, fitPolicyWarning{Code: FitPolicyWarningShadow})
	}
	if len(policy.Families) == 0 {
		warnings = append(warnings, fitPolicyWarning{Code: FitPolicyWarningNoFamily})
	}

	rules := 0
	ruleless := 0
	for _, family := range policy.Families {
		rules += len(family.Rules)
		if len(family.Rules) == 0 {
			ruleless++
		}
	}
	if len(policy.Families) > 0 && rules == 0 {
		warnings = append(warnings, fitPolicyWarning{Code: FitPolicyWarningNoRules, Count: ruleless})
	}
	if strings.TrimSpace(policy.Baseline) == "" {
		warnings = append(warnings, fitPolicyWarning{Code: FitPolicyWarningBaselineUnset})
	}
	return warnings
}

// fitPolicySummaryOf describes a compiled document.
func fitPolicySummaryOf(policy fitpolicy.Policy, hash string) *fitPolicySummary {
	summary := &fitPolicySummary{
		Version:   policy.Version,
		Enabled:   policy.Enabled,
		Shadow:    policy.Shadow,
		Baseline:  strings.TrimSpace(policy.Baseline),
		Families:  make([]string, 0, len(policy.Families)),
		Hash:      hash,
	}
	for _, family := range policy.Families {
		summary.Families = append(summary.Families, family.ID)
		summary.Rules += len(family.Rules)
		summary.Behaviors += len(family.Behaviors)
	}
	return summary
}

// currentFitPolicyLiveView describes the installed snapshot.
func currentFitPolicyLiveView() fitPolicyLiveView {
	view := fitPolicyLiveView{LastError: fitpolicy.LastError()}
	snapshot := fitpolicy.Current()
	if snapshot == nil {
		return view
	}
	view.Installed = true
	view.Version = snapshot.Version()
	view.Enabled = snapshot.Enabled()
	view.Shadow = snapshot.Shadow()
	view.Baseline = snapshot.Baseline()
	view.Hash = snapshot.Hash()
	return view
}

// currentFitPolicyHash returns the installed policy hash, or "" when no policy
// is installed.
func currentFitPolicyHash() string {
	if snapshot := fitpolicy.Current(); snapshot != nil {
		return snapshot.Hash()
	}
	return ""
}

// indentFitPolicyDocument pretty-prints a JSON document for display. An
// unparsable document is returned verbatim: reformatting is a courtesy, and
// hiding the bytes the operator actually wrote would defeat the point.
func indentFitPolicyDocument(document string) string {
	if strings.TrimSpace(document) == "" {
		return ""
	}
	indented, err := common.IndentJson([]byte(document))
	if err != nil {
		return document
	}
	return string(indented)
}
