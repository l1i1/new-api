package main

// Minimal probe bodies, response signatures and the comparison rules.
//
// Everything here is pure: no network, no credentials, no filesystem. The HTTP
// layer (client.go) only hands a status code and a response body to
// extractSignature, and a test can call classify directly.

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/QuantumNous/new-api/pkg/fitpolicy"
)

// 1x1 transparent PNG. A data URI keeps the probe self-contained: an image
// probe must not depend on an external host being reachable from this machine.
const onePixelPNG = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="

// ProbeSpec is the minimal request for one behaviour and the comparison rule.
type ProbeSpec struct {
	Behavior  string
	Path      string
	MaxTokens int
	// Fields are the request fields this dimension exercises. An upstream error
	// object that structurally names one of them — in its own locator field
	// (param/field) or as its exact code/type — is attributable to the dimension
	// rather than to an unrelated rejection. Prose is never evidence; see
	// annotateErrorField.
	Fields []string
	// Build returns the network body. It is family-aware because the same
	// behaviour is exercised by opposite shapes in different families:
	// usage.thinking_counting is a thinking-ON probe for DeepSeek V4 and a
	// thinking-OFF probe for kimi-k3 (docs/official-fit-mode.md:59-67).
	Build func(family, model string) map[string]any
	// StructuralCompare returns human-readable divergence shapes found by
	// comparing two successful responses. Empty means the responses agree on
	// every structural field this dimension can observe.
	StructuralCompare func(base, channel signature) []string
	// StructuralAbsenceIsDivergence is set when a successful response that
	// lacks the dimension's structure already contradicts the official
	// expectation (for example: tool_choice=required answered without any
	// tool_calls). It is deliberately false where a compliant model may still
	// answer without the structure (json_object content that is not JSON),
	// because there the absence is only decidable against a measured baseline.
	StructuralAbsenceIsDivergence bool
	// EvidenceIsGeneratedOutput is set when the dimension's structural evidence
	// can only arrive as sampled completion — tool_calls, reasoning content and
	// reasoning-token accounting. There a completion that spent the whole
	// max_tokens budget without emitting content plausibly ran out of room
	// before the evidence could appear, so looksTruncated's completion-vs-cap
	// clause may explain the absence. It is false for envelope-evidence marks
	// (logprobs, and the JSON-object content shape of response_format.json):
	// those live on the response envelope rather than being paid for out of the
	// generation budget, so the probe's own cap cannot explain their absence and
	// only a length cut that emitted no token at all may downgrade them.
	EvidenceIsGeneratedOutput bool
}

const chatPath = "/v1/chat/completions"

func chatBody(model string, messages []map[string]any, maxTokens int) map[string]any {
	return map[string]any{
		"model":      model,
		"messages":   messages,
		"max_tokens": maxTokens,
		"stream":     false,
	}
}

func userMessage(text string) map[string]any {
	return map[string]any{"role": "user", "content": text}
}

func functionTool(name string) map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        name,
			"description": "probe",
			"parameters":  map[string]any{"type": "object", "properties": map[string]any{}},
		},
	}
}

// logprobsProbeMaxTokens is the generation budget of the logprobs.dual_path
// probe. It must stay above 1: at a 1-token cap every channel that accepts the
// request answers finish_reason=length at completion_tokens=1, and that
// guaranteed cut is an artefact of the probe rather than a fact about the
// channel — 11 of the 15 stranded supported=false marks were undecidable for
// exactly this reason. 8 is the smallest budget in this table that lets a
// one-word answer ("hi") finish naturally; it is the cap
// usage.thinking_counting already uses, so the worst-case cost of this probe
// changes from 1 to 8 completion tokens and nothing else moves.
const logprobsProbeMaxTokens = 8

// toolsChoiceSemanticsProbeMaxTokens is the generation budget of the
// tools.choice_semantics probe. It must stay above the cost of a real tool
// call: at the original 16-token cap the eight kimi-k3 rows that produced no
// tool_calls all ended exactly at 16/16 with empty content, while the three
// channels that did answer tool_choice=required with tool_calls needed 60-76
// completion tokens. The 16-token budget therefore cut the answer off before
// the tool call could be emitted, which made the absence a property of the
// probe rather than of the channel — the same class of artefact
// logprobsProbeMaxTokens was raised for (round 1 withheld exactly these rows by
// hand; a later round had to downgrade them as truncation). 96 is the smallest
// round budget in this table with headroom over the observed 76; the worst-case
// cost of this one probe moves from 16 to 96 completion tokens and nothing else.
const toolsChoiceSemanticsProbeMaxTokens = 96

