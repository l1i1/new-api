package openai

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
)

// isDeepSeekV4ChatModel reports whether the client-facing request targets a
// DeepSeek V4 chat-completions model. The fit layer must key off the origin
// model name (not the upstream-mapped one) so validation and response shaping
// observe the same contract the client requested.
func isDeepSeekV4ChatModel(info *relaycommon.RelayInfo) bool {
	if info == nil || info.RelayMode != relayconstant.RelayModeChatCompletions {
		return false
	}
	modelName := strings.ToLower(strings.TrimSpace(info.OriginModelName))
	return strings.HasPrefix(modelName, "deepseek-v4-")
}

type deepSeekV4PromptTokensDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

type deepSeekV4CompletionTokensDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

// deepSeekV4UsageView mirrors the official usage key order exactly:
// prompt_tokens, completion_tokens, total_tokens, prompt_tokens_details,
// completion_tokens_details (thinking responses only),
// prompt_cache_hit_tokens, prompt_cache_miss_tokens.
type deepSeekV4UsageView struct {
	PromptTokens           int                                `json:"prompt_tokens"`
	CompletionTokens       int                                `json:"completion_tokens"`
	TotalTokens            int                                `json:"total_tokens"`
	PromptTokensDetails    deepSeekV4PromptTokensDetails      `json:"prompt_tokens_details"`
	CompletionTokenDetails *deepSeekV4CompletionTokensDetails `json:"completion_tokens_details,omitempty"`
	PromptCacheHitTokens   int                                `json:"prompt_cache_hit_tokens"`
	PromptCacheMissTokens  int                                `json:"prompt_cache_miss_tokens"`
}

// deepSeekV4UsageJSON renders usage in the exact official DeepSeek shape and
// key order. Disabled-thinking responses omit completion_tokens_details;
// thinking responses include reasoning_tokens. Generic provider extensions
// never cross this client boundary. The billing usage is never mutated;
// normalization is applied to a copy.
func deepSeekV4UsageJSON(usage *dto.Usage, includeReasoningDetails bool) (json.RawMessage, error) {
	if usage == nil {
		return json.RawMessage("null"), nil
	}
	normalized := *usage
	normalizeDeepSeekV4Usage(&normalized)
	view := deepSeekV4UsageView{
		PromptTokens:          normalized.PromptTokens,
		CompletionTokens:      normalized.CompletionTokens,
		TotalTokens:           normalized.TotalTokens,
		PromptTokensDetails:   deepSeekV4PromptTokensDetails{CachedTokens: normalized.PromptCacheHitTokens},
		PromptCacheHitTokens:  normalized.PromptCacheHitTokens,
		PromptCacheMissTokens: normalized.PromptTokens - normalized.PromptCacheHitTokens,
	}
	if includeReasoningDetails {
		view.CompletionTokenDetails = &deepSeekV4CompletionTokensDetails{ReasoningTokens: normalized.CompletionTokenDetails.ReasoningTokens}
	}
	return common.Marshal(view)
}

func normalizeDeepSeekV4Usage(usage *dto.Usage) {
	if usage == nil {
		return
	}
	if usage.PromptTokens < 0 {
		usage.PromptTokens = 0
	}
	if usage.CompletionTokens < 0 {
		usage.CompletionTokens = 0
	}
	usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens

	cacheHit := usage.PromptCacheHitTokens
	if cacheHit == 0 {
		cacheHit = usage.PromptTokensDetails.CachedTokens
	}
	if cacheHit < 0 {
		cacheHit = 0
	}
	if cacheHit > usage.PromptTokens {
		cacheHit = usage.PromptTokens
	}
	usage.PromptCacheHitTokens = cacheHit
	usage.PromptTokensDetails.CachedTokens = cacheHit
	if usage.CompletionTokenDetails.ReasoningTokens < 0 {
		usage.CompletionTokenDetails.ReasoningTokens = 0
	}
}

