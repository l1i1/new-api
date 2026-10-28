package controller

import (
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func calibrationShape(t *testing.T, behavior string) fitCalibrationShape {
	t.Helper()
	for _, shape := range fitCalibrationShapes() {
		if shape.behavior == behavior {
			return shape
		}
	}
	t.Fatalf("no calibration shape for %s", behavior)
	return fitCalibrationShape{}
}

// TestFitCalibrationSignatures reads the structural fields out of a relay
// answer without keeping the text.
func TestFitCalibrationSignatures(t *testing.T) {
	body := []byte(`{"choices":[{"finish_reason":"tool_calls","logprobs":{"content":[]},` +
		`"message":{"content":"{\"ok\":true}","reasoning_content":"thinking","tool_calls":[{"id":"1"}]}}],` +
		`"usage":{"prompt_tokens":123,"completion_tokens":45}}`)
	usage := &dto.Usage{PromptTokens: 123, CompletionTokens: 45}
	result := testResult{usage: usage, upstreamStatus: http.StatusOK, body: body}
	sig := fitCalibrationSignatureOf(result)
	assert.Equal(t, http.StatusOK, sig.status)
	assert.True(t, sig.accepted)
	assert.True(t, sig.content)
	assert.True(t, sig.contentJSON)
	assert.True(t, sig.toolCalls)
	assert.True(t, sig.logprobs)
	assert.True(t, sig.reasoning)
	assert.Equal(t, "tool_calls", sig.finishReason)
	assert.Equal(t, 123, sig.promptTokens)

	// A transport failure or a non-JSON body carries no structural evidence.
	empty := fitCalibrationSignatureOf(testResult{upstreamStatus: 0})
	assert.False(t, empty.accepted)
	notJSON := fitCalibrationSignatureOf(testResult{upstreamStatus: http.StatusOK, body: []byte("<html>")})
	assert.False(t, notJSON.accepted)
}

// TestFitCalibrationClassifyStructure pins the structural rules: presence and
// absence are compared against the official reference, a rejection is drift,
// and a cut answer is never attributed to the channel.
func TestFitCalibrationClassifyStructure(t *testing.T) {
	shape := calibrationShape(t, "tools.choice_semantics")
	officialWithTool := fitCalibrationSignature{status: http.StatusOK, accepted: true, toolCalls: true, finishReason: "tool_calls"}

	// Both sides serve the shape and the mark says supported: consistent.
	verdict, measured, _ := classifyFitCalibration(shape, true,
		fitCalibrationSignature{status: http.StatusOK, accepted: true, toolCalls: true, finishReason: "tool_calls"},
		officialWithTool, fitCalibrationSignature{}, fitCalibrationSignature{})
	require.Equal(t, fitCalibrationConsistent, verdict)
	require.NotNil(t, measured)
	assert.True(t, *measured)

	// The official reference serves it, the channel does not: drift to false.
	verdict, measured, reason := classifyFitCalibration(shape, true,
		fitCalibrationSignature{status: http.StatusOK, accepted: true, finishReason: "stop"},
		officialWithTool, fitCalibrationSignature{}, fitCalibrationSignature{})
	require.Equal(t, fitCalibrationDrift, verdict)
	require.NotNil(t, measured)
	assert.False(t, *measured)
	assert.Contains(t, reason, "official reference produced")

	// The same absence after hitting the probe's own cap is undecidable.
	verdict, _, _ = classifyFitCalibration(shape, true,
		fitCalibrationSignature{status: http.StatusOK, accepted: true, finishReason: "length"},
		officialWithTool, fitCalibrationSignature{}, fitCalibrationSignature{})
	assert.Equal(t, fitCalibrationInconclusive, verdict)

	// A rejection of a shape the official endpoint serves is drift.
	verdict, measured, _ = classifyFitCalibration(shape, true,
		fitCalibrationSignature{status: http.StatusBadRequest},
		officialWithTool, fitCalibrationSignature{}, fitCalibrationSignature{})
	require.Equal(t, fitCalibrationDrift, verdict)
	require.NotNil(t, measured)
	assert.False(t, *measured)

	// A mark that denies a structure the channel now produces is drift to true.
	verdict, measured, _ = classifyFitCalibration(shape, false,
		fitCalibrationSignature{status: http.StatusOK, accepted: true, toolCalls: true, finishReason: "tool_calls"},
		officialWithTool, fitCalibrationSignature{}, fitCalibrationSignature{})
	require.Equal(t, fitCalibrationDrift, verdict)
	require.NotNil(t, measured)
	assert.True(t, *measured)

	// Without an official answer there is nothing to compare.
	verdict, _, _ = classifyFitCalibration(shape, true,
		fitCalibrationSignature{status: http.StatusOK, accepted: true, toolCalls: true},
		fitCalibrationSignature{status: http.StatusBadGateway}, fitCalibrationSignature{}, fitCalibrationSignature{})
	assert.Equal(t, fitCalibrationInconclusive, verdict)
}

// TestFitCalibrationClassifyThinking pins the accounting direction of the
// thinking mark: the official endpoint does not reason on a thinking-disabled
// request, so a channel that does is drift.
func TestFitCalibrationClassifyThinking(t *testing.T) {
	shape := calibrationShape(t, "usage.thinking_counting")
	official := fitCalibrationSignature{status: http.StatusOK, accepted: true, reasoning: false}

	verdict, measured, _ := classifyFitCalibration(shape, true,
		fitCalibrationSignature{status: http.StatusOK, accepted: true, reasoning: true},
		official, fitCalibrationSignature{}, fitCalibrationSignature{})
	require.Equal(t, fitCalibrationDrift, verdict)
	require.NotNil(t, measured)
	assert.False(t, *measured)

	verdict, _, _ = classifyFitCalibration(shape, true,
		fitCalibrationSignature{status: http.StatusOK, accepted: true, reasoning: false},
		official, fitCalibrationSignature{}, fitCalibrationSignature{})
	assert.Equal(t, fitCalibrationConsistent, verdict)

	// If the official leg itself reasoned, the probe cannot discriminate.
	verdict, _, _ = classifyFitCalibration(shape, true,
		fitCalibrationSignature{status: http.StatusOK, accepted: true, reasoning: true},
		fitCalibrationSignature{status: http.StatusOK, accepted: true, reasoning: true},
		fitCalibrationSignature{}, fitCalibrationSignature{})
	assert.Equal(t, fitCalibrationInconclusive, verdict)
}

// TestFitCalibrationClassifyAccounting pins the delta rule for the two marks
// whose only evidence is the prompt-token delta against a bare reference.
func TestFitCalibrationClassifyAccounting(t *testing.T) {
	shape := calibrationShape(t, "history.assistant_first")
	official := fitCalibrationSignature{status: http.StatusOK, accepted: true, promptTokens: 111}
	officialRef := fitCalibrationSignature{status: http.StatusOK, accepted: true, promptTokens: 86}
	channelRef := fitCalibrationSignature{status: http.StatusOK, accepted: true, promptTokens: 4}

	// Same delta, different tokenizer offset: consistent.
	verdict, measured, _ := classifyFitCalibration(shape, true,
		fitCalibrationSignature{status: http.StatusOK, accepted: true, promptTokens: 29},
		official, channelRef, officialRef)
	require.Equal(t, fitCalibrationConsistent, verdict)
	require.NotNil(t, measured)
	assert.True(t, *measured)

	// A channel that does not count the assistant turn diverges from +25.
	verdict, measured, _ = classifyFitCalibration(shape, true,
		fitCalibrationSignature{status: http.StatusOK, accepted: true, promptTokens: 4},
		official, channelRef, officialRef)
	require.Equal(t, fitCalibrationDrift, verdict)
	require.NotNil(t, measured)
	assert.False(t, *measured)

	// Without a reference arm the delta cannot be computed.
	verdict, _, _ = classifyFitCalibration(shape, true,
		fitCalibrationSignature{status: http.StatusOK, accepted: true, promptTokens: 111},
		official, fitCalibrationSignature{}, fitCalibrationSignature{})
	assert.Equal(t, fitCalibrationInconclusive, verdict)
}

// TestFitCalibrationCoversTheDecidableMarks keeps the sweep scope honest: the
// six chat marks are covered, and the two Responses-API dimensions are not
// (they need a different relay format and stay with the external applier).
func TestFitCalibrationCoversTheDecidableMarks(t *testing.T) {
	covered := map[string]bool{}
	for _, shape := range fitCalibrationShapes() {
		covered[shape.behavior] = true
	}
	for _, behavior := range []string{
		"usage.thinking_counting", "tools.choice_semantics", "response_format.json",
		"logprobs.present", "history.assistant_first", "tools.dynamic_names",
	} {
		assert.True(t, covered[behavior], behavior+" must be covered")
	}
	for _, behavior := range []string{"responses.endpoint", "prompt_cache_options"} {
		assert.False(t, covered[behavior], behavior+" needs a different relay format")
	}
}

// TestFitCalibrationWriteGateDefaultsOff documents the safety property: the
// scheduled sweep is read-only unless the operator opts in.
func TestFitCalibrationWriteGateDefaultsOff(t *testing.T) {
	assert.False(t, common.GetEnvOrDefaultBool("FIT_CAPABILITY_CALIBRATION_WRITE", false))
	assert.True(t, (fitCalibrationHandler{}).Enabled())
	assert.Equal(t, fitCalibrationInterval, (fitCalibrationHandler{}).Interval())
}
