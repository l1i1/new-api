package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"
)

type result struct {
	CaseID    string         `json:"case_id"`
	Tier      string         `json:"tier"`
	Surface   string         `json:"surface"`
	Route     string         `json:"route"`
	Status    string         `json:"status"`
	HTTP      int            `json:"http_status,omitempty"`
	ErrorCode string         `json:"error_code,omitempty"`
	Evidence  map[string]any `json:"evidence,omitempty"`
}

type probe struct {
	client *http.Client
	model  string
}

var allCases = []string{
	"DS-A01", "DS-A02", "DS-A03", "DS-A04", "DS-A05",
	"DS-B01", "DS-B02", "DS-B03", "DS-B04", "DS-B05", "DS-B06", "DS-B07", "DS-B08",
	"DS-C01", "DS-C02", "DS-C03", "DS-C04", "DS-C05", "DS-C06", "DS-C07", "DS-C08", "DS-C09", "DS-C10", "DS-C11", "DS-C12",
	"DS-D01", "DS-D02", "DS-D03", "DS-D04", "DS-D05", "DS-D06", "DS-D07", "DS-D08", "DS-D09", "DS-D10", "DS-D11", "DS-D12",
	"DS-E01", "DS-E02", "DS-E03", "DS-E04", "DS-E05", "DS-E06", "DS-E07",
	"DS-F01", "DS-F02", "DS-F03", "DS-F04", "DS-F05", "DS-F06", "DS-F07", "DS-F08", "DS-F09", "DS-F10",
	"DS-G01", "DS-G02", "DS-G03", "DS-G04", "DS-G05", "DS-G06", "DS-G07",
	"DS-H01", "DS-H02", "DS-H03", "DS-H04", "DS-H05", "DS-H06", "DS-H07",
	"DS-I01", "DS-I02", "DS-I03", "DS-I04", "DS-I05", "DS-I06", "DS-I07", "DS-I08", "DS-I09", "DS-I10", "DS-I11",
	"DS-J01", "DS-J02", "DS-J03", "DS-J04", "DS-J05", "DS-J06",
}

var implementedLiveCases = map[string]struct{}{
	"DS-A01": {}, "DS-A02": {}, "DS-A03": {}, "DS-A04": {}, "DS-A05": {},
	"DS-B01": {}, "DS-B02": {}, "DS-B03": {}, "DS-B04": {}, "DS-B05": {},
	"DS-C01": {}, "DS-C02": {}, "DS-C03": {}, "DS-C04": {}, "DS-C05": {}, "DS-C06": {}, "DS-C07": {}, "DS-C08": {}, "DS-C09": {}, "DS-C10": {}, "DS-C11": {}, "DS-C12": {},
	"DS-D01": {}, "DS-D02": {}, "DS-D03": {}, "DS-D04": {}, "DS-D05": {}, "DS-D06": {}, "DS-D07": {}, "DS-D08": {}, "DS-D09": {}, "DS-D10": {},
	"DS-E01": {}, "DS-E02": {}, "DS-E03": {}, "DS-E04": {}, "DS-E05": {}, "DS-E06": {}, "DS-E07": {},
	"DS-F01": {}, "DS-F02": {}, "DS-F06": {}, "DS-F07": {}, "DS-F08": {}, "DS-F09": {},
	"DS-G01": {}, "DS-G02": {}, "DS-G03": {}, "DS-G04": {},
}

var fingerprintSecretPattern = regexp.MustCompile(`(?i)(sk-[a-z0-9_-]+|bearer\s+[a-z0-9._-]+)`)

func main() {
	client := &http.Client{Timeout: 45 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("FEATURE_PROBE_PROFILE")), "fit") {
		if err := runFitProfile(client); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		return
	}
	results := make([]result, 0, len(allCases))
	executed := make(map[string]bool)
	if key := strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY")); key != "" {
		base := envOr("DEEPSEEK_BASE_URL", "https://api.deepseek.com")
		live := runBasic(client, "official", base, key, "official-live")
		results = append(results, live...)
		for _, item := range live {
			executed[item.CaseID] = true
		}
	}
	for _, route := range []struct{ name, base string }{
		{"main", os.Getenv("NEW_API_BASE_URL")},
		{"backup", os.Getenv("NEW_API_BACKUP_URL")},
	} {
		if strings.TrimSpace(route.base) != "" && strings.TrimSpace(os.Getenv("NEW_API_KEY")) != "" {
			live := runBasic(client, route.name, route.base, os.Getenv("NEW_API_KEY"), "gateway-live")
			results = append(results, live...)
			for _, item := range live {
				executed[item.CaseID] = true
			}
		}
	}
	selected := basicCaseSelection()
	for _, id := range allCases {
		if !executed[id] {
			reason := "runner_not_implemented"
			if len(selected) > 0 {
				if _, selectedCase := selected[id]; !selectedCase {
					reason = "case_not_selected"
				} else if _, implemented := implementedLiveCases[id]; implemented {
					reason = "required_live_route_not_injected"
					if !liveProbeConfigured() {
						reason = "live_credentials_or_route_not_injected"
					}
				}
			} else if _, implemented := implementedLiveCases[id]; implemented {
				reason = "required_live_route_not_injected"
				if !liveProbeConfigured() {
					reason = "live_credentials_or_route_not_injected"
				}
			}
			results = append(results, result{CaseID: id, Tier: tierFor(id), Surface: surfaceFor(id), Route: "offline", Status: "inconclusive", Evidence: map[string]any{"reason": reason}})
		}
	}

	sort.Slice(results, func(i, j int) bool { return results[i].CaseID+results[i].Route < results[j].CaseID+results[j].Route })
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(true)
	for _, item := range results {
		if err := enc.Encode(item); err != nil {
			fmt.Fprintln(os.Stderr, "encode result:", err)
			os.Exit(1)
		}
	}
}

func runFitProfile(client *http.Client) error {
	officialRequested := strings.EqualFold(strings.TrimSpace(os.Getenv("FEATURE_PROBE_INCLUDE_OFFICIAL")), "true")
	routes := []struct{ name, base, key, tier string }{
		{"main", envOr("NEW_API_BASE_URL", "https://n.tokeness.dev/v1"), os.Getenv("NEW_API_KEY"), "gateway-live"},
		{"backup", envOr("NEW_API_BACKUP_URL", "https://n-cf.tokeness.dev/v1"), os.Getenv("NEW_API_KEY"), "gateway-live"},
	}
	if officialRequested {
		routes = append([]struct{ name, base, key, tier string }{
			{"official", envOr("DEEPSEEK_BASE_URL", "https://api.deepseek.com"), os.Getenv("DEEPSEEK_API_KEY"), "official-live"},
		}, routes...)
	}
	checks := fitChecks("deepseek-v4-flash")
	selected, err := selectFitChecks(checks, fitCaseSelection())
	if err != nil {
		return err
	}
	results := make([]result, 0, len(routes)*len(selected))
	for _, route := range routes {
		if strings.TrimSpace(route.base) == "" || strings.TrimSpace(route.key) == "" {
			reason := "live_api_key_not_injected"
			if strings.TrimSpace(route.base) == "" {
				reason = "live_route_not_injected"
			}
			for _, check := range selected {
				results = append(results, result{
					CaseID:  check.id,
					Tier:    route.tier,
					Surface: "chat-completions",
					Route:   route.name,
					Status:  "inconclusive",
					Evidence: map[string]any{
						"reason": reason,
					},
				})
			}
			continue
		}
		for _, check := range selected {
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			body, _ := json.Marshal(check.body)
			status, evidence, err := request(ctx, client, route.base, route.key, http.MethodPost, "/chat/completions", body)
			cancel()
			annotateFitEvidence(status, evidence, check.body)
			item := result{CaseID: check.id, Tier: route.tier, Surface: "chat-completions", Route: route.name, HTTP: status, Evidence: evidence, Status: "fail"}
			if code, ok := evidence["error_code"].(string); ok {
				item.ErrorCode = code
			}
			if err != nil {
				item.Status = "inconclusive"
			} else if fitExpected(check.id, status, evidence) {
				item.Status = "pass"
			} else if route.tier == "official-live" && isInsufficientBalance(evidence) {
				item.Status = "inconclusive"
				item.Evidence["reason"] = "provider_balance_or_quota"
			}
			if check.id == "K02" || check.id == "K03" {
				if route.tier == "official-live" && item.Status == "fail" && isValidationStatus(status) {
					item.Status = "doc_drift"
				}
			}
			results = append(results, item)
		}
	}
	applyFitOfficialBaseline(results, officialRequested)
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(true)
	for _, item := range results {
		if err := enc.Encode(item); err != nil {
			return err
		}
	}
	return nil
}

func applyFitOfficialBaseline(results []result, officialRequested bool) {
	official := make(map[string]result)
	if officialRequested {
		for _, item := range results {
			if item.Tier == "official-live" {
				official[item.CaseID] = item
			}
		}
	}
	for i := range results {
		item := &results[i]
		if item.Tier != "gateway-live" || item.Status != "pass" {
			continue
		}
		if item.Evidence == nil {
			item.Evidence = make(map[string]any)
		}
		item.Evidence["contract_status"] = "pass"
		if !officialRequested {
			item.Status = "inconclusive"
			item.Evidence["reason"] = "official_baseline_not_requested"
			continue
		}
		baseline, ok := official[item.CaseID]
		if !ok || baseline.Status != "pass" {
			item.Status = "inconclusive"
			item.Evidence["reason"] = "official_baseline_unavailable"
			continue
		}
		if item.HTTP != baseline.HTTP {
			item.Status = "fail"
			item.Evidence["reason"] = "official_http_status_mismatch"
			continue
		}
		if field := fitEvidenceMismatch(item.CaseID, baseline.Evidence, item.Evidence); field != "" {
			item.Status = "fail"
			item.Evidence["reason"] = "official_contract_mismatch"
			item.Evidence["mismatch_field"] = field
			continue
		}
		item.Evidence["official_baseline_matched"] = true
	}
}

func fitEvidenceMismatch(caseID string, official, gateway map[string]any) string {
	fields := []string{"json", "has_error", "error_type", "error_code", "error_param_null", "error_message_fingerprint", "stream", "has_response_id", "has_created", "object", "response_model", "choices", "choice_index_zero", "has_message", "message_role_assistant", "finish_reason_valid", "unexpected_top_level_fields", "unexpected_choice_fields", "unexpected_message_fields", "message_tool_calls_null", "done", "done_events", "done_last", "sse_json_errors", "stream_shape_valid", "official_usage_shape", "usage_shape", "usage_consistent", "cache_tokens_consistent", "completion_within_requested_max", "usage_events", "usage_only_events", "usage_on_finish_event", "usage_on_final_event"}
	caseID = normalizeFitCaseID(caseID)
	switch caseID {
	case "K01", "K11":
		fields = append(fields, "finish_reason", "contains_stop_sequence", "contains_pre_stop_content")
	case "K05":
		fields = append(fields, "has_content", "has_reasoning_content")
	case "K08", "K09", "K13":
		fields = append(fields, "has_content")
	case "K10":
		fields = append(fields, "has_reasoning_content")
	case "K12":
		fields = append(fields, "has_logprobs", "logprobs_content_valid", "logprobs_reasoning_content_valid")
	}
	for _, field := range fields {
		officialValue, officialOK := official[field]
		gatewayValue, gatewayOK := gateway[field]
		if !officialOK && !gatewayOK {
			continue
		}
		if !officialOK || !gatewayOK || !reflect.DeepEqual(officialValue, gatewayValue) {
			return field
		}
	}
	return ""
}

type fitCheck struct {
	id   string
	body map[string]any
}

