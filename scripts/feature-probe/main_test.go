package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type probeRoundTripper func(*http.Request) (*http.Response, error)

func (f probeRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type probeReadErrorBody struct {
	payload []byte
	read    bool
}

func (b *probeReadErrorBody) Read(p []byte) (int, error) {
	if b.read {
		return 0, errors.New("read failed")
	}
	b.read = true
	return copy(p, b.payload), errors.New("read failed")
}

func (b *probeReadErrorBody) Close() error { return nil }

func TestFeatureProbeMatrixIsCompleteAndUnique(t *testing.T) {
	seen := make(map[string]struct{}, len(allCases))
	for _, id := range allCases {
		require.Truef(t, strings.HasPrefix(id, "DS-"), "invalid case id %q", id)
		_, exists := seen[id]
		require.Falsef(t, exists, "duplicate case id %s", id)
		seen[id] = struct{}{}
	}
	require.Len(t, seen, 85)
	require.Len(t, implementedLiveCases, 49)
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

func TestRequestPayloadPreservesPartialStreamEvidenceOnReadError(t *testing.T) {
	client := &http.Client{Transport: probeRoundTripper(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       &probeReadErrorBody{payload: []byte("data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n")},
			Request:    req,
		}, nil
	})}

	status, evidence, _, err := requestPayload(context.Background(), client, "https://example.test/v1", "key", http.MethodPost, "/chat/completions", []byte(`{}`))

	assert.Equal(t, http.StatusOK, status)
	assert.Error(t, err)
	assert.Equal(t, true, evidence["has_content"])
	assert.NotEmpty(t, evidence["read_error"])
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

func TestExpectedPassRequiredToolChoiceRequiresValidToolCall(t *testing.T) {
	evidence := map[string]any{
		"json":                true,
		"has_error":           false,
		"has_tool_calls":      true,
		"tool_arguments_json": true,
	}
	assert.True(t, expectedPass("DS-F02", "official", http.StatusOK, evidence))

	evidence["tool_arguments_json"] = false
	assert.False(t, expectedPass("DS-F02", "official", http.StatusOK, evidence))
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

func TestResponsesFixturesUseResponsesInputAndFunctionCallItems(t *testing.T) {
	request := responsesRequest("deepseek-v4-flash")
	assert.Equal(t, "deepseek-v4-flash", request["model"])
	assert.Equal(t, "1+1=?", request["input"])
	assert.NotContains(t, request, "messages")

	toolRequest := responsesToolRequest("deepseek-v4-flash")
	tools, ok := toolRequest["tools"].([]any)
	require.True(t, ok)
	require.Len(t, tools, 1)
	tool, ok := tools[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "function", tool["type"])
	assert.Equal(t, map[string]any{"type": "function", "name": "get_weather"}, toolRequest["tool_choice"])
}

func TestSummarizeResponsesNonStreamReportsStructuralEvidence(t *testing.T) {
	evidence := summarizeResponses([]byte(`{"object":"response","status":"completed","output":[{"type":"reasoning","id":"rs_secret","status":"completed","summary":[{"type":"summary_text","text":"compare values"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"2"}]},{"type":"function_call","call_id":"call_secret","name":"get_weather","arguments":"{\"city\":\"Beijing\"}"}],"usage":{"input_tokens":4,"output_tokens":2,"total_tokens":6,"input_tokens_details":{"cached_tokens":3}}}`), false)

	assert.Equal(t, true, evidence["json"])
	assert.Equal(t, true, evidence["response_object"])
	assert.Equal(t, "completed", evidence["response_status"])
	assert.Equal(t, true, evidence["has_output_text"])
	assert.Equal(t, true, evidence["has_reasoning_output"])
	assert.Equal(t, true, evidence["has_function_call"])
	assert.Equal(t, true, evidence["function_call_arguments_json"])
	assert.Equal(t, true, evidence["usage"])
	assert.Equal(t, true, evidence["usage_valid"])
	assert.Equal(t, float64(3), evidence["cached_tokens"])
	assert.NotContains(t, evidence, "text")
	assert.NotContains(t, evidence, "arguments")
	assert.NotContains(t, evidence, "call_id")
}

func TestSummarizeResponsesStreamTracksSemanticTerminalWithoutDoneMarker(t *testing.T) {
	payload := strings.Join([]string{
		`event: response.created`,
		`data: {"type":"response.created","response":{"object":"response","status":"in_progress"}}`,
		``,
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","delta":"2"}`,
		``,
		`event: response.reasoning_text.delta`,
		`data: {"type":"response.reasoning_text.delta","delta":"thinking"}`,
		``,
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"object":"response","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"2"}]}],"usage":{"input_tokens":4,"output_tokens":1,"total_tokens":5}}}`,
	}, "\n") + "\n\n"
	evidence := summarizeResponses([]byte(payload), true)

	assert.Equal(t, true, evidence["json"])
	assert.Equal(t, true, evidence["response_object"])
	assert.Equal(t, true, evidence["response_terminal"])
	assert.Equal(t, "response.completed", evidence["response_terminal_event"])
	assert.Equal(t, true, evidence["response_terminal_last"])
	assert.Equal(t, true, evidence["has_output_text"])
	assert.Equal(t, true, evidence["has_reasoning_output"])
	assert.Equal(t, true, evidence["usage"])
	assert.NotContains(t, evidence, "done_marker")
}

func TestResponsesExpectedPassRequiresSemanticShape(t *testing.T) {
	base := map[string]any{
		"response_object":         true,
		"response_status":         "completed",
		"has_error":               false,
		"has_output_text":         true,
		"usage_valid":             true,
		"stream":                  true,
		"response_terminal":       true,
		"response_terminal_last":  true,
		"response_terminal_event": "response.completed",
		"has_reasoning_output":    true,
	}
	assert.True(t, responsesExpected("DS-G01", http.StatusOK, base))
	assert.True(t, responsesExpected("DS-G02", http.StatusOK, base))
	assert.True(t, responsesExpected("DS-G03", http.StatusOK, base))
	delete(base, "has_reasoning_output")
	assert.False(t, responsesExpected("DS-G03", http.StatusOK, base))
	base["response_terminal"] = false
	assert.False(t, responsesExpected("DS-G02", http.StatusOK, base))
	base["response_terminal"] = true
	base["response_terminal_last"] = false
	assert.False(t, responsesExpected("DS-G02", http.StatusOK, base))
	base["response_terminal_last"] = true
	base["response_terminal_event"] = "response.failed"
	assert.False(t, responsesExpected("DS-G02", http.StatusOK, base))
}

func TestResponsesUnsupportedClassificationDoesNotHideValidationErrors(t *testing.T) {
	assert.False(t, responsesUnsupported(http.StatusNotFound, nil))
	assert.True(t, responsesUnsupported(http.StatusMethodNotAllowed, nil))
	assert.False(t, responsesUnsupported(http.StatusNotFound, map[string]any{"error_message_fingerprint": "model not found"}))
	assert.True(t, responsesUnsupported(http.StatusNotFound, map[string]any{"error_message_fingerprint": "responses endpoint not found"}))
	assert.True(t, responsesUnsupported(http.StatusBadRequest, map[string]any{"error_message_fingerprint": "responses endpoint is not supported"}))
	assert.False(t, responsesUnsupported(http.StatusBadRequest, map[string]any{"error_message_fingerprint": "input is required"}))
}

func TestSummarizeResponsesValidationDoesNotInventUsageOrReasoning(t *testing.T) {
	emptyUsage := summarizeResponses([]byte(`{"object":"response","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"2"}]}],"usage":{}}`), false)
	assert.Equal(t, true, emptyUsage["usage"])
	assert.Equal(t, false, emptyUsage["usage_valid"])
	assert.False(t, responsesExpected("DS-G01", http.StatusOK, emptyUsage))

	emptyReasoning := summarizeResponses([]byte(`{"object":"response","status":"completed","output":[{"type":"reasoning","summary":[]},{"type":"message","content":[{"type":"output_text","text":"2"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`), false)
	assert.NotEqual(t, true, emptyReasoning["has_reasoning_output"])

	summaryReasoning := summarizeResponses([]byte(`{"object":"response","status":"completed","output":[{"type":"reasoning","summary":[{"type":"summary_text","text":"compare values"}]},{"type":"message","content":[{"type":"output_text","text":"2"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`), false)
	assert.Equal(t, true, summaryReasoning["has_reasoning_output"])
}

func TestSummarizeResponsesStreamRejectsLateTerminalAndRecognizesSummaryReasoning(t *testing.T) {
	payload := strings.Join([]string{
		`event: response.completed`,
		`data: {"type":"response.completed","response":{"object":"response","status":"completed"}}`,
		``,
		`event: response.reasoning_summary_text.delta`,
		`data: {"type":"response.reasoning_summary_text.delta","delta":"thinking"}`,
	}, "\n") + "\n\n"
	evidence := summarizeResponses([]byte(payload), true)
	assert.Equal(t, true, evidence["has_reasoning_output"])
	assert.Equal(t, false, evidence["response_terminal_last"])
	assert.False(t, responsesExpected("DS-G02", http.StatusOK, evidence))

	failedPayload := strings.Join([]string{
		`event: response.output_text.delta`,
		`data: {"type":"response.output_text.delta","delta":"partial"}`,
		``,
		`event: response.failed`,
		`data: {"type":"response.failed","response":{"object":"response","status":"failed"}}`,
	}, "\n") + "\n\n"
	failedEvidence := summarizeResponses([]byte(failedPayload), true)
	assert.Equal(t, true, failedEvidence["response_terminal_last"])
	assert.Equal(t, "response.failed", failedEvidence["response_terminal_event"])
	assert.False(t, responsesExpected("DS-G02", http.StatusOK, failedEvidence))
}

func TestFirstResponsesFunctionCallKeepsOnlyContinuationFields(t *testing.T) {
	call, ok := firstResponsesFunctionCall(map[string]any{
		"object": "response",
		"output": []any{map[string]any{
			"type":      "function_call",
			"call_id":   "call_1",
			"name":      "get_weather",
			"arguments": `{"city":"Beijing"}`,
		}},
	})
	require.True(t, ok)
	assert.Equal(t, responsesFunctionCall{callID: "call_1", name: "get_weather", arguments: `{"city":"Beijing"}`}, call)
}

type assertionError string

func (assertionError) Error() string { return "secret-key-or-prompt" }