// probeSpecs is keyed by behaviour name. A behaviour with no entry cannot be
// measured by a minimal probe and is reported as such rather than guessed.
var probeSpecs = map[string]ProbeSpec{
	fitpolicy.BehaviorLogprobsDualPath: {
		Behavior:  fitpolicy.BehaviorLogprobsDualPath,
		Path:      chatPath,
		MaxTokens: logprobsProbeMaxTokens,
		Fields:    []string{"logprobs", "top_logprobs"},
		Build: func(family, model string) map[string]any {
			body := chatBody(model, []map[string]any{userMessage("hi")}, logprobsProbeMaxTokens)
			body["logprobs"] = true
			body["top_logprobs"] = 1
			if family == "deepseek-v4" {
				// DeepSeek V4 defaults to thinking-on, and at this probe's
				// 8-token budget the model usually spends the whole budget on
				// reasoning. The official endpoint attaches logprobs to the
				// tokens it actually emits, so whether any logprobs came back
				// was decided by whether a content token happened to fit: on
				// byte-identical requests the official channel returned them in
				// 1 of 3 rounds, and in one round the official baseline leg and
				// the identically pinned official sample leg disagreed with each
				// other. A measured baseline that flips per round turns every
				// presence-derived verdict into a lottery, so this probe pins
				// the thinking state the same way the kimi probes do —
				// thinking.type=disabled, no new mechanism. Thinking off makes
				// the answer content (the path the official endpoint attaches
				// logprobs to), which is what makes the reference reproducible;
				// the budget, the logprobs fields and the logprobs=true shape
				// the policy pins are unchanged.
				body["thinking"] = map[string]any{"type": "disabled"}
			}
			return body
		},
		StructuralAbsenceIsDivergence: true,
		StructuralCompare: func(base, channel signature) []string {
			var shapes []string
			if base.HasLogprobs != channel.HasLogprobs {
				shapes = append(shapes, "logprobs_present official="+boolText(base.HasLogprobs)+" channel="+boolText(channel.HasLogprobs))
			}
			return shapes
		},
	},
	fitpolicy.BehaviorImageParts: {
		Behavior:  fitpolicy.BehaviorImageParts,
		Path:      chatPath,
		MaxTokens: 8,
		Fields:    []string{"image_url", "content"},
		Build: func(family, model string) map[string]any {
			content := []map[string]any{
				{"type": "text", "text": "Reply with the single word: ok"},
				{"type": "image_url", "image_url": map[string]any{"url": onePixelPNG}},
			}
			return chatBody(model, []map[string]any{{"role": "user", "content": content}}, 8)
		},
	},
	fitpolicy.BehaviorThinkingCounting: {
		Behavior:  fitpolicy.BehaviorThinkingCounting,
		Path:      chatPath,
		MaxTokens: 8,
		Fields:    []string{"thinking", "reasoning_effort", "reasoning", "usage"},
		Build: func(family, model string) map[string]any {
			body := chatBody(model, []map[string]any{userMessage("hi")}, 8)
			if family == "deepseek-v4" {
				// DeepSeek V4 requires counting for thinking-expected requests.
				body["thinking"] = map[string]any{"type": "enabled"}
			} else {
				// kimi-k3 requires it for thinking-disabled requests: the pool
				// reports the thinking-on prompt count there.
				body["thinking"] = map[string]any{"type": "disabled"}
			}
			return body
		},
		StructuralAbsenceIsDivergence: true,
		EvidenceIsGeneratedOutput:     true,
		StructuralCompare: func(base, channel signature) []string {
			var shapes []string
			if base.HasReasoningContent != channel.HasReasoningContent {
				shapes = append(shapes, "reasoning_content_present official="+boolText(base.HasReasoningContent)+" channel="+boolText(channel.HasReasoningContent))
			}
			// The reasoning token *count* is deliberately not compared: it is an
			// output-token count from a sampled generation, and this probe pins
			// no sampling parameter (it cannot: kimi-k3 accepts one fixed
			// temperature per thinking state, relay/helper/valid_request.go:1061-1062,
			// docs/official-fit-mode.md:31-32). Two identical requests therefore
			// may legitimately report different counts, and an exact-count
			// mismatch would write supported=false for sampling variance. Only
			// the reproducible accounting facts are compared.
			if (base.ReasoningTokens >= 0) != (channel.ReasoningTokens >= 0) {
				shapes = append(shapes, "reasoning_token_accounting official="+boolText(base.ReasoningTokens >= 0)+" channel="+boolText(channel.ReasoningTokens >= 0))
			}
			return shapes
		},
	},
	fitpolicy.BehaviorToolsChoiceSemantics: {
		Behavior:  fitpolicy.BehaviorToolsChoiceSemantics,
		Path:      chatPath,
		MaxTokens: toolsChoiceSemanticsProbeMaxTokens,
		Fields:    []string{"tool_choice", "tools", "function"},
		Build: func(family, model string) map[string]any {
			body := chatBody(model, []map[string]any{userMessage("Call the ping function.")}, toolsChoiceSemanticsProbeMaxTokens)
			body["tools"] = []map[string]any{functionTool("ping")}
			// "required" is accepted in both thinking states on kimi-k3
			// (relay/helper/valid_request.go:1234).
			body["tool_choice"] = "required"
			return body
		},
		StructuralAbsenceIsDivergence: true,
		EvidenceIsGeneratedOutput:     true,
		StructuralCompare: func(base, channel signature) []string {
			var shapes []string
			if base.HasToolCalls != channel.HasToolCalls {
				shapes = append(shapes, "tool_calls_present official="+boolText(base.HasToolCalls)+" channel="+boolText(channel.HasToolCalls))
			}
			if base.HasToolCalls && channel.HasToolCalls && !sameStrings(base.ToolCallNames, channel.ToolCallNames) {
				shapes = append(shapes, "tool_call_names official="+strings.Join(base.ToolCallNames, ",")+" channel="+strings.Join(channel.ToolCallNames, ","))
			}
			return shapes
		},
	},
	fitpolicy.BehaviorResponseFormatJSON: {
		Behavior:  fitpolicy.BehaviorResponseFormatJSON,
		Path:      chatPath,
		MaxTokens: 16,
		Fields:    []string{"response_format"},
		Build: func(family, model string) map[string]any {
			// The prompt must contain the word "json": DeepSeek V4 rejects
			// json_object otherwise, locally and upstream
			// (relay/helper/valid_request.go:620).
			body := chatBody(model, []map[string]any{userMessage(`Reply with json: {"ok":true}`)}, 16)
			body["response_format"] = map[string]any{"type": "json_object"}
			return body
		},
		StructuralCompare: func(base, channel signature) []string {
			// Only a baseline that itself produced a JSON object is a usable
			// reference for this mark. Diverging from a baseline that also
			// missed the requested shape would invent an unsupported verdict
			// for a channel that honoured it.
			if !base.ContentIsJSONObject || channel.ContentIsJSONObject {
				return nil
			}
			return []string{"content_is_json_object official=" + boolText(base.ContentIsJSONObject) + " channel=" + boolText(channel.ContentIsJSONObject)}
		},
	},
	fitpolicy.BehaviorHistoryAssistantFirst: {
		Behavior:  fitpolicy.BehaviorHistoryAssistantFirst,
		Path:      chatPath,
		MaxTokens: 1,
		Fields:    []string{"messages", "role", "assistant"},
		Build: func(family, model string) map[string]any {
			messages := []map[string]any{
				{"role": "assistant", "content": "prior turn"},
				userMessage("hi"),
			}
			return chatBody(model, messages, 1)
		},
	},
	fitpolicy.BehaviorToolsDynamicNames: {
		Behavior:  fitpolicy.BehaviorToolsDynamicNames,
		Path:      chatPath,
		MaxTokens: 1,
		Fields:    []string{"tools", "message tools"},
		Build: func(family, model string) map[string]any {
			// kimi-k3 only accepts message-level tools on a system message with
			// empty content (relay/helper/valid_request.go:1317).
			messages := []map[string]any{
				{"role": "system", "content": "", "tools": []map[string]any{functionTool("ping")}},
				userMessage("hi"),
			}
			return chatBody(model, messages, 1)
		},
	},
	fitpolicy.BehaviorFamilyWhole: {
		Behavior:  fitpolicy.BehaviorFamilyWhole,
		Path:      chatPath,
		MaxTokens: 1,
		Build: func(family, model string) map[string]any {
			return chatBody(model, []map[string]any{userMessage("hi")}, 1)
		},
	},
}