func fitChecks(model string) []fitCheck {
	return []fitCheck{
		{"K01", map[string]any{"stream": false, "messages": []any{map[string]any{"role": "user", "content": "依次说出：苹果、香蕉、橙子、西瓜。"}}, "stop": "香蕉", "max_tokens": 256, "reasoning_effort": "low", "model": model}},
		{"K02", map[string]any{"stream": false, "messages": []any{map[string]any{"role": "user", "content": "1+1=?"}}, "top_logprobs": 5, "max_tokens": 64, "reasoning_effort": "low", "model": model}},
		{"K03", map[string]any{"stream": false, "messages": []any{map[string]any{"role": "user", "content": "1+1=?"}}, "logprobs": true, "top_logprobs": 21, "max_tokens": 64, "reasoning_effort": "low", "model": model}},
		{"K04", map[string]any{"stream": false, "messages": []any{map[string]any{"role": "user", "content": "1+1=?"}}, "max_tokens": 393216, "reasoning_effort": "low", "model": model}},
		{"K05", map[string]any{"stream": false, "messages": []any{map[string]any{"role": "user", "content": "用一个词描述春天。"}}, "temperature": 0, "max_tokens": 256, "thinking": map[string]string{"type": "disabled"}, "model": model}},
		{"K06", map[string]any{"stream": false, "messages": []any{map[string]any{"role": "user", "content": "你好"}}, "frequency_penalty": 2, "presence_penalty": 2, "max_tokens": 64, "reasoning_effort": "low", "model": model}},
		{"K07", map[string]any{"stream": false, "messages": []any{map[string]any{"role": "system", "name": "teacher", "content": "你是一位数学老师。"}, map[string]any{"role": "user", "name": "student_a", "content": "1+1=?"}}, "max_tokens": 256, "reasoning_effort": "low", "model": model}},
		{"K08", map[string]any{"messages": []any{map[string]any{"role": "user", "content": "用一个词描述秋天。"}}, "temperature": 2, "top_p": 0.1, "presence_penalty": 1.5, "frequency_penalty": 1.5, "max_tokens": 1024, "reasoning_effort": "low", "model": model, "stream": true, "stream_options": map[string]any{"include_usage": true}}},
		{"K09", map[string]any{"messages": []any{map[string]any{"role": "user", "content": "1+1=?"}}, "max_tokens": 1024, "reasoning_effort": "low", "model": model, "stream": true, "stream_options": map[string]any{"include_usage": true}}},
		{"K10", map[string]any{"stream": false, "messages": []any{map[string]any{"role": "user", "content": "北京今天天气怎么样？"}}, "tools": []any{map[string]any{"type": "function", "function": map[string]any{"name": "get_weather", "description": "查询指定城市的当前天气", "parameters": map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string", "description": "城市名，例如 北京"}}, "required": []string{"city"}}}}}, "tool_choice": "auto", "max_tokens": 1024, "thinking": map[string]string{"type": "disabled"}, "model": model}},
		{"K11", map[string]any{"stream": false, "messages": []any{map[string]any{"role": "user", "content": "依次说出：苹果、香蕉、橙子、西瓜。"}}, "stop": "香蕉", "max_tokens": 256, "reasoning_effort": "low", "model": model}},
		{"K12", map[string]any{"stream": false, "messages": []any{map[string]any{"role": "user", "content": "1+1=?"}}, "logprobs": true, "top_logprobs": 5, "max_tokens": 1024, "reasoning_effort": "low", "model": model}},
		{"K13", map[string]any{"messages": []any{map[string]any{"role": "user", "content": "1+1=?"}}, "max_tokens": 1024, "reasoning_effort": "low", "model": model, "stream": true, "stream_options": map[string]any{"include_usage": true}}},
	}
}

func fitCaseSelection() []string {
	raw := os.Getenv("FEATURE_PROBE_CASES")
	if strings.TrimSpace(raw) == "" {
		raw = os.Getenv("FEATURE_PROBE_CASE_ID")
	}
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	return strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\r' || r == '\n'
	})
}

func normalizeFitCaseID(id string) string {
	key := strings.ToUpper(strings.TrimSpace(id))
	key = strings.TrimPrefix(key, "DS-")
	if len(key) == 3 && key[0] == 'K' && key[1] >= '0' && key[1] <= '1' && key[2] >= '0' && key[2] <= '9' {
		if key >= "K01" && key <= "K13" {
			return key
		}
	}
	return ""
}

func selectFitChecks(checks []fitCheck, requested []string) ([]fitCheck, error) {
	if len(requested) == 0 {
		return checks, nil
	}
	wanted := make(map[string]struct{}, len(requested))
	for _, id := range requested {
		canonical := normalizeFitCaseID(id)
		if canonical == "" {
			return nil, fmt.Errorf("unknown fit case id %q", strings.TrimSpace(id))
		}
		wanted[canonical] = struct{}{}
	}
	selected := make([]fitCheck, 0, len(wanted))
	for _, check := range checks {
		if _, ok := wanted[check.id]; ok {
			selected = append(selected, check)
		}
	}
	return selected, nil
}

func fitExpected(caseID string, status int, evidence map[string]any) bool {
	caseID = normalizeFitCaseID(caseID)
	switch caseID {
	case "K02":
		return expectedLogprobsValidation(status, evidence, "invalid top_logprobs and logprobs value, logprobs must be set to true if top_logprobs is used.")
	case "K03":
		return expectedLogprobsValidation(status, evidence, "invalid top_logprobs value, the valid range of top_logprobs is [0, 20].")
	case "K01", "K11":
		return status == http.StatusOK && hasOfficialNonStreamResponse(evidence) && evidence["has_content"] == true && evidence["contains_pre_stop_content"] == true && evidence["finish_reason"] == "stop" && evidence["contains_stop_sequence"] != true
	case "K08":
		return status == http.StatusOK && hasCompleteChatSSE(evidence) && evidence["has_error"] != true &&
			(evidence["has_content"] == true || (evidence["has_reasoning_content"] == true && evidence["finish_reason"] == "length")) &&
			hasOfficialFinalUsage(evidence)
	case "K09", "K13":
		return status == http.StatusOK && hasCompleteChatSSE(evidence) && evidence["has_error"] != true && evidence["has_content"] == true && hasOfficialFinalUsage(evidence)
	case "K12":
		contentCount, contentOK := evidence["logprobs_content"].(int)
		reasoningCount, reasoningOK := evidence["logprobs_reasoning_content"].(int)
		contentMax, contentMaxOK := evidence["max_top_logprobs"].(int)
		reasoningMax, reasoningMaxOK := evidence["max_reasoning_top_logprobs"].(int)
		return status == http.StatusOK && hasOfficialNonStreamResponse(evidence) && evidence["has_logprobs"] == true && evidence["logprobs_content_valid"] == true && evidence["logprobs_reasoning_content_valid"] == true && contentOK && contentCount > 0 && reasoningOK && reasoningCount > 0 && contentMaxOK && contentMax <= 5 && reasoningMaxOK && reasoningMax <= 5
	case "K05":
		return status == http.StatusOK && hasOfficialNonStreamResponse(evidence) && evidence["has_content"] == true && evidence["has_reasoning_content"] != true
	case "K04", "K06", "K07":
		return status == http.StatusOK && hasOfficialNonStreamResponse(evidence) &&
			(evidence["has_content"] == true ||
				(evidence["has_reasoning_content"] == true && evidence["finish_reason"] == "length"))
	case "K10":
		return status == http.StatusOK && hasOfficialNonStreamResponse(evidence) && evidence["has_reasoning_content"] != true &&
			(evidence["has_content"] == true || (evidence["has_tool_calls"] == true && evidence["tool_arguments_json"] == true && evidence["fit_tool_calls_valid"] == true && evidence["finish_reason"] == "tool_calls"))
	default:
		return status == http.StatusOK && hasOfficialNonStreamResponse(evidence) && evidence["has_content"] == true
	}
}

func isInsufficientBalance(evidence map[string]any) bool {
	return strings.Contains(evidenceString(evidence, "error_message_fingerprint"), "insufficient balance") || strings.Contains(evidenceString(evidence, "error_message_fingerprint"), "quota")
}

func annotateFitEvidence(status int, evidence map[string]any, requestBody ...map[string]any) {
	if status != http.StatusOK || evidence == nil || evidence["has_error"] == true {
		return
	}
	if len(requestBody) > 0 {
		if maxTokens, ok := requestBody[0]["max_tokens"].(int); ok && maxTokens > 0 {
			evidence["requested_max_tokens"] = maxTokens
			if completionTokens, ok := evidence["completion_tokens"].(float64); ok {
				evidence["completion_within_requested_max"] = completionTokens <= float64(maxTokens)
			}
		}
	}
	evidence["protocol_accepted"] = true
	effectiveSuccess := evidence["has_content"] == true || (evidence["has_tool_calls"] == true && evidence["tool_arguments_json"] == true)
	evidence["effective_success"] = effectiveSuccess
	if !effectiveSuccess {
		evidence["failure_reason"] = "empty_final_content"
	}
}

type basicCheck struct {
	id   string
	body map[string]any
}

func runBasic(client *http.Client, route, base, key, tier string) []result {
	p := &probe{client: client, model: "deepseek-v4-flash"}
	checks := []basicCheck{
		{"DS-A01", nil},
		{"DS-A02", nil},
		{"DS-A03", basicRequest(p.model)},
		{"DS-A04", withFields(basicRequest(p.model), "model", "deepseek-v4-unknown")},
		{"DS-A05", nil},
		{"DS-B01", basicRequest(p.model)},
		{"DS-B02", streamRequest(p.model, true)},
		{"DS-B03", streamRequest(p.model, false)},
		{"DS-B04", basicRequest(p.model)},
		{"DS-B05", roleRoundTripRequest(p.model)},
		{"DS-C01", basicRequest(p.model)},
		{"DS-C02", withFields(basicRequest(p.model), "thinking", map[string]string{"type": "enabled"})},
		{"DS-C03", withFields(basicRequest(p.model), "thinking", map[string]string{"type": "disabled"})},
		{"DS-C04", withFields(basicRequest(p.model), "reasoning_effort", "xhigh")},
		{"DS-C05", withFields(basicRequest(p.model), "reasoning_effort", "none")},
		{"DS-C06", withFields(basicRequest(p.model), "reasoning_effort", "extreme")},
		{"DS-C07", withFields(withFields(withFields(withFields(withFields(basicRequest(p.model), "thinking", map[string]string{"type": "enabled"}), "temperature", 1.2), "top_p", 0.8), "presence_penalty", 0.2), "frequency_penalty", 0.2)},
		{"DS-C12", withFields(basicRequest(p.model), "model", "deepseek-v4-flash-max")},
		{"DS-D03", withFields(basicRequest(p.model), "top_p", 1.5)},
		{"DS-D04", withFields(basicRequest(p.model), "top_p", 1.5)},
		{"DS-D05", withFields(basicRequest(p.model), "top_p", -0.1)},
		{"DS-D06", withFields(withFields(basicRequest(p.model), "max_tokens", 1), "thinking", map[string]string{"type": "disabled"})},
		{"DS-D08", stopRequest(p.model)},
		{"DS-D09", stopArrayRequest(p.model)},
		{"DS-D10", jsonRequest(p.model)},
		{"DS-E01", withFields(withFields(basicRequest(p.model), "logprobs", true), "top_logprobs", 5)},
		{"DS-E02", withFields(withFields(withFields(basicRequest(p.model), "thinking", map[string]string{"type": "enabled"}), "logprobs", true), "top_logprobs", 5)},
		{"DS-E03", withFields(withFields(basicRequest(p.model), "logprobs", true), "top_logprobs", 20)},
		{"DS-E04", withFields(basicRequest(p.model), "top_logprobs", 5)},
		{"DS-E05", withFields(withFields(withFields(streamRequest(p.model, true), "thinking", map[string]string{"type": "disabled"}), "logprobs", true), "top_logprobs", 5)},
		{"DS-E06", withFields(withFields(withFields(basicRequest(p.model), "thinking", map[string]string{"type": "enabled"}), "logprobs", true), "top_logprobs", 5)},
		{"DS-E07", withFields(withFields(basicRequest(p.model), "logprobs", true), "top_logprobs", 21)},
		{"DS-F01", toolRequest(p.model, "auto")},
		{"DS-F02", toolRequest(p.model, "required")},
	}
	selected := basicCaseSelection()
	results := make([]result, 0, len(checks))
	for _, check := range checks {
		if !caseSelected(check.id, selected) || (check.id == "DS-C12" && route == "official") {
			continue
		}
		results = append(results, runSingleBasicCase(client, route, base, key, tier, p.model, check))
	}
	// Variant and advanced checks are constructed at execution time so their
	// second request can depend on the first response shape.
	for _, id := range []string{"DS-D01", "DS-D02", "DS-D07", "DS-C08", "DS-C09", "DS-C10", "DS-C11", "DS-F06", "DS-F07", "DS-F08", "DS-F09"} {
		if !caseSelected(id, selected) || containsResult(results, id) {
			continue
		}
		if id == "DS-D01" || id == "DS-D02" || id == "DS-D07" {
			results = append(results, runVariantBasicCase(client, route, base, key, tier, p.model, id))
		} else {
			results = append(results, runAdvancedBasicCase(client, route, base, key, tier, p.model, id))
		}
	}
	results = append(results, runResponses(client, route, base, key, tier, p.model, selected)...)
	return results
}

type responsesCheck struct {
	id   string
	body map[string]any
}

func runResponses(client *http.Client, route, base, key, tier, model string, selected map[string]struct{}) []result {
	checks := []responsesCheck{
		{"DS-G01", responsesRequest(model)},
		{"DS-G02", responsesStreamRequest(model)},
		{"DS-G03", responsesReasoningRequest(model)},
		{"DS-G04", responsesToolRequest(model)},
	}
	results := make([]result, 0, len(checks))
	for _, check := range checks {
		if !caseSelected(check.id, selected) {
			continue
		}
		if check.id == "DS-G04" {
			results = append(results, runResponsesToolCase(client, route, base, key, tier, model))
			continue
		}
		results = append(results, runSingleResponsesCase(client, route, base, key, tier, check))
	}
	return results
}

func runSingleResponsesCase(client *http.Client, route, base, key, tier string, check responsesCheck) result {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	body, _ := json.Marshal(check.body)
	status, evidence, _, err := requestPayload(ctx, client, base, key, http.MethodPost, "/responses", body)
	item := result{CaseID: check.id, Tier: tier, Surface: "responses", Route: route, HTTP: status, Evidence: evidence, Status: "fail"}
	if code, ok := evidence["error_code"].(string); ok {
		item.ErrorCode = code
	}
	if err != nil {
		item.Status = "inconclusive"
		item.Evidence["reason"] = "responses_transport_error"
		return item
	}
	if responsesUnsupported(status, evidence) {
		item.Status = "expected_unsupported"
		return item
	}
	if check.id == "DS-G03" && status == http.StatusOK && evidence["response_object"] == true && evidence["has_reasoning_output"] != true {
		item.Status = "expected_unsupported"
		item.Evidence["reason"] = "responses_reasoning_not_advertised"
		return item
	}
	if responsesExpected(check.id, status, evidence) {
		item.Status = "pass"
	}
	return item
}

func runResponsesToolCase(client *http.Client, route, base, key, tier, model string) result {
	item := result{CaseID: "DS-G04", Tier: tier, Surface: "responses", Route: route, Status: "fail"}
	firstBody := responsesToolRequest(model)
	firstBytes, _ := json.Marshal(firstBody)
	firstCtx, firstCancel := context.WithTimeout(context.Background(), 45*time.Second)
	firstStatus, firstEvidence, firstPayload, firstErr := requestPayload(firstCtx, client, base, key, http.MethodPost, "/responses", firstBytes)
	firstCancel()
	evidence := map[string]any{
		"first_http_status":       firstStatus,
		"first_response_object":   firstEvidence["response_object"] == true,
		"first_status":            firstEvidence["response_status"],
		"first_has_output_text":   firstEvidence["has_output_text"] == true,
		"first_has_function_call": firstEvidence["has_function_call"] == true,
	}
	item.HTTP, item.Evidence = firstStatus, evidence
	if code, ok := firstEvidence["error_code"].(string); ok {
		item.ErrorCode = code
	}
	if firstErr != nil {
		item.Status = "inconclusive"
		evidence["reason"] = "responses_first_turn_transport_error"
		return item
	}
	if responsesUnsupported(firstStatus, firstEvidence) {
		item.Status = "expected_unsupported"
		return item
	}
	call, ok := firstResponsesFunctionCall(firstPayload)
	if !ok {
		if firstStatus == http.StatusOK && firstEvidence["response_object"] == true && firstEvidence["has_error"] != true {
			item.Status = "expected_unsupported"
			evidence["reason"] = "responses_function_call_not_advertised"
		} else {
			item.Status = "fail"
			evidence["reason"] = "responses_first_turn_invalid_or_missing_function_call"
		}
		return item
	}
	evidence["first_function_arguments_json"] = json.Valid([]byte(call.arguments))
	if evidence["first_function_arguments_json"] != true {
		item.Status = "fail"
		evidence["reason"] = "responses_function_call_arguments_invalid"
		return item
	}
	secondBody := map[string]any{
		"model": model,
		"input": []any{
			map[string]any{"role": "user", "content": "Beijing weather?"},
			map[string]any{
				"type":      "function_call",
				"call_id":   call.callID,
				"name":      call.name,
				"arguments": call.arguments,
			},
			map[string]any{
				"type":    "function_call_output",
				"call_id": call.callID,
				"output":  "Beijing: sunny, 25C.",
			},
		},
		"max_output_tokens": 128,
	}
	secondBytes, _ := json.Marshal(secondBody)
	secondCtx, secondCancel := context.WithTimeout(context.Background(), 45*time.Second)
	secondStatus, secondEvidence, _, secondErr := requestPayload(secondCtx, client, base, key, http.MethodPost, "/responses", secondBytes)
	secondCancel()
	evidence["second_http_status"] = secondStatus
	evidence["second_response_object"] = secondEvidence["response_object"] == true
	evidence["second_status"] = secondEvidence["response_status"]
	evidence["second_has_output_text"] = secondEvidence["has_output_text"] == true
	if secondErr != nil {
		item.Status = "inconclusive"
		evidence["reason"] = "responses_second_turn_transport_error"
		item.HTTP = secondStatus
		return item
	}
	if responsesUnsupported(secondStatus, secondEvidence) {
		item.Status = "expected_unsupported"
		item.HTTP = secondStatus
		return item
	}
	item.HTTP = secondStatus
	secondResponseStatus := secondEvidence["response_status"]
	validSecondStatus := secondResponseStatus == "completed" || secondResponseStatus == "incomplete"
	if secondStatus == http.StatusOK && secondEvidence["response_object"] == true && validSecondStatus && secondEvidence["has_output_text"] == true && secondEvidence["has_error"] != true {
		item.Status = "pass"
	}
	return item
}

type responsesFunctionCall struct {
	callID    string
	name      string
	arguments string
}

func firstResponsesFunctionCall(payload map[string]any) (responsesFunctionCall, bool) {
	response, ok := payload["response"].(map[string]any)
	if !ok {
		response = payload
	}
	output, ok := response["output"].([]any)
	if !ok {
		return responsesFunctionCall{}, false
	}
	for _, raw := range output {
		item, ok := raw.(map[string]any)
		if !ok || item["type"] != "function_call" {
			continue
		}
		callID, _ := item["call_id"].(string)
		name, _ := item["name"].(string)
		arguments, _ := item["arguments"].(string)
		if callID == "" || name == "" || arguments == "" {
			continue
		}
		return responsesFunctionCall{callID: callID, name: name, arguments: arguments}, true
	}
	return responsesFunctionCall{}, false
}

func responsesRequest(model string) map[string]any {
	return map[string]any{
		"model":             model,
		"input":             "1+1=?",
		"max_output_tokens": 64,
	}
}

func responsesStreamRequest(model string) map[string]any {
	request := responsesRequest(model)
	request["stream"] = true
	return request
}

func responsesReasoningRequest(model string) map[string]any {
	request := responsesRequest(model)
	request["input"] = "Compare 17 and 19, then answer with the larger number."
	request["reasoning"] = map[string]string{"effort": "low"}
	request["max_output_tokens"] = 128
	return request
}

func responsesToolRequest(model string) map[string]any {
	request := responsesRequest(model)
	request["input"] = "Beijing weather?"
	request["tools"] = []any{map[string]any{
		"type":        "function",
		"name":        "get_weather",
		"description": "Get current weather",
		"parameters": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"city": map[string]any{"type": "string"},
			},
			"required": []string{"city"},
		},
	}}
	request["tool_choice"] = map[string]any{"type": "function", "name": "get_weather"}
	request["max_output_tokens"] = 128
	return request
}

func responsesExpected(caseID string, status int, evidence map[string]any) bool {
	if status != http.StatusOK || evidence["response_object"] != true || evidence["has_error"] == true {
		return false
	}
	validStatus := evidence["response_status"] == "completed" || evidence["response_status"] == "incomplete"
	switch caseID {
	case "DS-G01":
		return validStatus && evidence["has_output_text"] == true && evidence["usage_valid"] == true
	case "DS-G02":
		terminalEvent, _ := evidence["response_terminal_event"].(string)
		return evidence["stream"] == true && evidence["has_output_text"] == true && evidence["response_terminal"] == true && evidence["response_terminal_last"] == true && (terminalEvent == "response.completed" || terminalEvent == "response.incomplete")
	case "DS-G03":
		return validStatus && evidence["has_output_text"] == true && evidence["has_reasoning_output"] == true
	default:
		return false
	}
}

func responsesUnsupported(status int, evidence map[string]any) bool {
	if status == http.StatusMethodNotAllowed || status == http.StatusNotImplemented {
		return true
	}
	fingerprint := strings.ToLower(evidenceString(evidence, "error_message_fingerprint"))
	if status == http.StatusNotFound && !strings.Contains(fingerprint, "responses") {
		return false
	}
	for _, marker := range []string{"responses api is not supported", "responses endpoint is not supported", "responses endpoint not found", "endpoint not supported", "unsupported endpoint", "not implemented"} {
		if strings.Contains(fingerprint, marker) {
			return true
		}
	}
	return false
}

func runSingleBasicCase(client *http.Client, route, base, key, tier, model string, check basicCheck) result {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	item := result{CaseID: check.id, Tier: tier, Surface: "chat-completions", Route: route, Status: "fail"}
	if check.id == "DS-A01" || check.id == "DS-A02" || check.id == "DS-A05" {
		item.Surface = "models"
	}
	requestKey := key
	method, path := http.MethodPost, "/chat/completions"
	var body []byte
	if check.body == nil {
		method, path = http.MethodGet, "/models"
		if check.id == "DS-A02" {
			requestKey = ""
		}
	} else {
		body, _ = json.Marshal(check.body)
		if check.id == "DS-A03" {
			requestKey = "sk-invalid-feature-probe"
		}
	}
	status, evidence, err := request(ctx, client, base, requestKey, method, path, body)
	item.HTTP, item.Evidence = status, evidence
	if code, ok := evidence["error_code"].(string); ok {
		item.ErrorCode = code
	}
	if err != nil {
		item.Status = "inconclusive"
		return item
	}
	if check.id == "DS-B05" {
		mergeRoleEvidence(evidence, check.body)
	}
	if check.id == "DS-B03" && evidence["usage"] == true {
		evidence["usage_without_include_usage"] = true
	}
	if expectedPass(check.id, route, status, evidence) {
		item.Status = "pass"
	}
	if check.id == "DS-E02" || check.id == "DS-E05" || check.id == "DS-E06" {
		if evidence["has_error"] == true && isValidationStatus(status) && isDFLASHCapabilityError(evidence) {
			item.Status = "expected_unsupported"
		} else if check.id == "DS-E06" && status == http.StatusOK && evidence["has_error"] != true {
			item.Status = "inconclusive"
			evidence["reason"] = "capability_error_not_observed"
		}
	}
	return item
}

func containsResult(results []result, id string) bool {
	for _, item := range results {
		if item.CaseID == id {
			return true
		}
	}
	return false
}

func basicCaseSelection() map[string]struct{} {
	raw := os.Getenv("FEATURE_PROBE_CASES")
	if strings.TrimSpace(raw) == "" {
		raw = os.Getenv("FEATURE_PROBE_CASE_ID")
	}
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	selected := make(map[string]struct{})
	for _, token := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\r' || r == '\n'
	}) {
		id := strings.ToUpper(strings.TrimSpace(token))
		if !strings.HasPrefix(id, "DS-") {
			continue
		}
		selected[id] = struct{}{}
	}
	return selected
}