// fitDeepSeekV4TextResponseBody normalizes a non-stream chat completion body to
// the official DeepSeek V4 schema: strip aggregator extensions (top-level cost,
// null message.tool_calls), replace usage with the official seven-key shape,
// while preserving an upstream-provided system_fingerprint. Values that only
// the real upstream knows are never replaced with a fabricated identity.
// Editing is surgical: key order and formatting outside the touched values are
// forwarded exactly as the upstream sent them.
func fitDeepSeekV4TextResponseBody(body []byte, usage *dto.Usage, includeReasoningDetails bool) ([]byte, error) {
	var payload map[string]json.RawMessage
	if err := common.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	if patched, ok := fitDeepSeekV4TextResponseBodyInPlace(body, payload, usage, includeReasoningDetails); ok {
		return patched, nil
	}
	// Fallback rewrite for inputs the surgical editor declines.
	if _, ok := payload["cost"]; ok {
		delete(payload, "cost")
	}
	if rawChoices, ok := payload["choices"]; ok {
		choices, err := fitDeepSeekV4Choices(rawChoices)
		if err != nil {
			return nil, err
		}
		payload["choices"] = choices
	}
	if _, ok := payload["usage"]; ok {
		encodedUsage, err := deepSeekV4UsageJSON(usage, includeReasoningDetails)
		if err != nil {
			return nil, err
		}
		payload["usage"] = encodedUsage
	}
	return common.Marshal(payload)
}

func fitDeepSeekV4TextResponseBodyInPlace(body []byte, payload map[string]json.RawMessage, usage *dto.Usage, includeReasoningDetails bool) ([]byte, bool) {
	result := body
	if _, ok := payload["cost"]; ok {
		patched, ok := deleteTopLevelJSONKey(result, "cost")
		if !ok {
			return nil, false
		}
		result = patched
	}
	if rawChoices, ok := payload["choices"]; ok {
		fitted, ok := fitDeepSeekV4ChoicesInPlace(rawChoices)
		if !ok {
			return nil, false
		}
		if !bytes.Equal(fitted, rawChoices) {
			patched, ok := replaceTopLevelJSONValue(result, "choices", fitted)
			if !ok {
				return nil, false
			}
			result = patched
		}
	}
	if _, ok := payload["usage"]; ok {
		encoded, err := deepSeekV4UsageJSON(usage, includeReasoningDetails)
		if err != nil {
			return nil, false
		}
		patched, ok := replaceTopLevelJSONValue(result, "usage", encoded)
		if !ok {
			return nil, false
		}
		result = patched
	}
	return result, true
}

// fitDeepSeekV4Choices rewrites a choices array through a map, sorting keys.
// It is the fallback for inputs the in-place editor declines.
func fitDeepSeekV4Choices(rawChoices json.RawMessage) (json.RawMessage, error) {
	var choices []map[string]json.RawMessage
	if err := common.Unmarshal(rawChoices, &choices); err != nil {
		return nil, err
	}
	for i, choice := range choices {
		rawMessage, ok := choice["message"]
		if !ok {
			continue
		}
		var message map[string]json.RawMessage
		if err := common.Unmarshal(rawMessage, &message); err != nil {
			return nil, err
		}
		if isJSONNull(message["tool_calls"]) {
			delete(message, "tool_calls")
		}
		encodedMessage, err := common.Marshal(message)
		if err != nil {
			return nil, err
		}
		choice["message"] = encodedMessage
		choices[i] = choice
	}
	return common.Marshal(choices)
}

// fitDeepSeekV4ChoicesInPlace removes null message.tool_calls entries from a
// choices array without disturbing any other byte.
func fitDeepSeekV4ChoicesInPlace(rawChoices json.RawMessage) (json.RawMessage, bool) {
	spans, ok := jsonArrayElementSpans(rawChoices)
	if !ok {
		return nil, false
	}
	if len(spans) == 0 {
		return rawChoices, true
	}
	type edit struct {
		start, end int
		bytes      []byte
	}
	edits := make([]edit, 0, len(spans))
	for _, span := range spans {
		element := rawChoices[span[0]:span[1]]
		fitted, ok := stripNullToolCallsInChoice(element)
		if !ok {
			return nil, false
		}
		if !bytes.Equal(fitted, element) {
			edits = append(edits, edit{start: span[0], end: span[1], bytes: fitted})
		}
	}
	if len(edits) == 0 {
		return rawChoices, true
	}
	out := make([]byte, 0, len(rawChoices))
	prev := 0
	for _, e := range edits {
		out = append(out, rawChoices[prev:e.start]...)
		out = append(out, e.bytes...)
		prev = e.end
	}
	out = append(out, rawChoices[prev:]...)
	return out, true
}

