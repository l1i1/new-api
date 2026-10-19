package payloadnorm

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
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
	if !msg.IsStringContent() {
		t.Fatalf("message %q does not hold string content", msg.Role)
	}
	return msg.Content.(string)
}

func TestApplyRejectsEverythingOutsideScope(t *testing.T) {
	document := testDocument(200)
	cfg := scopedConfig(document)

	tests := []struct {
		name  string
		cfg   Config
		token int
		req   *dto.GeneralOpenAIRequest
		want  string
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
			name:  "multi-turn conversation",
			cfg:   cfg,
			token: 88,
			req:   withMessages(buildRequest(testSystemPrompt, document), "assistant", "之前的回答"),
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
				FingerprintChars: 4096, FingerprintSHA256: Fingerprint(strings.Repeat("A different opening paragraph. ", 500), 4096),
			},
			token: 88,
			req:   buildRequest(testSystemPrompt, document),
			want:  "fingerprint mismatch",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, decision := Apply(test.cfg, test.token, 7, test.req)
			if decision.Apply {
				t.Fatalf("rewrite applied, want rejection (%s)", decision.Reason)
			}
			if decision.Reason != test.want {
				t.Fatalf("reason = %q, want %q", decision.Reason, test.want)
			}
			if got != test.req {
				t.Fatalf("rejected request must be returned unchanged")
			}
		})
	}
}

func TestApplyRejectsToolAndMultimodalRequests(t *testing.T) {
	document := testDocument(200)
	cfg := scopedConfig(document)

	toolRequest := buildRequest(testSystemPrompt, document)
	toolRequest.Tools = []dto.ToolCallRequest{{Type: "function"}}
	if _, decision := Apply(cfg, 88, 7, toolRequest); decision.Apply {
		t.Fatalf("tool request must not be rewritten")
	}

	multimodal := buildRequest(testSystemPrompt, document)
	multimodal.Messages[1].SetMediaContent([]dto.MediaContent{{Type: "text", Text: document}})
	if _, decision := Apply(cfg, 88, 7, multimodal); decision.Apply {
		t.Fatalf("multimodal request must not be rewritten")
	}
}

func TestApplySplitModeKeepsTheTailAsUserTurn(t *testing.T) {
	document := testDocument(200)
	tail := "RAG/工作流真实对话场景RAG/"
	cfg := scopedConfig(document)
	cfg.DocumentChars = len(document)

	got, decision := Apply(cfg, 88, 7, buildRequest(testSystemPrompt, document+tail))
	if !decision.Apply {
		t.Fatalf("rewrite not applied: %s", decision.Reason)
	}
	wantSystem := testSystemPrompt + systemSeparator + document
	if system := messageText(t, got.Messages[0]); system != wantSystem {
		t.Fatalf("system message = %q..., want the instruction followed by the document", system[:80])
	}
	if user := messageText(t, got.Messages[1]); user != tail {
		t.Fatalf("user message = %q, want the unchanged tail %q", user, tail)
	}
	if !strings.HasSuffix(messageText(t, got.Messages[0]), document) {
		t.Fatalf("system message must end with the stable block")
	}
}

func TestApplyWholeContentModeMovesEverythingAndKeepsAUserTurn(t *testing.T) {
	document := testDocument(200)
	cfg := scopedConfig(document)

	got, decision := Apply(cfg, 88, 7, buildRequest(testSystemPrompt, document))
	if !decision.Apply {
		t.Fatalf("rewrite not applied: %s", decision.Reason)
	}
	if system := messageText(t, got.Messages[0]); system != testSystemPrompt+systemSeparator+document {
		t.Fatalf("system message must hold the instruction and the whole user content")
	}
	if user := messageText(t, got.Messages[1]); user != testSystemPrompt {
		t.Fatalf("user turn must keep the original instruction, got %q", user)
	}
}

func TestApplyLeavesTheInputRequestUntouched(t *testing.T) {
	document := testDocument(200)
	cfg := scopedConfig(document)
	req := buildRequest(testSystemPrompt, document)

	before, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	if _, decision := Apply(cfg, 88, 7, req); !decision.Apply {
		t.Fatalf("rewrite not applied: %s", decision.Reason)
	}
	after, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal input again: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("Apply mutated the caller's request")
	}
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
	if !decision.Apply {
		t.Fatalf("rewrite not applied: %s", decision.Reason)
	}
	firstBytes, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshal first rewrite: %v", err)
	}
	for i := 0; i < 5; i++ {
		again, _ := Apply(cfg, 88, 7, req)
		againBytes, err := json.Marshal(again)
		if err != nil {
			t.Fatalf("marshal repeated rewrite: %v", err)
		}
		if string(againBytes) != string(firstBytes) {
			t.Fatalf("rewrite %d produced different bytes", i)
		}
	}
}

func TestFingerprintCoversOnlyTheLeadingCharacters(t *testing.T) {
	document := testDocument(200)
	if Fingerprint(document+"tail-a", 4096) != Fingerprint(document+"tail-b", 4096) {
		t.Fatalf("fingerprint must ignore content past the configured prefix length")
	}
	if Fingerprint(document, 4096) == Fingerprint("other", 4096) {
		t.Fatalf("fingerprint must distinguish different documents")
	}
}

func withMessages(req *dto.GeneralOpenAIRequest, role, content string) *dto.GeneralOpenAIRequest {
	msg := dto.Message{Role: role}
	msg.SetStringContent(content)
	req.Messages = append(req.Messages, msg)
	return req
}
