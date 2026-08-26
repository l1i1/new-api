package main

import (
	"context"
	"errors"
	"fmt"
	"io"
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

func TestSummarizeNonStreamDetectsFitStopSequence(t *testing.T) {
	for _, content := range []string{"苹果、香蕉、橙子", "apple, BaNaNa, orange"} {
		evidence := summarize([]byte(fmt.Sprintf(`{"choices":[{"message":{"content":%q},"finish_reason":"stop"}]}`, content)), false)
		assert.Equal(t, true, evidence["contains_stop_sequence"], content)
		assert.Equal(t, true, evidence["contains_pre_stop_content"], content)
	}
}

func TestFitExpectedRequiresOfficialNonStreamResponseShape(t *testing.T) {
	official := summarize([]byte(`{"id":"chat_1","object":"chat.completion","created":1710000000,"model":"deepseek-v4-flash","choices":[{"index":0,"message":{"role":"assistant","content":"2","reasoning_content":"think"},"logprobs":null,"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6,"prompt_tokens_details":{"cached_tokens":1},"completion_tokens_details":{"reasoning_tokens":1},"prompt_cache_hit_tokens":1,"prompt_cache_miss_tokens":3}}`), false)
	assert.True(t, fitExpected("K04", http.StatusOK, official))

	aggregator := summarize([]byte(`{"id":"router_1","object":"chat.completion","created":1710000000,"model":"deepseek-v4-flash","choices":[{"index":0,"message":{"role":"assistant","content":"2","reasoning_content":"think","tool_calls":null},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6,"input_tokens":4},"cost":"0"}`), false)
	assert.False(t, fitExpected("K04", http.StatusOK, aggregator))

	wrongEnvelope := summarize([]byte(`{"id":"chat_1","object":"chat.completion","created":1710000000,"model":"deepseek-v4-flash","choices":[{"index":1,"message":{"role":"user","content":"2"},"finish_reason":"invented"}],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6,"prompt_tokens_details":{"cached_tokens":1},"completion_tokens_details":{"reasoning_tokens":1},"prompt_cache_hit_tokens":1,"prompt_cache_miss_tokens":3}}`), false)
	assert.False(t, fitExpected("K04", http.StatusOK, wrongEnvelope))
}

func TestFitExpectedRequiresExpectedContentBeforeStop(t *testing.T) {
	usage := `"usage":{"prompt_tokens":4,"completion_tokens":1,"total_tokens":5,"prompt_tokens_details":{"cached_tokens":0},"completion_tokens_details":{"reasoning_tokens":0},"prompt_cache_hit_tokens":0,"prompt_cache_miss_tokens":4}`
	unrelated := summarize([]byte(`{"id":"chat_1","object":"chat.completion","created":1710000000,"model":"deepseek-v4-flash","choices":[{"index":0,"message":{"role":"assistant","content":"梨"},"finish_reason":"stop"}],`+usage+`}`), false)
	assert.False(t, fitExpected("K01", http.StatusOK, unrelated))

	stopped := summarize([]byte(`{"id":"chat_1","object":"chat.completion","created":1710000000,"model":"deepseek-v4-flash","choices":[{"index":0,"message":{"role":"assistant","content":"苹果、"},"finish_reason":"stop"}],`+usage+`}`), false)
	assert.True(t, fitExpected("K01", http.StatusOK, stopped))
}

func TestFitExpectedAcceptsReasoningOnlyLengthForBoundedReasoningCases(t *testing.T) {
	evidence := summarize([]byte(`{"id":"chat_1","object":"chat.completion","created":1710000000,"model":"deepseek-v4-flash","choices":[{"index":0,"message":{"role":"assistant","content":null,"reasoning_content":"thinking"},"finish_reason":"length"}],"usage":{"prompt_tokens":8,"completion_tokens":64,"total_tokens":72,"prompt_tokens_details":{"cached_tokens":0},"completion_tokens_details":{"reasoning_tokens":64},"prompt_cache_hit_tokens":0,"prompt_cache_miss_tokens":8}}`), false)
	assert.True(t, fitExpected("K06", http.StatusOK, evidence))
	assert.True(t, fitExpected("K07", http.StatusOK, evidence))
	assert.False(t, fitExpected("K05", http.StatusOK, evidence))
}

func TestFitExpectedAcceptsCompleteReasoningOnlyLengthStreamForK08(t *testing.T) {
	evidence := summarize([]byte("data: {\"id\":\"chat_1\",\"object\":\"chat.completion.chunk\",\"created\":1710000000,\"model\":\"deepseek-v4-flash\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"thinking\"},\"finish_reason\":\"length\"}],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":1024,\"total_tokens\":1033,\"prompt_tokens_details\":{\"cached_tokens\":0},\"completion_tokens_details\":{\"reasoning_tokens\":1024},\"prompt_cache_hit_tokens\":0,\"prompt_cache_miss_tokens\":9}}\n\ndata: [DONE]\n"), true)
	annotateFitEvidence(http.StatusOK, evidence, map[string]any{"max_tokens": 1024})
	assert.True(t, fitExpected("K08", http.StatusOK, evidence))
}

func TestSummarizeStreamCountsUsageAndTermination(t *testing.T) {
	evidence := summarize([]byte("data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"x\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"2\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":1,\"total_tokens\":5}}\n\ndata: [DONE]\n"), true)

	assert.Equal(t, true, evidence["done"])
	assert.Equal(t, true, evidence["has_content"])
	assert.Equal(t, true, evidence["has_reasoning_content"])
	assert.Equal(t, 1, evidence["usage_events"])
	assert.Equal(t, true, evidence["usage_on_finish_event"])
	assert.Equal(t, 0, evidence["usage_only_events"])
	assert.Equal(t, "stop", evidence["finish_reason"])
}

func TestSummarizeRecordsFingerprintAndIntermediateUsageShapes(t *testing.T) {
	tests := []struct {
		name              string
		body              string
		fingerprintShape  string
		intermediateShape string
	}{
		{
			name:              "missing",
			body:              `{"choices":[{"message":{"content":"2"},"finish_reason":"stop"}]}`,
			fingerprintShape:  "missing",
			intermediateShape: "missing",
		},
		{
			name:              "null",
			body:              `{"system_fingerprint":null,"choices":[{"message":{"content":"2"},"finish_reason":"stop"}],"usage":null}`,
			fingerprintShape:  "null",
			intermediateShape: "null",
		},
		{
			name:              "string and object",
			body:              `{"system_fingerprint":"fp","choices":[{"message":{"content":"2"},"finish_reason":"stop"}],"usage":{}}`,
			fingerprintShape:  "string",
			intermediateShape: "object",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evidence := summarize([]byte(test.body), false)
			assert.Equal(t, test.fingerprintShape, evidence["system_fingerprint_shape"])
			assert.Equal(t, test.intermediateShape, evidence["intermediate_usage_shape"])
		})
	}
}

