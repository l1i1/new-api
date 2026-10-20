package openai

import (
	"errors"
	"fmt"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// NormalizeResponseShape stabilizes the field set of an OpenAI-compatible
// non-streaming chat.completion body. Some compatible upstreams omit fields
// depending on the request (for example choices[0].logprobs only when logprobs
// were requested, message.tool_calls only when tools were used, or
// usage.prompt_tokens_details.cache_write_tokens only when the cache was
// written). Clients that bind the response into a fixed schema then see a
// different shape on every request even though the upstream is the same.
//
// The normalization only fills in fields that are absent, and only with the
// values that mean "not applicable": JSON null for optional objects/arrays and
// 0 for cache_write_tokens. Present values are left byte-for-byte untouched, so
// the upstream's semantics and key order survive. Bodies that are not JSON
// objects, or that have no choices array, are returned unchanged: this must
// never break a response it was not written for.
func NormalizeResponseShape(body []byte) ([]byte, error) {
	if !gjson.ValidBytes(body) {
		return nil, errors.New("normalize response shape: invalid JSON")
	}
	if len(body) == 0 || body[0] != '{' {
		return body, nil
	}

	choices := gjson.GetBytes(body, "choices")
	if !choices.Exists() || !choices.IsArray() {
		return body, nil
	}

	normalized := body
	for i, choice := range choices.Array() {
		if !choice.IsObject() {
			continue
		}
		choicePath := fmt.Sprintf("choices.%d", i)
		if !gjson.GetBytes(normalized, choicePath+".logprobs").Exists() {
			var err error
			if normalized, err = sjson.SetBytes(normalized, choicePath+".logprobs", nil); err != nil {
				return nil, err
			}
		}
		if gjson.GetBytes(normalized, choicePath+".message").IsObject() {
			for _, field := range []string{"name", "tool_calls"} {
				fieldPath := choicePath + ".message." + field
				if gjson.GetBytes(normalized, fieldPath).Exists() {
					continue
				}
				var err error
				if normalized, err = sjson.SetBytes(normalized, fieldPath, nil); err != nil {
					return nil, err
				}
			}
		}
	}

	if usage := gjson.GetBytes(normalized, "usage"); usage.IsObject() {
		details := gjson.GetBytes(normalized, "usage.prompt_tokens_details")
		switch {
		case details.IsObject():
			if !gjson.GetBytes(normalized, "usage.prompt_tokens_details.cache_write_tokens").Exists() {
				var err error
				if normalized, err = sjson.SetBytes(normalized, "usage.prompt_tokens_details.cache_write_tokens", 0); err != nil {
					return nil, err
				}
			}
		case !details.Exists() || details.Type == gjson.Null:
			var err error
			if normalized, err = sjson.SetRawBytes(normalized, "usage.prompt_tokens_details", []byte(`{"cache_write_tokens":0}`)); err != nil {
				return nil, err
			}
		}
	}
	return normalized, nil
}
