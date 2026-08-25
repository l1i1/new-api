package main

import (
	"testing"
	"time"
)

func TestSuccessfulRPSUsesActualElapsedWindow(t *testing.T) {
	if got := successfulRPS(100, 2*time.Second); got != 50 {
		t.Fatalf("successfulRPS() = %v, want 50", got)
	}
	if got := successfulRPS(100, 0); got != 0 {
		t.Fatalf("successfulRPS() with zero duration = %v, want 0", got)
	}
}

func TestHasEffectiveOutputRejectsEmptyHTTP200Body(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"content":""},"finish_reason":"length"}]}`)
	if hasEffectiveOutput(body, false) {
		t.Fatal("empty final content must not count as success")
	}
}

func TestHasEffectiveOutputAcceptsContentAndToolCalls(t *testing.T) {
	content := []byte(`{"choices":[{"message":{"content":"ok"}}]}`)
	if !hasEffectiveOutput(content, false) {
		t.Fatal("non-empty content must count as success")
	}
	toolCall := []byte(`{"choices":[{"message":{"content":null,"tool_calls":[{"type":"function","function":{"name":"lookup"}}]}}]}`)
	if !hasEffectiveOutput(toolCall, false) {
		t.Fatal("valid function tool call must count as success")
	}
}

func TestHasEffectiveStreamOutputRequiresOutputAndDone(t *testing.T) {
	complete := []byte("data:{\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
	if !hasEffectiveOutput(complete, true) {
		t.Fatal("complete content stream must count as success")
	}
	partial := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
	if hasEffectiveOutput(partial, true) {
		t.Fatal("stream without [DONE] must not count as success")
	}
	reasoningOnly := []byte("data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"thinking\"}}]}\n\ndata: [DONE]\n\n")
	if hasEffectiveOutput(reasoningOnly, true) {
		t.Fatal("reasoning-only stream must not count as success")
	}
}

func TestHasModelListRequiresNonEmptyModelID(t *testing.T) {
	if !hasModelList([]byte(`{"data":[{"id":"gpt-4o-mini"}]}`)) {
		t.Fatal("non-empty model id must count as a valid model list")
	}
	if hasModelList([]byte(`{"data":[{"id":""}]}`)) || hasModelList([]byte(`{"data":[]}`)) {
		t.Fatal("empty model lists must not count as success")
	}
}
