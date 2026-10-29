package common

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// TestCompareGjsonValuesContainsOnArrays pins the array rule of the `contains`
// condition: a JSON array contains a value when one of its elements does. Before
// this, `contains ""` matched the serialized array and therefore matched `[]`,
// which made the ch91 stop guard reject every Claude-Code style request.
func TestCompareGjsonValuesContainsOnArrays(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		path   string
		target string
		want   bool
	}{
		{name: "empty array does not contain", body: `{"stop":[]}`, path: "stop", target: "", want: false},
		{name: "empty array does not contain a word", body: `{"stop":[]}`, path: "stop", target: "西瓜", want: false},
		{name: "non-empty array contains empty", body: `{"stop":["西瓜"]}`, path: "stop", target: "", want: true},
		{name: "array element contains word", body: `{"stop":["西瓜","苹果"]}`, path: "stop", target: "苹果", want: true},
		{name: "array element misses word", body: `{"stop":["西瓜"]}`, path: "stop", target: "苹果", want: false},
		{name: "string form still matches", body: `{"stop":"西瓜"}`, path: "stop", target: "", want: true},
		{name: "string form misses word", body: `{"stop":"西瓜"}`, path: "stop", target: "苹果", want: false},
		{name: "nested pipeline keeps raw comparison", body: `{"messages":[{"content":[{"image_url":{"url":"https://x/y.png"}}]}]}`, path: "messages.#.content.#.image_url.url", target: "http", want: true},
		{name: "nested pipeline without url", body: `{"messages":[{"content":[{"text":"hi"}]}]}`, path: "messages.#.content.#.image_url.url", target: "http", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value := gjson.Get(tc.body, tc.path)
			require.True(t, value.Exists())
			targetJSON, marshalErr := json.Marshal(tc.target)
			require.NoError(t, marshalErr)
			got, err := compareGjsonValues(value, gjson.ParseBytes(targetJSON), "contains")
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestApplyParamOverrideStopGuardIgnoresEmptyStop replays the production ch91
// guard (ch91 silently drops stop sequences, so requests that really carry one
// are served elsewhere). `"stop": []` means "no stop sequences" and must reach
// the upstream, otherwise every Claude-Code style client is bounced on every
// request - the 2026-10-29 amplifier that pushed this customer's traffic onto
// the channel that was cutting tool-call streams.
func TestApplyParamOverrideStopGuardIgnoresEmptyStop(t *testing.T) {
	override := map[string]any{
		"operations": []any{
			map[string]any{
				"mode": "return_error",
				"value": map[string]any{
					"status_code": 503,
					"code":        "channel:unsupported_feature",
					"type":        "upstream_error",
					"message":     "stop sequences are not honored on this upstream (silently ignored); serve elsewhere",
					"skip_retry":  false,
				},
				"conditions": []any{
					map[string]any{"path": "stop", "mode": "contains", "value": ""},
				},
				"logic": "AND",
			},
		},
	}

	passed := []string{
		`{"model":"kimi-k3.1-pro","stop":[]}`,
		`{"model":"kimi-k3.1-pro","stop":null}`,
		`{"model":"kimi-k3.1-pro"}`,
	}
	for _, body := range passed {
		t.Run("passes "+body, func(t *testing.T) {
			out, err := ApplyParamOverride([]byte(body), override, nil)
			require.NoError(t, err, "an empty stop list carries no stop sequence")
			assert.JSONEq(t, body, string(out))
		})
	}

	rejected := []string{
		`{"model":"kimi-k3.1-pro","stop":["西瓜"]}`,
		`{"model":"kimi-k3.1-pro","stop":"西瓜"}`,
	}
	for _, body := range rejected {
		t.Run("rejects "+body, func(t *testing.T) {
			_, err := ApplyParamOverride([]byte(body), override, nil)
			require.Error(t, err)
			guard, ok := AsParamOverrideReturnError(err)
			require.True(t, ok, "the stop guard must surface as a channel rejection")
			assert.Equal(t, 503, guard.StatusCode)
		})
	}
}