func TestSummarizeStreamTracksFingerprintConsistencyAndIntermediateUsageShape(t *testing.T) {
	consistent := summarize([]byte("data: {\"system_fingerprint\":\"fp\",\"choices\":[{\"delta\":{\"content\":\"2\"}}],\"usage\":null}\n\ndata: {\"system_fingerprint\":\"fp\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{}}\n\ndata: [DONE]\n"), true)
	assert.Equal(t, "string", consistent["system_fingerprint_shape"])
	assert.Equal(t, true, consistent["system_fingerprint_consistent"])
	assert.Equal(t, "null", consistent["intermediate_usage_shape"])

	inconsistent := summarize([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"2\"}}]}\n\ndata: {\"system_fingerprint\":null,\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n"), true)
	assert.Equal(t, "mixed", inconsistent["system_fingerprint_shape"])
	assert.Equal(t, false, inconsistent["system_fingerprint_consistent"])
	assert.Equal(t, "missing", inconsistent["intermediate_usage_shape"])

	cumulative := summarize([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"2\"}}],\"usage\":{}}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{}}\n\ndata: [DONE]\n"), true)
	assert.Equal(t, "object", cumulative["intermediate_usage_shape"])
}

func TestFitEvidenceMismatchComparesUsageAndFingerprintEvidence(t *testing.T) {
	official := map[string]any{
		"prompt_tokens":                 float64(10),
		"system_fingerprint_shape":      "string",
		"system_fingerprint_consistent": true,
		"intermediate_usage_shape":      "object",
	}
	gateway := map[string]any{
		"prompt_tokens":                 float64(11),
		"system_fingerprint_shape":      "string",
		"system_fingerprint_consistent": true,
		"intermediate_usage_shape":      "object",
	}
	assert.Equal(t, "prompt_tokens", fitEvidenceMismatch("K04", official, gateway))

	gateway["prompt_tokens"] = float64(10)
	gateway["system_fingerprint_shape"] = "null"
	assert.Equal(t, "system_fingerprint_shape", fitEvidenceMismatch("K04", official, gateway))

	gateway["system_fingerprint_shape"] = "string"
	gateway["intermediate_usage_shape"] = "mixed"
	assert.Equal(t, "intermediate_usage_shape", fitEvidenceMismatch("K04", official, gateway))
}