// stripNullToolCallsInChoice deletes a null tool_calls key from the choice's
// message object, preserving every other byte of the choice.
func stripNullToolCallsInChoice(choice []byte) ([]byte, bool) {
	choicePairs, _, err := parseTopLevelPairs(choice)
	if err != nil {
		return nil, false
	}
	messagePair, found, err := findJSONPair(choicePairs, "message")
	if err != nil || !found {
		return nil, false
	}
	message := choice[messagePair.valueStart:messagePair.valueEnd]
	messagePairs, _, err := parseTopLevelPairs(message)
	if err != nil {
		return nil, false
	}
	toolCallsPair, found, err := findJSONPair(messagePairs, "tool_calls")
	if err != nil {
		return nil, false
	}
	if !found || !isJSONNull(message[toolCallsPair.valueStart:toolCallsPair.valueEnd]) {
		return choice, true
	}
	stripped, ok := deleteTopLevelJSONKey(message, "tool_calls")
	if !ok {
		return nil, false
	}
	out := make([]byte, 0, len(choice))
	out = append(out, choice[:messagePair.valueStart]...)
	out = append(out, stripped...)
	out = append(out, choice[messagePair.valueEnd:]...)
	return out, true
}

// fitDeepSeekV4StreamEvent renders usage in the official shape while
// preserving only a real upstream-provided system_fingerprint. Editing is
// surgical: every byte outside the usage value (including key order) reaches
// the client exactly as the upstream sent it.
func fitDeepSeekV4StreamEvent(data string, usage *dto.Usage, includeUsage bool, includeReasoningDetails bool) (string, error) {
	if data == "" {
		return data, nil
	}
	var payload map[string]json.RawMessage
	if err := common.UnmarshalJsonStr(data, &payload); err != nil {
		return data, err
	}
	var replacement json.RawMessage
	if includeUsage && usage != nil {
		encoded, err := deepSeekV4UsageJSON(usage, includeReasoningDetails)
		if err != nil {
			return data, err
		}
		replacement = encoded
	} else if !includeUsage {
		// Official chunks always carry a usage field; non-carriers are null.
		replacement = json.RawMessage("null")
	}
	if replacement == nil {
		// Usage requested but nothing to render: keep upstream bytes.
		return data, nil
	}
	if patched, ok := spliceTopLevelUsage([]byte(data), replacement); ok {
		return string(patched), nil
	}
	// Structural surprise (duplicate keys, unusual shape): fall back to a full
	// rewrite so the client still receives the official usage shape.
	payload["usage"] = replacement
	patched, err := common.Marshal(payload)
	if err != nil {
		return data, err
	}
	return string(patched), nil
}

// spliceTopLevelUsage replaces the value of the top-level "usage" key in place
// and appends the key when absent, preserving every other byte. Official V4
// chunks carry usage last, so an appended key lands in the official position.
func spliceTopLevelUsage(data []byte, value json.RawMessage) ([]byte, bool) {
	patched, ok := replaceTopLevelJSONValue(data, "usage", value)
	if ok {
		return patched, true
	}
	return appendTopLevelJSONValue(data, "usage", value)
}

