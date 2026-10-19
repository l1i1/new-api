package payloadnorm

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/stretchr/testify/require"
)

const testSystemPrompt = "你是一个专业的文档助手，总结文章并输出32token左右的内容。"

func testDocument(n int) string {
	return strings.Repeat("The lake remembers every winter it has seen. ", n)
}

func buildRequest(system, user string) *dto.GeneralOpenAIRequest {
	sys := dto.Message{Role: "system"}
	sys.SetStringContent(system)
	usr := dto.Message{Role: "user"}
	usr.SetStringContent(user)
	return &dto.GeneralOpenAIRequest{Model: "glm-5.3-flash", Messages: []dto.Message{sys, usr}}
}

func scopedConfig(document string) Config {
	return Config{
		Enabled:           true,
		TokenIDs:          []int{88},
		ChannelIDs:        []int{7},
		MinUserChars:      1000,
		FingerprintChars:  4096,
		FingerprintSHA256: Fingerprint(document, 4096),
	}
}

func messageText(t *testing.T, msg dto.Message) string {
	t.Helper()
	require.True(t, msg.IsStringContent(), "message %q does not hold plain string content", msg.Role)
	text, ok := msg.Content.(string)
	require.True(t, ok)
	return text
}

func withMessage(req *dto.GeneralOpenAIRequest, role, content string) *dto.GeneralOpenAIRequest {
	msg := dto.Message{Role: role}
	msg.SetStringContent(content)
	req.Messages = append(req.Messages, msg)
	return req
}

// TestApplyRejectsEverythingOutsideScope pins the wiring rule: the rewrite is a per-request
// decision, and every request that is not exactly the configured single-turn text request
// passes through untouched. A rewriten rejected request must also come back as the same
// pointer, so the relay cannot accidentally forward a copy it believes was rewritten.
func TestApplyRejectsEverythingOutsideScope(t *testing.T) {
	document := testDocument(200)
	cfg := scopedConfig(document)

	tests := []struct {
		name    string
		cfg     Config
		token   int
		channel int
		req     *dto.GeneralOpenAIRequest
		want    string
	}{
		{
			name:  "disabled",
			cfg:   Config{},
			token: 88,
			req:   buildRequest(testSystemPrompt, document),
			want:  "disabled",
		},
		{
			name:  "empty allowlists mean nobody",
			cfg:   Config{Enabled: true, MinUserChars: 1000},
			token: 88,
			req:   buildRequest(testSystemPrompt, document),
			want:  "token/channel not in scope",
		},
		{
			name:  "token outside the allowlist",
			cfg:   cfg,
			token: 99,
			req:   buildRequest(testSystemPrompt, document),
			want:  "token/channel not in scope",
		},
		{
			name:    "channel outside the allowlist",
			cfg:     cfg,
			token:   88,
			channel: 9,
			req:     buildRequest(testSystemPrompt, document),
			want:    "token/channel not in scope",
		},
		{
			name:  "multi-turn conversation",
			cfg:   cfg,
			token: 88,
			req:   withMessage(buildRequest(testSystemPrompt, document), "assistant", "之前的回答"),
			want:  "not a single-turn request",
		},
		{
			name:  "user message below the size floor",
			cfg:   cfg,
			token: 88,
			req:   buildRequest(testSystemPrompt, "short question"),
			want:  "user message below the size floor",
		},
		{
			name: "fingerprint mismatch",
			cfg: Config{
				Enabled: true, TokenIDs: []int{88}, ChannelIDs: []int{7}, MinUserChars: 1000,
				FingerprintChars:  4096,
				FingerprintSHA256: Fingerprint(strings.Repeat("A different opening paragraph. ", 500), 4096),
			},
			token: 88,
			req:   buildRequest(testSystemPrompt, document),
			want:  "fingerprint mismatch",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			channelID := test.channel
			if channelID == 0 {
				channelID = 7
			}
			got, decision := Apply(test.cfg, test.token, channelID, test.req)
			require.False(t, decision.Apply, "rewrite applied, want rejection (%s)", decision.Reason)
			require.Equal(t, test.want, decision.Reason)
			require.True(t, got == test.req, "rejected request must be returned unchanged")
		})
	}
}