func TestFeatureProbeClientRejectsHTTPSDowngrade(t *testing.T) {
	client := newFeatureProbeClient()
	calls := 0
	client.Transport = probeRoundTripper(func(req *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{
			StatusCode: http.StatusFound,
			Header:     http.Header{"Location": []string{"http://insecure.example/"}},
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    req,
		}, nil
	})

	status, evidence, _, err := requestPayload(context.Background(), client, "https://example.test/v1", "", http.MethodGet, "/models", nil)

	assert.Error(t, err)
	assert.Equal(t, 0, status)
	assert.Equal(t, 1, calls)
	assert.NotEmpty(t, evidence["transport_error"])
}

func TestFitExpectedRejectsUsageOnlyStreamTail(t *testing.T) {
	officialShape := summarize([]byte("data: {\"id\":\"chat_1\",\"object\":\"chat.completion.chunk\",\"created\":1710000000,\"model\":\"deepseek-v4-flash\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"2\"},\"logprobs\":null,\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":1,\"total_tokens\":5,\"prompt_tokens_details\":{\"cached_tokens\":1},\"completion_tokens_details\":{\"reasoning_tokens\":0},\"prompt_cache_hit_tokens\":1,\"prompt_cache_miss_tokens\":3}}\n\ndata: [DONE]\n"), true)
	assert.True(t, fitExpected("K09", http.StatusOK, officialShape))
	assert.True(t, expectedPass("DS-B02", "official", http.StatusOK, officialShape))

	wrongChunk := summarize([]byte("data: {\"id\":\"chat_1\",\"object\":\"chat.completion\",\"created\":1710000000,\"model\":\"wrong-model\",\"choices\":[{\"index\":1,\"delta\":{\"role\":\"user\",\"content\":\"2\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":1,\"total_tokens\":5,\"prompt_tokens_details\":{\"cached_tokens\":1},\"completion_tokens_details\":{\"reasoning_tokens\":0},\"prompt_cache_hit_tokens\":1,\"prompt_cache_miss_tokens\":3}}\n\ndata: [DONE]\n"), true)
	assert.False(t, fitExpected("K09", http.StatusOK, wrongChunk))

	usageOnlyTail := summarize([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"2\"},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":1,\"total_tokens\":5}}\n\ndata: [DONE]\n"), true)
	assert.False(t, fitExpected("K09", http.StatusOK, usageOnlyTail))
	assert.False(t, expectedPass("DS-B02", "official", http.StatusOK, usageOnlyTail))

	aggregatorUsage := summarize([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"2\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":1,\"total_tokens\":5,\"input_tokens\":4}}\n\ndata: [DONE]\n"), true)
	assert.False(t, fitExpected("K09", http.StatusOK, aggregatorUsage))

	fractionalUsage := summarize([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"2\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":4.5,\"completion_tokens\":1.5,\"total_tokens\":6,\"prompt_tokens_details\":{\"cached_tokens\":1.5},\"completion_tokens_details\":{\"reasoning_tokens\":0.5},\"prompt_cache_hit_tokens\":1.5,\"prompt_cache_miss_tokens\":3}}\n\ndata: [DONE]\n"), true)
	assert.False(t, fitExpected("K09", http.StatusOK, fractionalUsage))
}