// replaceTopLevelJSONValue replaces the value of an existing top-level key,
// preserving every other byte. ok=false lets the caller fall back.
func replaceTopLevelJSONValue(data []byte, key string, value json.RawMessage) ([]byte, bool) {
	pairs, _, err := parseTopLevelPairs(data)
	if err != nil {
		return nil, false
	}
	pair, found, err := findJSONPair(pairs, key)
	if err != nil || !found {
		return nil, false
	}
	out := make([]byte, 0, len(data)+len(value))
	out = append(out, data[:pair.valueStart]...)
	out = append(out, value...)
	out = append(out, data[pair.valueEnd:]...)
	return out, true
}

// appendTopLevelJSONValue appends one key/value pair before the closing brace,
// preserving every existing byte.
func appendTopLevelJSONValue(data []byte, key string, value json.RawMessage) ([]byte, bool) {
	pairs, closeIdx, err := parseTopLevelPairs(data)
	if err != nil {
		return nil, false
	}
	out := make([]byte, 0, len(data)+len(key)+len(value)+8)
	out = append(out, data[:closeIdx]...)
	if len(pairs) > 0 {
		out = append(out, ',')
	}
	out = append(out, strconv.Quote(key)...)
	out = append(out, ':')
	out = append(out, value...)
	out = append(out, data[closeIdx:]...)
	return out, true
}

// deleteTopLevelJSONKey removes one key/value pair while preserving the byte
// layout of everything else. A missing key is a no-op.
func deleteTopLevelJSONKey(data []byte, key string) ([]byte, bool) {
	pairs, _, err := parseTopLevelPairs(data)
	if err != nil {
		return nil, false
	}
	idx := -1
	for i := range pairs {
		if pairs[i].key == key {
			if idx >= 0 {
				return nil, false
			}
			idx = i
		}
	}
	if idx < 0 {
		return data, true
	}
	if idx+1 < len(pairs) {
		out := make([]byte, 0, len(data))
		out = append(out, data[:pairs[idx].keyStart]...)
		out = append(out, data[pairs[idx+1].keyStart:]...)
		return out, true
	}
	if idx > 0 {
		comma := skipJSONSpace(data, pairs[idx-1].valueEnd)
		if comma >= len(data) || data[comma] != ',' {
			return nil, false
		}
		out := make([]byte, 0, len(data))
		out = append(out, data[:comma]...)
		out = append(out, data[pairs[idx].valueEnd:]...)
		return out, true
	}
	// Sole pair: keep the braces, drop everything between them.
	openEnd := skipJSONSpace(data, 1)
	closeIdx := skipJSONSpace(data, pairs[0].valueEnd)
	if openEnd >= len(data) || closeIdx >= len(data) || data[closeIdx] != '}' {
		return nil, false
	}
	out := make([]byte, 0, len(data))
	out = append(out, data[:openEnd]...)
	out = append(out, data[closeIdx:]...)
	return out, true
}

// jsonTopLevelPair locates one key/value pair of a JSON object.
type jsonTopLevelPair struct {
	key        string
	keyStart   int // offset of the key's opening quote
	valueStart int // offset of the value's first byte
	valueEnd   int // offset just past the value
}

var errJSONScan = errors.New("unexpected JSON structure")

// parseTopLevelPairs walks the top-level pairs of a JSON object. Callers
// validate the JSON first; the scan is defensive and any structural surprise
// is an error so callers can fall back to a full rewrite.
func parseTopLevelPairs(data []byte) ([]jsonTopLevelPair, int, error) {
	i := skipJSONSpace(data, 0)
	if i >= len(data) || data[i] != '{' {
		return nil, 0, errJSONScan
	}
	i = skipJSONSpace(data, i+1)
	pairs := make([]jsonTopLevelPair, 0, 8)
	if i < len(data) && data[i] == '}' {
		if skipJSONSpace(data, i+1) != len(data) {
			return nil, 0, errJSONScan
		}
		return pairs, i, nil
	}
	for {
		if i >= len(data) || data[i] != '"' {
			return nil, 0, errJSONScan
		}
		keyEnd, err := skipJSONString(data, i)
		if err != nil {
			return nil, 0, err
		}
		key, err := strconv.Unquote(string(data[i:keyEnd]))
		if err != nil {
			return nil, 0, errJSONScan
		}
		pair := jsonTopLevelPair{key: key, keyStart: i}
		i = skipJSONSpace(data, keyEnd)
		if i >= len(data) || data[i] != ':' {
			return nil, 0, errJSONScan
		}
		i = skipJSONSpace(data, i+1)
		if i >= len(data) {
			return nil, 0, errJSONScan
		}
		pair.valueStart = i
		pair.valueEnd, err = skipJSONValue(data, i)
		if err != nil {
			return nil, 0, err
		}
		pairs = append(pairs, pair)
		i = skipJSONSpace(data, pair.valueEnd)
		if i >= len(data) {
			return nil, 0, errJSONScan
		}
		switch data[i] {
		case ',':
			i = skipJSONSpace(data, i+1)
		case '}':
			if skipJSONSpace(data, i+1) != len(data) {
				return nil, 0, errJSONScan
			}
			return pairs, i, nil
		default:
			return nil, 0, errJSONScan
		}
	}
}