// TestApplyRejectsEveryToolSurface covers the wiring rule, not the rewrite: a request that
// declares tools in any shape the OpenAI-compatible wire format carries must pass through
// untouched. The fixtures are raw wire bodies on purpose - the point is that every field the
// upstream may interpret as a tool declaration is recognised, including the legacy top-level
// fields and the per-message shapes (Kimi's dynamic tool loading lives on the message).
func TestApplyRejectsEveryToolSurface(t *testing.T) {
	document := testDocument(200)
	cfg := scopedConfig(document)

	documentJSON, err := json.Marshal(document)
	require.NoError(t, err)
	systemJSON, err := json.Marshal(testSystemPrompt)
	require.NoError(t, err)

	messagesOpen := `"messages":[{"role":"system","content":` + string(systemJSON) + `},{"role":"user","content":` + string(documentJSON)
	withTopLevel := func(extra string) string {
		return `{"model":"glm-5.3-flash",` + messagesOpen + `}],` + extra + `}`
	}

	tests := []struct {
		name string
		body string
	}{
		{
			name: "top-level tools",
			body: withTopLevel(`"tools":[{"type":"function","function":{"name":"lookup"}}]`),
		},
		{
			name: "top-level tool_choice",
			body: withTopLevel(`"tool_choice":"auto"`),
		},
		{
			name: "legacy top-level functions",
			body: withTopLevel(`"functions":[{"name":"lookup"}]`),
		},
		{
			name: "legacy top-level function_call",
			body: withTopLevel(`"function_call":"auto"`),
		},
		{
			name: "message tool_calls",
			body: `{"model":"glm-5.3-flash",` + messagesOpen + `,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]}]}`,
		},
		{
			name: "message tool_call_id",
			body: `{"model":"glm-5.3-flash",` + messagesOpen + `,"tool_call_id":"call_1"}]}`,
		},
		{
			name: "message-level tools",
			body: `{"model":"glm-5.3-flash","messages":[{"role":"system","content":` + string(systemJSON) + `,"tools":[{"name":"lookup"}]},{"role":"user","content":` + string(documentJSON) + `}]}`,
		},
		{
			name: "multimodal user content",
			body: `{"model":"glm-5.3-flash","messages":[{"role":"system","content":` + string(systemJSON) + `},{"role":"user","content":[{"type":"text","text":` + string(documentJSON) + `}]}]}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var req dto.GeneralOpenAIRequest
			require.NoError(t, json.Unmarshal([]byte(test.body), &req), "fixture must decode: %s", test.body)
			require.Len(t, req.Messages, 2)
			if _, decision := Apply(cfg, 88, 7, &req); decision.Apply {
				t.Fatalf("request with %s must not be rewritten", test.name)
			}
		})
	}
}

func TestApplySplitModeKeepsTheTailAsUserTurn(t *testing.T) {
	document := testDocument(200)
	const tail = "RAG/工作流真实对话场景RAG/"
	cfg := scopedConfig(document)
	cfg.DocumentChars = len(document)

	got, decision := Apply(cfg, 88, 7, buildRequest(testSystemPrompt, document+tail))
	require.True(t, decision.Apply, "rewrite not applied: %s", decision.Reason)
	require.Equal(t, testSystemPrompt+systemSeparator+document, messageText(t, got.Messages[0]))
	require.Equal(t, tail, messageText(t, got.Messages[1]))
}

func TestApplyWholeContentModeMovesEverythingAndKeepsAUserTurn(t *testing.T) {
	document := testDocument(200)
	cfg := scopedConfig(document)

	got, decision := Apply(cfg, 88, 7, buildRequest(testSystemPrompt, document))
	require.True(t, decision.Apply, "rewrite not applied: %s", decision.Reason)
	require.Equal(t, testSystemPrompt+systemSeparator+document, messageText(t, got.Messages[0]))
	require.Equal(t, testSystemPrompt, messageText(t, got.Messages[1]))
}

// TestApplyRejectsDegenerateDocumentChars is the conservative-failure contract: only an
// explicit zero may choose the whole-message rewrite, and a positive length that reaches the
// end of the message must be rejected instead of silently becoming the most aggressive mode.
func TestApplyRejectsDegenerateDocumentChars(t *testing.T) {
	document := testDocument(200)
	userText := document + "RAG/"
	userRunes := utf8.RuneCountInString(userText)

	tests := []struct {
		name  string
		chars int
		want  string
	}{
		{name: "negative", chars: -1, want: "negative document length"},
		{name: "exactly the whole message", chars: userRunes, want: "document length covers the whole user message"},
		{name: "beyond the whole message", chars: userRunes + 100, want: "document length covers the whole user message"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := buildRequest(testSystemPrompt, userText)
			cfg := scopedConfig(document)
			cfg.FingerprintSHA256 = ""
			cfg.DocumentChars = test.chars
			got, decision := Apply(cfg, 88, 7, req)
			require.False(t, decision.Apply)
			require.Equal(t, test.want, decision.Reason)
			require.True(t, got == req, "rejected request must be returned unchanged")
		})
	}
}

func TestApplyLeavesTheInputRequestUntouched(t *testing.T) {
	document := testDocument(200)
	cfg := scopedConfig(document)
	req := buildRequest(testSystemPrompt, document)

	before, err := common.Marshal(req)
	require.NoError(t, err)
	_, decision := Apply(cfg, 88, 7, req)
	require.True(t, decision.Apply, "rewrite not applied: %s", decision.Reason)
	after, err := common.Marshal(req)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after), "Apply mutated the caller's request")
}

// TestApplyIsDeterministic is the property the upstream cache depends on: the same input must
// produce the same bytes, otherwise the rewrite would scatter the very prefix it is meant to
// stabilise.
func TestApplyIsDeterministic(t *testing.T) {
	document := testDocument(200)
	cfg := scopedConfig(document)
	cfg.DocumentChars = len(document)
	req := buildRequest(testSystemPrompt, document+"RAG/")

	first, decision := Apply(cfg, 88, 7, req)
	require.True(t, decision.Apply, "rewrite not applied: %s", decision.Reason)
	firstBytes, err := common.Marshal(first)
	require.NoError(t, err)
	for i := 0; i < 5; i++ {
		again, _ := Apply(cfg, 88, 7, req)
		againBytes, err := common.Marshal(again)
		require.NoError(t, err)
		require.Equal(t, string(firstBytes), string(againBytes), "rewrite %d produced different bytes", i)
	}
}

func TestFingerprintCoversOnlyTheLeadingCharacters(t *testing.T) {
	document := testDocument(200)
	require.Equal(t, Fingerprint(document+"tail-a", 4096), Fingerprint(document+"tail-b", 4096),
		"fingerprint must ignore content past the configured prefix length")
	require.NotEqual(t, Fingerprint(document, 4096), Fingerprint("other", 4096),
		"fingerprint must distinguish different documents")
}

// TestApplySplitModeNeverSplitsAUTF8Character guards the real mainland workload: a Chinese
// document is multi-byte, so a split expressed in characters must land on a rune boundary.
// A broken boundary would relay invalid UTF-8 and change the text the model sees.
func TestApplySplitModeNeverSplitsAUTF8Character(t *testing.T) {
	document := strings.Repeat("湖记得它见过的每一个冬天。", 900)
	const tail = "RAG/工作流真实对话场景"
	cfg := scopedConfig(document)
	cfg.MinUserChars = 100
	cfg.DocumentChars = 300 // characters, not bytes
	cfg.FingerprintSHA256 = ""

	out, decision := Apply(cfg, 88, 7, buildRequest(testSystemPrompt, document+tail))
	require.True(t, decision.Apply, "rewrite not applied: %s", decision.Reason)
	systemText := messageText(t, out.Messages[0])
	userText := messageText(t, out.Messages[1])
	require.True(t, utf8.ValidString(systemText))
	require.True(t, utf8.ValidString(userText))
	moved := strings.TrimPrefix(systemText, testSystemPrompt+systemSeparator)
	require.Equal(t, document+tail, moved+userText, "rewrite changed the text")
	require.Equal(t, 300, utf8.RuneCountInString(moved))
}

// TestApplyRejectsWholeModeWithoutSystemPrompt: moving the whole user turn would leave an
// empty user message, which some upstreams reject. The rewrite must fail closed instead.
func TestApplyRejectsWholeModeWithoutSystemPrompt(t *testing.T) {
	document := testDocument(200)
	cfg := scopedConfig(document)
	cfg.FingerprintSHA256 = ""

	_, decision := Apply(cfg, 88, 7, buildRequest("", document))
	require.False(t, decision.Apply, "whole-content mode must not apply without a system prompt")
}

// TestApplyCountsCharactersForTheSizeFloor: 2,000 Chinese characters are 6,000 bytes, so a
// byte-counted floor would fire on a message that is far shorter than the operator intended.
func TestApplyCountsCharactersForTheSizeFloor(t *testing.T) {
	document := strings.Repeat("湖", 2000)
	cfg := Config{Enabled: true, TokenIDs: []int{88}, MinUserChars: 3000}
	_, decision := Apply(cfg, 88, 7, buildRequest(testSystemPrompt, document))
	require.False(t, decision.Apply, "byte length must not satisfy a character floor")
}

// ---------------------------------------------------------------------------
// Setting document: parse, validate, fail-closed install, snapshot
// ---------------------------------------------------------------------------

func TestParseSettingAcceptsACompleteDocument(t *testing.T) {
	document := `{
		"enabled": true,
		"token_ids": [88],
		"channel_ids": [7],
		"min_user_chars": 100000,
		"fingerprint_chars": 4096,
		"fingerprint_sha256": "` + Fingerprint("x", 4096) + `",
		"document_chars": 315569
	}`
	setting, err := ParseSetting([]byte(document))
	require.NoError(t, err)
	require.True(t, setting.Enabled)
	require.Equal(t, []int{88}, setting.TokenIDs)
	require.Equal(t, []int{7}, setting.ChannelIDs)
	require.Equal(t, 100000, setting.MinUserChars)
	require.Equal(t, 4096, setting.FingerprintChars)
	require.Equal(t, 315569, setting.DocumentChars)
}

// TestParseSettingRejectsBadDocuments is the fail-closed contract of the write path: a
// document that does not mean exactly one thing is rejected instead of being applied
// partially. Unknown fields are rejected because a misspelled field would otherwise be
// silently ignored and the document would not mean what it says.
func TestParseSettingRejectsBadDocuments(t *testing.T) {
	scope := `"token_ids":[88]`
	tests := []struct {
		name     string
		document string
		wantErr  string
	}{
		{name: "not json", document: `{`, wantErr: "payload normalize setting"},
		{name: "unknown field", document: `{"enabled":true,` + scope + `,"documnt_chars":10}`, wantErr: "unknown field"},
		{name: "trailing data", document: `{"enabled":true,` + scope + `} {}`, wantErr: "trailing data"},
		{name: "negative min_user_chars", document: `{"enabled":true,` + scope + `,"min_user_chars":-1}`, wantErr: "min_user_chars"},
		{name: "negative fingerprint_chars", document: `{"enabled":true,` + scope + `,"fingerprint_chars":-1}`, wantErr: "fingerprint_chars"},
		{name: "negative document_chars", document: `{"enabled":true,` + scope + `,"document_chars":-1}`, wantErr: "document_chars"},
		{name: "bad fingerprint hex", document: `{"enabled":true,` + scope + `,"fingerprint_sha256":"not-hex"}`, wantErr: "not hex"},
		{name: "short fingerprint", document: `{"enabled":true,` + scope + `,"fingerprint_sha256":"abcd"}`, wantErr: "hex characters"},
		{name: "non-positive token id", document: `{"enabled":true,"token_ids":[0]}`, wantErr: "must be positive"},
		{name: "non-positive channel id", document: `{"enabled":true,"channel_ids":[-7]}`, wantErr: "must be positive"},
		{name: "enabled without scope", document: `{"enabled":true}`, wantErr: "matches nobody"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseSetting([]byte(test.document))
			require.ErrorContains(t, err, test.wantErr)
		})
	}
}

func TestValidateSettingJSONAllowsBlankAsDisable(t *testing.T) {
	require.NoError(t, ValidateSettingJSON(""))
	require.NoError(t, ValidateSettingJSON("   "))
	require.Error(t, ValidateSettingJSON(`{"enabled":true}`))
}

// TestInstallSettingJSONFailsClosed is the blocker this design exists for: an unreadable
// document must take the rewrite out of service, never leave an older scope live and never
// install a half-applied one.
func TestInstallSettingJSONFailsClosed(t *testing.T) {
	t.Cleanup(InstallDisabled)

	require.NoError(t, InstallSettingJSON(`{"enabled":true,"token_ids":[88],"channel_ids":[7],"document_chars":100}`))
	require.True(t, Current().Enabled)

	require.Error(t, InstallSettingJSON(`{"enabled":true,"token_ids":`))
	require.False(t, Current().Enabled, "an unparseable document must disable the rewrite")

	require.NoError(t, InstallSettingJSON(`{"enabled":true,"token_ids":[88],"document_chars":100}`))
	require.True(t, Current().Enabled)
	require.Error(t, InstallSettingJSON(`{"enabled":true,"token_ids":[0]}`))
	require.False(t, Current().Enabled, "a rejected document must disable the rewrite")

	require.NoError(t, InstallSettingJSON(""))
	require.False(t, Current().Enabled, "a blank document means disabled")
}

func TestCurrentConfigMirrorsTheSetting(t *testing.T) {
	t.Cleanup(InstallDisabled)
	require.NoError(t, InstallSettingJSON(`{"enabled":true,"token_ids":[88],"channel_ids":[7],"min_user_chars":1000,"document_chars":42}`))

	config := CurrentConfig()
	require.True(t, config.Enabled)
	require.Equal(t, []int{88}, config.TokenIDs)
	require.Equal(t, []int{7}, config.ChannelIDs)
	require.Equal(t, 1000, config.MinUserChars)
	require.Equal(t, 42, config.DocumentChars)
}

// TestInstallAndReadAreSafeForConcurrentUse is the regression guard for the reload race: the
// request path loads one immutable snapshot while an admin update installs the next one. The
// assertion that matters runs under `go test -race`; the loops stay small on purpose.
func TestInstallAndReadAreSafeForConcurrentUse(t *testing.T) {
	t.Cleanup(InstallDisabled)

	documents := []string{
		`{"enabled":true,"token_ids":[88],"channel_ids":[7],"document_chars":100}`,
		`{"enabled":true,"channel_ids":[7]}`,
		`{"enabled":false}`,
		`{"enabled":true,"token_ids":[88]}`,
	}

	installErrors := make(chan error, 1)
	go func() {
		var firstErr error
		for i := 0; i < 50; i++ {
			if err := InstallSettingJSON(documents[i%len(documents)]); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		installErrors <- firstErr
	}()

	for i := 0; i < 200; i++ {
		config := CurrentConfig()
		// Touch what the request path touches; a torn read would surface under -race.
		_ = len(config.TokenIDs)
		_ = config.DocumentChars
	}

	require.NoError(t, <-installErrors)
}
