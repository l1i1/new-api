package main

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFeatureProbeMatrixIsCompleteAndUnique(t *testing.T) {
	seen := make(map[string]struct{}, len(allCases))
	for _, id := range allCases {
		require.Truef(t, strings.HasPrefix(id, "DS-"), "invalid case id %q", id)
		_, exists := seen[id]
		require.Falsef(t, exists, "duplicate case id %s", id)
		seen[id] = struct{}{}
	}
	require.Len(t, seen, 85)
	require.Len(t, implementedLiveCases, 45)
	for id := range implementedLiveCases {
		_, exists := seen[id]
		require.Truef(t, exists, "implemented live case %s is missing from the matrix", id)
	}
}

func TestBasicCaseSelectionKeepsOnlyNormalMatrixIDs(t *testing.T) {
	t.Setenv("FEATURE_PROBE_CASES", "DS-A01, DS-F09, K03, unknown")
	selected := basicCaseSelection()
	assert.Contains(t, selected, "DS-A01")
	assert.Contains(t, selected, "DS-F09")
	assert.NotContains(t, selected, "K03")
	assert.NotContains(t, selected, "UNKNOWN")
}

func TestRoleRoundTripFixtureContainsOrderedRolesWithoutResponseData(t *testing.T) {
	request := roleRoundTripRequest("deepseek-v4-flash")
	messages, ok := request["messages"].([]any)
	require.True(t, ok)
	require.Len(t, messages, 5)
	roles := make([]string, 0, len(messages))
	for _, raw := range messages {
		message, ok := raw.(map[string]any)
		require.True(t, ok)
		role, ok := message["role"].(string)
		require.True(t, ok)
		roles = append(roles, role)
	}
	assert.Equal(t, []string{"system", "user", "assistant", "tool", "user"}, roles)
	assert.NotContains(t, request, "response")
}

func TestVariantRequestsCoverBoundedParameterEdges(t *testing.T) {
	assert.Len(t, variantRequests("deepseek-v4-flash", "DS-D01"), 3)
	assert.Len(t, variantRequests("deepseek-v4-flash", "DS-D02"), 3)
	variants := variantRequests("deepseek-v4-flash", "DS-D07")
	require.Len(t, variants, 4)
	assert.NotContains(t, variants[0].body, "max_tokens")
	assert.Equal(t, 0, variants[1].body["max_tokens"])
	assert.Equal(t, -1, variants[2].body["max_tokens"])
	assert.Equal(t, 393217, variants[3].body["max_tokens"])
}

func TestReplayAssistantMessageKeepsOnlyProtocolFields(t *testing.T) {
	payload := map[string]any{
		"choices": []any{map[string]any{
			"message": map[string]any{
				"role":              "assistant",
				"content":           "2",
				"reasoning_content": "private reasoning",
				"tool_calls": []any{map[string]any{
					"id": "call_1",
				}},
			},
		}},
	}
	message, ok := replayAssistantMessage(payload, true)
	require.True(t, ok)
	assert.Equal(t, "assistant", message["role"])
	assert.Equal(t, "2", message["content"])
	assert.Equal(t, "private reasoning", message["reasoning_content"])
	assert.NotContains(t, message, "usage")
}

func TestSafeErrorDoesNotExposeMessage(t *testing.T) {
	require.Equal(t, "main.assertionError", safeError(assertionError("secret-key-or-prompt")))
}

func TestSummarizeNonStreamRedactsContentButReportsProtocolShape(t *testing.T) {
	evidence := summarize([]byte(`{"object":"chat.completion","choices":[{"message":{"role":"assistant","content":"2","reasoning_content":"private reasoning"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":1,"total_tokens":5,"prompt_tokens_details":{"cached_tokens":3}}}`), false)

	assert.Equal(t, true, evidence["has_content"])
	assert.Equal(t, true, evidence["has_reasoning_content"])
	assert.Equal(t, "stop", evidence["finish_reason"])
	assert.Equal(t, float64(3), evidence["cached_tokens"])
	_, leaked := evidence["content"]
	assert.False(t, leaked)
}

func TestSummarizeStreamCountsUsageAndTermination(t *testing.T) {
	evidence := summarize([]byte("data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"x\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"2\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":1,\"total_tokens\":5}}\n\ndata: [DONE]\n"), true)

	assert.Equal(t, true, evidence["done"])
	assert.Equal(t, true, evidence["has_content"])
	assert.Equal(t, true, evidence["has_reasoning_content"])
	assert.Equal(t, 1, evidence["usage_events"])
	assert.Equal(t, "stop", evidence["finish_reason"])
}

func TestSummarizeStreamAcceptsDataFieldWithoutSpace(t *testing.T) {
	evidence := summarize([]byte("data:{\"choices\":[{\"delta\":{\"content\":\"2\"},\"finish_reason\":\"stop\"}]}\n\ndata:[DONE]\n"), true)

	assert.Equal(t, true, evidence["done"])
	assert.Equal(t, true, evidence["has_content"])
	assert.Equal(t, "stop", evidence["finish_reason"])
}