func TestFitExpectedRejectsMalformedOrPostDoneStreamEvents(t *testing.T) {
	usage := `"usage":{"prompt_tokens":4,"completion_tokens":1,"total_tokens":5,"prompt_tokens_details":{"cached_tokens":1},"completion_tokens_details":{"reasoning_tokens":0},"prompt_cache_hit_tokens":1,"prompt_cache_miss_tokens":3}`
	malformed := summarize([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"2\"}}]}\n\ndata: {not-json}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],"+usage+"}\n\ndata: [DONE]\n"), true)
	assert.False(t, fitExpected("K09", http.StatusOK, malformed))

	postDone := summarize([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"2\"}}]}\n\ndata: [DONE]\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],"+usage+"}\n"), true)
	assert.False(t, fitExpected("K09", http.StatusOK, postDone))
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
	assert.Equal(t, "text/event-stream", evidence["content_type"])
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
		"stream":                  true,
		"done":                    true,
		"done_events":             1,
		"done_last":               true,
		"sse_json_errors":         0,
		"stream_shape_valid":      true,
		"has_content":             true,
		"sse_events":              3,
		"usage_events":            1,
		"usage_only_events":       0,
		"usage_on_finish_event":   true,
		"usage_on_final_event":    true,
		"official_usage_shape":    true,
		"usage_consistent":        true,
		"cache_tokens_consistent": true,
	}
	assert.True(t, expectedPass("DS-B02", "official", 200, evidence))
	evidence["usage_only_events"] = 1
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
		"done_events":         1,
		"done_last":           true,
		"sse_json_errors":     0,
		"stream_shape_valid":  true,
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
		"stream":                  true,
		"done":                    true,
		"done_events":             1,
		"done_last":               true,
		"sse_json_errors":         0,
		"stream_shape_valid":      true,
		"has_error":               false,
		"has_content":             true,
		"has_logprobs":            true,
		"logprobs_content":        1,
		"logprobs_content_valid":  true,
		"max_top_logprobs":        5,
		"usage_events":            1,
		"usage_only_events":       0,
		"usage_on_finish_event":   true,
		"usage_on_final_event":    true,
		"official_usage_shape":    true,
		"usage_consistent":        true,
		"cache_tokens_consistent": true,
	}
	assert.True(t, expectedPass("DS-E05", "official", http.StatusOK, evidence))
	evidence["usage_on_final_event"] = false
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

func TestSummarizeUsageAcceptsOfficialDisabledThinkingShape(t *testing.T) {
	withoutReasoningDetails := summarize([]byte(`{"choices":[{"message":{"content":"spring"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":95,"total_tokens":104,"prompt_tokens_details":{"cached_tokens":0},"prompt_cache_hit_tokens":0,"prompt_cache_miss_tokens":9}}`), false)
	assert.Equal(t, true, withoutReasoningDetails["official_usage_shape"])
	assert.Equal(t, "completion_tokens,prompt_cache_hit_tokens,prompt_cache_miss_tokens,prompt_tokens,prompt_tokens_details,total_tokens|prompt_tokens_details:cached_tokens|completion_tokens_details:-", withoutReasoningDetails["usage_shape"])

	withReasoningDetails := summarize([]byte(`{"choices":[{"message":{"content":"2","reasoning_content":"think"},"finish_reason":"stop"}],"usage":{"prompt_tokens":8,"completion_tokens":30,"total_tokens":38,"prompt_tokens_details":{"cached_tokens":0},"completion_tokens_details":{"reasoning_tokens":28},"prompt_cache_hit_tokens":0,"prompt_cache_miss_tokens":8}}`), false)
	assert.Equal(t, true, withReasoningDetails["official_usage_shape"])
	assert.NotEqual(t, withoutReasoningDetails["usage_shape"], withReasoningDetails["usage_shape"])
}

func TestSummarizeLogprobsReportsMaximumTopEntries(t *testing.T) {
	evidence := summarize([]byte(`{"choices":[{"message":{"content":"2"},"finish_reason":"stop","logprobs":{"content":[{"token":"2","top_logprobs":[{},{},{}]}],"reasoning_content":[{"token":"thinking","top_logprobs":[{},{}]}]}}]}`), false)

	assert.Equal(t, 3, evidence["max_top_logprobs"])
	assert.Equal(t, 2, evidence["max_reasoning_top_logprobs"])
	assert.Equal(t, false, evidence["logprobs_content_valid"])
	assert.Equal(t, false, evidence["logprobs_reasoning_content_valid"])
}

