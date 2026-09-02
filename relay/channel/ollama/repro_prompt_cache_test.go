package ollama

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/gin-gonic/gin"
)

// Temporary diagnostic helper: replays a captured client request through the
// real conversion + prompt-cache identity pipeline. Fixture path via REPRO_REQ.
func TestReproPromptCacheIdentity(t *testing.T) {
	path := os.Getenv("REPRO_REQ")
	if path == "" {
		t.Skip("REPRO_REQ not set")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var req dto.GeneralOpenAIRequest
	if err := common.Unmarshal(data, &req); err != nil {
		t.Fatalf("decode GeneralOpenAIRequest: %v", err)
	}
	t.Logf("decoded: model=%q messages=%d tools=%d reasoning_effort=%q", req.Model, len(req.Messages), len(req.Tools), req.ReasoningEffort)

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)

	chatReq, convErr := openAIChatToOllamaChat(c, &req)
	if convErr != nil {
		t.Fatalf("openAIChatToOllamaChat: %v", convErr)
	}
	t.Logf("converted: messages=%d tools=%v options=%v think=%s keep_alive=%v format=%v",
		len(chatReq.Messages), toolCount(chatReq.Tools), chatReq.Options, string(chatReq.Think), chatReq.KeepAlive, chatReq.Format)

	wire, err := common.Marshal(chatReq)
	if err != nil {
		t.Fatal(err)
	}
	var final OllamaChatRequest
	if err := common.Unmarshal(wire, &final); err != nil {
		t.Fatalf("re-decode final body: %v", err)
	}
	t.Logf("final body: messages=%d bytes=%d", len(final.Messages), len(wire))
	imgCount := 0
	for i, m := range final.Messages {
		if len(m.Images) > 0 {
			imgCount++
			if imgCount <= 3 {
				t.Logf("message %d role=%q has %d images", i, m.Role, len(m.Images))
			}
		}
	}
	t.Logf("messages with images: %d", imgCount)

	identity := buildOllamaChatPromptCacheIdentity(&final)
	t.Logf("identity: family=%q uncacheable=%v clear=%v hashes=%d rootHash=%q",
		identity.Family, identity.Uncacheable, identity.Clear, len(identity.MessageHashes), identity.RootHash)
	t.Logf("rootHash full: %s", identity.RootHash)
	t.Logf("keyMaterial hex: %x", identity.KeyMaterial)

	// empty-messages variant: same key material, no hashes (what a final body
	// with zero messages would produce)
	emptyIdentity := promptCacheIdentity{Family: "chat", KeyMaterial: identity.KeyMaterial}
	_ = emptyIdentity

	// dump first/last message shapes for inspection
	if len(final.Messages) > 0 {
		first := final.Messages[0]
		last := final.Messages[len(final.Messages)-1]
		fj, _ := json.Marshal(first)
		lj, _ := json.Marshal(last)
		if len(fj) > 300 {
			fj = fj[:300]
		}
		if len(lj) > 300 {
			lj = lj[:300]
		}
		t.Logf("first msg: %s", fj)
		t.Logf("last msg: %s", lj)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func toolCount(v any) int {
	switch v := v.(type) {
	case []OllamaTool:
		return len(v)
	case []any:
		return len(v)
	default:
		return -1
	}
}

var _ = fmt.Sprintf
