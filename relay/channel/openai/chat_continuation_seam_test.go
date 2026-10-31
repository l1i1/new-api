package openai

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSeamDeduperStripsRepeatedRoundPrefix(t *testing.T) {
	d := newSeamDeduper("the quick brown fox jumps")
	require.NotNil(t, d)
	// The model restarts from part of the delivered tail, then continues.
	text, ready := d.feed("brown fox jumps over the lazy dog")
	require.False(t, ready, "a round shorter than the lookahead is held until it ends")
	assert.Empty(t, text)
	out, ready := d.finish()
	require.True(t, ready)
	assert.Equal(t, " over the lazy dog", out, "the repeated tail must be dropped, the new text kept")
}

func TestSeamDeduperKeepsShortOverlap(t *testing.T) {
	d := newSeamDeduper("the quick brown fox jumps")
	text, _ := d.feed("jumps again")
	out, _ := d.finish()
	assert.Equal(t, "", text)
	// "jumps" is five runes, below seamMinOverlap: a legitimate continuation
	// must not be truncated.
	assert.Equal(t, "jumps again", out)
}

func TestSeamDeduperHoldsUntilLookaheadThenStreams(t *testing.T) {
	d := newSeamDeduper(strings.Repeat("A", 40))
	// The first deltas are held while the deduper collects its lookahead.
	held, ready := d.feed(strings.Repeat("A", 40))
	require.False(t, ready)
	assert.Empty(t, held)
	emit, ready := d.feed(strings.Repeat("B", 300))
	require.True(t, ready)
	assert.Equal(t, strings.Repeat("B", 300), emit, "the 40-rune overlap is stripped and the new text follows")
	// After the decision every delta is forwarded untouched.
	tail, ready := d.feed("C")
	assert.True(t, ready)
	assert.Equal(t, "C", tail)
}

func TestSeamDeduperNilWhenNothingDelivered(t *testing.T) {
	assert.Nil(t, newSeamDeduper(""))
}

// TestOaiStreamHandlerStripsRepeatedContinuationSeam pins the client-visible
// contract: when the resumed round repeats the tail of the delivered answer, the
// client sees that text once, and the response stays a single stream.
func TestOaiStreamHandlerStripsRepeatedContinuationSeam(t *testing.T) {
	first := strings.Join([]string{
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"role":"assistant","content":"First part of the answer ends with brown fox jumps"},"finish_reason":null}]}`,
		`data: {"id":"chat_1","object":"chat.completion.chunk","created":1,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":"length"}],"usage":{"prompt_tokens":5,"completion_tokens":12,"total_tokens":17}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	second := strings.Join([]string{
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{"content":"brown fox jumps over the lazy dog."},"finish_reason":null}]}`,
		`data: {"id":"chat_2","object":"chat.completion.chunk","created":2,"model":"kimi-k3.1-pro","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":8,"total_tokens":17}}`,
		`data: [DONE]`,
		``,
	}, "\n")

	c, info, recorder := newContinuationTestContext(t, func(partial string, remaining int) (*http.Response, error) {
		return continuationSSEResponse(second), nil
	}, 2, 64)
	info.ChatContinuation.Send = func(partial string, remaining int) (*http.Response, error) {
		return continuationSSEResponse(second), nil
	}

	usage, err := OaiStreamHandler(c, info, continuationSSEResponse(first))
	// err is a *types.NewAPIError; a nil pointer must be asserted as nil, not
	// through require.NoError, which would see a typed non-nil interface.
	require.Nil(t, err)
	require.NotNil(t, usage)

	delivered := recorder.Body.String()
	assert.Equal(t, 1, strings.Count(delivered, "brown fox jumps"), "the repeated seam must be delivered once")
	assert.Contains(t, delivered, " over the lazy dog.")
	assert.Equal(t, 1, strings.Count(delivered, `"finish_reason":"stop"`))
	assert.Equal(t, 1, strings.Count(delivered, "[DONE]"))
	require.NotNil(t, info.ChatContinuationAudit, "the log row must carry the continuation audit")
	assert.Equal(t, []string{"length"}, info.ChatContinuationAudit.Modes)
	assert.Equal(t, 1, info.ChatContinuationAudit.Rounds)
	assert.Equal(t, 2, info.ChatContinuationAudit.MaxSteps)
	assert.Equal(t, "stop", info.ChatContinuationAudit.Finish)
	assert.Equal(t, 8, info.ChatContinuationAudit.ExtraCompletionTokens,
		"only the resumed round's completion tokens are extra")
}

// TestSeamDeduperDropsFromTheStartRestart pins the rotation hazard found in
// review: a different channel may regenerate the answer from its beginning
// instead of continuing it. A tail-only overlap would forward the whole prefix
// twice once the answer is longer than the lookahead window.
func TestSeamDeduperDropsFromTheStartRestart(t *testing.T) {
	delivered := strings.Repeat("甲乙丙丁戊己庚辛壬癸", 60) // 600 runes
	d := newSeamDeduper(delivered)
	require.NotNil(t, d)

	var out strings.Builder
	regenerated := []rune(delivered + "新的后半段")
	for i := 0; i < len(regenerated); i += 37 {
		end := i + 37
		if end > len(regenerated) {
			end = len(regenerated)
		}
		if text, ready := d.feed(string(regenerated[i:end])); ready {
			out.WriteString(text)
		}
	}
	if text, ready := d.finish(); ready {
		out.WriteString(text)
	}
	require.Equal(t, "新的后半段", out.String(), "the regenerated prefix is dropped exactly once")
}

// TestSeamDeduperDropsLongTailRepeat pins the second review hazard: a repeat
// longer than the lookahead window must keep being dropped instead of leaking the
// part past the window edge into the client stream.
func TestSeamDeduperDropsLongTailRepeat(t *testing.T) {
	delivered := strings.Repeat("一二三四五六七八九十", 40) // 400 runes
	repeatLen := 300
	tail := delivered[len(delivered)-repeatLen:]
	d := newSeamDeduper(delivered)

	var out strings.Builder
	tailRunes := []rune(tail)
	for i := 0; i < len(tailRunes); i += 40 {
		end := i + 40
		if end > len(tailRunes) {
			end = len(tailRunes)
		}
		if text, ready := d.feed(string(tailRunes[i:end])); ready {
			out.WriteString(text)
		}
	}
	if text, ready := d.finish(); ready {
		out.WriteString(text)
	}
	require.Empty(t, out.String(), "a repeat longer than the window must not leak past its edge")

	text, ready := d.feed("继续正文")
	if !ready {
		text, ready = d.finish()
	}
	require.True(t, ready)
	require.Equal(t, "继续正文", text)
}

// TestSeamDeduperKeepsLegitimateContinuation makes sure the restart heuristics
// do not eat an answer that genuinely continues the text.
func TestSeamDeduperKeepsLegitimateContinuation(t *testing.T) {
	delivered := "这是一个已经发出的答案前缀，用来测试接缝去重。"
	d := newSeamDeduper(delivered)
	continuation := "后续的内容从这里开始，应当完整保留。"
	var out strings.Builder
	continuationRunes := []rune(continuation)
	for i := 0; i < len(continuationRunes); i += 6 {
		end := i + 6
		if end > len(continuationRunes) {
			end = len(continuationRunes)
		}
		if text, ready := d.feed(string(continuationRunes[i:end])); ready {
			out.WriteString(text)
		}
	}
	if text, ready := d.finish(); ready {
		out.WriteString(text)
	}
	require.Equal(t, continuation, out.String())
}
