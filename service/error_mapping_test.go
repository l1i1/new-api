package service

import (
	"errors"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/require"
)

// TestResetStatusCodeConditionedMapping pins the 2026-10-29 channel-91 shape: a
// channel can relabel one upstream wording without relabelling the rest of its
// answers in the same status class.
func TestResetStatusCodeConditionedMapping(t *testing.T) {
	const mapping = `{"502":{"to":400,"when_message_contains":["上下文长度超过模型限制"]}}`

	overlong := types.NewOpenAIError(
		errors.New("上下文长度超过模型限制，请裁剪对话历史后重试。"),
		types.ErrorCodeBadResponseStatusCode,
		http.StatusBadGateway,
	)
	ResetStatusCode(overlong, mapping)
	require.Equal(t, http.StatusBadRequest, overlong.StatusCode,
		"the conditioned wording is relabelled to 400")

	streamFailure := types.NewOpenAIError(
		errors.New("upstream stream ended without a terminal finish_reason"),
		types.ErrorCodeBadResponseStatusCode,
		http.StatusBadGateway,
	)
	ResetStatusCode(streamFailure, mapping)
	require.Equal(t, http.StatusBadGateway, streamFailure.StatusCode,
		"a real stream failure keeps its 502")

	// The historical unconditional form is unchanged.
	unconditional := types.NewOpenAIError(
		errors.New("anything"),
		types.ErrorCodeBadResponseStatusCode,
		http.StatusBadGateway,
	)
	ResetStatusCode(unconditional, `{"502":400}`)
	require.Equal(t, http.StatusBadRequest, unconditional.StatusCode)

	// A malformed condition must not relabel anything.
	malformed := types.NewOpenAIError(
		errors.New("上下文长度超过模型限制，请裁剪对话历史后重试。"),
		types.ErrorCodeBadResponseStatusCode,
		http.StatusBadGateway,
	)
	ResetStatusCode(malformed, `{"502":{"to":400,"when_message_contains":"上下文长度超过模型限制"}}`)
	require.Equal(t, http.StatusBadGateway, malformed.StatusCode)
}