// unmeasurableBehaviors lists policy behaviours a minimal probe cannot decide,
// with the reason. It is empty for the shipped policy. A mark listed here (or
// any mark with no probe spec at all) is reported as not measured by
// unmeasurableRow instead of being silently dropped from the output.
var unmeasurableBehaviors = map[string]string{}

// signature is the redacted, derived view of one probe response. Response
// bodies are never retained or printed; only these fields leave the process.
type signature struct {
	Status              int      `json:"status"`
	OK                  bool     `json:"ok"`
	JSON                bool     `json:"json"`
	Accepted            bool     `json:"accepted"`
	HasUsage            bool     `json:"has_usage"`
	PromptTokens        int      `json:"prompt_tokens"`
	CompletionTokens    int      `json:"completion_tokens"`
	ReasoningTokens     int      `json:"reasoning_tokens"`
	HasContent          bool     `json:"has_content"`
	ContentIsJSONObject bool     `json:"content_is_json_object"`
	HasReasoningContent bool     `json:"has_reasoning_content"`
	HasLogprobs         bool     `json:"has_logprobs"`
	HasToolCalls        bool     `json:"has_tool_calls"`
	ToolCallNames       []string `json:"tool_call_names,omitempty"`
	HasFinishReason     bool     `json:"has_finish_reason"`
	ErrorCode           string   `json:"error_code,omitempty"`
	ErrorFingerprint    string   `json:"error_fingerprint,omitempty"`
	ErrorField          string   `json:"error_field,omitempty"`
	RelayLocal          bool     `json:"relay_local,omitempty"`

	// errorLocator and errorCategory carry the error object's own structural
	// values (param/field vs code/type), NUL-joined. They are unexported on
	// purpose: the raw upstream strings are used only in-process to match a
	// field name from probeSpecs, never serialized, and the only thing that
	// leaves is that field name in ErrorField.
	errorLocator  string
	errorCategory string

	// finishReason is the upstream's own finish_reason, trimmed and lowercased.
	// Like the values above it is unexported on purpose: it is read in-process
	// only (to recognise a length cut) and never serialized; HasFinishReason
	// remains the fact that leaves the process.
	finishReason string
}

