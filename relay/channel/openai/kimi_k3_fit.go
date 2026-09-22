package openai

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/officialfit"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

// Kimi K3 response fit. The official contract was live-probed on 2026-09-21;
// the aggregator upstream (NeurVibe) diverges in three ways this layer
// repairs, and one way it cannot:
//
//  1. usage carries ~13 extra keys (credit, prompt_cache_*,
//     completion_thinking_tokens, cache_read_input_tokens, ...) and its
//     prompt_tokens_details/completion_tokens_details objects carry five keys
//     where official sends one or two. Official key order is preserved:
//     prompt_tokens, completion_tokens, total_tokens, cached_tokens (only when
//     > 0), completion_tokens_details (only when reasoning), then
//     prompt_tokens_details with cached_tokens (only when > 0) and
//     cache_write_tokens.
//  2. stream usage arrives as a top-level `usage` key on the final chunk,
//     while official always attaches it to choices[0].usage and never emits a
//     top-level usage. Stream chunks also carry choices[].logprobs on every
//     event where official carries it only when the request asked for it.
//  3. official stream chunks all carry a system_fingerprint. The upstream
//     sends none, and per the DeepSeek V4 precedent this layer never invents a
//     backend identity: the key stays absent and the divergence is recorded.
//
// Editing is surgical (same helpers as the V4 fit): key order and byte layout
// outside the touched values reach the client exactly as the upstream sent
// them.

// kimiK3OfficialTopLevelKeys is the official K3 envelope key set. The stream
// form additionally carries system_fingerprint (preserved when the upstream
// provides it, never synthesised).
var kimiK3OfficialTopLevelKeys = map[string]struct{}{
	"id":                 {},
	"object":             {},
	"created":            {},
	"model":              {},
	"choices":            {},
	"usage":              {},
	"system_fingerprint": {},
}

// kimiK3OfficialChoiceKeys is the official choice key set. `usage` appears only
// on the terminal stream chunk, `logprobs` only when the request asked for it.
var kimiK3OfficialChoiceKeys = map[string]struct{}{
	"index":         {},
	"message":       {},
	"delta":         {},
	"logprobs":      {},
	"finish_reason": {},
	"usage":         {},
}

// kimiK3OfficialMessageKeys is the official message key set (non-stream).
var kimiK3OfficialMessageKeys = map[string]struct{}{
	"role":              {},
	"content":           {},
	"reasoning_content": {},
	"tool_calls":        {},
}

// kimiK3OfficialDeltaKeys is the official delta key set (stream).
var kimiK3OfficialDeltaKeys = map[string]struct{}{
	"role":              {},
	"content":           {},
	"reasoning_content": {},
	"tool_calls":        {},
}

// kimiK3OfficialStreamChoiceKeys excludes logprobs: official omits the key
// unless the request asked for logprobs, and the upstream sends it on every
// event. A request that did ask keeps the upstream value untouched.
var kimiK3OfficialStreamChoiceKeys = map[string]struct{}{
	"index":         {},
	"delta":         {},
	"finish_reason": {},
}

// kimiK3OfficialStreamTopLevelKeys is the official top-level key set for a
// usage-carrying stream chunk. It differs from the non-stream set by omitting
// `usage`: official moves the aggregator's top-level usage into choices[0].
var kimiK3OfficialStreamTopLevelKeys = map[string]struct{}{
	"id":                 {},
	"object":             {},
	"created":            {},
	"model":              {},
	"choices":            {},
	"system_fingerprint": {},
}

// kimiK3FitEnabled reports whether the K3 response fit applies: the client
// asked for a kimi-k3 chat completion and enabled the Shape dimension.
func kimiK3FitEnabled(info *relaycommon.RelayInfo) bool {
	if info == nil || info.RelayMode != relayconstant.RelayModeChatCompletions || info.RelayFormat != types.RelayFormatOpenAI {
		return false
	}
	if officialfit.FamilyOf(info.OriginModelName) != officialfit.FamilyKimiK3 {
		return false
	}
	profile, ok := info.UserSetting.OfficialFitProfileFor(info.OriginModelName)
	return ok && profile.Shape
}