func caseSelected(id string, selected map[string]struct{}) bool {
	if len(selected) == 0 {
		return true
	}
	_, ok := selected[id]
	return ok
}

func runVariantBasicCase(client *http.Client, route, base, key, tier, model, caseID string) result {
	variants := variantRequests(model, caseID)
	evidence := map[string]any{"variant_count": len(variants)}
	variantEvidence := make([]any, 0, len(variants))
	allExpected := true
	anyTransportError := false
	lastStatus := 0
	for _, variant := range variants {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		body, _ := json.Marshal(variant.body)
		status, itemEvidence, err := request(ctx, client, base, key, http.MethodPost, "/chat/completions", body)
		cancel()
		lastStatus = status
		entry := map[string]any{"label": variant.label, "http_status": status}
		for _, field := range []string{
			"json", "has_error", "has_content", "finish_reason", "error_type", "error_code", "error_param_null",
			"error_message_fingerprint", "usage_consistent", "cached_tokens_valid", "content_json",
		} {
			if value, ok := itemEvidence[field]; ok {
				entry[field] = value
			}
		}
		variantEvidence = append(variantEvidence, entry)
		if err != nil {
			anyTransportError = true
			allExpected = false
			continue
		}
		if !variantExpected(caseID, status, itemEvidence, variant.label) {
			allExpected = false
		}
	}
	evidence["variants"] = variantEvidence
	item := result{CaseID: caseID, Tier: tier, Surface: "chat-completions", Route: route, HTTP: lastStatus, Evidence: evidence, Status: "fail"}
	if anyTransportError {
		item.Status = "inconclusive"
		item.Evidence["reason"] = "variant_transport_error"
	} else if allExpected {
		item.Status = "pass"
	}
	return item
}