// extractSignature derives the signature from one HTTP response. body may be
// truncated by the caller; a body that does not parse as JSON yields
// JSON=false and only the status survives.
func extractSignature(status int, body []byte) signature {
	sig := signature{
		Status:           status,
		OK:               status >= 200 && status < 300,
		PromptTokens:     -1,
		CompletionTokens: -1,
		ReasoningTokens:  -1,
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return sig
	}
	sig.JSON = true

	if usage, ok := payload["usage"].(map[string]any); ok {
		sig.HasUsage = true
		sig.PromptTokens = intField(usage, "prompt_tokens")
		sig.CompletionTokens = intField(usage, "completion_tokens")
		if details, ok := usage["completion_tokens_details"].(map[string]any); ok {
			sig.ReasoningTokens = intField(details, "reasoning_tokens")
		}
	}

	if choices, ok := payload["choices"].([]any); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]any); ok {
			if reason, ok := choice["finish_reason"].(string); ok && strings.TrimSpace(reason) != "" {
				sig.HasFinishReason = true
				sig.finishReason = strings.ToLower(strings.TrimSpace(reason))
			}
			if message, ok := choice["message"].(map[string]any); ok {
				text, isJSONObject := messageContent(message["content"])
				sig.HasContent = strings.TrimSpace(text) != ""
				sig.ContentIsJSONObject = isJSONObject
				if reasoning, ok := message["reasoning_content"].(string); ok && strings.TrimSpace(reasoning) != "" {
					sig.HasReasoningContent = true
				}
				if calls, ok := message["tool_calls"].([]any); ok && len(calls) > 0 {
					sig.HasToolCalls = true
					sig.ToolCallNames = toolCallNames(calls)
				}
			}
			if hasLogprobData(choice["logprobs"]) {
				sig.HasLogprobs = true
			}
		}
	}
	sig.Accepted = sig.OK && sig.JSON && (sig.HasContent || sig.HasFinishReason)

	if errValue, ok := payload["error"].(map[string]any); ok {
		if code, ok := errValue["code"].(string); ok {
			sig.ErrorCode = code
		}
		if message, ok := errValue["message"].(string); ok {
			sig.ErrorFingerprint = redactText(message, 160)
		}
		sig.errorLocator, sig.errorCategory = errorStructuralValues(errValue)
	} else if message, ok := payload["message"].(string); ok && !sig.OK {
		sig.ErrorFingerprint = redactText(message, 160)
	}
	sig.RelayLocal = isRelayLocalError(sig.ErrorCode, sig.ErrorFingerprint)
	return sig
}

