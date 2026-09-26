package fitpolicy

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
)

func mustCompileBuiltin(t *testing.T) *Snapshot {
	t.Helper()
	snapshot, err := Compile(BuiltinPolicy())
	if err != nil {
		t.Fatalf("builtin policy must compile: %v", err)
	}
	return snapshot
}

func TestBuiltinPolicyCompiles(t *testing.T) {
	snapshot := mustCompileBuiltin(t)
	if !snapshot.Enabled() {
		t.Fatal("builtin policy must be enabled")
	}
	if !snapshot.Shadow() {
		t.Fatal("builtin policy must start in shadow mode")
	}
	if snapshot.Version() != 1 {
		t.Fatalf("version = %d, want 1", snapshot.Version())
	}
	if snapshot.Hash() == "" {
		t.Fatal("snapshot hash must not be empty")
	}
	for _, family := range RegisteredFamilyIDs() {
		if _, known := snapshot.families[family]; !known {
			t.Fatalf("builtin policy is missing registered family %q", family)
		}
	}
}

func TestBuiltinPolicyCoversEveryRegisteredFamily(t *testing.T) {
	policy := BuiltinPolicy()
	declared := make(map[string]struct{}, len(policy.Families))
	for _, family := range policy.Families {
		declared[family.ID] = struct{}{}
	}
	for _, id := range RegisteredFamilyIDs() {
		if _, ok := declared[id]; !ok {
			t.Fatalf("family %q is registered but has no builtin rules; a new family must not be silently unpinned", id)
		}
	}
}

func TestDecideGating(t *testing.T) {
	snapshot := mustCompileBuiltin(t)
	thinkingEnabled := func() RequestView {
		return RequestView{
			Model:    "kimi-k3",
			Thinking: json.RawMessage(`{"type":"enabled"}`),
		}
	}

	if got := snapshot.Decide("kimi-k3", false, thinkingEnabled()); got.HasOpinion() {
		t.Fatal("route-disabled request must produce no opinion")
	}
	if got := snapshot.Decide("kimi-k3", true, thinkingEnabled()); got.HasOpinion() {
		t.Fatal("a shape no clause matches must produce no opinion")
	}
	if got := snapshot.Decide("", true, thinkingEnabled()); got.HasOpinion() {
		t.Fatal("an empty model must produce no opinion")
	}
	if got := snapshot.Decide("gpt-4o", true, thinkingEnabled()); got.HasOpinion() {
		t.Fatal("a model outside every family must produce no opinion")
	}

	var disabled *Snapshot
	if got := disabled.Decide("kimi-k3", true, thinkingEnabled()); got.HasOpinion() {
		t.Fatal("a nil snapshot must produce no opinion")
	}
	if !disabled.Shadow() || disabled.Enabled() {
		t.Fatal("a nil snapshot must be disabled and shadow")
	}
}

func TestDecideReportsPolicyIdentity(t *testing.T) {
	snapshot := mustCompileBuiltin(t)
	view := RequestView{Model: "kimi-k3", Thinking: json.RawMessage(`{"type":"disabled"}`)}
	got := snapshot.Decide("kimi-k3", true, view)
	if !got.HasOpinion() {
		t.Fatal("thinking-off must require a behaviour")
	}
	if got.Family != "kimi-k3" {
		t.Fatalf("family = %q, want kimi-k3", got.Family)
	}
	if got.PolicyVersion != snapshot.Version() || got.PolicyHash != snapshot.Hash() {
		t.Fatal("requirement must carry the producing snapshot identity")
	}
	if got.UnknownMarkPolicy != UnknownMarkConservative {
		t.Fatalf("unknown mark policy = %q", got.UnknownMarkPolicy)
	}
	if got.EmptyMatchPolicy != EmptyMatchLegacyHardPin {
		t.Fatalf("empty match policy = %q", got.EmptyMatchPolicy)
	}
	if got.Behaviors[BehaviorThinkingCounting] != ClassVerdict {
		t.Fatal("required behaviour must carry its declared class")
	}
	if !got.Shadow {
		t.Fatal("requirement must record that the producing policy was shadow")
	}
}

func TestDecideDeduplicatesMarksInRuleOrder(t *testing.T) {
	policy := Policy{
		Version: 1,
		Enabled: true,
		Families: []FamilyPolicy{{
			ID: "kimi-k3",
			Rules: []Rule{
				{ID: "a", When: "ThinkingDisabled()", Require: []string{"one", "two"}},
				{ID: "b", When: "ThinkingDisabled()", Require: []string{"two", "three"}},
			},
			Behaviors: map[string]Behavior{
				"one":   {Class: ClassVerdict},
				"two":   {Class: ClassCapability},
				"three": {Class: ClassCourtesy},
			},
		}},
	}
	snapshot, err := Compile(policy)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	got := snapshot.Decide("kimi-k3", true, RequestView{Thinking: json.RawMessage(`{"type":"disabled"}`)})
	want := []string{"one", "two", "three"}
	if len(got.Marks) != len(want) {
		t.Fatalf("marks = %v, want %v", got.Marks, want)
	}
	for i := range want {
		if got.Marks[i] != want[i] {
			t.Fatalf("marks = %v, want %v", got.Marks, want)
		}
	}
}