type variantRequest struct {
	label string
	body  map[string]any
}

func variantRequests(model, caseID string) []variantRequest {
	switch caseID {
	case "DS-D01":
		return []variantRequest{
			{"temperature_0", withFields(withFields(basicRequest(model), "temperature", 0), "thinking", map[string]string{"type": "disabled"})},
			{"temperature_1", withFields(withFields(basicRequest(model), "temperature", 1), "thinking", map[string]string{"type": "disabled"})},
			{"temperature_2", withFields(withFields(basicRequest(model), "temperature", 2), "thinking", map[string]string{"type": "disabled"})},
		}
	case "DS-D02":
		return []variantRequest{
			{"top_p_min_positive", withFields(withFields(basicRequest(model), "top_p", 0.000001), "thinking", map[string]string{"type": "disabled"})},
			{"top_p_half", withFields(withFields(basicRequest(model), "top_p", 0.5), "thinking", map[string]string{"type": "disabled"})},
			{"top_p_one", withFields(withFields(basicRequest(model), "top_p", 1), "thinking", map[string]string{"type": "disabled"})},
		}
	case "DS-D07":
		omitted := basicRequest(model)
		delete(omitted, "max_tokens")
		omitted["thinking"] = map[string]string{"type": "disabled"}
		return []variantRequest{
			{"omitted", omitted},
			{"zero", withFields(withFields(basicRequest(model), "thinking", map[string]string{"type": "disabled"}), "max_tokens", 0)},
			{"negative", withFields(withFields(basicRequest(model), "thinking", map[string]string{"type": "disabled"}), "max_tokens", -1)},
			{"excessive", withFields(withFields(basicRequest(model), "thinking", map[string]string{"type": "disabled"}), "max_tokens", 393217)},
		}
	default:
		return nil
	}
}

func variantExpected(caseID string, status int, evidence map[string]any, label string) bool {
	switch caseID {
	case "DS-D01", "DS-D02":
		return status == http.StatusOK && evidence["json"] == true && evidence["has_error"] != true && evidence["has_content"] == true
	case "DS-D07":
		if label == "omitted" {
			return status == http.StatusOK && evidence["json"] == true && evidence["has_error"] != true && evidence["has_content"] == true
		}
		return expectedOfficialValidation(status, evidence)
	default:
		return false
	}
}

func runAdvancedBasicCase(client *http.Client, route, base, key, tier, model, caseID string) result {
	switch caseID {
	case "DS-C08", "DS-C09":
		return runThinkingConversationCase(client, route, base, key, tier, model, caseID)
	case "DS-C10", "DS-C11":
		return runThinkingToolConversationCase(client, route, base, key, tier, model, caseID)
	case "DS-F06":
		return runToolConversationCase(client, route, base, key, tier, model, false)
	case "DS-F07", "DS-F08":
		return runToolConversationCase(client, route, base, key, tier, model, true, caseID == "DS-F08")
	case "DS-F09":
		return runSingleBasicCase(client, route, base, key, tier, model, basicCheck{caseID, streamingToolRequest(model)})
	default:
		return result{CaseID: caseID, Tier: tier, Surface: "chat-completions", Route: route, Status: "inconclusive", Evidence: map[string]any{"reason": "runner_not_implemented"}}
	}
}

func runThinkingConversationCase(client *http.Client, route, base, key, tier, model, caseID string) result {
	firstBody := withFields(basicRequest(model), "thinking", map[string]string{"type": "enabled"})
	firstBody["reasoning_effort"] = "low"
	firstBody["max_tokens"] = 128
	secondBodyBase := map[string]any{
		"model":            model,
		"thinking":         map[string]string{"type": "enabled"},
		"reasoning_effort": "low",
		"max_tokens":       128,
	}
	return runTwoTurnCase(client, route, base, key, tier, caseID, firstBody, func(payload map[string]any) (map[string]any, bool) {
		assistant, ok := replayAssistantMessage(payload, caseID == "DS-C08")
		if !ok {
			return nil, false
		}
		firstMessages, _ := firstBody["messages"].([]any)
		messages := append([]any{}, firstMessages...)
		messages = append(messages, assistant, map[string]any{"role": "user", "content": "第二个问题是 2+2 等于多少？请只给出答案。"})
		return withMessages(secondBodyBase, messages), true
	})
}

func runThinkingToolConversationCase(client *http.Client, route, base, key, tier, model, caseID string) result {
	firstBody := namedToolRequest(model)
	firstBody["thinking"] = map[string]string{"type": "enabled"}
	firstBody["reasoning_effort"] = "low"
	return runTwoTurnCase(client, route, base, key, tier, caseID, firstBody, func(payload map[string]any) (map[string]any, bool) {
		assistant, ok := replayAssistantMessage(payload, caseID == "DS-C10")
		if !ok || !hasToolCallsMessage(assistant) {
			return nil, false
		}
		messages := []any{
			map[string]any{"role": "user", "content": "请调用天气工具，然后给出最终答案。"},
			assistant,
			map[string]any{"role": "tool", "tool_call_id": firstToolCallID(assistant), "content": "北京：晴，25 摄氏度。"},
		}
		body := withMessages(map[string]any{
			"model":            model,
			"thinking":         map[string]string{"type": "enabled"},
			"reasoning_effort": "low",
			"max_tokens":       128,
			"tools":            namedToolRequest(model)["tools"],
		}, messages)
		if caseID == "DS-C11" {
			delete(body, "thinking")
			body["thinking"] = map[string]string{"type": "enabled"}
		}
		return body, true
	})
}

func runToolConversationCase(client *http.Client, route, base, key, tier, model string, thinking bool, expectValidation ...bool) result {
	caseID := "DS-F06"
	omitReasoning := false
	if thinking {
		caseID = "DS-F07"
		if len(expectValidation) > 0 && expectValidation[0] {
			caseID = "DS-F08"
			omitReasoning = true
		}
	}
	firstBody := namedToolRequest(model)
	if thinking {
		firstBody["thinking"] = map[string]string{"type": "enabled"}
		firstBody["reasoning_effort"] = "low"
	} else {
		firstBody["thinking"] = map[string]string{"type": "disabled"}
	}
	return runTwoTurnCase(client, route, base, key, tier, caseID, firstBody, func(payload map[string]any) (map[string]any, bool) {
		assistant, ok := replayAssistantMessage(payload, thinking && !omitReasoning)
		if !ok || !hasToolCallsMessage(assistant) {
			return nil, false
		}
		body := withMessages(map[string]any{
			"model":      model,
			"tools":      namedToolRequest(model)["tools"],
			"max_tokens": 128,
		}, []any{
			map[string]any{"role": "user", "content": "请调用天气工具，然后给出最终答案。"},
			assistant,
			map[string]any{"role": "tool", "tool_call_id": firstToolCallID(assistant), "content": "北京：晴，25 摄氏度。"},
		})
		if thinking {
			body["thinking"] = map[string]string{"type": "enabled"}
			body["reasoning_effort"] = "low"
		}
		return body, true
	})
}

func runTwoTurnCase(client *http.Client, route, base, key, tier, caseID string, firstBody map[string]any, buildSecond func(map[string]any) (map[string]any, bool)) result {
	item := result{CaseID: caseID, Tier: tier, Surface: "chat-completions", Route: route, Status: "fail"}
	firstCtx, firstCancel := context.WithTimeout(context.Background(), 45*time.Second)
	firstBytes, _ := json.Marshal(firstBody)
	firstStatus, firstEvidence, firstPayload, firstErr := requestPayload(firstCtx, client, base, key, http.MethodPost, "/chat/completions", firstBytes)
	firstCancel()
	evidence := map[string]any{
		"turns":                       2,
		"first_http_status":           firstStatus,
		"first_has_content":           firstEvidence["has_content"] == true,
		"first_has_reasoning_content": firstEvidence["has_reasoning_content"] == true,
		"first_has_tool_calls":        firstEvidence["has_tool_calls"] == true,
	}
	if firstErr != nil {
		item.Status = "inconclusive"
		evidence["reason"] = "first_turn_transport_error"
		item.HTTP, item.Evidence = firstStatus, evidence
		return item
	}
	secondBody, ok := buildSecond(firstPayload)
	if !ok {
		item.Status = "inconclusive"
		evidence["reason"] = "first_turn_missing_required_shape"
		item.HTTP, item.Evidence = firstStatus, evidence
		return item
	}
	secondCtx, secondCancel := context.WithTimeout(context.Background(), 45*time.Second)
	secondBytes, _ := json.Marshal(secondBody)
	secondStatus, secondEvidence, _, secondErr := requestPayload(secondCtx, client, base, key, http.MethodPost, "/chat/completions", secondBytes)
	secondCancel()
	for key, value := range secondEvidence {
		evidence[key] = value
	}
	evidence["second_http_status"] = secondStatus
	item.HTTP, item.Evidence = secondStatus, evidence
	if code, ok := secondEvidence["error_code"].(string); ok {
		item.ErrorCode = code
	}
	if secondErr != nil {
		item.Status = "inconclusive"
		return item
	}
	if expectedPass(caseID, route, secondStatus, evidence) {
		item.Status = "pass"
	}
	return item
}

func withMessages(body map[string]any, messages []any) map[string]any {
	return withFields(body, "messages", messages)
}

func replayAssistantMessage(payload map[string]any, includeReasoning bool) (map[string]any, bool) {
	assistant := firstAssistantMessage(payload)
	if assistant == nil {
		return nil, false
	}
	message := map[string]any{"role": "assistant"}
	if content, ok := assistant["content"].(string); ok {
		message["content"] = content
	} else {
		message["content"] = nil
	}
	if includeReasoning {
		reasoning, ok := assistant["reasoning_content"].(string)
		if !ok || strings.TrimSpace(reasoning) == "" {
			return nil, false
		}
		message["reasoning_content"] = reasoning
	}
	if calls, ok := assistant["tool_calls"].([]any); ok {
		message["tool_calls"] = calls
	}
	return message, true
}