// errorLocatorKeys are the error-object keys whose documented purpose is to name
// the request member that caused the error. errorCategoryKeys classify the
// error; their values are identifiers, not sentences.
var (
	errorLocatorKeys  = []string{"param", "parameter", "field"}
	errorCategoryKeys = []string{"code", "type"}
)

// errorStructuralValues returns the NUL-joined structural values of one error
// object: the locators (which name the offending member) and the categories
// (which classify the failure). A non-string value cannot name a field, so it
// is dropped.
func errorStructuralValues(errValue map[string]any) (locator, category string) {
	var locators, categories []string
	for _, key := range errorLocatorKeys {
		if value, ok := errValue[key].(string); ok && strings.TrimSpace(value) != "" {
			locators = append(locators, value)
		}
	}
	for _, key := range errorCategoryKeys {
		if value, ok := errValue[key].(string); ok && strings.TrimSpace(value) != "" {
			categories = append(categories, value)
		}
	}
	return strings.Join(locators, "\x00"), strings.Join(categories, "\x00")
}

// annotateErrorField records which of the dimension's fields the channel's own
// error names *structurally*. It is deliberately not a substring test over the
// prose message: a generic rejection that merely mentions a field word
// ("content policy", "tools unavailable", "invalid usage") is not evidence that
// the channel rejected that parameter, and treating it as evidence writes a
// structural supported=false derived from a coincidence.
//
// Only the error object's own structural values count:
//
//   - a locator value (error.param / error.field) matched by whole identifier
//     tokens, so "messages[0].content[1].image_url.url" names content and
//     image_url;
//   - a category value (error.code / error.type) that is exactly the field
//     name, so the generic code "content_filter" does not name content.
//
// Everything else stays unattributed, which keeps the failure inconclusive: an
// unattributed error never becomes a divergence and never becomes a
// consistency.
func annotateErrorField(sig *signature, fields []string) {
	if sig == nil || sig.OK {
		return
	}
	for _, field := range fields {
		if sig.errorNamesField(field) {
			sig.ErrorField = field
			return
		}
	}
}

// errorNamesField reports whether the error object names this request field. The
// locator and category values are the only inputs; the prose fingerprint is not
// consulted at all.
func (sig signature) errorNamesField(field string) bool {
	needle := identifierTokens(field)
	if len(needle) == 0 {
		return false
	}
	for _, locator := range splitStructuralValues(sig.errorLocator) {
		if containsTokenRun(identifierTokens(locator), needle) {
			return true
		}
	}
	for _, category := range splitStructuralValues(sig.errorCategory) {
		if sameStrings(identifierTokens(category), needle) {
			return true
		}
	}
	return false
}

// splitStructuralValues undoes the NUL join of errorStructuralValues.
func splitStructuralValues(joined string) []string {
	if joined == "" {
		return nil
	}
	return strings.Split(joined, "\x00")
}

// identifierTokens splits a value into lowercased identifier tokens, so a
// request path and a field name are comparable regardless of punctuation:
// "messages[0].content[1].image_url.url" -> [messages 0 content 1 image_url url].
func identifierTokens(value string) []string {
	return strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_')
	})
}

// containsTokenRun reports whether needle appears in haystack as a contiguous
// run of whole tokens.
func containsTokenRun(haystack, needle []string) bool {
	if len(needle) == 0 || len(needle) > len(haystack) {
		return false
	}
	for start := 0; start+len(needle) <= len(haystack); start++ {
		if sameStrings(haystack[start:start+len(needle)], needle) {
			return true
		}
	}
	return false
}