func TestFitExpectedRejectsPlaceholderLogprobs(t *testing.T) {
	usage := `"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6,"prompt_tokens_details":{"cached_tokens":1},"completion_tokens_details":{"reasoning_tokens":1},"prompt_cache_hit_tokens":1,"prompt_cache_miss_tokens":3}`
	fake := summarize([]byte(`{"id":"chat_1","object":"chat.completion","created":1710000000,"model":"deepseek-v4-flash","choices":[{"index":0,"message":{"role":"assistant","content":"2","reasoning_content":"think"},"logprobs":{"content":[{}],"reasoning_content":[{}]},"finish_reason":"stop"}],`+usage+`}`), false)
	assert.False(t, fitExpected("K12", http.StatusOK, fake))

	valid := summarize([]byte(`{"id":"chat_1","object":"chat.completion","created":1710000000,"model":"deepseek-v4-flash","choices":[{"index":0,"message":{"role":"assistant","content":"2","reasoning_content":"think"},"logprobs":{"content":[{"token":"2","logprob":-0.1,"top_logprobs":[{"token":"2","logprob":-0.1}]}],"reasoning_content":[{"token":"think","logprob":-0.2,"top_logprobs":[{"token":"think","logprob":-0.2}]}]},"finish_reason":"stop"}],`+usage+`}`), false)
	assert.True(t, fitExpected("K12", http.StatusOK, valid))
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
	assert.False(t, expectedPass("DS-E04", "official", 422, evidence))
	assert.False(t, expectedPass("DS-E07", "official", 400, evidence))
}

func TestFitExpectedRequiresHTTP400ForLogprobsValidation(t *testing.T) {
	pairEvidence := map[string]any{
		"json":                      true,
		"has_error":                 true,
		"error_type":                "invalid_request_error",
		"error_code":                "invalid_request_error",
		"error_param_null":          true,
		"error_message_fingerprint": "invalid top_logprobs and logprobs value, logprobs must be set to true if top_logprobs is used.",
	}
	assert.True(t, fitExpected("K02", http.StatusBadRequest, pairEvidence))
	assert.False(t, fitExpected("K02", http.StatusUnprocessableEntity, pairEvidence))
	pairEvidence["error_message_fingerprint"] = "invalid top_logprobs and logprobs value, logprobs must be set to true if top_logprobs is used. (request id: redacted)"
	assert.False(t, fitExpected("K02", http.StatusBadRequest, pairEvidence))

	rangeEvidence := map[string]any{
		"json":                      true,
		"has_error":                 true,
		"error_type":                "invalid_request_error",
		"error_code":                "invalid_request_error",
		"error_param_null":          true,
		"error_message_fingerprint": "invalid top_logprobs value, the valid range of top_logprobs is [0, 20].",
	}
	assert.True(t, fitExpected("K03", http.StatusBadRequest, rangeEvidence))
	assert.False(t, fitExpected("K03", http.StatusUnprocessableEntity, rangeEvidence))
}