func firstAssistantMessage(payload map[string]any) map[string]any {
	choices, ok := payload["choices"].([]any)
	if !ok || len(choices) == 0 {
		return nil
	}
	choice, ok := choices[0].(map[string]any)
	if !ok {
		return nil
	}
	message, _ := choice["message"].(map[string]any)
	return message
}

func hasToolCallsMessage(message map[string]any) bool {
	calls, ok := message["tool_calls"].([]any)
	return ok && len(calls) > 0
}

func firstToolCallID(message map[string]any) string {
	calls, ok := message["tool_calls"].([]any)
	if !ok || len(calls) == 0 {
		return "call_feature_probe"
	}
	call, _ := calls[0].(map[string]any)
	if id, ok := call["id"].(string); ok && id != "" {
		return id
	}
	return "call_feature_probe"
}

func basicRequest(model string) map[string]any {
	return map[string]any{"model": model, "messages": []any{map[string]any{"role": "user", "content": "1+1=?"}}, "max_tokens": 64}
}

func roleRoundTripRequest(model string) map[string]any {
	request := map[string]any{
		"model": model,
		"messages": []any{
			map[string]any{"role": "system", "content": "You are a concise weather assistant."},
			map[string]any{"role": "user", "content": "What is the weather in Beijing?"},
			map[string]any{
				"role":    "assistant",
				"content": nil,
				"tool_calls": []any{map[string]any{
					"id":   "call_role_fixture",
					"type": "function",
					"function": map[string]any{
						"name":      "get_weather",
						"arguments": `{"city":"Beijing"}`,
					},
				}},
			},
			map[string]any{"role": "tool", "tool_call_id": "call_role_fixture", "content": "Beijing: sunny, 25C."},
			map[string]any{"role": "user", "content": "Answer using the tool result."},
		},
		"thinking":   map[string]string{"type": "disabled"},
		"max_tokens": 128,
	}
	return request
}

func namedToolRequest(model string) map[string]any {
	request := toolRequest(model, "auto")
	request["tool_choice"] = map[string]any{
		"type": "function",
		"function": map[string]any{
			"name": "get_weather",
		},
	}
	return request
}

func streamingToolRequest(model string) map[string]any {
	request := namedToolRequest(model)
	request["stream"] = true
	request["stream_options"] = map[string]any{"include_usage": true}
	request["thinking"] = map[string]string{"type": "disabled"}
	request["max_tokens"] = 128
	return request
}

func streamRequest(model string, includeUsage bool) map[string]any {
	request := basicRequest(model)
	request["stream"] = true
	if includeUsage {
		request["stream_options"] = map[string]any{"include_usage": true}
	}
	return request
}

func stopRequest(model string) map[string]any {
	request := basicRequest(model)
	request["messages"] = []any{map[string]any{"role": "user", "content": "Say: apple, banana, orange, watermelon."}}
	request["stop"] = "banana"
	return request
}

func stopArrayRequest(model string) map[string]any {
	request := stopRequest(model)
	request["stop"] = []string{"banana", "orange"}
	request["thinking"] = map[string]string{"type": "disabled"}
	return request
}

func toolRequest(model, choice string) map[string]any {
	request := map[string]any{
		"model":       model,
		"messages":    []any{map[string]any{"role": "user", "content": "Beijing weather?"}},
		"tools":       []any{map[string]any{"type": "function", "function": map[string]any{"name": "get_weather", "description": "Get current weather", "parameters": map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}, "required": []string{"city"}}}}},
		"tool_choice": choice,
		"max_tokens":  128,
	}
	return request
}

func jsonRequest(model string) map[string]any {
	request := basicRequest(model)
	request["messages"] = []any{map[string]any{"role": "user", "content": "Return exactly a JSON object with the answer to 1+1. Do not include any explanation."}}
	request["thinking"] = map[string]string{"type": "disabled"}
	request["max_tokens"] = 128
	request["response_format"] = map[string]string{"type": "json_object"}
	return request
}

func withFields(request map[string]any, key string, value any) map[string]any {
	copy := make(map[string]any, len(request)+1)
	for name, item := range request {
		copy[name] = item
	}
	copy[key] = value
	return copy
}

func mergeRoleEvidence(evidence map[string]any, body map[string]any) {
	messages, ok := body["messages"].([]any)
	if !ok {
		return
	}
	roles := make([]string, 0, len(messages))
	hasTool := false
	for _, raw := range messages {
		message, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role, _ := message["role"].(string)
		if role == "" {
			continue
		}
		roles = append(roles, role)
		if role == "tool" {
			hasTool = true
		}
	}
	evidence["request_role_count"] = len(roles)
	evidence["request_roles"] = roles
	evidence["request_has_tool_role"] = hasTool
}

func expectedPass(caseID, route string, status int, evidence map[string]any) bool {
	switch caseID {
	case "DS-A01":
		count, ok := evidence["model_count"].(int)
		return status == http.StatusOK && evidence["json"] == true && ok && count > 0
	case "DS-A02", "DS-A03":
		return status == http.StatusUnauthorized && evidence["json"] == true && evidence["has_error"] == true && evidence["error_param_null"] == true &&
			evidenceString(evidence, "error_type") == "authentication_error" && evidenceString(evidence, "error_code") == "invalid_request_error"
	case "DS-A04":
		return isValidationStatus(status) && evidence["json"] == true && evidence["has_error"] == true && evidence["error_param_null"] == true &&
			evidenceString(evidence, "error_type") == "invalid_request_error" && evidenceString(evidence, "error_code") == "model_not_found"
	case "DS-A05":
		return status == http.StatusOK && evidence["tls"] == true && evidence["initial_scheme"] == "https" && evidence["final_scheme"] == "https"
	case "DS-D03", "DS-D04", "DS-D05", "DS-C06":
		return expectedOfficialValidation(status, evidence)
	case "DS-B01", "DS-B04":
		valid := status == http.StatusOK && evidence["json"] == true && evidence["has_error"] != true && evidence["choices"] == 1 && evidence["has_content"] == true && evidence["finish_reason"] != nil
		return valid && (caseID != "DS-B04" || (evidence["usage_consistent"] == true && (evidence["cached_tokens"] == nil || evidence["cached_tokens_valid"] == true)))
	case "DS-B05":
		return status == http.StatusOK && evidence["json"] == true && evidence["has_error"] != true && evidence["has_content"] == true && evidence["finish_reason"] != nil && evidence["request_has_tool_role"] == true
	case "DS-B02":
		return status == http.StatusOK && hasCompleteChatSSE(evidence) && evidence["has_error"] != true && evidence["has_content"] == true && hasOfficialFinalUsage(evidence)
	case "DS-B03":
		return status == http.StatusOK && hasCompleteChatSSE(evidence) && evidence["has_error"] != true && evidence["has_content"] == true
	case "DS-C01":
		return status >= 200 && status < 300 && evidence["json"] == true && evidence["has_error"] != true && evidence["has_content"] == true
	case "DS-C02":
		return status >= 200 && status < 300 && evidence["json"] == true && evidence["has_error"] != true && evidence["has_content"] == true && evidence["has_reasoning_content"] == true
	case "DS-C03":
		return status >= 200 && status < 300 && evidence["json"] == true && evidence["has_error"] != true && evidence["has_content"] == true && evidence["has_reasoning_content"] != true
	case "DS-C05", "DS-C07":
		return status >= 200 && status < 300 && evidence["json"] == true && evidence["has_error"] != true && evidence["has_content"] == true
	case "DS-C08", "DS-C09":
		return status == http.StatusOK && evidence["first_http_status"] == http.StatusOK && evidence["first_has_content"] == true && evidence["second_http_status"] == http.StatusOK && evidence["json"] == true && evidence["has_error"] != true && evidence["has_content"] == true
	case "DS-C10":
		return status == http.StatusOK && evidence["first_http_status"] == http.StatusOK && evidence["first_has_tool_calls"] == true && evidence["first_has_reasoning_content"] == true && evidence["second_http_status"] == http.StatusOK && evidence["json"] == true && evidence["has_error"] != true && evidence["has_content"] == true
	case "DS-C11":
		return evidence["first_http_status"] == http.StatusOK && evidence["first_has_tool_calls"] == true && expectedOfficialValidation(status, evidence)
	case "DS-C04":
		return status >= 200 && status < 300 && evidence["json"] == true && evidence["has_error"] != true && evidence["has_content"] == true
	case "DS-C12":
		return route != "official" && status >= 200 && status < 300 && evidence["json"] == true && evidence["has_error"] != true && evidence["has_content"] == true
	case "DS-D08":
		return status == http.StatusOK && evidence["json"] == true && evidence["has_error"] != true && evidence["has_content"] == true && evidence["finish_reason"] == "stop" && evidence["contains_stop_sequence"] != true
	case "DS-D06":
		return status == http.StatusOK && evidence["json"] == true && evidence["has_error"] != true && evidence["has_content"] == true && evidence["finish_reason"] == "length"
	case "DS-D09":
		return status == http.StatusOK && evidence["json"] == true && evidence["has_error"] != true && evidence["has_content"] == true && evidence["finish_reason"] == "stop"
	case "DS-D10":
		return status == http.StatusOK && evidence["json"] == true && evidence["has_error"] != true && evidence["has_content"] == true && evidence["content_json"] == true
	case "DS-F02":
		return status == http.StatusOK && evidence["json"] == true && evidence["has_error"] != true && evidence["has_tool_calls"] == true && evidence["tool_arguments_json"] == true
	case "DS-E01":
		count, ok := evidence["logprobs_content"].(int)
		maxEntries, maxOK := evidence["max_top_logprobs"].(int)
		return status == http.StatusOK && evidence["json"] == true && evidence["has_error"] != true && evidence["has_logprobs"] == true && evidence["logprobs_content_valid"] == true && ok && count > 0 && maxOK && maxEntries <= 5
	case "DS-E02":
		contentCount, contentOK := evidence["logprobs_content"].(int)
		reasoningCount, reasoningOK := evidence["logprobs_reasoning_content"].(int)
		contentMax, contentMaxOK := evidence["max_top_logprobs"].(int)
		reasoningMax, reasoningMaxOK := evidence["max_reasoning_top_logprobs"].(int)
		return status == http.StatusOK && evidence["json"] == true && evidence["has_error"] != true && evidence["has_logprobs"] == true && evidence["logprobs_content_valid"] == true && evidence["logprobs_reasoning_content_valid"] == true && contentOK && contentCount > 0 && reasoningOK && reasoningCount > 0 && contentMaxOK && contentMax <= 5 && reasoningMaxOK && reasoningMax <= 5
	case "DS-E03":
		maxEntries, maxOK := evidence["max_top_logprobs"].(int)
		return status == http.StatusOK && evidence["json"] == true && evidence["has_error"] != true && evidence["has_logprobs"] == true && evidence["logprobs_content_valid"] == true && maxOK && maxEntries <= 20
	case "DS-E04":
		return expectedLogprobsValidation(status, evidence, "invalid top_logprobs and logprobs value, logprobs must be set to true if top_logprobs is used.")
	case "DS-E05":
		count, ok := evidence["logprobs_content"].(int)
		maxEntries, maxOK := evidence["max_top_logprobs"].(int)
		return status == http.StatusOK && hasCompleteChatSSE(evidence) && evidence["has_error"] != true && evidence["has_content"] == true && evidence["has_logprobs"] == true && evidence["logprobs_content_valid"] == true && ok && count > 0 && maxOK && maxEntries <= 5 && hasOfficialFinalUsage(evidence)
	case "DS-E06":
		return false
	case "DS-E07":
		return expectedLogprobsValidation(status, evidence, "invalid top_logprobs value, the valid range of top_logprobs is [0, 20].")
	case "DS-F01":
		return status == http.StatusOK && evidence["json"] == true && evidence["has_error"] != true && (evidence["has_content"] == true || evidence["has_tool_calls"] == true)
	case "DS-F06":
		return status == http.StatusOK && evidence["first_http_status"] == http.StatusOK && evidence["first_has_tool_calls"] == true && evidence["second_http_status"] == http.StatusOK && evidence["json"] == true && evidence["has_error"] != true && evidence["has_content"] == true && evidence["finish_reason"] == "stop"
	case "DS-F07":
		return status == http.StatusOK && evidence["first_http_status"] == http.StatusOK && evidence["first_has_tool_calls"] == true && evidence["first_has_reasoning_content"] == true && evidence["second_http_status"] == http.StatusOK && evidence["json"] == true && evidence["has_error"] != true && evidence["has_content"] == true
	case "DS-F08":
		return evidence["first_http_status"] == http.StatusOK && evidence["first_has_tool_calls"] == true && expectedOfficialValidation(status, evidence)
	case "DS-F09":
		return status == http.StatusOK && hasCompleteChatSSE(evidence) && evidence["has_error"] != true && evidence["has_tool_calls"] == true && evidence["tool_arguments_json"] == true && (evidence["finish_reason"] == "tool_calls" || evidence["finish_reason"] == "stop")
	default:
		return status >= 200 && status < 300 && evidence["has_error"] != true
	}
}

