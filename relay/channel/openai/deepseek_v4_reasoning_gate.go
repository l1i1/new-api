package openai

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
)

// The reasoning gate closes the last fit hole of the selective official-fit
// routing: aggregator pools nondeterministically drop reasoning_content on
// thinking-mode responses (live evidence 2026-09-06, CN channels 1/2/9/11),
// which official never does. A fit user (Shape) expecting thinking output who
// is served by a non-official channel gets the response rejected so the retry
// chain can land on a channel that reproduces official bytes — the official
// upstream itself is exempt (its response defines the contract).

// deepSeekV4ReasoningGateHoldLimit caps how many bytes the streaming gate may
// hold while undecided. Reaching it releases the stream ungated: a degenerate
// upstream that keeps sending non-classifiable frames must not wedge forever.
const deepSeekV4ReasoningGateHoldLimit = 1 << 20

// newDeepSeekV4ReasoningGateBody wraps an upstream SSE stream with the
// reasoning gate, or returns the body unchanged when the gate does not apply
// (official channel, non-fit user, suppressed reasoning, thinking disabled).
func newDeepSeekV4ReasoningGateBody(info *relaycommon.RelayInfo, body io.ReadCloser) io.ReadCloser {
	if body == nil || !deepSeekV4ReasoningGateApplies(info) {
		return body
	}
	return &deepSeekV4ReasoningGateReader{src: body}
}

// deepSeekV4ReasoningGateApplies reports whether thinking output is expected
// from this request and the response needs gate protection.
func deepSeekV4ReasoningGateApplies(info *relaycommon.RelayInfo) bool {
	if info == nil || isOfficialDeepSeekV4Upstream(info) || shouldSuppressReasoningContent(info) {
		return false
	}
	if !deepSeekV4FitEnabled(info) {
		return false
	}
	return deepSeekV4RequestThinkingExpected(info)
}

// deepSeekV4RequestThinkingExpected mirrors the official no-thinking states
// for the request: thinking.type=disabled or reasoning_effort=none mean the
// upstream legitimately emits no reasoning_content.
func deepSeekV4RequestThinkingExpected(info *relaycommon.RelayInfo) bool {
	request, ok := info.Request.(*dto.GeneralOpenAIRequest)
	if !ok {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(request.ReasoningEffort), "none") {
		return false
	}
	return !deepSeekThinkingDisabled(request.THINKING)
}

// deepSeekV4ReasoningGateReader holds SSE bytes until it can decide whether
// the upstream honors the thinking contract: a reasoning delta must appear
// before any content delta. Until then nothing reaches the client, so a
// violation aborts with zero bytes written and the retry chain (writer not
// committed) moves to another channel. Byte streams that pass are forwarded
// verbatim.
type deepSeekV4ReasoningGateReader struct {
	src           io.ReadCloser
	srcBuf        []byte
	held          []byte
	seenReasoning bool
	decided       bool
	violated      bool
	heldBytes     int
}

func (g *deepSeekV4ReasoningGateReader) Read(p []byte) (int, error) {
	for {
		if g.violated {
			common.SysLog("deepseek v4 reasoning gate: upstream emitted content before reasoning_content in thinking mode; aborting stream for channel retry")
			return 0, io.EOF
		}
		if g.decided {
			if len(g.held) > 0 {
				n := copy(p, g.held)
				g.held = g.held[n:]
				return n, nil
			}
			if len(g.srcBuf) > 0 {
				// Bytes already pulled from the source but never classified
				// (e.g. the trailing blank SSE line after [DONE]).
				n := copy(p, g.srcBuf)
				g.srcBuf = g.srcBuf[n:]
				return n, nil
			}
			return g.src.Read(p)
		}
		if idx := bytes.IndexByte(g.srcBuf, '\n'); idx >= 0 {
			line := append([]byte(nil), g.srcBuf[:idx+1]...)
			g.srcBuf = g.srcBuf[idx+1:]
			g.holdLine(line)
			continue
		}
		tmp := make([]byte, 4096)
		n, err := g.src.Read(tmp)
		if n > 0 {
			g.srcBuf = append(g.srcBuf, tmp[:n]...)
		}
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				// Source ended without a verdict: release what was held and
				// let the downstream empty-output checks judge the stream.
				g.decided = true
				continue
			}
			return 0, err
		}
		if g.heldBytes > deepSeekV4ReasoningGateHoldLimit {
			g.decided = true
		}
	}
}

func (g *deepSeekV4ReasoningGateReader) holdLine(line []byte) {
	g.held = append(g.held, line...)
	g.heldBytes += len(line)
	payload, ok := sseDataPayload(line)
	if !ok {
		return
	}
	if bytes.Equal(payload, []byte("[DONE]")) {
		g.decided = true
		return
	}
	var event struct {
		Choices []dto.ChatCompletionsStreamResponseChoice `json:"choices"`
	}
	if err := common.Unmarshal(payload, &event); err != nil {
		return
	}
	for _, choice := range event.Choices {
		if strings.TrimSpace(choice.Delta.GetReasoningContent()) != "" {
			g.seenReasoning = true
			g.decided = true
			return
		}
		if strings.TrimSpace(choice.Delta.GetContentString()) != "" && !g.seenReasoning {
			g.violated = true
			return
		}
	}
}

// sseDataPayload extracts the payload of an SSE "data:" line, or reports false
// for any other frame (comments, blanks, event/field lines). Trailing newline
// handling stays with the raw line bytes; only the payload is trimmed.
func sseDataPayload(line []byte) ([]byte, bool) {
	trimmed := bytes.TrimLeft(line, " \t")
	if !bytes.HasPrefix(trimmed, []byte("data:")) {
		return nil, false
	}
	payload := bytes.TrimSpace(trimmed[len("data:"):])
	if len(payload) == 0 {
		return nil, false
	}
	return payload, true
}

func (g *deepSeekV4ReasoningGateReader) Close() error {
	return g.src.Close()
}

// requiresDeepSeekV4ReasoningOutput reports whether the non-stream response of
// this request must carry reasoning_content (fit user, thinking expected).
func requiresDeepSeekV4ReasoningOutput(info *relaycommon.RelayInfo) bool {
	if info == nil || info.RelayMode != relayconstant.RelayModeChatCompletions {
		return false
	}
	if isOfficialDeepSeekV4Upstream(info) || shouldSuppressReasoningContent(info) {
		return false
	}
	if !deepSeekV4FitEnabled(info) {
		return false
	}
	return deepSeekV4RequestThinkingExpected(info)
}

// responseHasReasoningOutput reports whether any choice carried non-empty
// reasoning_content. A reasoning-only completion (empty content) passes; the
// violation is reasoning missing entirely.
func responseHasReasoningOutput(choices []dto.OpenAITextResponseChoice) bool {
	for _, choice := range choices {
		if strings.TrimSpace(choice.Message.GetReasoningContent()) != "" {
			return true
		}
	}
	return false
}

func missingReasoningOutputError() *types.NewAPIError {
	return types.NewOpenAIError(
		errors.New("upstream did not return reasoning_content in thinking mode"),
		types.ErrorCodeChannelUnsupportedFeature,
		http.StatusBadGateway,
	)
}