// findJSONPair returns the pair for key; a duplicated key is an error so the
// map-based fallback (last value wins) decides the outcome.
func findJSONPair(pairs []jsonTopLevelPair, key string) (*jsonTopLevelPair, bool, error) {
	var match *jsonTopLevelPair
	for i := range pairs {
		if pairs[i].key == key {
			if match != nil {
				return nil, false, errJSONScan
			}
			match = &pairs[i]
		}
	}
	return match, match != nil, nil
}

// jsonArrayElementSpans returns the byte spans of a JSON array's elements.
func jsonArrayElementSpans(data []byte) ([][2]int, bool) {
	i := skipJSONSpace(data, 0)
	if i >= len(data) || data[i] != '[' {
		return nil, false
	}
	i = skipJSONSpace(data, i+1)
	var spans [][2]int
	if i < len(data) && data[i] == ']' {
		if skipJSONSpace(data, i+1) != len(data) {
			return nil, false
		}
		return spans, true
	}
	for {
		start := i
		end, err := skipJSONValue(data, i)
		if err != nil {
			return nil, false
		}
		spans = append(spans, [2]int{start, end})
		i = skipJSONSpace(data, end)
		if i >= len(data) {
			return nil, false
		}
		switch data[i] {
		case ',':
			i = skipJSONSpace(data, i+1)
		case ']':
			if skipJSONSpace(data, i+1) != len(data) {
				return nil, false
			}
			return spans, true
		default:
			return nil, false
		}
	}
}

func skipJSONSpace(data []byte, i int) int {
	for i < len(data) {
		switch data[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return i
		}
	}
	return i
}

// skipJSONString returns the offset just past the string starting at i.
func skipJSONString(data []byte, i int) (int, error) {
	i++ // opening quote
	for i < len(data) {
		switch data[i] {
		case '\\':
			if i+1 >= len(data) {
				return 0, errJSONScan
			}
			i += 2
		case '"':
			return i + 1, nil
		default:
			i++
		}
	}
	return 0, errJSONScan
}

// skipJSONValue returns the offset just past the JSON value starting at i.
func skipJSONValue(data []byte, i int) (int, error) {
	if i >= len(data) {
		return 0, errJSONScan
	}
	switch data[i] {
	case '"':
		return skipJSONString(data, i)
	case '{', '[':
		depth := 0
		for i < len(data) {
			switch data[i] {
			case '"':
				var err error
				i, err = skipJSONString(data, i)
				if err != nil {
					return 0, err
				}
				continue
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return i + 1, nil
				}
				if depth < 0 {
					return 0, errJSONScan
				}
			}
			i++
		}
		return 0, errJSONScan
	default:
		j := i
		for j < len(data) {
			c := data[j]
			if c == ',' || c == '}' || c == ']' || c == ':' ||
				c == ' ' || c == '\t' || c == '\n' || c == '\r' {
				break
			}
			j++
		}
		if j == i {
			return 0, errJSONScan
		}
		return j, nil
	}
}

func isJSONNull(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	return strings.TrimSpace(string(raw)) == "null"
}