func isValidationStatus(status int) bool {
	return status == http.StatusBadRequest || status == http.StatusUnprocessableEntity
}

func expectedLogprobsValidation(status int, evidence map[string]any, message string) bool {
	return status == http.StatusBadRequest && evidence["json"] == true && evidence["has_error"] == true && evidence["error_param_null"] == true && evidenceString(evidence, "error_type") == "invalid_request_error" && evidenceString(evidence, "error_code") == "invalid_request_error" && evidenceString(evidence, "error_message_fingerprint") == message
}

func hasOfficialFinalUsage(evidence map[string]any) bool {
	usageEvents, usageOK := evidence["usage_events"].(int)
	usageOnlyEvents, usageOnlyOK := evidence["usage_only_events"].(int)
	return usageOK && usageEvents == 1 && usageOnlyOK && usageOnlyEvents == 0 && evidence["usage_on_finish_event"] == true && evidence["usage_on_final_event"] == true && evidence["official_usage_shape"] == true && evidence["usage_consistent"] == true && evidence["cache_tokens_consistent"] == true && evidence["completion_within_requested_max"] != false
}

func hasCompleteChatSSE(evidence map[string]any) bool {
	doneEvents, doneOK := evidence["done_events"].(int)
	jsonErrors, jsonOK := evidence["sse_json_errors"].(int)
	return evidence["stream"] == true && evidence["done"] == true && doneOK && doneEvents == 1 && evidence["done_last"] == true && jsonOK && jsonErrors == 0 && evidence["sse_scan_error"] != true && evidence["stream_shape_valid"] == true
}

func validChatStreamEvent(value map[string]any) bool {
	if !hasOnlyKeys(value, "id", "object", "created", "model", "choices", "usage", "system_fingerprint") || !hasNonEmptyString(value["id"]) || value["object"] != "chat.completion.chunk" || value["model"] != "deepseek-v4-flash" {
		return false
	}
	created, createdOK := value["created"].(float64)
	if !createdOK || created <= 0 || math.Trunc(created) != created {
		return false
	}
	if fingerprint, exists := value["system_fingerprint"]; exists && fingerprint != nil {
		if _, ok := fingerprint.(string); !ok {
			return false
		}
	}
	if usage, exists := value["usage"]; exists && usage != nil {
		if _, ok := usage.(map[string]any); !ok {
			return false
		}
	}
	choices, ok := value["choices"].([]any)
	if !ok || len(choices) != 1 {
		return false
	}
	choice, ok := choices[0].(map[string]any)
	if !ok || !hasOnlyKeys(choice, "index", "delta", "logprobs", "finish_reason") || choice["index"] != float64(0) {
		return false
	}
	delta, ok := choice["delta"].(map[string]any)
	if !ok || !hasOnlyKeys(delta, "role", "content", "reasoning_content", "tool_calls") {
		return false
	}
	if role, exists := delta["role"]; exists && role != nil && role != "assistant" {
		return false
	}
	for _, field := range []string{"content", "reasoning_content"} {
		if item, exists := delta[field]; exists && item != nil {
			if _, ok := item.(string); !ok {
				return false
			}
		}
	}
	if calls, exists := delta["tool_calls"]; exists && calls != nil {
		if _, ok := calls.([]any); !ok {
			return false
		}
	}
	if logprobs, exists := choice["logprobs"]; exists && logprobs != nil {
		if _, ok := logprobs.(map[string]any); !ok {
			return false
		}
	}
	finishReason, exists := choice["finish_reason"]
	if !exists {
		return false
	}
	if finishReason != nil {
		reason, ok := finishReason.(string)
		if !ok || !validFinishReason(reason) {
			return false
		}
	}
	return true
}

func hasOfficialNonStreamResponse(evidence map[string]any) bool {
	choices, choicesOK := evidence["choices"].(int)
	return evidence["stream"] == false && evidence["json"] == true && evidence["has_error"] != true && evidence["has_response_id"] == true && evidence["has_created"] == true && evidenceString(evidence, "object") == "chat.completion" && evidenceString(evidence, "response_model") == "deepseek-v4-flash" && choicesOK && choices == 1 && evidence["choice_index_zero"] == true && evidence["has_message"] == true && evidence["message_role_assistant"] == true && evidence["finish_reason_valid"] == true && evidence["unexpected_top_level_fields"] != true && evidence["unexpected_choice_fields"] != true && evidence["unexpected_message_fields"] != true && evidence["message_tool_calls_null"] != true && evidence["official_usage_shape"] == true && evidence["usage_consistent"] == true && evidence["cache_tokens_consistent"] == true && evidence["completion_within_requested_max"] != false
}

func validFinishReason(reason string) bool {
	switch reason {
	case "stop", "length", "tool_calls", "content_filter", "insufficient_system_resource":
		return true
	default:
		return false
	}
}

func expectedOfficialValidation(status int, evidence map[string]any) bool {
	return isValidationStatus(status) && evidence["json"] == true && evidence["has_error"] == true && evidence["error_param_null"] == true && evidenceString(evidence, "error_type") == "invalid_request_error" && evidenceString(evidence, "error_code") == "invalid_request_error"
}

func isDFLASHCapabilityError(evidence map[string]any) bool {
	fingerprint := evidenceString(evidence, "error_message_fingerprint")
	return strings.Contains(fingerprint, "return_logprob") || strings.Contains(fingerprint, "return logprob")
}

func evidenceString(evidence map[string]any, key string) string {
	value, _ := evidence[key].(string)
	return value
}

func request(ctx context.Context, client *http.Client, base, key, method, path string, body []byte) (int, map[string]any, error) {
	status, evidence, _, err := requestPayload(ctx, client, base, key, method, path, body)
	return status, evidence, err
}

func requestPayload(ctx context.Context, client *http.Client, base, key, method, path string, body []byte) (int, map[string]any, map[string]any, error) {
	url := strings.TrimRight(base, "/") + "/" + strings.TrimLeft(path, "/")
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return 0, map[string]any{"request_error": safeError(err)}, nil, err
	}
	if !strings.EqualFold(req.URL.Scheme, "https") {
		return 0, map[string]any{"initial_scheme": req.URL.Scheme}, nil, fmt.Errorf("feature probe requires HTTPS")
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Accept", "application/json")
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, map[string]any{"transport_error": safeError(err)}, nil, err
	}
	defer resp.Body.Close()
	stream := strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream")
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if readErr != nil {
		evidence := summarize(data, stream)
		evidence["read_error"] = safeError(readErr)
		evidence["initial_scheme"] = req.URL.Scheme
		evidence["tls"] = resp.TLS != nil
		if resp.Request != nil && resp.Request.URL != nil {
			evidence["final_scheme"] = resp.Request.URL.Scheme
		}
		return resp.StatusCode, evidence, nil, readErr
	}
	isResponses := strings.HasSuffix(strings.TrimRight(path, "/"), "/responses")
	evidence := summarize(data, stream)
	if isResponses {
		evidence = summarizeResponses(data, stream)
	}
	evidence["initial_scheme"] = req.URL.Scheme
	evidence["tls"] = resp.TLS != nil
	if resp.Request != nil && resp.Request.URL != nil {
		evidence["final_scheme"] = resp.Request.URL.Scheme
	}
	var payload map[string]any
	if !stream {
		_ = json.Unmarshal(data, &payload)
	}
	return resp.StatusCode, evidence, payload, nil
}

type streamedToolCall struct {
	name      strings.Builder
	arguments strings.Builder
}