// FitKimiK3TextResponseBodyForAdapters exposes the K3 non-stream body fit to
// adapters that assemble their own client body.
func FitKimiK3TextResponseBodyForAdapters(c *gin.Context, info *relaycommon.RelayInfo, body []byte, usage *dto.Usage) []byte {
	if !kimiK3FitEnabled(info) {
		return body
	}
	fitted, err := fitKimiK3TextResponseBody(body, usage)
	if err != nil {
		if c != nil {
			logger.LogError(c, fmt.Sprintf("kimi k3 response fit rewrite failed: %v", err))
		}
		return body
	}
	return fitted
}

// FitKimiK3StreamEventForAdapters exposes the K3 stream-event fit to adapters
// that assemble their own stream chunks.
func FitKimiK3StreamEventForAdapters(c *gin.Context, info *relaycommon.RelayInfo, data string, usage *dto.Usage, final bool) string {
	if !kimiK3FitEnabled(info) {
		return data
	}
	patched, err := fitKimiK3StreamEvent(data, usage, final, kimiK3RequestWantsLogprobs(info))
	if err != nil {
		if c != nil {
			logger.LogError(c, fmt.Sprintf("kimi k3 stream fit rewrite failed: %v", err))
		}
		return data
	}
	return patched
}

// kimiK3RequestWantsLogprobs reports whether the request asked for logprobs, in
// which case the upstream's choice.logprobs value is official-shaped and kept.
func kimiK3RequestWantsLogprobs(info *relaycommon.RelayInfo) bool {
	request, ok := info.Request.(*dto.GeneralOpenAIRequest)
	return ok && request.LogProbs != nil && *request.LogProbs
}

// kimiK3UsageOnlyChunk is the official usage-only event: the terminal chunk's
// identity, no choices, the usage object at the top level. Field order matches
// the endpoint (id, object, created, model, choices, usage).
type kimiK3UsageOnlyChunk struct {
	ID      json.RawMessage `json:"id"`
	Object  string          `json:"object"`
	Created json.RawMessage `json:"created"`
	Model   json.RawMessage `json:"model"`
	Choices json.RawMessage `json:"choices"`
	Usage   json.RawMessage `json:"usage"`
}

// FitKimiK3StreamUsageOnlyChunk builds the usage-only event official Moonshot
// emits after the terminal chunk when the request asked for stream usage, or ""
// when it must not be sent.
//
// A standard OpenAI client reads its token counts from a top-level usage chunk,
// so dropping this event leaves every such client with no usage at all on K3 —
// the load bench measured 0/200 usage reports (TPM and cache-hit rate
// unmeasurable) purely because of it. Official sends the event only when the
// *client* asked (live-probed: an omitted stream_options and an explicit false
// both produce no top-level usage), which is why it is gated on the client's
// own flag rather than on info.ShouldIncludeUsage — that one defaults to true
// when the client asked nothing, and it is also the value the relay forces on
// for billing.
//
// terminalData is the terminal chunk *after* the fit ran, so the usage object
// is lifted from it verbatim: the two events cannot disagree, whatever the
// upstream reported.
func FitKimiK3StreamUsageOnlyChunk(info *relaycommon.RelayInfo, terminalData string) string {
	if !kimiK3FitEnabled(info) || terminalData == "" || !info.ClientIncludeUsage {
		return ""
	}
	var payload map[string]json.RawMessage
	if err := common.UnmarshalJsonStr(terminalData, &payload); err != nil {
		return ""
	}
	id, okID := payload["id"]
	created, okCreated := payload["created"]
	model, okModel := payload["model"]
	if !okID || !okCreated || !okModel {
		return ""
	}
	usage, ok := kimiK3FirstChoiceUsage(payload["choices"])
	if !ok {
		return ""
	}
	event, err := common.Marshal(kimiK3UsageOnlyChunk{
		ID:      id,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   model,
		Choices: json.RawMessage(`[]`),
		Usage:   usage,
	})
	if err != nil {
		return ""
	}
	return string(event)
}

