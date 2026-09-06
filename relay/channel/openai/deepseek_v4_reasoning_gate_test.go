package openai

import (
	"io"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	relaycommon "github.com/QuantumNous/new-api/relay/common"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func reasoningGateInfo() *relaycommon.RelayInfo {
	return &relaycommon.RelayInfo{
		OriginModelName: "deepseek-v4-flash",
		RelayMode:       0, // chat completions constant lives in relay/constant; set below
		Request:         &dto.GeneralOpenAIRequest{},
	}
}

func sseLines(lines ...string) io.ReadCloser {
	return io.NopCloser(strings.NewReader(strings.Join(lines, "")))
}

func readAll(t *testing.T, r io.Reader) string {
	t.Helper()
	out, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(out)
}

func TestDeepSeekV4ReasoningGateReaderPassOnReasoningFirst(t *testing.T) {
	lines := []string{
		"data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n",
		"data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"think\"}}]}\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\"2\"}}]}\n\n",
		"data: [DONE]\n\n",
	}
	g := &deepSeekV4ReasoningGateReader{src: sseLines(lines...)}
	out := readAll(t, g)
	assert.Equal(t, strings.Join(lines, ""), out)
}

func TestDeepSeekV4ReasoningGateReaderViolatesOnContentBeforeReasoning(t *testing.T) {
	lines := []string{
		"data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\"2\"}}]}\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\"more\"}}]}\n\n",
		"data: [DONE]\n\n",
	}
	g := &deepSeekV4ReasoningGateReader{src: sseLines(lines...)}
	// io.ReadAll treats the injected io.EOF as a normal terminator: the
	// violation surfaces as an EMPTY stream (zero bytes delivered), which the
	// downstream machinery reports as the retryable empty-output 502.
	out, err := io.ReadAll(g)
	assert.NoError(t, err)
	assert.Empty(t, out, "held bytes must never reach the client on violation")
}

func TestDeepSeekV4ReasoningGateReaderPassthroughWithoutThinkingSignals(t *testing.T) {
	// A stream that ends with neither reasoning nor content (role-only +
	// [DONE]) is released ungated: the downstream empty-output machinery owns
	// that judgment, and the tool-call-only shape must not false-positive.
	lines := []string{
		"data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\n",
		"data: [DONE]\n\n",
	}
	g := &deepSeekV4ReasoningGateReader{src: sseLines(lines...)}
	assert.Equal(t, strings.Join(lines, ""), readAll(t, g))
}

func TestDeepSeekV4ReasoningGateReaderToolCallStreamPasses(t *testing.T) {
	lines := []string{
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"id\":\"c1\",\"type\":\"function\",\"function\":{\"name\":\"f\",\"arguments\":\"{}\"}}]}}]}\n\n",
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n",
		"data: [DONE]\n\n",
	}
	g := &deepSeekV4ReasoningGateReader{src: sseLines(lines...)}
	assert.Equal(t, strings.Join(lines, ""), readAll(t, g))
}

func TestDeepSeekV4ReasoningGateReaderChunkedSourceIsReassembled(t *testing.T) {
	// SSE frames split across arbitrary read boundaries must be reassembled
	// before classification.
	payload := "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"abc\"}}]}\n\ndata: [DONE]\n\n"
	g := &deepSeekV4ReasoningGateReader{src: io.NopCloser(newOneByteReader(payload))}
	out := readAll(t, g)
	assert.Equal(t, payload, out)
}

type oneByteReader struct {
	s string
}

func newOneByteReader(s string) *oneByteReader { return &oneByteReader{s: s} }

func (r *oneByteReader) Read(p []byte) (int, error) {
	if len(r.s) == 0 {
		return 0, io.EOF
	}
	p[0] = r.s[0]
	r.s = r.s[1:]
	return 1, nil
}

func TestResponseHasReasoningOutput(t *testing.T) {
	assert.False(t, responseHasReasoningOutput([]dto.OpenAITextResponseChoice{{Message: dto.Message{Role: "assistant", Content: "2"}}}))
	empty := ""
	assert.False(t, responseHasReasoningOutput([]dto.OpenAITextResponseChoice{{Message: dto.Message{Role: "assistant", Content: "2", ReasoningContent: &empty}}}))
	reasoning := "think"
	assert.True(t, responseHasReasoningOutput([]dto.OpenAITextResponseChoice{{Message: dto.Message{Role: "assistant", Content: "", ReasoningContent: &reasoning}}}))
}

func TestMissingReasoningOutputErrorIsRetryable(t *testing.T) {
	err := missingReasoningOutputError()
	assert.False(t, types.IsSkipRetryError(err))
}