func TestApplyFitOfficialBaselineGatesGatewayPasses(t *testing.T) {
	withoutOfficial := []result{{CaseID: "K01", Route: "main", Tier: "gateway-live", HTTP: http.StatusOK, Status: "pass", Evidence: map[string]any{}}}
	applyFitOfficialBaseline(withoutOfficial, false)
	assert.Equal(t, "inconclusive", withoutOfficial[0].Status)
	assert.Equal(t, "official_baseline_not_requested", withoutOfficial[0].Evidence["reason"])

	paired := []result{
		{CaseID: "K02", Route: "official", Tier: "official-live", HTTP: http.StatusBadRequest, Status: "pass", Evidence: map[string]any{"json": true, "has_error": true, "error_type": "invalid_request_error", "error_code": "invalid_request_error", "error_param_null": true, "error_message_fingerprint": "same"}},
		{CaseID: "K02", Route: "main", Tier: "gateway-live", HTTP: http.StatusBadRequest, Status: "pass", Evidence: map[string]any{"json": true, "has_error": true, "error_type": "invalid_request_error", "error_code": "invalid_request_error", "error_param_null": true, "error_message_fingerprint": "same"}},
	}
	applyFitOfficialBaseline(paired, true)
	assert.Equal(t, "pass", paired[1].Status)
	assert.Equal(t, true, paired[1].Evidence["official_baseline_matched"])

	httpMismatch := []result{
		{CaseID: "K03", Route: "official", Tier: "official-live", HTTP: http.StatusBadRequest, Status: "pass", Evidence: map[string]any{}},
		{CaseID: "K03", Route: "main", Tier: "gateway-live", HTTP: http.StatusUnprocessableEntity, Status: "pass", Evidence: map[string]any{}},
	}
	applyFitOfficialBaseline(httpMismatch, true)
	assert.Equal(t, "fail", httpMismatch[1].Status)
	assert.Equal(t, "official_http_status_mismatch", httpMismatch[1].Evidence["reason"])

	evidenceMismatch := []result{
		{CaseID: "K02", Route: "official", Tier: "official-live", HTTP: http.StatusBadRequest, Status: "pass", Evidence: map[string]any{"error_message_fingerprint": "official"}},
		{CaseID: "K02", Route: "main", Tier: "gateway-live", HTTP: http.StatusBadRequest, Status: "pass", Evidence: map[string]any{"error_message_fingerprint": "gateway"}},
	}
	applyFitOfficialBaseline(evidenceMismatch, true)
	assert.Equal(t, "fail", evidenceMismatch[1].Status)
	assert.Equal(t, "official_contract_mismatch", evidenceMismatch[1].Evidence["reason"])

	nondeterministicOutput := []result{
		{CaseID: "K07", Route: "official", Tier: "official-live", HTTP: http.StatusOK, Status: "pass", Evidence: map[string]any{"has_content": false, "has_reasoning_content": true, "finish_reason": "length"}},
		{CaseID: "K07", Route: "main", Tier: "gateway-live", HTTP: http.StatusOK, Status: "pass", Evidence: map[string]any{"has_content": true, "has_reasoning_content": true, "finish_reason": "stop"}},
	}
	applyFitOfficialBaseline(nondeterministicOutput, true)
	assert.Equal(t, "pass", nondeterministicOutput[1].Status)

	usageShapeMismatch := []result{
		{CaseID: "K05", Route: "official", Tier: "official-live", HTTP: http.StatusOK, Status: "pass", Evidence: map[string]any{"usage_shape": "official-disabled"}},
		{CaseID: "K05", Route: "main", Tier: "gateway-live", HTTP: http.StatusOK, Status: "pass", Evidence: map[string]any{"usage_shape": "seven-fields"}},
	}
	applyFitOfficialBaseline(usageShapeMismatch, true)
	assert.Equal(t, "fail", usageShapeMismatch[1].Status)
	assert.Equal(t, "usage_shape", usageShapeMismatch[1].Evidence["mismatch_field"])
}

func TestFitExpectedAcceptsToolCallWithThinkingDisabled(t *testing.T) {
	evidence := summarize([]byte(`{"id":"chat_1","object":"chat.completion","created":1710000000,"model":"deepseek-v4-flash","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"北京\"}"}}]},"logprobs":null,"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":20,"completion_tokens":4,"total_tokens":24,"prompt_tokens_details":{"cached_tokens":0},"completion_tokens_details":{"reasoning_tokens":0},"prompt_cache_hit_tokens":0,"prompt_cache_miss_tokens":20}}`), false)

	assert.True(t, fitExpected("DS-K10", http.StatusOK, evidence))

	wrongTool := summarize([]byte(`{"id":"chat_1","object":"chat.completion","created":1710000000,"model":"deepseek-v4-flash","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},"logprobs":null,"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":20,"completion_tokens":4,"total_tokens":24,"prompt_tokens_details":{"cached_tokens":0},"completion_tokens_details":{"reasoning_tokens":0},"prompt_cache_hit_tokens":0,"prompt_cache_miss_tokens":20}}`), false)
	assert.False(t, fitExpected("DS-K10", http.StatusOK, wrongTool))
}