func summarize(data []byte, stream bool) map[string]any {
	evidence := map[string]any{"bytes": len(data), "stream": stream}
	if stream {
		scanner := bufio.NewScanner(bytes.NewReader(data))
		scanner.Buffer(make([]byte, 64*1024), 2<<20)
		events, done, doneEvents, usageEvents, usageOnlyEvents := 0, false, 0, 0, 0
		jsonErrors, parsedEvents, doneLast := 0, 0, false
		streamShapeValid := true
		contentChunks, reasoningChunks := 0, 0
		usageOnFinishEvent, usageOnFinalEvent := false, false
		var finishReason string
		toolCalls := make(map[int]*streamedToolCall)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data:") {
				events++
				payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				if payload == "[DONE]" {
					done = true
					doneEvents++
					doneLast = true
					continue
				}
				doneLast = false
				usageOnFinalEvent = false
				var value map[string]any
				if json.Unmarshal([]byte(payload), &value) != nil {
					jsonErrors++
					continue
				}
				parsedEvents++
				if !validChatStreamEvent(value) {
					streamShapeValid = false
				}
				if errValue, ok := value["error"].(map[string]any); ok {
					addErrorEvidence(evidence, errValue)
				}
				eventHasFinish := false
				if choices, ok := value["choices"].([]any); ok {
					for _, rawChoice := range choices {
						choice, ok := rawChoice.(map[string]any)
						if !ok {
							continue
						}
						if reason, ok := choice["finish_reason"].(string); ok && reason != "" {
							finishReason = reason
							eventHasFinish = true
						}
						if logprobs, ok := choice["logprobs"].(map[string]any); ok {
							addLogprobsEvidence(evidence, logprobs)
						}
						for _, field := range []string{"delta", "message"} {
							part, ok := choice[field].(map[string]any)
							if !ok {
								continue
							}
							if hasNonEmptyString(part["content"]) {
								contentChunks++
							}
							if hasNonEmptyString(part["reasoning_content"]) {
								reasoningChunks++
							}
							if logprobs, ok := part["logprobs"].(map[string]any); ok {
								addLogprobsEvidence(evidence, logprobs)
							}
							if rawCalls, ok := part["tool_calls"].([]any); ok {
								for callIndex, rawCall := range rawCalls {
									call, ok := rawCall.(map[string]any)
									if !ok {
										continue
									}
									index := callIndex
									if value, ok := call["index"].(float64); ok && value >= 0 {
										index = int(value)
									}
									toolCall := toolCalls[index]
									if toolCall == nil {
										toolCall = &streamedToolCall{}
										toolCalls[index] = toolCall
									}
									function, _ := call["function"].(map[string]any)
									if name, ok := function["name"].(string); ok {
										toolCall.name.WriteString(name)
									}
									if arguments, ok := function["arguments"].(string); ok {
										toolCall.arguments.WriteString(arguments)
									}
								}
							}
						}
					}
				}
				if usage, ok := value["usage"].(map[string]any); ok {
					usageEvents++
					addUsageEvidence(evidence, usage)
					choices, hasChoices := value["choices"].([]any)
					if !hasChoices || len(choices) == 0 {
						usageOnlyEvents++
						evidence["usage_event_empty_choices"] = true
					}
					if eventHasFinish {
						usageOnFinishEvent = true
						usageOnFinalEvent = true
					}
				}
			}
		}
		evidence["sse_events"], evidence["done"] = events, done
		evidence["done_events"], evidence["done_last"], evidence["sse_json_errors"] = doneEvents, doneLast, jsonErrors
		evidence["stream_shape_valid"] = parsedEvents > 0 && streamShapeValid
		if scanner.Err() != nil {
			evidence["sse_scan_error"] = true
		}
		evidence["content_chunks"], evidence["reasoning_chunks"] = contentChunks, reasoningChunks
		evidence["has_content"], evidence["has_reasoning_content"] = contentChunks > 0, reasoningChunks > 0
		if len(toolCalls) > 0 {
			evidence["has_tool_calls"] = true
			evidence["stream_tool_call_count"] = len(toolCalls)
			validArguments := true
			for _, toolCall := range toolCalls {
				if strings.TrimSpace(toolCall.name.String()) == "" || !json.Valid([]byte(toolCall.arguments.String())) {
					validArguments = false
				}
			}
			evidence["tool_arguments_json"] = validArguments
		}
		if usageEvents > 0 {
			evidence["usage_events"] = usageEvents
			evidence["usage_only_events"] = usageOnlyEvents
			evidence["usage_on_finish_event"] = usageOnFinishEvent
			evidence["usage_on_final_event"] = usageOnFinalEvent
		}
		if finishReason != "" {
			evidence["finish_reason"] = finishReason
		}
		return evidence
	}
	var value map[string]any
	if json.Unmarshal(data, &value) != nil {
		evidence["json"] = false
		return evidence
	}
	evidence["json"] = true
	evidence["has_content"], evidence["has_reasoning_content"] = false, false
	evidence["unexpected_top_level_fields"] = !hasOnlyKeys(value, "id", "object", "created", "model", "choices", "usage", "system_fingerprint")
	if id, ok := value["id"].(string); ok && strings.TrimSpace(id) != "" {
		evidence["has_response_id"] = true
	}
	if created, ok := value["created"].(float64); ok && created > 0 {
		evidence["has_created"] = true
	}
	if model, ok := value["model"].(string); ok && model != "" {
		evidence["response_model"] = model
	}
	if models, ok := value["data"].([]any); ok {
		evidence["model_count"] = len(models)
		for _, rawModel := range models {
			model, ok := rawModel.(map[string]any)
			if ok && model["id"] == "deepseek-v4-flash" {
				evidence["has_target_model"] = true
			}
		}
	}
	if choices, ok := value["choices"].([]any); ok {
		evidence["choices"] = len(choices)
		if len(choices) > 0 {
			if choice, ok := choices[0].(map[string]any); ok {
				evidence["unexpected_choice_fields"] = !hasOnlyKeys(choice, "index", "message", "logprobs", "finish_reason")
				if index, ok := choice["index"].(float64); ok && index == 0 {
					evidence["choice_index_zero"] = true
				}
				if reason, ok := choice["finish_reason"].(string); ok && reason != "" {
					evidence["finish_reason"] = reason
					evidence["finish_reason_valid"] = validFinishReason(reason)
				}
				for _, field := range []string{"message", "delta"} {
					part, ok := choice[field].(map[string]any)
					if !ok {
						continue
					}
					if field == "message" {
						evidence["has_message"] = true
						evidence["unexpected_message_fields"] = !hasOnlyKeys(part, "role", "content", "reasoning_content", "tool_calls")
						evidence["message_role_assistant"] = part["role"] == "assistant"
						if toolCalls, exists := part["tool_calls"]; exists && toolCalls == nil {
							evidence["message_tool_calls_null"] = true
						}
					}
					if content, ok := part["content"].(string); ok && strings.TrimSpace(content) != "" {
						evidence["has_content"] = true
						evidence["contains_stop_sequence"] = strings.Contains(content, "香蕉") || strings.Contains(strings.ToLower(content), "banana")
						evidence["contains_pre_stop_content"] = strings.Contains(content, "苹果") || strings.Contains(strings.ToLower(content), "apple")
						if json.Valid([]byte(content)) {
							evidence["content_json"] = true
						}
					}
					if hasNonEmptyString(part["reasoning_content"]) {
						evidence["has_reasoning_content"] = true
					}
				}
				if logprobs, ok := choice["logprobs"].(map[string]any); ok {
					evidence["has_logprobs"] = true
					if content, ok := logprobs["content"].([]any); ok {
						evidence["logprobs_content"] = len(content)
						evidence["max_top_logprobs"] = maxTopLogprobs(content)
						evidence["logprobs_content_valid"] = validLogprobsEntries(content)
					}
					if reasoningContent, ok := logprobs["reasoning_content"].([]any); ok {
						evidence["logprobs_reasoning_content"] = len(reasoningContent)
						evidence["max_reasoning_top_logprobs"] = maxTopLogprobs(reasoningContent)
						evidence["logprobs_reasoning_content_valid"] = validLogprobsEntries(reasoningContent)
					}
				}
				if message, ok := choice["message"].(map[string]any); ok {
					if toolCalls, ok := message["tool_calls"].([]any); ok && len(toolCalls) > 0 {
						evidence["has_tool_calls"] = true
						validArguments := true
						fitToolCallsValid := true
						for _, rawToolCall := range toolCalls {
							toolCall, ok := rawToolCall.(map[string]any)
							function, functionOK := toolCall["function"].(map[string]any)
							arguments, argumentsOK := function["arguments"].(string)
							if !ok || !functionOK || !hasNonEmptyString(function["name"]) || !argumentsOK || !json.Valid([]byte(arguments)) {
								validArguments = false
							}
							var decodedArguments map[string]any
							if !ok || toolCall["type"] != "function" || function["name"] != "get_weather" || !argumentsOK || json.Unmarshal([]byte(arguments), &decodedArguments) != nil || !hasNonEmptyString(decodedArguments["city"]) {
								fitToolCallsValid = false
							}
						}
						evidence["tool_arguments_json"] = validArguments
						evidence["fit_tool_calls_valid"] = fitToolCallsValid
					}
				}
			}
		}
	}
	if usage, ok := value["usage"].(map[string]any); ok {
		addUsageEvidence(evidence, usage)
	}
	if value["object"] != nil {
		evidence["object"] = value["object"]
	}
	if errValue, ok := value["error"].(map[string]any); ok {
		addErrorEvidence(evidence, errValue)
	}
	return evidence
}

func summarizeResponses(data []byte, stream bool) map[string]any {
	evidence := map[string]any{"bytes": len(data), "stream": stream}
	if stream {
		return summarizeResponsesStream(data, evidence)
	}
	var payload map[string]any
	if json.Unmarshal(data, &payload) != nil {
		evidence["json"] = false
		return evidence
	}
	evidence["json"] = true
	if errValue, ok := payload["error"].(map[string]any); ok {
		addErrorEvidence(evidence, errValue)
	}
	response := payload
	if nested, ok := payload["response"].(map[string]any); ok {
		response = nested
	}
	if object, ok := response["object"].(string); ok {
		evidence["response_object"] = object == "response"
		evidence["response_object_type"] = object
	}
	if status, ok := response["status"].(string); ok && status != "" {
		evidence["response_status"] = status
	}
	if output, ok := response["output"].([]any); ok {
		summarizeResponsesOutput(evidence, output)
	}
	if usage, ok := response["usage"].(map[string]any); ok {
		addResponsesUsageEvidence(evidence, usage)
	}
	return evidence
}

func summarizeResponsesStream(data []byte, evidence map[string]any) map[string]any {
	seenTypes := make(map[string]struct{})
	eventTypes := make([]string, 0, 16)
	argumentBuilders := make(map[string]*strings.Builder)
	var eventName string
	var dataLines []string
	var lastEventType string
	flush := func() {
		if eventName == "" && len(dataLines) == 0 {
			return
		}
		payloadText := strings.Join(dataLines, "\n")
		dataLines = nil
		eventType := strings.TrimSpace(eventName)
		eventName = ""
		if payloadText == "[DONE]" {
			evidence["done_marker"] = true
			return
		}
		var payload map[string]any
		if json.Unmarshal([]byte(payloadText), &payload) != nil {
			return
		}
		if value, ok := payload["type"].(string); ok && value != "" {
			eventType = value
		}
		if eventType != "" {
			lastEventType = eventType
			if _, exists := seenTypes[eventType]; !exists {
				seenTypes[eventType] = struct{}{}
				if len(eventTypes) < 32 {
					eventTypes = append(eventTypes, eventType)
				}
			}
		}
		if eventType == "response.completed" || eventType == "response.incomplete" || eventType == "response.failed" {
			evidence["response_terminal"] = true
			evidence["response_terminal_event"] = eventType
		}
		if errValue, ok := payload["error"].(map[string]any); ok {
			addErrorEvidence(evidence, errValue)
		}
		response, _ := payload["response"].(map[string]any)
		if response != nil {
			if object, ok := response["object"].(string); ok {
				evidence["response_object"] = object == "response"
				evidence["response_object_type"] = object
			}
			if status, ok := response["status"].(string); ok && status != "" {
				evidence["response_status"] = status
			}
			if output, ok := response["output"].([]any); ok {
				summarizeResponsesOutput(evidence, output)
			}
			if usage, ok := response["usage"].(map[string]any); ok {
				addResponsesUsageEvidence(evidence, usage)
			}
		}
		if delta, ok := payload["delta"].(string); ok && strings.TrimSpace(delta) != "" {
			switch eventType {
			case "response.output_text.delta":
				evidence["has_output_text"] = true
			case "response.reasoning_text.delta", "response.reasoning_summary_text.delta":
				evidence["has_reasoning_output"] = true
			case "response.function_call_arguments.delta":
				key := responsesStreamItemKey(payload)
				builder := argumentBuilders[key]
				if builder == nil {
					builder = &strings.Builder{}
					argumentBuilders[key] = builder
				}
				builder.WriteString(delta)
			}
		}
		if (eventType == "response.reasoning_text.done" || eventType == "response.reasoning_summary_text.done") && hasNonEmptyString(payload["text"]) {
			evidence["has_reasoning_output"] = true
		}
		if item, ok := payload["item"].(map[string]any); ok {
			summarizeResponsesOutput(evidence, []any{item})
		}
		if eventType == "response.function_call_arguments.done" {
			arguments, _ := payload["arguments"].(string)
			if arguments == "" {
				arguments = argumentBuilders[responsesStreamItemKey(payload)].String()
			}
			if arguments != "" {
				evidence["function_call_arguments_json"] = json.Valid([]byte(arguments))
			}
		}
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 2<<20)
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	flush()
	evidence["json"] = len(eventTypes) > 0 || evidence["done_marker"] == true
	evidence["responses_event_types"] = eventTypes
	evidence["responses_event_count"] = len(eventTypes)
	evidence["response_terminal_last"] = isResponsesTerminalEvent(lastEventType)
	return evidence
}

func isResponsesTerminalEvent(eventType string) bool {
	switch eventType {
	case "response.completed", "response.incomplete", "response.failed":
		return true
	default:
		return false
	}
}

func responsesStreamItemKey(payload map[string]any) string {
	if itemID, ok := payload["item_id"].(string); ok && itemID != "" {
		return itemID
	}
	if index, ok := payload["output_index"].(float64); ok {
		return fmt.Sprintf("output-%d", int(index))
	}
	return "default"
}

func summarizeResponsesOutput(evidence map[string]any, output []any) {
	typesSeen := make(map[string]struct{})
	outputTypes, _ := evidence["output_types"].([]string)
	functionCalls := 0
	functionArgumentsValid := true
	for _, raw := range output {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		typeName, _ := item["type"].(string)
		if typeName != "" {
			if _, exists := typesSeen[typeName]; !exists {
				typesSeen[typeName] = struct{}{}
				outputTypes = append(outputTypes, typeName)
			}
		}
		switch typeName {
		case "message", "output_text":
			if content, ok := item["content"].([]any); ok {
				for _, rawContent := range content {
					part, ok := rawContent.(map[string]any)
					if !ok {
						continue
					}
					partType, _ := part["type"].(string)
					if partType == "output_text" && hasNonEmptyString(part["text"]) {
						evidence["has_output_text"] = true
					}
					if strings.Contains(partType, "reasoning") && hasNonEmptyString(part["text"]) {
						evidence["has_reasoning_output"] = true
					}
				}
			}
		case "reasoning", "reasoning_text":
			if responsesItemHasText(item) {
				evidence["has_reasoning_output"] = true
			}
		case "function_call":
			functionCalls++
			arguments, _ := item["arguments"].(string)
			if arguments == "" || !json.Valid([]byte(arguments)) {
				functionArgumentsValid = false
			}
		}
	}
	if len(outputTypes) > 0 {
		evidence["output_types"] = outputTypes
	}
	if functionCalls > 0 {
		evidence["has_function_call"] = true
		evidence["function_call_count"] = functionCalls
		evidence["function_call_arguments_json"] = functionArgumentsValid
	}
}