// messageContent returns the assistant text and whether it is a JSON object.
// The second result is deliberately an object test, not "valid JSON":
// response_format.json asks for a `{"type":"json_object"}` answer, so a bare
// scalar such as `true` is a valid JSON value but is not evidence for that mark.
func messageContent(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		trimmed := strings.TrimSpace(typed)
		return typed, isJSONObject(trimmed)
	case []any:
		var builder strings.Builder
		for _, part := range typed {
			entry, ok := part.(map[string]any)
			if !ok {
				continue
			}
			if text, ok := entry["text"].(string); ok {
				builder.WriteString(text)
			}
		}
		joined := builder.String()
		trimmed := strings.TrimSpace(joined)
		return joined, isJSONObject(trimmed)
	default:
		return "", false
	}
}

// isJSONObject reports whether text parses as a JSON object. Arrays, scalars
// and null are not objects.
func isJSONObject(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(trimmed), &object); err != nil {
		return false
	}
	return object != nil
}

// hasLogprobData reports whether a choice's logprobs field carries actual
// token-level logprob data. An empty object, or an object whose token carriers
// are empty, only proves that the field exists; the mark claims the official
// dual-path logprobs were reproduced, which needs at least one token entry.
func hasLogprobData(value any) bool {
	object, ok := value.(map[string]any)
	if !ok || len(object) == 0 {
		return false
	}
	for _, carrier := range []string{"content", "tokens"} {
		if entries, ok := object[carrier].([]any); ok && len(entries) > 0 {
			return true
		}
	}
	return false
}