// kimiK3FirstChoiceUsage lifts choices[0].usage out of a fitted terminal chunk.
func kimiK3FirstChoiceUsage(rawChoices json.RawMessage) (json.RawMessage, bool) {
	spans, ok := jsonArrayElementSpans(rawChoices)
	if !ok || len(spans) == 0 {
		return nil, false
	}
	first := rawChoices[spans[0][0]:spans[0][1]]
	pairs, _, err := parseTopLevelPairs(first)
	if err != nil {
		return nil, false
	}
	found, present, err := findJSONPair(pairs, "usage")
	if err != nil || !present || found == nil {
		return nil, false
	}
	return json.RawMessage(first[found.valueStart:found.valueEnd]), true
}

// kimiK3PromptTokensDetails mirrors the official prompt_tokens_details:
// cached_tokens only on a cache hit, cache_write_tokens always.
type kimiK3PromptTokensDetails struct {
	CachedTokens     int `json:"cached_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_write_tokens"`
}

// kimiK3CompletionTokensDetails mirrors the official completion_tokens_details.
type kimiK3CompletionTokensDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

// kimiK3UsageView mirrors the official usage key order exactly:
// prompt_tokens, completion_tokens, total_tokens, cached_tokens (hit only),
// completion_tokens_details (reasoning only), prompt_tokens_details.
type kimiK3UsageView struct {
	PromptTokens           int                            `json:"prompt_tokens"`
	CompletionTokens       int                            `json:"completion_tokens"`
	TotalTokens            int                            `json:"total_tokens"`
	CachedTokens           int                            `json:"cached_tokens,omitempty"`
	CompletionTokenDetails *kimiK3CompletionTokensDetails `json:"completion_tokens_details,omitempty"`
	PromptTokensDetails    kimiK3PromptTokensDetails      `json:"prompt_tokens_details"`
}

// kimiK3UsageJSON renders the official K3 usage shape from the platform's
// normalized usage. cache_write is the upstream-reported write count (K3's
// aggregator reports it as prompt_cache_write_tokens); zero is the honest
// value when the upstream did not report one.
func kimiK3UsageJSON(usage *dto.Usage, cacheWrite int) (json.RawMessage, error) {
	if usage == nil {
		return json.RawMessage("null"), nil
	}
	normalized := *usage
	if normalized.PromptTokens < 0 {
		normalized.PromptTokens = 0
	}
	if normalized.CompletionTokens < 0 {
		normalized.CompletionTokens = 0
	}
	normalized.TotalTokens = normalized.PromptTokens + normalized.CompletionTokens
	hit := normalized.PromptCacheHitTokens
	if hit <= 0 {
		hit = normalized.PromptTokensDetails.CachedTokens
	}
	if hit < 0 {
		hit = 0
	}
	view := kimiK3UsageView{
		PromptTokens:     normalized.PromptTokens,
		CompletionTokens: normalized.CompletionTokens,
		TotalTokens:      normalized.TotalTokens,
		CachedTokens:     hit,
		PromptTokensDetails: kimiK3PromptTokensDetails{
			CachedTokens:     hit,
			CacheWriteTokens: cacheWrite,
		},
	}
	if reasoning := normalized.CompletionTokenDetails.ReasoningTokens; reasoning > 0 {
		view.CompletionTokenDetails = &kimiK3CompletionTokensDetails{ReasoningTokens: reasoning}
	}
	return common.Marshal(view)
}