func TestFitChecksCoverProvidedRequests(t *testing.T) {
	checks := fitChecks("deepseek-v4-flash")
	expected := []fitCheck{
		{"K01", map[string]any{"stream": false, "messages": []any{map[string]any{"role": "user", "content": "依次说出：苹果、香蕉、橙子、西瓜。"}}, "stop": "香蕉", "max_tokens": 256, "reasoning_effort": "low", "model": "deepseek-v4-flash"}},
		{"K02", map[string]any{"stream": false, "messages": []any{map[string]any{"role": "user", "content": "1+1=?"}}, "top_logprobs": 5, "max_tokens": 64, "reasoning_effort": "low", "model": "deepseek-v4-flash"}},
		{"K03", map[string]any{"stream": false, "messages": []any{map[string]any{"role": "user", "content": "1+1=?"}}, "logprobs": true, "top_logprobs": 21, "max_tokens": 64, "reasoning_effort": "low", "model": "deepseek-v4-flash"}},
		{"K04", map[string]any{"stream": false, "messages": []any{map[string]any{"role": "user", "content": "1+1=?"}}, "max_tokens": 393216, "reasoning_effort": "low", "model": "deepseek-v4-flash"}},
		{"K05", map[string]any{"stream": false, "messages": []any{map[string]any{"role": "user", "content": "用一个词描述春天。"}}, "temperature": 0, "max_tokens": 256, "thinking": map[string]string{"type": "disabled"}, "model": "deepseek-v4-flash"}},
		{"K06", map[string]any{"stream": false, "messages": []any{map[string]any{"role": "user", "content": "你好"}}, "frequency_penalty": 2, "presence_penalty": 2, "max_tokens": 64, "reasoning_effort": "low", "model": "deepseek-v4-flash"}},
		{"K07", map[string]any{"stream": false, "messages": []any{map[string]any{"role": "system", "name": "teacher", "content": "你是一位数学老师。"}, map[string]any{"role": "user", "name": "student_a", "content": "1+1=?"}}, "max_tokens": 256, "reasoning_effort": "low", "model": "deepseek-v4-flash"}},
		{"K08", map[string]any{"messages": []any{map[string]any{"role": "user", "content": "用一个词描述秋天。"}}, "temperature": 2, "top_p": 0.1, "presence_penalty": 1.5, "frequency_penalty": 1.5, "max_tokens": 1024, "reasoning_effort": "low", "model": "deepseek-v4-flash", "stream": true, "stream_options": map[string]any{"include_usage": true}}},
		{"K09", map[string]any{"messages": []any{map[string]any{"role": "user", "content": "1+1=?"}}, "max_tokens": 1024, "reasoning_effort": "low", "model": "deepseek-v4-flash", "stream": true, "stream_options": map[string]any{"include_usage": true}}},
		{"K10", map[string]any{"stream": false, "messages": []any{map[string]any{"role": "user", "content": "北京今天天气怎么样？"}}, "tools": []any{map[string]any{"type": "function", "function": map[string]any{"name": "get_weather", "description": "查询指定城市的当前天气", "parameters": map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string", "description": "城市名，例如 北京"}}, "required": []string{"city"}}}}}, "tool_choice": "auto", "max_tokens": 1024, "thinking": map[string]string{"type": "disabled"}, "model": "deepseek-v4-flash"}},
		{"K11", map[string]any{"stream": false, "messages": []any{map[string]any{"role": "user", "content": "依次说出：苹果、香蕉、橙子、西瓜。"}}, "stop": "香蕉", "max_tokens": 256, "reasoning_effort": "low", "model": "deepseek-v4-flash"}},
		{"K12", map[string]any{"stream": false, "messages": []any{map[string]any{"role": "user", "content": "1+1=?"}}, "logprobs": true, "top_logprobs": 5, "max_tokens": 1024, "reasoning_effort": "low", "model": "deepseek-v4-flash"}},
		{"K13", map[string]any{"messages": []any{map[string]any{"role": "user", "content": "1+1=?"}}, "max_tokens": 1024, "reasoning_effort": "low", "model": "deepseek-v4-flash", "stream": true, "stream_options": map[string]any{"include_usage": true}}},
	}
	assert.Equal(t, expected, checks)
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

func TestAnnotateFitEvidenceChecksRequestedMaxTokens(t *testing.T) {
	evidence := map[string]any{"completion_tokens": float64(65), "has_content": true}
	annotateFitEvidence(http.StatusOK, evidence, map[string]any{"max_tokens": 64})
	assert.Equal(t, false, evidence["completion_within_requested_max"])
	assert.Equal(t, 64, evidence["requested_max_tokens"])
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