func responsesItemHasText(item map[string]any) bool {
	if hasNonEmptyString(item["text"]) {
		return true
	}
	for _, field := range []string{"content", "summary"} {
		parts, ok := item[field].([]any)
		if !ok {
			continue
		}
		for _, rawPart := range parts {
			part, ok := rawPart.(map[string]any)
			if ok && hasNonEmptyString(part["text"]) {
				return true
			}
		}
	}
	return false
}

func addResponsesUsageEvidence(evidence map[string]any, usage map[string]any) {
	evidence["usage"] = true
	values := make(map[string]float64, 3)
	valid := true
	for _, field := range []string{"input_tokens", "output_tokens", "total_tokens"} {
		value, ok := usage[field]
		if !ok {
			valid = false
			continue
		}
		number, ok := responsesUsageNumber(value)
		if !ok {
			valid = false
			continue
		}
		evidence[field] = value
		values[field] = number
	}
	if valid && values["total_tokens"] != values["input_tokens"]+values["output_tokens"] {
		valid = false
	}
	evidence["usage_valid"] = valid
	if details, ok := usage["input_tokens_details"].(map[string]any); ok {
		if cached, ok := details["cached_tokens"]; ok {
			evidence["cached_tokens"] = cached
		}
	}
}

func responsesUsageNumber(value any) (float64, bool) {
	number, ok := value.(float64)
	return number, ok && !math.IsNaN(number) && !math.IsInf(number, 0) && number >= 0 && math.Trunc(number) == number
}

func hasNonEmptyString(value any) bool {
	text, ok := value.(string)
	return ok && strings.TrimSpace(text) != ""
}

func maxTopLogprobs(entries []any) int {
	maxEntries := 0
	for _, rawEntry := range entries {
		entry, ok := rawEntry.(map[string]any)
		if !ok {
			continue
		}
		topLogprobs, ok := entry["top_logprobs"].([]any)
		if ok && len(topLogprobs) > maxEntries {
			maxEntries = len(topLogprobs)
		}
	}
	return maxEntries
}

func validLogprobsEntries(entries []any) bool {
	if len(entries) == 0 {
		return false
	}
	for _, rawEntry := range entries {
		entry, ok := rawEntry.(map[string]any)
		if !ok {
			return false
		}
		if _, ok := entry["token"].(string); !ok || !finiteNumber(entry["logprob"]) {
			return false
		}
		topLogprobs, ok := entry["top_logprobs"].([]any)
		if !ok {
			return false
		}
		for _, rawCandidate := range topLogprobs {
			candidate, ok := rawCandidate.(map[string]any)
			if !ok {
				return false
			}
			if _, ok := candidate["token"].(string); !ok || !finiteNumber(candidate["logprob"]) {
				return false
			}
		}
	}
	return true
}

func finiteNumber(value any) bool {
	number, ok := value.(float64)
	return ok && !math.IsNaN(number) && !math.IsInf(number, 0)
}

func addLogprobsEvidence(evidence map[string]any, logprobs map[string]any) {
	evidence["has_logprobs"] = true
	if content, ok := logprobs["content"].([]any); ok {
		count, _ := evidence["logprobs_content"].(int)
		evidence["logprobs_content"] = count + len(content)
		mergeBoolEvidence(evidence, "logprobs_content_valid", validLogprobsEntries(content))
		maxEntries := maxTopLogprobs(content)
		if current, ok := evidence["max_top_logprobs"].(int); !ok || maxEntries > current {
			evidence["max_top_logprobs"] = maxEntries
		}
	}
	if reasoningContent, ok := logprobs["reasoning_content"].([]any); ok {
		count, _ := evidence["logprobs_reasoning_content"].(int)
		evidence["logprobs_reasoning_content"] = count + len(reasoningContent)
		mergeBoolEvidence(evidence, "logprobs_reasoning_content_valid", validLogprobsEntries(reasoningContent))
		maxEntries := maxTopLogprobs(reasoningContent)
		if current, ok := evidence["max_reasoning_top_logprobs"].(int); !ok || maxEntries > current {
			evidence["max_reasoning_top_logprobs"] = maxEntries
		}
	}
}

func mergeBoolEvidence(evidence map[string]any, key string, value bool) {
	if current, ok := evidence[key].(bool); ok {
		evidence[key] = current && value
		return
	}
	evidence[key] = value
}

func addUsageEvidence(evidence map[string]any, usage map[string]any) {
	evidence["usage"] = true
	evidence["usage_shape"] = usageShape(usage)
	for _, field := range []string{"prompt_tokens", "completion_tokens", "total_tokens"} {
		if value, ok := usage[field]; ok {
			evidence[field] = value
		}
	}
	if details, ok := usage["prompt_tokens_details"].(map[string]any); ok {
		if cached, ok := details["cached_tokens"]; ok {
			evidence["cached_tokens"] = cached
		}
	}
	for _, field := range []string{"prompt_cache_hit_tokens", "cached_tokens"} {
		if value, ok := usage[field]; ok {
			evidence["cached_tokens"] = value
		}
	}
	prompt, promptOK := usage["prompt_tokens"].(float64)
	completion, completionOK := usage["completion_tokens"].(float64)
	total, totalOK := usage["total_tokens"].(float64)
	if promptOK && completionOK && totalOK {
		evidence["usage_consistent"] = total == prompt+completion
	}
	if cached, cachedOK := evidence["cached_tokens"].(float64); cachedOK && promptOK {
		evidence["cached_tokens_valid"] = cached >= 0 && cached <= prompt
	}
	promptDetails, promptDetailsOK := usage["prompt_tokens_details"].(map[string]any)
	completionDetails, completionDetailsOK := usage["completion_tokens_details"].(map[string]any)
	completionDetailsPresent := completionDetailsOK
	cached, cachedOK := promptDetails["cached_tokens"].(float64)
	reasoning, reasoningOK := completionDetails["reasoning_tokens"].(float64)
	cacheHit, cacheHitOK := usage["prompt_cache_hit_tokens"].(float64)
	cacheMiss, cacheMissOK := usage["prompt_cache_miss_tokens"].(float64)
	usageKeysOK := hasExactKeys(usage, "prompt_tokens", "completion_tokens", "total_tokens", "prompt_tokens_details", "completion_tokens_details", "prompt_cache_hit_tokens", "prompt_cache_miss_tokens") ||
		hasExactKeys(usage, "prompt_tokens", "completion_tokens", "total_tokens", "prompt_tokens_details", "prompt_cache_hit_tokens", "prompt_cache_miss_tokens")
	completionDetailsOK = !completionDetailsPresent || hasExactKeys(completionDetails, "reasoning_tokens")
	reasoningNumberOK := !completionDetailsPresent || (reasoningOK && reasoning >= 0 && math.Trunc(reasoning) == reasoning)
	evidence["official_usage_shape"] = usageKeysOK &&
		promptDetailsOK && hasExactKeys(promptDetails, "cached_tokens") && completionDetailsOK &&
		promptOK && completionOK && totalOK && cachedOK && cacheHitOK && cacheMissOK && reasoningNumberOK &&
		prompt >= 0 && completion >= 0 && total >= 0 && cached >= 0 && cacheHit >= 0 && cacheMiss >= 0 &&
		math.Trunc(prompt) == prompt && math.Trunc(completion) == completion && math.Trunc(total) == total && math.Trunc(cached) == cached && math.Trunc(cacheHit) == cacheHit && math.Trunc(cacheMiss) == cacheMiss
	if promptOK && cachedOK && cacheHitOK && cacheMissOK {
		evidence["cache_tokens_consistent"] = cached == cacheHit && cacheHit >= 0 && cacheMiss >= 0 && cacheHit+cacheMiss == prompt
	}
}

func usageShape(usage map[string]any) string {
	keys := make([]string, 0, len(usage))
	for key := range usage {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := []string{strings.Join(keys, ",")}
	for _, field := range []string{"prompt_tokens_details", "completion_tokens_details"} {
		details, ok := usage[field].(map[string]any)
		if !ok {
			parts = append(parts, field+":-")
			continue
		}
		detailKeys := make([]string, 0, len(details))
		for key := range details {
			detailKeys = append(detailKeys, key)
		}
		sort.Strings(detailKeys)
		parts = append(parts, field+":"+strings.Join(detailKeys, ","))
	}
	return strings.Join(parts, "|")
}

func hasExactKeys(value map[string]any, keys ...string) bool {
	if len(value) != len(keys) {
		return false
	}
	for _, key := range keys {
		if _, ok := value[key]; !ok {
			return false
		}
	}
	return true
}

func hasOnlyKeys(value map[string]any, keys ...string) bool {
	allowed := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		allowed[key] = struct{}{}
	}
	for key := range value {
		if _, ok := allowed[key]; !ok {
			return false
		}
	}
	return true
}

func addErrorEvidence(evidence map[string]any, errValue map[string]any) {
	evidence["has_error"] = true
	if param, exists := errValue["param"]; exists {
		evidence["error_param_present"] = true
		evidence["error_param_null"] = param == nil
	} else {
		evidence["error_param_present"] = false
		evidence["error_param_null"] = false
	}
	for _, field := range []string{"code", "type"} {
		if value, ok := errValue[field].(string); ok && value != "" {
			evidence["error_"+field] = value
		}
	}
	if message, ok := errValue["message"].(string); ok && message != "" {
		evidence["error_message_fingerprint"] = fingerprint(message)
	}
}

func fingerprint(message string) string {
	message = strings.ToLower(strings.Join(strings.Fields(message), " "))
	message = fingerprintSecretPattern.ReplaceAllString(message, "***")
	if len(message) > 160 {
		message = message[:160]
	}
	return message
}

func liveProbeConfigured() bool {
	if strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY")) != "" {
		return true
	}
	return strings.TrimSpace(os.Getenv("NEW_API_KEY")) != "" &&
		(strings.TrimSpace(os.Getenv("NEW_API_BASE_URL")) != "" || strings.TrimSpace(os.Getenv("NEW_API_BACKUP_URL")) != "")
}

func tierFor(id string) string {
	if strings.HasPrefix(id, "DS-A") || strings.HasPrefix(id, "DS-B") || strings.HasPrefix(id, "DS-C") || strings.HasPrefix(id, "DS-D") || strings.HasPrefix(id, "DS-E") || strings.HasPrefix(id, "DS-F") || strings.HasPrefix(id, "DS-G") {
		return "T2/T3"
	}
	if strings.HasPrefix(id, "DS-H") || strings.HasPrefix(id, "DS-I") || strings.HasPrefix(id, "DS-J") {
		return "T0/T1/T4"
	}
	return "offline"
}
func surfaceFor(id string) string {
	switch {
	case strings.HasPrefix(id, "DS-A"), strings.HasPrefix(id, "DS-B"), strings.HasPrefix(id, "DS-C"), strings.HasPrefix(id, "DS-D"), strings.HasPrefix(id, "DS-E"), strings.HasPrefix(id, "DS-F"), strings.HasPrefix(id, "DS-G"):
		return "provider"
	case strings.HasPrefix(id, "DS-H"):
		return "gateway-fidelity"
	case strings.HasPrefix(id, "DS-I"):
		return "retry"
	default:
		return "billing-security"
	}
}
func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
func safeError(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("%T", err)
}
