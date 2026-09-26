package middleware

import (
	"encoding/json"
	"testing"

	"github.com/QuantumNous/new-api/pkg/fitpolicy"
	"github.com/QuantumNous/new-api/relaykit/dto"
)

// toRequestView maps the distributor's pin-request shape onto the policy
// package's view. Keeping this mapping in the test makes the equivalence claim
// explicit: the built-in policy sees exactly the fields the shipped predicates
// read, in the same raw form.
func toRequestView(req v4OfficialPinRequest) fitpolicy.RequestView {
	return fitpolicy.RequestView{
		Model:           req.Model,
		LogProbs:        req.LogProbs,
		ReasoningEffort: req.ReasoningEffort,
		Thinking:        req.THINKING,
		ToolChoice:      req.ToolChoice,
		ResponseFormat:  req.ResponseFormat,
		Messages:        req.Messages,
	}
}

// TestFitPolicyBuiltinMatchesShippedPredicates is the step-A equivalence gate.
// For every fixture the built-in policy must select exactly the requests the
// shipped Go predicate for that family pins — no misses (which would leak pool
// behaviour to a caller comparing against the official endpoint) and no
// over-selection (which would spend official capacity on shapes the pool
// serves faithfully).
//
// Each case names one family and is compared only against that family's
// predicate and policy: the predicates are family-specific, so calling both on
// every fixture would compare a model against the wrong family.
func TestFitPolicyBuiltinMatchesShippedPredicates(t *testing.T) {
	snapshot, err := fitpolicy.Compile(fitpolicy.BuiltinPolicy())
	if err != nil {
		t.Fatalf("builtin policy must compile: %v", err)
	}

	boolPtr := func(value bool) *bool { return &value }
	imageMessage := dto.Message{
		Role: "user",
		Content: []any{
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example/a.png"}},
		},
	}
	userOnly := []dto.Message{{Role: "user"}}
	thinkingOn := json.RawMessage(`{"type":"enabled"}`)
	thinkingOff := json.RawMessage(`{"type":"disabled"}`)

	cases := []struct {
		name string
		// family is the canonical family id the fixture belongs to.
		family string
		req    v4OfficialPinRequest
		// want documents the shipped predicate's answer. It is asserted twice:
		// against the Go predicate and against the policy, so a change to
		// either side fails this test.
		want bool
	}{
		// kimi-k3
		{
			name: "k3 thinking disabled pins", family: "kimi-k3", want: true,
			req: v4OfficialPinRequest{Model: "kimi-k3", THINKING: thinkingOff, Messages: userOnly},
		},
		{
			name: "k3 thinking enabled does not pin", family: "kimi-k3", want: false,
			req: v4OfficialPinRequest{Model: "kimi-k3", THINKING: thinkingOn, Messages: userOnly},
		},
		{
			name: "k3 effort none pins", family: "kimi-k3", want: true,
			req: v4OfficialPinRequest{Model: "kimi-k3", ReasoningEffort: "none", Messages: userOnly},
		},
		{
			name: "k3 tool_choice required pins", family: "kimi-k3", want: true,
			req: v4OfficialPinRequest{Model: "kimi-k3", THINKING: thinkingOn, ToolChoice: json.RawMessage(`"required"`), Messages: userOnly},
		},
		{
			name: "k3 tool_choice auto does not pin", family: "kimi-k3", want: false,
			req: v4OfficialPinRequest{Model: "kimi-k3", THINKING: thinkingOn, ToolChoice: json.RawMessage(`"auto"`), Messages: userOnly},
		},
		{
			name: "k3 named-function tool_choice pins", family: "kimi-k3", want: true,
			req: v4OfficialPinRequest{Model: "kimi-k3", THINKING: thinkingOn, ToolChoice: json.RawMessage(`{"type":"function","function":{"name":"f"}}`), Messages: userOnly},
		},
		{
			name: "k3 non-string tool_choice pins", family: "kimi-k3", want: true,
			req: v4OfficialPinRequest{Model: "kimi-k3", THINKING: thinkingOn, ToolChoice: json.RawMessage(`7`), Messages: userOnly},
		},
		{
			name: "k3 response_format json_object pins", family: "kimi-k3", want: true,
			req: v4OfficialPinRequest{Model: "kimi-k3", THINKING: thinkingOn, ResponseFormat: json.RawMessage(`{"type":"json_object"}`), Messages: userOnly},
		},
		{
			name: "k3 response_format text does not pin", family: "kimi-k3", want: false,
			req: v4OfficialPinRequest{Model: "kimi-k3", THINKING: thinkingOn, ResponseFormat: json.RawMessage(`{"type":"text"}`), Messages: userOnly},
		},
		{
			name: "k3 unparseable response_format pins", family: "kimi-k3", want: true,
			req: v4OfficialPinRequest{Model: "kimi-k3", THINKING: thinkingOn, ResponseFormat: json.RawMessage(`{"type":`), Messages: userOnly},
		},
		{
			name: "k3 assistant-first history pins", family: "kimi-k3", want: true,
			req: v4OfficialPinRequest{Model: "kimi-k3", THINKING: thinkingOn, Messages: []dto.Message{{Role: "assistant"}, {Role: "user"}}},
		},
		{
			name: "k3 system-only history pins", family: "kimi-k3", want: true,
			req: v4OfficialPinRequest{Model: "kimi-k3", THINKING: thinkingOn, Messages: []dto.Message{{Role: "system"}}},
		},
		{
			name: "k3 dynamic tools pin", family: "kimi-k3", want: true,
			req: v4OfficialPinRequest{Model: "kimi-k3", THINKING: thinkingOn, Messages: []dto.Message{{Role: "user"}, {Role: "system", Tools: json.RawMessage(`[{"type":"function"}]`)}}},
		},
		{
			name: "k3 fully servable shape does not pin", family: "kimi-k3", want: false,
			req: v4OfficialPinRequest{Model: "kimi-k3", THINKING: thinkingOn, ReasoningEffort: "high", ToolChoice: json.RawMessage(`"auto"`), ResponseFormat: json.RawMessage(`{"type":"text"}`), Messages: userOnly},
		},
		{
			name: "k3 empty messages does not pin", family: "kimi-k3", want: false,
			req: v4OfficialPinRequest{Model: "kimi-k3", THINKING: thinkingOn},
		},
		{
			name: "k3 leading system then user does not pin", family: "kimi-k3", want: false,
			req: v4OfficialPinRequest{Model: "kimi-k3", THINKING: thinkingOn, Messages: []dto.Message{{Role: "system"}, {Role: "user"}}},
		},

		// deepseek-v4
		{
			name: "ds thinking enabled pins", family: "deepseek-v4", want: true,
			req: v4OfficialPinRequest{Model: "deepseek-v4-flash", THINKING: thinkingOn, Messages: userOnly},
		},
		{
			name: "ds thinking disabled does not pin", family: "deepseek-v4", want: false,
			req: v4OfficialPinRequest{Model: "deepseek-v4-flash", THINKING: thinkingOff, Messages: userOnly},
		},
		{
			name: "ds effort none does not pin", family: "deepseek-v4", want: false,
			req: v4OfficialPinRequest{Model: "deepseek-v4-flash", ReasoningEffort: "none", Messages: userOnly},
		},
		{
			name: "ds absent thinking pins", family: "deepseek-v4", want: true,
			req: v4OfficialPinRequest{Model: "deepseek-v4-flash", Messages: userOnly},
		},
		{
			name: "ds logprobs pins despite thinking off", family: "deepseek-v4", want: true,
			req: v4OfficialPinRequest{Model: "deepseek-v4-flash", LogProbs: boolPtr(true), THINKING: thinkingOff, Messages: userOnly},
		},
		{
			name: "ds logprobs false does not pin", family: "deepseek-v4", want: false,
			req: v4OfficialPinRequest{Model: "deepseek-v4-flash", LogProbs: boolPtr(false), THINKING: thinkingOff, Messages: userOnly},
		},
		{
			name: "ds image part pins despite thinking off", family: "deepseek-v4", want: true,
			req: v4OfficialPinRequest{Model: "deepseek-v4-flash", THINKING: thinkingOff, Messages: []dto.Message{imageMessage}},
		},
		{
			name: "ds dotted model id pins", family: "deepseek-v4", want: true,
			req: v4OfficialPinRequest{Model: "deepseek-v4.1-flash", THINKING: thinkingOn, Messages: userOnly},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			req := testCase.req

			var legacy bool
			switch testCase.family {
			case "kimi-k3":
				legacy = kimiK3RequestNeedsOfficial(req)
			case "deepseek-v4":
				legacy = deepSeekV4RequestNeedsOfficial(req)
			default:
				t.Fatalf("fixture names unsupported family %q", testCase.family)
			}

			if legacy != testCase.want {
				t.Fatalf("shipped %s predicate = %v, table says %v (update the table if the predicate changed on purpose)",
					testCase.family, legacy, testCase.want)
			}

			got := snapshot.Decide(testCase.family, true, toRequestView(req)).HasOpinion()
			if got != legacy {
				t.Fatalf("policy = %v, shipped %s predicate = %v (equivalence violated)", got, testCase.family, legacy)
			}
		})
	}
}

