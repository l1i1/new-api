package operation_setting

import "github.com/QuantumNous/new-api/setting/config"

type ChannelAffinityKeySource struct {
	Type string `json:"type"` // context_int, context_string, request_header, gjson
	Key  string `json:"key,omitempty"`
	Path string `json:"path,omitempty"`
}

type ChannelAffinityRule struct {
	Name             string                     `json:"name"`
	ModelRegex       []string                   `json:"model_regex"`
	PathRegex        []string                   `json:"path_regex"`
	UserAgentInclude []string                   `json:"user_agent_include,omitempty"`
	KeySources       []ChannelAffinityKeySource `json:"key_sources"`

	ValueRegex string `json:"value_regex"`
	TTLSeconds int    `json:"ttl_seconds"`

	ParamOverrideTemplate map[string]interface{} `json:"param_override_template,omitempty"`

	SkipRetryOnFailure bool `json:"skip_retry_on_failure"`

	IncludeUsingGroup bool `json:"include_using_group"`
	IncludeModelName  bool `json:"include_model_name"`
	IncludeRuleName   bool `json:"include_rule_name"`
}

type ChannelAffinitySetting struct {
	Enabled               bool                  `json:"enabled"`
	SwitchOnSuccess       bool                  `json:"switch_on_success"`
	KeepOnChannelDisabled bool                  `json:"keep_on_channel_disabled"`
	MaxEntries            int                   `json:"max_entries"`
	DefaultTTLSeconds     int                   `json:"default_ttl_seconds"`
	Rules                 []ChannelAffinityRule `json:"rules"`
}

// codexCliPassThroughHeaders is the full upstream Codex session-routing
// header set. Keep it as the reference list for restoring upstream parity.
// Codex uses lower-case header names, while HTTP matching here is
// case-insensitive. The default template ships the trimmed production set
// (codexCliPassThroughHeadersActive) instead.
// Request session/thread headers:
// https://github.com/openai/codex/commit/7c7b4861d88960f7e3bd5b7f30f8351be666dd84
// Responses metadata headers/client_metadata:
// https://github.com/openai/codex/commit/14df0e8833aad0d6d78287954b61ffac67af936c
// x-codex-turn-state response/request round trip:
// https://github.com/openai/codex/commit/ebdd8795e924a8149b616e46ca2ed7848c207a4b
var codexCliPassThroughHeaders = []string{
	"Originator",
	"Session_id",
	"Thread_id",
	"Session-Id",
	"Thread-Id",
	"X-Client-Request-Id",
	"User-Agent",
	"X-Codex-Beta-Features",
	"X-Codex-Turn-State",
	"X-Codex-Turn-Metadata",
	"X-Codex-Window-Id",
	"X-Codex-Parent-Thread-Id",
	//"X-Codex-Installation-Id",
	"X-OpenAI-Subagent",
	"X-OpenAI-Memgen-Request",
	//"X-OAI-Attestation",
	"X-ResponsesAPI-Include-Timing-Metrics",
	"X-OpenAI-Internal-Codex-Responses-Lite",
}

// codexCliPassThroughHeadersActive is the header set shipped by the default
// "codex cli trace" rule. It matches the Tokeness production rules: only the
// headers proven safe to pass upstream are forwarded, so session routing
// works without leaking optional flags.
var codexCliPassThroughHeadersActive = []string{
	"Originator",
	"Session_id",
	"User-Agent",
	"X-Codex-Beta-Features",
	"X-Codex-Turn-Metadata",
}

var claudeCliPassThroughHeaders = []string{
	"X-Stainless-Arch",
	"X-Stainless-Lang",
	"X-Stainless-Os",
	"X-Stainless-Package-Version",
	"X-Stainless-Retry-Count",
	"X-Stainless-Runtime",
	"X-Stainless-Runtime-Version",
	"X-Stainless-Timeout",
	"User-Agent",
	"X-App",
	"Anthropic-Beta",
	"Anthropic-Dangerous-Direct-Browser-Access",
	"Anthropic-Version",
}

func buildPassHeaderTemplate(headers []string) map[string]interface{} {
	clonedHeaders := make([]string, 0, len(headers))
	clonedHeaders = append(clonedHeaders, headers...)
	return map[string]interface{}{
		"operations": []map[string]interface{}{
			{
				"mode":        "pass_headers",
				"value":       clonedHeaders,
				"keep_origin": true,
			},
		},
	}
}