func toolCallNames(calls []any) []string {
	names := make([]string, 0, len(calls))
	for _, call := range calls {
		entry, ok := call.(map[string]any)
		if !ok {
			continue
		}
		function, ok := entry["function"].(map[string]any)
		if !ok {
			continue
		}
		if name, ok := function["name"].(string); ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func intField(source map[string]any, key string) int {
	switch value := source[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case json.Number:
		parsed, err := value.Int64()
		if err != nil {
			return -1
		}
		return int(parsed)
	default:
		return -1
	}
}

// relay error codes the gateway itself emits before or instead of an upstream
// answer (relaykit/types/error.go:41-80). A response carrying one of these says
// nothing about the channel's behaviour.
var relayLocalCodes = map[string]bool{
	"invalid_request":          true,
	"model_not_found":          true,
	"access_denied":            true,
	"get_channel_failed":       true,
	"gen_relay_info_failed":    true,
	"do_request_failed":        true,
	"invalid_api_type":         true,
	"bad_request_body":         true,
	"count_token_failed":       true,
	"insufficient_user_quota":  true,
	"read_request_body_failed": true,
}

func isRelayLocalError(code, fingerprint string) bool {
	if strings.HasPrefix(code, "channel:") || relayLocalCodes[code] {
		return true
	}
	lower := strings.ToLower(fingerprint)
	for _, marker := range []string{"no available channel", "无可用渠道", "channel not found"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// verdict is the three-way comparison outcome.
type verdict string

const (
	verdictConsistent   verdict = "consistent"
	verdictDivergence   verdict = "divergence"
	verdictInconclusive verdict = "inconclusive"
)

// strength records how much a consistent verdict actually proves.
const (
	strengthStructural = "structural"
	strengthAcceptance = "acceptance"
)

// finishReasonLength is the OpenAI-compatible finish_reason an upstream sends
// when it cut the answer off at the token cap. The relay uses the same word
// (relay/channel/openai/relay_openai_stream_usage_test.go:302).
const finishReasonLength = "length"

// looksTruncated reports whether a successful answer looks cut off by the
// probe's own max_tokens rather than finished by the model. The signals come
// from the response itself, and what they explain depends on where the mark's
// evidence lives:
//
//   - Generated-output evidence (tool_calls, reasoning content and reasoning
//     accounting) can only arrive as sampled completion, so a spent budget
//     explains its absence. For those marks the upstream's own
//     finish_reason=length and a completion count that reached (or exceeded)
//     max_tokens with no usable content are both truncation.
//   - Envelope evidence (logprobs) is attached to whatever tokens the upstream
//     did emit, so a length cut does not by itself explain its absence: a real
//     run returned logprobs under finish_reason=length at
//     completion_tokens=1/max_tokens=1 (round 3, ch=21 — the answer was cut and
//     the field was there anyway). The only cut that can explain an absent
//     envelope field is one that emitted no token at all, because then there
//     was nothing to attach it to; an absent usage block counts as that case,
//     since absence of evidence is not evidence.
//
// The predicate is deliberately one-directional: it can only turn an
// absence-derived divergence into an inconclusive row, never the reverse. It is
// reached only from the absence branch of classify.
func (sig signature) looksTruncated(maxTokens int, evidenceIsGeneratedOutput bool) bool {
	if evidenceIsGeneratedOutput {
		return sig.finishReason == finishReasonLength ||
			(!sig.HasContent && maxTokens > 0 && sig.CompletionTokens >= maxTokens)
	}
	if sig.finishReason != finishReasonLength {
		return false
	}
	return sig.CompletionTokens <= 0
}

// classify compares one channel probe against the baseline.
//
// baseline == nil means no measured official baseline is available: only the
// doc-derived expectation is known. In that mode a dimension whose only
// discriminator is prompt_tokens can never be called consistent, because
// equality of a token count cannot be observed without the reference count.
func classify(spec ProbeSpec, exp Expectation, baseline *signature, channel signature) (verdict, string, string, string) {
	if channel.Status == 0 {
		return verdictInconclusive, strengthAcceptance, "probe was not executed (transport error)", ""
	}
	if channel.RelayLocal {
		return verdictInconclusive, strengthAcceptance, "the gateway answered, not the channel (" + firstNonEmpty(channel.ErrorFingerprint, channel.ErrorCode) + ")", ""
	}

	expectedAccepted := exp.Official != expectUnsupported
	hasMeasuredBaseline := baseline != nil && baseline.Status != 0
	if hasMeasuredBaseline && (baseline.RelayLocal || !baseline.Accepted) {
		// A probe the official endpoint itself rejects cannot discriminate.
		return verdictInconclusive, strengthAcceptance, "the measured official baseline did not accept the probe (status " + itoa(baseline.Status) + "); the probe is not usable as a reference", ""
	}

	// Negative channel result.
	if !channel.Accepted {
		if !expectedAccepted {
			return verdictConsistent, strengthAcceptance, "channel rejected a shape the official expectation also rejects", ""
		}
		attributed := channel.ErrorField != ""
		officialAccepted := hasMeasuredBaseline
		if attributed || officialAccepted {
			shape := "official expectation=" + exp.Official + ", channel status=" + itoa(channel.Status)
			if officialAccepted {
				shape = "official status=" + itoa(baseline.Status) + ", channel status=" + itoa(channel.Status)
			}
			if channel.ErrorField != "" {
				shape += ", error names " + channel.ErrorField
			}
			basis := "channel did not accept the shape"
			if officialAccepted {
				basis = "official accepted the same request, the channel did not"
			} else if attributed {
				basis = "the channel's own error names the probe field"
			}
			return verdictDivergence, strengthStructural, basis, shape
		}
		return verdictInconclusive, strengthAcceptance, "channel rejected the request but the rejection is not attributable to this dimension", ""
	}

	// Both sides accepted (or only the channel, with a doc expectation).
	strength := strengthFor(exp)
	if hasMeasuredBaseline && spec.StructuralCompare != nil {
		if shapes := spec.StructuralCompare(*baseline, channel); len(shapes) > 0 {
			return verdictDivergence, strengthStructural, "structural fields differ from the measured official baseline", strings.Join(shapes, "; ")
		}
	}
	if hasMeasuredBaseline && hasDiscriminator(exp, discPromptTokens) {
		// A difference carries exactly the strength the dimension's
		// discriminator table declares, the same tier the equality branch
		// below returns: a prompt_tokens-only mark can no more be diverged on
		// a token count than it can be confirmed by one, so both directions
		// are gated identically in resultToPayload.
		switch {
		case baseline.PromptTokens < 0 || channel.PromptTokens < 0:
			return verdictInconclusive, strength, "prompt_tokens missing on one side; the doc-derived discriminator cannot be checked", ""
		case baseline.PromptTokens != channel.PromptTokens:
			return verdictDivergence, strength,
				"prompt_tokens differ from the measured official baseline",
				"prompt_tokens official=" + itoa(baseline.PromptTokens) + " channel=" + itoa(channel.PromptTokens)
		}
	}

	if hasMeasuredBaseline {
		return verdictConsistent, strength, "channel matches the measured official baseline on this probe", ""
	}

	// Document-derived baseline only. A consistency verdict needs positive
	// evidence: the dimension's structure is present, or the dimension is
	// acceptance-only by construction.
	//
	// Both structural branches need the same three facts: the dimension's
	// declared strength is structural, this tool knows how to test the mark,
	// and the test actually observed the structure. The absence branch without
	// those gates would call a missing structure a divergence even for a
	// dimension whose only discriminator is prompt_tokens (kimi-k3
	// usage.thinking_counting, spec.go:103), writing supported=false on every
	// accepted probe.
	evidenceKnown, evidencePresent := structuralEvidence(spec, channel)
	if strength == strengthStructural && evidenceKnown && evidencePresent {
		return verdictConsistent, strengthStructural,
			"channel produced the dimension's structural evidence; the expectation table marks the official endpoint as " + exp.Official, ""
	}
	if spec.StructuralAbsenceIsDivergence && strength == strengthStructural && evidenceKnown && !evidencePresent {
		// An answer that the probe's own budget cut off says nothing about the
		// channel where the missing structure is paid for out of that budget:
		// nine rows of a real run had to be withheld by hand for exactly this
		// shape (kimi-k3 tools.choice_semantics at 16/16, one 19/16,
		// deepseek-v4 usage.thinking_counting at 8/8). The kimi tools probe
		// budget has since been raised to toolsChoiceSemanticsProbeMaxTokens so
		// a real tool call can finish; this clause still protects every mark
		// whose evidence is generated output. That reasoning only holds
		// for generated-output evidence; for an envelope mark the probe budget
		// explains the absence only when the cut emitted no token at all. Both
		// cases are what looksTruncated decides.
		if channel.looksTruncated(spec.MaxTokens, spec.EvidenceIsGeneratedOutput) {
			return verdictInconclusive, strength,
				"channel accepted the request but the response looks truncated at the probe's own token cap (max_tokens=" + itoa(spec.MaxTokens) + ", completion_tokens=" + itoa(channel.CompletionTokens) + "), so the absent structure is explained by the probe budget rather than by the channel",
				""
		}
		return verdictDivergence, strengthStructural,
			"channel accepted the request but the dimension's structural evidence is absent",
			"structure absent: " + spec.Behavior
	}
	if hasDiscriminator(exp, discPromptTokens) {
		return verdictInconclusive, strength, "no measured official baseline: this dimension's discriminator is prompt_tokens, which a lone probe cannot check", ""
	}
	if exp.Official == expectUndefined {
		// The shipped table has no row for this mark, so there is no
		// doc-derived baseline to confirm: a bare 200 OK must not become a
		// stored mark (spec.go:9-11).
		return verdictInconclusive, strength, "no expectation table row for this mark: acceptance alone cannot confirm a dimension the doc-derived table does not describe", ""
	}
	if strength == strengthAcceptance && channel.Accepted {
		return verdictConsistent, strengthAcceptance,
			"channel accepted the shape; the expectation table marks the official endpoint as " + exp.Official + " (acceptance-level evidence only)", ""
	}
	return verdictInconclusive, strength, "no structural evidence and no measured baseline to confirm acceptance", ""
}

// structuralEvidence reports whether this tool knows how to test the mark's
// structural evidence (known) and whether the channel's response carries it
// (present). known == false means no explicit evidence rule exists for the
// behaviour, so neither presence nor absence may be treated as evidence: the
// fail-closed answer for an unmodelled mark is "no verdict", never a written
// mark.
func structuralEvidence(spec ProbeSpec, channel signature) (known, present bool) {
	switch spec.Behavior {
	case fitpolicy.BehaviorLogprobsDualPath:
		return true, channel.HasLogprobs
	case fitpolicy.BehaviorToolsChoiceSemantics:
		return true, channel.HasToolCalls
	case fitpolicy.BehaviorResponseFormatJSON:
		return true, channel.ContentIsJSONObject
	case fitpolicy.BehaviorThinkingCounting:
		return true, channel.HasReasoningContent || channel.ReasoningTokens >= 0
	default:
		return false, false
	}
}

func strengthFor(exp Expectation) string {
	if len(exp.Discriminators) == 1 && exp.Discriminators[0] == discAcceptance {
		return strengthAcceptance
	}
	for _, discriminator := range exp.Discriminators {
		if discriminator == discStructure {
			return strengthStructural
		}
	}
	return strengthAcceptance
}

func hasDiscriminator(exp Expectation, name string) bool {
	for _, discriminator := range exp.Discriminators {
		if discriminator == name {
			return true
		}
	}
	return false
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}

func boolText(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