func TestSummarizeStreamReassemblesToolArgumentsAndLogprobs(t *testing.T) {
	payload := strings.Join([]string{
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"get_"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"weather","arguments":"{\"city\":\"Bei"}}]},"logprobs":{"content":[{"token":"x","top_logprobs":[{},{}]}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"jing\"}"}}]},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	}, "\n\n") + "\n"
	evidence := summarize([]byte(payload), true)

	assert.Equal(t, true, evidence["has_tool_calls"])
	assert.Equal(t, true, evidence["tool_arguments_json"])
	assert.Equal(t, 1, evidence["stream_tool_call_count"])
	assert.Equal(t, 1, evidence["logprobs_content"])
	assert.Equal(t, 2, evidence["max_top_logprobs"])
	_, leaked := evidence["arguments"]
	assert.False(t, leaked)
}

func TestExpectedPassRequiresNonEmptyContentForBasicCompletion(t *testing.T) {
	evidence := map[string]any{
		"json":          true,
		"choices":       1,
		"has_content":   false,
		"finish_reason": "stop",
	}
	assert.False(t, expectedPass("DS-B01", "official", 200, evidence))
}

func TestExpectedPassRequiresClientErrorForUnknownModel(t *testing.T) {
	evidence := map[string]any{"json": true, "has_error": true, "error_type": "invalid_request_error", "error_code": "model_not_found", "error_param_null": true}
	assert.True(t, expectedPass("DS-A04", "official", 400, evidence))
	assert.True(t, expectedPass("DS-A04", "official", 422, evidence))
	assert.False(t, expectedPass("DS-A04", "official", 503, evidence))
}

func TestExpectedPassRequiresUsageForRequestedStreamUsage(t *testing.T) {
	evidence := map[string]any{
		"stream":                    true,
		"done":                      true,
		"has_content":               true,
		"sse_events":                3,
		"usage_events":              1,
		"usage_event_empty_choices": true,
	}
	assert.True(t, expectedPass("DS-B02", "official", 200, evidence))
	delete(evidence, "usage_events")
	assert.False(t, expectedPass("DS-B02", "official", 200, evidence))
}

func TestExpectedPassRejectsEmptyStreamContent(t *testing.T) {
	evidence := map[string]any{
		"stream":                    true,
		"done":                      true,
		"has_content":               false,
		"sse_events":                3,
		"usage_events":              1,
		"usage_event_empty_choices": true,
	}

	assert.False(t, expectedPass("DS-B02", "official", 200, evidence))
	assert.False(t, expectedPass("DS-B03", "official", 200, evidence))
}

func TestExpectedPassCoversNewConversationAndToolCases(t *testing.T) {
	base := map[string]any{
		"first_http_status":           http.StatusOK,
		"first_has_content":           true,
		"first_has_reasoning_content": true,
		"first_has_tool_calls":        true,
		"second_http_status":          http.StatusOK,
		"json":                        true,
		"has_error":                   false,
		"has_content":                 true,
		"finish_reason":               "stop",
	}
	assert.True(t, expectedPass("DS-C08", "official", http.StatusOK, base))
	assert.True(t, expectedPass("DS-C09", "official", http.StatusOK, base))
	assert.True(t, expectedPass("DS-C10", "official", http.StatusOK, base))
	assert.True(t, expectedPass("DS-F06", "official", http.StatusOK, base))
	assert.True(t, expectedPass("DS-F07", "official", http.StatusOK, base))

	validation := map[string]any{
		"first_http_status":    http.StatusOK,
		"first_has_tool_calls": true,
		"json":                 true,
		"has_error":            true,
		"error_type":           "invalid_request_error",
		"error_code":           "invalid_request_error",
		"error_param_null":     true,
	}
	assert.True(t, expectedPass("DS-C11", "official", http.StatusBadRequest, validation))
	assert.True(t, expectedPass("DS-F08", "official", http.StatusBadRequest, validation))
}

func TestExpectedPassStreamingToolCallRequiresValidArguments(t *testing.T) {
	evidence := map[string]any{
		"stream":              true,
		"done":                true,
		"has_error":           false,
		"has_tool_calls":      true,
		"tool_arguments_json": true,
		"finish_reason":       "tool_calls",
	}
	assert.True(t, expectedPass("DS-F09", "official", http.StatusOK, evidence))
	evidence["tool_arguments_json"] = false
	assert.False(t, expectedPass("DS-F09", "official", http.StatusOK, evidence))
}