// TestFitPolicyBuiltinPinsGlmWholeFamily pins the one family with no selective
// predicate: every glm-5.3 request stays pinned until a measured predicate
// exists.
func TestFitPolicyBuiltinPinsGlmWholeFamily(t *testing.T) {
	snapshot, err := fitpolicy.Compile(fitpolicy.BuiltinPolicy())
	if err != nil {
		t.Fatalf("builtin policy must compile: %v", err)
	}
	for _, model := range []string{"glm-5.3", "glm-5.3-flash"} {
		req := v4OfficialPinRequest{Model: model, Messages: []dto.Message{{Role: "user"}}}
		if got := snapshot.Decide(model, true, toRequestView(req)).HasOpinion(); !got {
			t.Fatalf("%s: glm-5.3 must remain whole-family pinned, policy said no opinion", model)
		}
	}
}

// TestFitPolicyBuiltinPinsNothingWhenRouteDisabled guards the gate that keeps
// this layer invisible to users who never enabled fidelity routing.
func TestFitPolicyBuiltinPinsNothingWhenRouteDisabled(t *testing.T) {
	snapshot, err := fitpolicy.Compile(fitpolicy.BuiltinPolicy())
	if err != nil {
		t.Fatalf("builtin policy must compile: %v", err)
	}
	req := v4OfficialPinRequest{Model: "deepseek-v4-flash", THINKING: json.RawMessage(`{"type":"enabled"}`), Messages: []dto.Message{{Role: "user"}}}
	if got := snapshot.Decide("deepseek-v4", false, toRequestView(req)).HasOpinion(); got {
		t.Fatal("a route-disabled request must keep the legacy path with no policy opinion")
	}
}
