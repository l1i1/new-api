package helper

import (
	"strings"

	"github.com/QuantumNous/new-api/relaykit/dto"
)

// normalizeAllowedToolsToolChoice rewrites OpenAI's `allowed_tools` tool_choice
// form into the equivalent classic shape: the declared tools list filtered down
// to the allowed names plus the tool_choice string "auto" / "required" / "none".
//
// Why the rewrite exists (2026-10-25): the form
//
//	{"type":"allowed_tools",
//	 "allowed_tools":{"mode":"auto|required",
//	                  "tools":[{"type":"function","function":{"name":"..."}}]}}
//
// is a bounded tool choice, not a forced one: mode=auto lets the model decide
// whether to call among the allowed tools, mode=required demands at least one of
// them. Neither upstream in the CN mix implements the field. Live probes through
// channel 8 (official Moonshot) and channel 91 (NeurVibe aggregator) with
// thinking disabled — the only state that currently gets past the local K3
// contract — returned 200 but ignored the constraint: an allowed set containing
// only get_time still called get_weather, in both modes. Forwarding the object
// therefore answers 200 with the constraint silently dropped.
//
// Filtering the declarations and sending the classic string is the same contract
// on the wire both upstreams already honour, so the translation is lossless for
// function tools and needs no per-channel support flag. mode=auto with an empty
// intersection becomes "none" (no tool may be called), mode=required keeps
// "required" so the endpoint's own no-tools rejection stays visible.
//
// Only the shape this function can translate exactly is rewritten. A list with a
// non-function entry, a malformed body, an unknown mode or an absent tool name
// leaves the request untouched, and the existing per-family validation or the
// upstream answers it. Message-level dynamic tools are not filtered: they are a
// K3 extension, and allowed_tools does not define how it references them.
func normalizeAllowedToolsToolChoice(request *dto.GeneralOpenAIRequest) {
	if request == nil {
		return
	}
	choice, ok := request.ToolChoice.(map[string]any)
	if !ok {
		return
	}
	if choiceType, _ := choice["type"].(string); choiceType != "allowed_tools" {
		return
	}
	allowed, ok := choice["allowed_tools"].(map[string]any)
	if !ok {
		return
	}
	mode, _ := allowed["mode"].(string)
	if mode != "auto" && mode != "required" {
		return
	}
	entries, ok := allowed["tools"].([]any)
	if !ok {
		return
	}
	names := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		reference, ok := entry.(map[string]any)
		if !ok {
			return
		}
		if entryType, _ := reference["type"].(string); entryType != "function" {
			return
		}
		function, ok := reference["function"].(map[string]any)
		if !ok {
			return
		}
		name, _ := function["name"].(string)
		if name = strings.TrimSpace(name); name == "" {
			return
		}
		names[name] = struct{}{}
	}

	filtered := make([]dto.ToolCallRequest, 0, len(request.Tools))
	for _, tool := range request.Tools {
		if tool.Type != "function" {
			continue
		}
		if _, allowed := names[tool.Function.Name]; allowed {
			filtered = append(filtered, tool)
		}
	}
	request.Tools = filtered

	if mode == "required" {
		request.ToolChoice = "required"
		return
	}
	if len(filtered) == 0 {
		request.ToolChoice = "none"
		return
	}
	request.ToolChoice = "auto"
}