func TestPolicyValidationRejects(t *testing.T) {
	valid := func() Policy {
		return Policy{
			Version: 1,
			Enabled: true,
			Families: []FamilyPolicy{{
				ID:        "kimi-k3",
				Rules:     []Rule{{ID: "r", When: "ThinkingDisabled()", Require: []string{"b"}}},
				Behaviors: map[string]Behavior{"b": {Class: ClassVerdict}},
			}},
		}
	}
	cases := map[string]func(p *Policy){
		"zero version":            func(p *Policy) { p.Version = 0 },
		"unregistered family":     func(p *Policy) { p.Families[0].ID = "not-a-family" },
		"empty family id":         func(p *Policy) { p.Families[0].ID = "" },
		"duplicate family":        func(p *Policy) { p.Families = append(p.Families, p.Families[0]) },
		"undeclared behaviour":    func(p *Policy) { p.Families[0].Rules[0].Require = []string{"missing"} },
		"empty require":           func(p *Policy) { p.Families[0].Rules[0].Require = nil },
		"empty when":              func(p *Policy) { p.Families[0].Rules[0].When = " " },
		"empty rule id":           func(p *Policy) { p.Families[0].Rules[0].ID = "" },
		"invalid class":           func(p *Policy) { p.Families[0].Behaviors["b"] = Behavior{Class: "nope"} },
		"invalid unknown policy":  func(p *Policy) { p.Families[0].UnknownMarkPolicy = "yolo" },
		"unsupported empty match": func(p *Policy) { p.Families[0].EmptyMatchPolicy = "fallthrough_priority" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			policy := valid()
			mutate(&policy)
			if err := policy.Validate(); err == nil {
				t.Fatal("expected validation to reject this policy")
			}
			if _, err := Compile(policy); err == nil {
				t.Fatal("Compile must reject an invalid policy too")
			}
		})
	}
}

func TestCompileRejectsUnknownFunctionAndNonBooleanRule(t *testing.T) {
	base := func(when string) Policy {
		return Policy{
			Version: 1,
			Enabled: true,
			Families: []FamilyPolicy{{
				ID:        "kimi-k3",
				Rules:     []Rule{{ID: "r", When: when, Require: []string{"b"}}},
				Behaviors: map[string]Behavior{"b": {Class: ClassVerdict}},
			}},
		}
	}
	for name, when := range map[string]string{
		"unknown function": "ThinkingDisable()",
		"unknown variable": "model == \"kimi-k3\"",
		"non boolean":      "1 + 1",
		"syntax error":     "ThinkingDisabled(",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Compile(base(when)); err == nil {
				t.Fatalf("expected %q to be rejected at compile time", when)
			}
		})
	}
}

func TestParsePolicyRejectsUnknownFields(t *testing.T) {
	raw := []byte(`{"version":1,"enabled":true,"families":[],"typo":true}`)
	if _, err := ParsePolicy(raw); err == nil {
		t.Fatal("an unknown top-level field must be rejected so typos are not silently ignored")
	}
}

func TestReloadKeepsLastKnownGood(t *testing.T) {
	Install(nil)
	t.Cleanup(func() { Install(nil); clearError() })

	if _, err := InstallJSON([]byte(`{"version":1,"enabled":true,"shadow":true,"families":[]}`)); err != nil {
		t.Fatalf("first install: %v", err)
	}
	good := Current()
	if good == nil {
		t.Fatal("snapshot must be installed")
	}

	bad := []byte(`{"version":2,"enabled":true,"families":[{"id":"nope","rules":[]}]}`)
	if err := Reload(func() ([]byte, bool, error) { return bad, true, nil }); err == nil {
		t.Fatal("reload of an invalid policy must fail")
	}
	if Current() != good {
		t.Fatal("a failed reload must keep the last-known-good snapshot")
	}
	if LastError() == "" {
		t.Fatal("a failed reload must be observable")
	}

	if err := Reload(func() ([]byte, bool, error) { return nil, false, nil }); err != nil {
		t.Fatalf("removing the option is not an error: %v", err)
	}
	if Current() != nil {
		t.Fatal("a removed option must drop back to no opinion")
	}
	if LastError() != "" {
		t.Fatal("a successful reload must clear the recorded error")
	}
}