// fitKimiK3TextResponseBody normalizes a non-stream chat completion body to the
// official K3 schema: strip aggregator extensions, replace usage with the
// official shape, and keep only official message keys.
func fitKimiK3TextResponseBody(body []byte, usage *dto.Usage) ([]byte, error) {
	var payload map[string]json.RawMessage
	if err := common.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	result := body
	if stripped, ok := deleteNonAllowedTopLevelKeys(result, kimiK3OfficialTopLevelKeys); ok {
		result = stripped
	}
	if rawChoices, ok := payload["choices"]; ok {
		if stripped, ok := fitKimiK3Choices(rawChoices, false, false); ok {
			if patched, replaced := replaceTopLevelJSONValue(result, "choices", stripped); replaced {
				result = patched
			}
		}
	}
	if usage != nil {
		encoded, err := kimiK3UsageJSON(usage, cacheWriteFromRows(payload))
		if err != nil {
			return nil, err
		}
		if patched, ok := replaceTopLevelJSONValue(result, "usage", encoded); ok {
			result = patched
		}
	}
	return result, nil
}

// cacheWriteFromRows reads the upstream's prompt_cache_write_tokens value from
// the raw usage object; zero when absent (the field is aggregator-only, so the
// official shape's cache_write_tokens falls back to zero).
func cacheWriteFromRows(payload map[string]json.RawMessage) int {
	rawUsage, ok := payload["usage"]
	if !ok {
		return 0
	}
	var usage map[string]json.RawMessage
	if err := common.Unmarshal(rawUsage, &usage); err != nil {
		return 0
	}
	raw, ok := usage["prompt_cache_write_tokens"]
	if !ok {
		return 0
	}
	var value int
	if err := common.Unmarshal(raw, &value); err != nil {
		return 0
	}
	return value
}

// fitKimiK3Choices rewrites the choices array: keep only official choice keys,
// keep only official message/delta keys, and (stream, final) leave usage in
// place. streamChoice drops the upstream's blanket logprobs key unless the
// request asked for logprobs.
func fitKimiK3Choices(rawChoices json.RawMessage, stream bool, keepLogprobs bool) (json.RawMessage, bool) {
	spans, ok := jsonArrayElementSpans(rawChoices)
	if !ok {
		return nil, false
	}
	if len(spans) == 0 {
		return rawChoices, true
	}
	allowedChoice := kimiK3OfficialChoiceKeys
	if stream && !keepLogprobs {
		allowedChoice = kimiK3OfficialStreamChoiceKeys
	}
	out := make([]byte, 0, len(rawChoices))
	prev := 0
	for _, span := range spans {
		element := rawChoices[span[0]:span[1]]
		element, ok = deleteNonAllowedTopLevelKeys(element, allowedChoice)
		if !ok {
			return nil, false
		}
		for _, pair := range []struct {
			key     string
			allowed map[string]struct{}
		}{
			{"message", kimiK3OfficialMessageKeys},
			{"delta", kimiK3OfficialDeltaKeys},
		} {
			pairs, _, err := parseTopLevelPairs(element)
			if err != nil {
				return nil, false
			}
			found, present, err := findJSONPair(pairs, pair.key)
			if err != nil {
				return nil, false
			}
			if !present {
				continue
			}
			stripped, ok := deleteNonAllowedTopLevelKeys(element[found.valueStart:found.valueEnd], pair.allowed)
			if !ok {
				return nil, false
			}
			element, ok = replaceTopLevelJSONValue(element, pair.key, json.RawMessage(stripped))
			if !ok {
				return nil, false
			}
		}
		out = append(out, rawChoices[prev:span[0]]...)
		out = append(out, element...)
		prev = span[1]
	}
	out = append(out, rawChoices[prev:]...)
	return out, true
}