func TestExpectedPassStreamingLogprobsRequiresUsageAndBound(t *testing.T) {
	evidence := map[string]any{
		"stream":           true,
		"done":             true,
		"has_error":        false,
		"has_content":      true,
		"has_logprobs":     true,
		"logprobs_content": 1,
		"max_top_logprobs": 5,
		"usage_events":     1,
	}
	assert.True(t, expectedPass("DS-E05", "official", http.StatusOK, evidence))
	delete(evidence, "usage_events")
	assert.False(t, expectedPass("DS-E05", "official", http.StatusOK, evidence))
}

func TestSummarizeToolCallValidatesArgumentsWithoutCopyingThem(t *testing.T) {
	evidence := summarize([]byte(`{"choices":[{"message":{"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Beijing\"}"}}]},"finish_reason":"tool_calls"}]}`), false)

	assert.Equal(t, true, evidence["has_tool_calls"])
	assert.Equal(t, true, evidence["tool_arguments_json"])
	_, leaked := evidence["tool_calls"]
	assert.False(t, leaked)
}

func TestSummarizeUsageReportsArithmeticConsistency(t *testing.T) {
	evidence := summarize([]byte(`{"choices":[{"message":{"content":"2"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":1,"total_tokens":5,"prompt_tokens_details":{"cached_tokens":3}}}`), false)

	assert.Equal(t, true, evidence["usage_consistent"])
	assert.Equal(t, true, evidence["cached_tokens_valid"])
}

func TestSummarizeLogprobsReportsMaximumTopEntries(t *testing.T) {
	evidence := summarize([]byte(`{"choices":[{"message":{"content":"2"},"finish_reason":"stop","logprobs":{"content":[{"token":"2","top_logprobs":[{},{},{}]}],"reasoning_content":[{"token":"thinking","top_logprobs":[{},{}]}]}}]}`), false)

	assert.Equal(t, 3, evidence["max_top_logprobs"])
	assert.Equal(t, 2, evidence["max_reasoning_top_logprobs"])
}

func TestExpectedPassRecognizesLogprobsValidationFingerprint(t *testing.T) {
	evidence := map[string]any{
		"json":                      true,
		"has_error":                 true,
		"error_type":                "invalid_request_error",
		"error_code":                "invalid_request_error",
		"error_param_null":          true,
		"error_message_fingerprint": "invalid top_logprobs and logprobs value, logprobs must be set to true if top_logprobs is used.",
	}
	assert.True(t, expectedPass("DS-E04", "official", 400, evidence))
	assert.False(t, expectedPass("DS-E07", "official", 400, evidence))
}

func TestFitExpectedAcceptsToolCallWithThinkingDisabled(t *testing.T) {
	evidence := map[string]any{
		"json":                  true,
		"has_content":           false,
		"has_reasoning_content": false,
		"has_tool_calls":        true,
		"tool_arguments_json":   true,
	}

	assert.True(t, fitExpected("DS-K10", http.StatusOK, evidence))
}

func TestFitChecksCoverProvidedRequests(t *testing.T) {
	checks := fitChecks("deepseek-v4-flash")
	require.Len(t, checks, 13)
	for index, check := range checks {
		assert.Equal(t, fmt.Sprintf("K%02d", index+1), check.id)
	}
}

func TestSelectFitChecksAcceptsCanonicalAndLegacyIDs(t *testing.T) {
	checks, err := selectFitChecks(fitChecks("deepseek-v4-flash"), []string{"K03", "DS-K02", "k03"})
	require.NoError(t, err)
	require.Len(t, checks, 2)
	assert.Equal(t, "K02", checks[0].id)
	assert.Equal(t, "K03", checks[1].id)
}

func TestSelectFitChecksRejectsUnknownID(t *testing.T) {
	_, err := selectFitChecks(fitChecks("deepseek-v4-flash"), []string{"K14"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "K14")
}

func TestAddErrorEvidenceRecordsParamAndRedactsFingerprint(t *testing.T) {
	evidence := make(map[string]any)
	addErrorEvidence(evidence, map[string]any{
		"type":    "invalid_request_error",
		"code":    "invalid_request_error",
		"param":   nil,
		"message": "invalid request Bearer sk-secret-value",
	})

	assert.Equal(t, true, evidence["has_error"])
	assert.Equal(t, true, evidence["error_param_present"])
	assert.Equal(t, true, evidence["error_param_null"])
	assert.Equal(t, "invalid_request_error", evidence["error_code"])
	assert.NotContains(t, evidence["error_message_fingerprint"], "secret-value")
}

func TestAnnotateFitEvidenceSeparatesProtocolAcceptanceFromEffectiveSuccess(t *testing.T) {
	evidence := map[string]any{"json": true, "has_content": false, "has_reasoning_content": true, "finish_reason": "length"}

	annotateFitEvidence(http.StatusOK, evidence)

	assert.Equal(t, true, evidence["protocol_accepted"])
	assert.Equal(t, false, evidence["effective_success"])
	assert.Equal(t, "empty_final_content", evidence["failure_reason"])
}

type assertionError string

func (assertionError) Error() string { return "secret-key-or-prompt" }