func TestReloadPropagatesLoadError(t *testing.T) {
	Install(nil)
	t.Cleanup(func() { Install(nil); clearError() })
	sentinel := errFake("store unavailable")
	if err := Reload(func() ([]byte, bool, error) { return nil, false, sentinel }); err != sentinel {
		t.Fatalf("load error must be returned, got %v", err)
	}
	if Current() != nil {
		t.Fatal("a load failure must not install anything")
	}
}

func TestRegisterReloadHookRunsOnlyOnce(t *testing.T) {
	calls := 0
	register := func(hook func()) { calls++ }
	RegisterReloadHook(register, nil)
	RegisterReloadHook(register, nil)
	if calls != 1 {
		t.Fatalf("hook registration calls = %d, want 1", calls)
	}
}

func TestPrimitivesMatchShippedSemantics(t *testing.T) {
	cases := []struct {
		name          string
		view          RequestView
		wantDisabled  bool
		wantDynamics  bool
		wantImagePart bool
		wantHistory   bool
		wantFormat    bool
		wantToolForce bool
	}{
		{
			name:        "empty request defaults to thinking-expected",
			view:        RequestView{Messages: []dto.Message{{Role: "user"}}},
			wantHistory: true,
		},
		{
			name:         "thinking disabled",
			view:         RequestView{Thinking: json.RawMessage(`{"type":"disabled"}`)},
			wantDisabled: true,
			wantHistory:  true,
		},
		{
			name:        "thinking enabled wins over effort none",
			view:        RequestView{Thinking: json.RawMessage(`{"type":"enabled"}`), ReasoningEffort: "none"},
			wantHistory: true,
		},
		{
			name:         "effort none without thinking object",
			view:         RequestView{ReasoningEffort: "none"},
			wantDisabled: true,
			wantHistory:  true,
		},
		{
			name:         "unparseable thinking stays thinking-expected",
			view:         RequestView{Thinking: json.RawMessage(`{"type":`)},
			wantDisabled: false,
			wantHistory:  true,
		},
		{
			name:        "assistant first",
			view:        RequestView{Messages: []dto.Message{{Role: "assistant"}, {Role: "user"}}},
			wantHistory: false,
		},
		{
			name:        "system only",
			view:        RequestView{Messages: []dto.Message{{Role: "system"}}},
			wantHistory: false,
		},
		{
			name:         "dynamic tools on a message",
			view:         RequestView{Messages: []dto.Message{{Role: "system", Tools: json.RawMessage(`[{"type":"function"}]`)}}},
			wantDynamics: true,
			wantHistory:  false,
		},
		{
			name:          "image part",
			view:          RequestView{Messages: []dto.Message{{Role: "user", Content: []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example/a.png"}}}}}},
			wantImagePart: true,
			wantHistory:   true,
		},
		{
			name:        "response_format json_object",
			view:        RequestView{ResponseFormat: json.RawMessage(`{"type":"json_object"}`)},
			wantFormat:  true,
			wantHistory: true,
		},
		{
			name:        "response_format text",
			view:        RequestView{ResponseFormat: json.RawMessage(`{"type":"text"}`)},
			wantHistory: true,
		},
		{
			name:          "tool_choice named function",
			view:          RequestView{ToolChoice: json.RawMessage(`{"type":"function","function":{"name":"f"}}`)},
			wantToolForce: true,
			wantHistory:   true,
		},
		{
			name:        "tool_choice auto",
			view:        RequestView{ToolChoice: json.RawMessage(`"auto"`)},
			wantHistory: true,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			view := testCase.view
			if got := view.ThinkingDisabled(); got != testCase.wantDisabled {
				t.Fatalf("ThinkingDisabled() = %v, want %v", got, testCase.wantDisabled)
			}
			if got := view.MessagesCarryDynamicTools(); got != testCase.wantDynamics {
				t.Fatalf("MessagesCarryDynamicTools() = %v, want %v", got, testCase.wantDynamics)
			}
			if got := view.HasImagePart(); got != testCase.wantImagePart {
				t.Fatalf("HasImagePart() = %v, want %v", got, testCase.wantImagePart)
			}
			if got := view.HistoryBeginsWithUserTurn(); got != testCase.wantHistory {
				t.Fatalf("HistoryBeginsWithUserTurn() = %v, want %v", got, testCase.wantHistory)
			}
			if got := view.ResponseFormatNotText(); got != testCase.wantFormat {
				t.Fatalf("ResponseFormatNotText() = %v, want %v", got, testCase.wantFormat)
			}
			if got := view.ToolChoiceForcesOfficial(); got != testCase.wantToolForce {
				t.Fatalf("ToolChoiceForcesOfficial() = %v, want %v", got, testCase.wantToolForce)
			}
		})
	}
}

type errFake string

func (e errFake) Error() string { return string(e) }