// fitKimiK3StreamEvent repairs one SSE data object: strip aggregator top-level
// and choice keys, and render the official usage placement. Official attaches
// usage to choices[0] of the terminal chunk unconditionally — the upstream only
// sends one when the request asked for stream usage — so the terminal chunk
// receives the official usage shape whether or not the upstream provided it.
// A usage-only event (no choices to attach to) is dropped; official never
// emits one.
func fitKimiK3StreamEvent(data string, usage *dto.Usage, final bool, keepLogprobs bool) (string, error) {
	if data == "" {
		return data, nil
	}
	var payload map[string]json.RawMessage
	if err := common.UnmarshalJsonStr(data, &payload); err != nil {
		return data, err
	}
	_, upstreamHasUsage := payload["usage"]
	if !final || usage == nil {
		// Non-terminal chunks never carry usage: drop the key the upstream
		// attached and strip the non-official keys.
		if !upstreamHasUsage {
			return stripKimiK3StreamKeys(data, keepLogprobs)
		}
		stripped, ok := deleteNonAllowedTopLevelKeys([]byte(data), kimiK3OfficialStreamTopLevelKeys)
		if !ok {
			return data, nil
		}
		return stripKimiK3StreamKeys(string(stripped), keepLogprobs)
	}
	// Terminal chunk: render the official usage inside choices[0].
	encoded, err := kimiK3UsageJSON(usage, cacheWriteFromRows(payload))
	if err != nil {
		return data, err
	}
	rawChoices, ok := payload["choices"]
	if !ok {
		return "", nil
	}
	choices, ok := fitKimiK3Choices(rawChoices, true, keepLogprobs)
	if !ok {
		return data, nil
	}
	spans, ok := jsonArrayElementSpans(choices)
	if !ok || len(spans) == 0 {
		// A usage-only event has no choice to carry the official usage; the
		// official stream has no such event, so it is dropped.
		return "", nil
	}
	// The choice normally has no usage yet, so append rather than replace;
	// replaceTopLevelJSONValue reports a miss by returning nil, which must
	// never be written back over the original slice.
	first := choices[spans[0][0]:spans[0][1]]
	withUsage, replaced := replaceTopLevelJSONValue(first, "usage", encoded)
	if !replaced {
		withUsage, replaced = appendTopLevelJSONValue(first, "usage", encoded)
		if !replaced {
			return data, nil
		}
	}
	rebuilt := make([]byte, 0, len(choices))
	rebuilt = append(rebuilt, choices[:spans[0][0]]...)
	rebuilt = append(rebuilt, withUsage...)
	rebuilt = append(rebuilt, choices[spans[0][1]:]...)
	patched, ok := replaceTopLevelJSONValue([]byte(data), "choices", rebuilt)
	if !ok {
		return data, nil
	}
	// The stream key set omits usage: official carries it on the choice.
	stripped, ok := deleteNonAllowedTopLevelKeys(patched, kimiK3OfficialStreamTopLevelKeys)
	if !ok {
		return data, nil
	}
	return string(stripped), nil
}

// stripKimiK3StreamKeys removes non-official top-level and choice keys from one
// SSE data object.
func stripKimiK3StreamKeys(data string, keepLogprobs bool) (string, error) {
	stripped, ok := deleteNonAllowedTopLevelKeys([]byte(data), kimiK3OfficialTopLevelKeys)
	if !ok {
		return data, nil
	}
	var payload map[string]json.RawMessage
	if err := common.Unmarshal(stripped, &payload); err != nil {
		return data, err
	}
	rawChoices, ok := payload["choices"]
	if !ok {
		return string(stripped), nil
	}
	choices, ok := fitKimiK3Choices(rawChoices, true, keepLogprobs)
	if !ok {
		return string(stripped), nil
	}
	patched, ok := replaceTopLevelJSONValue(stripped, "choices", choices)
	if !ok {
		return string(stripped), nil
	}
	return string(patched), nil
}

// kimiK3StreamHasTerminalFinish reports whether the chunk closes the stream
// (a non-empty finish_reason), which is where the official usage rides.
func kimiK3StreamHasTerminalFinish(data string) bool {
	if !strings.Contains(data, `"finish_reason"`) {
		return false
	}
	var payload struct {
		Choices []struct {
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := common.UnmarshalJsonStr(data, &payload); err != nil {
		return false
	}
	for _, choice := range payload.Choices {
		if choice.FinishReason != nil && *choice.FinishReason != "" {
			return true
		}
	}
	return false
}