var channelAffinitySetting = ChannelAffinitySetting{
	Enabled:               true,
	SwitchOnSuccess:       true,
	KeepOnChannelDisabled: false,
	MaxEntries:            100_000,
	DefaultTTLSeconds:     3600,
	Rules: []ChannelAffinityRule{
		{
			Name:       "codex cli trace",
			ModelRegex: []string{"^gpt-.*$"},
			PathRegex:  []string{"/v1/responses"},
			KeySources: []ChannelAffinityKeySource{
				{Type: "gjson", Path: "prompt_cache_key"},
			},
			ValueRegex:            "",
			TTLSeconds:            0,
			ParamOverrideTemplate: buildPassHeaderTemplate(codexCliPassThroughHeadersActive),
			SkipRetryOnFailure:    false,
			IncludeUsingGroup:     true,
			IncludeRuleName:       true,
			UserAgentInclude:      []string{},
		},
		{
			Name:       "claude cli trace",
			ModelRegex: []string{"^claude-.*$"},
			PathRegex:  []string{"/v1/messages"},
			KeySources: []ChannelAffinityKeySource{
				{Type: "gjson", Path: "metadata.user_id"},
			},
			ValueRegex:            "",
			TTLSeconds:            0,
			ParamOverrideTemplate: buildPassHeaderTemplate(claudeCliPassThroughHeaders),
			SkipRetryOnFailure:    false,
			IncludeUsingGroup:     true,
			IncludeRuleName:       true,
			UserAgentInclude:      []string{},
		},
		{
			// Largest uncovered block: /v1/chat/completions carries no
			// session identifier, so fall back to request `user`/`metadata`,
			// then token id, then user id. Keyed per model so one token
			// calling several models keeps independent bindings.
			Name:       "chat completion affinity",
			ModelRegex: []string{".*"},
			PathRegex:  []string{"^/v1/chat/completions", "^/pg/chat/completions"},
			KeySources: []ChannelAffinityKeySource{
				{Type: "gjson", Path: "metadata.user_id"},
				{Type: "gjson", Path: "user"},
				{Type: "context_int", Key: "token_id"},
				{Type: "context_int", Key: "id"},
			},
			ValueRegex:         "",
			TTLSeconds:         0,
			SkipRetryOnFailure: false,
			IncludeUsingGroup:  true,
			IncludeModelName:   true,
			IncludeRuleName:    true,
			UserAgentInclude:   []string{},
		},
		{
			// Non-Codex traffic on /v1/responses (deepseek, grok, codex-auto-
			// review, ...) is missed by the `^gpt-.*$` rule above. Prefer the
			// native prompt_cache_key when present, else token/user fallback.
			Name:       "responses trace",
			ModelRegex: []string{".*"},
			PathRegex:  []string{"^/v1/responses"},
			KeySources: []ChannelAffinityKeySource{
				{Type: "gjson", Path: "prompt_cache_key"},
				{Type: "context_int", Key: "token_id"},
				{Type: "context_int", Key: "id"},
			},
			ValueRegex:         "",
			TTLSeconds:         0,
			SkipRetryOnFailure: false,
			IncludeUsingGroup:  true,
			IncludeModelName:   true,
			IncludeRuleName:    true,
			UserAgentInclude:   []string{},
		},
		{
			// Non-Claude traffic on /v1/messages (deepseek, gpt, ...) is
			// missed by the `^claude-.*$` rule above.
			Name:       "messages trace",
			ModelRegex: []string{".*"},
			PathRegex:  []string{"^/v1/messages"},
			KeySources: []ChannelAffinityKeySource{
				{Type: "gjson", Path: "metadata.user_id"},
				{Type: "context_int", Key: "token_id"},
				{Type: "context_int", Key: "id"},
			},
			ValueRegex:         "",
			TTLSeconds:         0,
			SkipRetryOnFailure: false,
			IncludeUsingGroup:  true,
			IncludeModelName:   true,
			IncludeRuleName:    true,
			UserAgentInclude:   []string{},
		},
		{
			// Gemini native paths have no OpenAI/Anthropic session header;
			// a coarse token/user key is the only stable signal.
			Name:       "gemini native affinity",
			ModelRegex: []string{".*"},
			PathRegex:  []string{"^/v1beta/models/", "^/v1/models/"},
			KeySources: []ChannelAffinityKeySource{
				{Type: "context_int", Key: "token_id"},
				{Type: "context_int", Key: "id"},
			},
			ValueRegex:         "",
			TTLSeconds:         0,
			SkipRetryOnFailure: false,
			IncludeUsingGroup:  true,
			IncludeModelName:   true,
			IncludeRuleName:    true,
			UserAgentInclude:   []string{},
		},
	},
}

func init() {
	config.GlobalConfig.Register("channel_affinity_setting", &channelAffinitySetting)
}

func GetChannelAffinitySetting() *ChannelAffinitySetting {
	return &channelAffinitySetting
}
