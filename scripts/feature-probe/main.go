package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
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
	"DS-B01": {}, "DS-B02": {}, "DS-B03": {}, "DS-B04": {},
	"DS-C01": {}, "DS-C02": {}, "DS-C03": {}, "DS-C04": {}, "DS-C05": {}, "DS-C06": {}, "DS-C07": {}, "DS-C12": {},
	"DS-D03": {}, "DS-D04": {}, "DS-D05": {}, "DS-D08": {}, "DS-D10": {},
	"DS-E01": {}, "DS-E02": {}, "DS-E03": {}, "DS-E04": {}, "DS-E07": {}, "DS-F01": {}, "DS-F02": {},
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
	for _, id := range allCases {
		if !executed[id] {
			reason := "runner_not_implemented"
			if _, implemented := implementedLiveCases[id]; implemented {
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
	routes := []struct{ name, base, key, tier string }{
		{"main", envOr("NEW_API_BASE_URL", "https://n.tokeness.dev/v1"), os.Getenv("NEW_API_KEY"), "gateway-live"},
		{"backup", envOr("NEW_API_BACKUP_URL", "https://n-cf.tokeness.dev/v1"), os.Getenv("NEW_API_KEY"), "gateway-live"},
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("FEATURE_PROBE_INCLUDE_OFFICIAL")), "true") {
		routes = append([]struct{ name, base, key, tier string }{
			{"official", envOr("DEEPSEEK_BASE_URL", "https://api.deepseek.com"), os.Getenv("DEEPSEEK_API_KEY"), "official-live"},
		}, routes...)
	}
	checks := fitChecks("deepseek-v4-flash")
	selected, err := selectFitChecks(checks, fitCaseSelection())
	if err != nil {
		return err
	}
	for _, route := range routes {
		if strings.TrimSpace(route.base) == "" || strings.TrimSpace(route.key) == "" {
			reason := "live_api_key_not_injected"
			if strings.TrimSpace(route.base) == "" {
				reason = "live_route_not_injected"
			}
			for _, check := range selected {
				_ = json.NewEncoder(os.Stdout).Encode(result{
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
			annotateFitEvidence(status, evidence)
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
			json.NewEncoder(os.Stdout).Encode(item)
		}
	}
	return nil
}

type fitCheck struct {
	id   string
	body map[string]any
}

func fitChecks(model string) []fitCheck {
	checks := make([]fitCheck, 0, 13)
	stop := map[string]any{"stream": false, "messages": []any{map[string]any{"role": "user", "content": "依次说出：苹果、香蕉、橙子、西瓜。"}}, "stop": "香蕉", "max_tokens": 256, "reasoning_effort": "low", "model": model}
	checks = append(checks, fitCheck{"K01", stop})
	checks = append(checks, fitCheck{"K02", withFields(basicRequest(model), "top_logprobs", 5)})
	checks = append(checks, fitCheck{"K03", withFields(withFields(basicRequest(model), "logprobs", true), "top_logprobs", 21)})
	checks = append(checks, fitCheck{"K04", withFields(withFields(basicRequest(model), "max_tokens", 393216), "reasoning_effort", "low")})
	checks = append(checks, fitCheck{"K05", withFields(withFields(withFields(basicRequest(model), "temperature", 0), "max_tokens", 256), "thinking", map[string]string{"type": "disabled"})})
	checks = append(checks, fitCheck{"K06", withFields(withFields(withFields(withFields(basicRequest(model), "frequency_penalty", 2), "presence_penalty", 2), "max_tokens", 64), "reasoning_effort", "low")})
	checks = append(checks, fitCheck{"K07", map[string]any{"stream": false, "messages": []any{map[string]any{"role": "system", "name": "teacher", "content": "你是一位数学老师。"}, map[string]any{"role": "user", "name": "student_a", "content": "1+1=?"}}, "max_tokens": 256, "reasoning_effort": "low", "model": model}})
	checks = append(checks, fitCheck{"K08", map[string]any{"messages": []any{map[string]any{"role": "user", "content": "用一个词描述秋天。"}}, "temperature": 2, "top_p": 0.1, "presence_penalty": 1.5, "frequency_penalty": 1.5, "max_tokens": 1024, "reasoning_effort": "low", "model": model, "stream": true, "stream_options": map[string]any{"include_usage": true}}})
	checks = append(checks, fitCheck{"K09", map[string]any{"messages": []any{map[string]any{"role": "user", "content": "1+1=?"}}, "max_tokens": 1024, "reasoning_effort": "low", "model": model, "stream": true, "stream_options": map[string]any{"include_usage": true}}})
	checks = append(checks, fitCheck{"K10", toolRequestWithThinking(model)})
	checks = append(checks, fitCheck{"K11", stop})
	checks = append(checks, fitCheck{"K12", withFields(withFields(withFields(basicRequest(model), "logprobs", true), "top_logprobs", 5), "reasoning_effort", "low")})
	checks = append(checks, fitCheck{"K13", map[string]any{"messages": []any{map[string]any{"role": "user", "content": "1+1=?"}}, "max_tokens": 1024, "reasoning_effort": "low", "model": model, "stream": true, "stream_options": map[string]any{"include_usage": true}}})
	return checks
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

func toolRequestWithThinking(model string) map[string]any {
	request := toolRequest(model, "auto")
	request["thinking"] = map[string]string{"type": "disabled"}
	return request
}

func fitExpected(caseID string, status int, evidence map[string]any) bool {
	caseID = normalizeFitCaseID(caseID)
	validJSON := evidence["json"] == true && evidence["has_error"] != true
	switch caseID {
	case "K02":
		return expectedLogprobsValidation(status, evidence, "invalid top_logprobs and logprobs value")
	case "K03":
		return expectedLogprobsValidation(status, evidence, "invalid top_logprobs value")
	case "K01", "K11":
		return status == http.StatusOK && validJSON && evidence["has_content"] == true && evidence["finish_reason"] == "stop" && evidence["contains_stop_sequence"] != true
	case "K08", "K09", "K13":
		return status == http.StatusOK && evidence["stream"] == true && evidence["has_error"] != true && evidence["has_content"] == true && evidence["done"] == true && evidence["usage_events"] != nil
	case "K12":
		contentCount, contentOK := evidence["logprobs_content"].(int)
		reasoningCount, reasoningOK := evidence["logprobs_reasoning_content"].(int)
		contentMax, contentMaxOK := evidence["max_top_logprobs"].(int)
		reasoningMax, reasoningMaxOK := evidence["max_reasoning_top_logprobs"].(int)
		return status == http.StatusOK && validJSON && evidence["has_logprobs"] == true && contentOK && contentCount > 0 && reasoningOK && reasoningCount > 0 && contentMaxOK && contentMax <= 5 && reasoningMaxOK && reasoningMax <= 5
	case "K05":
		return status == http.StatusOK && validJSON && evidence["has_content"] == true && evidence["has_reasoning_content"] != true
	case "K10":
		return status == http.StatusOK && validJSON && evidence["has_reasoning_content"] != true &&
			(evidence["has_content"] == true || (evidence["has_tool_calls"] == true && evidence["tool_arguments_json"] == true))
	default:
		return status == http.StatusOK && validJSON && evidence["has_content"] == true
	}
}

func isInsufficientBalance(evidence map[string]any) bool {
	return strings.Contains(evidenceString(evidence, "error_message_fingerprint"), "insufficient balance") || strings.Contains(evidenceString(evidence, "error_message_fingerprint"), "quota")
}

func annotateFitEvidence(status int, evidence map[string]any) {
	if status != http.StatusOK || evidence == nil || evidence["has_error"] == true {
		return
	}
	evidence["protocol_accepted"] = true
	effectiveSuccess := evidence["has_content"] == true || (evidence["has_tool_calls"] == true && evidence["tool_arguments_json"] == true)
	evidence["effective_success"] = effectiveSuccess
	if !effectiveSuccess {
		evidence["failure_reason"] = "empty_final_content"
	}
}

func runBasic(client *http.Client, route, base, key, tier string) []result {
	p := &probe{client: client, model: "deepseek-v4-flash"}
	checks := []struct {
		id   string
		body any
	}{
		{"DS-A01", nil},
		{"DS-A02", nil},
		{"DS-A03", basicRequest(p.model)},
		{"DS-A04", withFields(basicRequest(p.model), "model", "deepseek-v4-unknown")},
		{"DS-A05", nil},
		{"DS-B01", basicRequest(p.model)},
		{"DS-B02", streamRequest(p.model, true)},
		{"DS-B03", streamRequest(p.model, false)},
		{"DS-B04", basicRequest(p.model)},
		{"DS-C01", basicRequest(p.model)},
		{"DS-C02", withFields(basicRequest(p.model), "thinking", map[string]string{"type": "enabled"})},
		{"DS-C03", withFields(basicRequest(p.model), "thinking", map[string]string{"type": "disabled"})},
		{"DS-C04", withFields(basicRequest(p.model), "reasoning_effort", "xhigh")},
		{"DS-C05", withFields(basicRequest(p.model), "reasoning_effort", "none")},
		{"DS-C06", withFields(basicRequest(p.model), "reasoning_effort", "extreme")},
		{"DS-C07", withFields(withFields(withFields(withFields(withFields(basicRequest(p.model), "thinking", map[string]string{"type": "enabled"}), "temperature", 1.2), "top_p", 0.8), "presence_penalty", 0.2), "frequency_penalty", 0.2)},
		{"DS-D03", withFields(basicRequest(p.model), "top_p", 1.5)},
		{"DS-D04", withFields(basicRequest(p.model), "top_p", 1.5)},
		{"DS-D05", withFields(basicRequest(p.model), "top_p", -0.1)},
		{"DS-C12", withFields(basicRequest(p.model), "model", "deepseek-v4-flash-max")},
		{"DS-F01", toolRequest(p.model, "auto")},
		{"DS-F02", toolRequest(p.model, "required")},
		{"DS-D08", stopRequest(p.model)},
		{"DS-D10", jsonRequest(p.model)},
		{"DS-E01", withFields(withFields(basicRequest(p.model), "logprobs", true), "top_logprobs", 5)},
		{"DS-E02", withFields(withFields(withFields(basicRequest(p.model), "thinking", map[string]string{"type": "enabled"}), "logprobs", true), "top_logprobs", 5)},
		{"DS-E03", withFields(withFields(basicRequest(p.model), "logprobs", true), "top_logprobs", 20)},
		{"DS-E04", withFields(basicRequest(p.model), "top_logprobs", 5)},
		{"DS-E07", withFields(withFields(basicRequest(p.model), "logprobs", true), "top_logprobs", 21)},
	}
	results := make([]result, 0, len(checks))
	for _, check := range checks {
		if check.id == "DS-C12" && route == "official" {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		surface := "chat-completions"
		if check.id == "DS-A01" || check.id == "DS-A02" || check.id == "DS-A05" {
			surface = "models"
		}
		result := result{CaseID: check.id, Tier: tier, Surface: surface, Route: route, Status: "fail"}
		if check.body == nil {
			requestKey := key
			if check.id == "DS-A02" {
				requestKey = ""
			}
			status, evidence, err := request(ctx, client, base, requestKey, http.MethodGet, "/models", nil)
			result.HTTP, result.Evidence = status, evidence
			if code, ok := evidence["error_code"].(string); ok {
				result.ErrorCode = code
			}
			if err == nil && expectedPass(check.id, route, status, evidence) {
				result.Status = "pass"
			}
		} else {
			body, _ := json.Marshal(check.body)
			requestKey := key
			if check.id == "DS-A03" {
				requestKey = "sk-invalid-feature-probe"
			}
			status, evidence, err := request(ctx, client, base, requestKey, http.MethodPost, "/chat/completions", body)
			result.HTTP, result.Evidence = status, evidence
			if code, ok := evidence["error_code"].(string); ok {
				result.ErrorCode = code
			}
			if err == nil && expectedPass(check.id, route, status, evidence) {
				result.Status = "pass"
			}
			if check.id == "DS-B03" && evidence["usage"] == true {
				evidence["usage_without_include_usage"] = true
			}
			if err == nil && evidence["has_error"] == true && check.id == "DS-E02" && isValidationStatus(status) && strings.Contains(evidenceString(evidence, "error_message_fingerprint"), "return_logprob") {
				result.Status = "expected_unsupported"
			}
		}
		cancel()
		results = append(results, result)
	}
	return results
}

func basicRequest(model string) map[string]any {
	return map[string]any{"model": model, "messages": []any{map[string]any{"role": "user", "content": "1+1=?"}}, "max_tokens": 64}
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
	case "DS-B02":
		return status == http.StatusOK && evidence["stream"] == true && evidence["has_error"] != true && evidence["done"] == true && evidence["has_content"] == true && evidence["usage_events"] != nil && evidence["usage_event_empty_choices"] == true
	case "DS-B03":
		return status == http.StatusOK && evidence["stream"] == true && evidence["has_error"] != true && evidence["done"] == true && evidence["has_content"] == true
	case "DS-C01":
		return status >= 200 && status < 300 && evidence["json"] == true && evidence["has_error"] != true && evidence["has_content"] == true
	case "DS-C02":
		return status >= 200 && status < 300 && evidence["json"] == true && evidence["has_error"] != true && evidence["has_content"] == true && evidence["has_reasoning_content"] == true
	case "DS-C03":
		return status >= 200 && status < 300 && evidence["json"] == true && evidence["has_error"] != true && evidence["has_content"] == true && evidence["has_reasoning_content"] != true
	case "DS-C05", "DS-C07":
		return status >= 200 && status < 300 && evidence["json"] == true && evidence["has_error"] != true && evidence["has_content"] == true
	case "DS-C04":
		return status >= 200 && status < 300 && evidence["json"] == true && evidence["has_error"] != true && evidence["has_content"] == true
	case "DS-C12":
		return route != "official" && status >= 200 && status < 300 && evidence["json"] == true && evidence["has_error"] != true && evidence["has_content"] == true
	case "DS-D08":
		return status == http.StatusOK && evidence["json"] == true && evidence["has_error"] != true && evidence["has_content"] == true && evidence["finish_reason"] == "stop" && evidence["contains_stop_sequence"] != true
	case "DS-D10":
		return status == http.StatusOK && evidence["json"] == true && evidence["has_error"] != true && evidence["has_content"] == true && evidence["content_json"] == true
	case "DS-F02":
		return expectedOfficialValidation(status, evidence)
	case "DS-E01":
		count, ok := evidence["logprobs_content"].(int)
		maxEntries, maxOK := evidence["max_top_logprobs"].(int)
		return status == http.StatusOK && evidence["json"] == true && evidence["has_error"] != true && evidence["has_logprobs"] == true && ok && count > 0 && maxOK && maxEntries <= 5
	case "DS-E02":
		contentCount, contentOK := evidence["logprobs_content"].(int)
		reasoningCount, reasoningOK := evidence["logprobs_reasoning_content"].(int)
		contentMax, contentMaxOK := evidence["max_top_logprobs"].(int)
		reasoningMax, reasoningMaxOK := evidence["max_reasoning_top_logprobs"].(int)
		return status == http.StatusOK && evidence["json"] == true && evidence["has_error"] != true && evidence["has_logprobs"] == true && contentOK && contentCount > 0 && reasoningOK && reasoningCount > 0 && contentMaxOK && contentMax <= 5 && reasoningMaxOK && reasoningMax <= 5
	case "DS-E03":
		maxEntries, maxOK := evidence["max_top_logprobs"].(int)
		return status == http.StatusOK && evidence["json"] == true && evidence["has_error"] != true && evidence["has_logprobs"] == true && maxOK && maxEntries <= 20
	case "DS-E04":
		return expectedLogprobsValidation(status, evidence, "invalid top_logprobs and logprobs value")
	case "DS-E07":
		return expectedLogprobsValidation(status, evidence, "invalid top_logprobs value")
	case "DS-F01":
		return status == http.StatusOK && evidence["json"] == true && evidence["has_error"] != true && (evidence["has_content"] == true || evidence["has_tool_calls"] == true)
	default:
		return status >= 200 && status < 300 && evidence["has_error"] != true
	}
}

func isValidationStatus(status int) bool {
	return status == http.StatusBadRequest || status == http.StatusUnprocessableEntity
}

func expectedLogprobsValidation(status int, evidence map[string]any, prefix string) bool {
	return isValidationStatus(status) && evidence["json"] == true && evidence["has_error"] == true && evidence["error_param_null"] == true && evidenceString(evidence, "error_type") == "invalid_request_error" && evidenceString(evidence, "error_code") == "invalid_request_error" && strings.HasPrefix(evidenceString(evidence, "error_message_fingerprint"), prefix)
}

func expectedOfficialValidation(status int, evidence map[string]any) bool {
	return isValidationStatus(status) && evidence["json"] == true && evidence["has_error"] == true && evidence["error_param_null"] == true && evidenceString(evidence, "error_type") == "invalid_request_error" && evidenceString(evidence, "error_code") == "invalid_request_error"
}

func evidenceString(evidence map[string]any, key string) string {
	value, _ := evidence[key].(string)
	return value
}

func request(ctx context.Context, client *http.Client, base, key, method, path string, body []byte) (int, map[string]any, error) {
	url := strings.TrimRight(base, "/") + "/" + strings.TrimLeft(path, "/")
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return 0, map[string]any{"request_error": safeError(err)}, err
	}
	if !strings.EqualFold(req.URL.Scheme, "https") {
		return 0, map[string]any{"initial_scheme": req.URL.Scheme}, fmt.Errorf("feature probe requires HTTPS")
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
		return 0, map[string]any{"transport_error": safeError(err)}, err
	}
	defer resp.Body.Close()
	data, readErr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if readErr != nil {
		return resp.StatusCode, map[string]any{"read_error": safeError(readErr)}, readErr
	}
	evidence := summarize(data, strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream"))
	evidence["initial_scheme"] = req.URL.Scheme
	evidence["tls"] = resp.TLS != nil
	if resp.Request != nil && resp.Request.URL != nil {
		evidence["final_scheme"] = resp.Request.URL.Scheme
	}
	return resp.StatusCode, evidence, nil
}

func summarize(data []byte, stream bool) map[string]any {
	evidence := map[string]any{"bytes": len(data), "stream": stream}
	if stream {
		scanner := bufio.NewScanner(bytes.NewReader(data))
		scanner.Buffer(make([]byte, 64*1024), 2<<20)
		events, done, usageEvents := 0, false, 0
		contentChunks, reasoningChunks := 0, 0
		var finishReason string
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data:") {
				events++
				payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
				if payload == "[DONE]" {
					done = true
					continue
				}
				var value map[string]any
				if json.Unmarshal([]byte(payload), &value) != nil {
					continue
				}
				if errValue, ok := value["error"].(map[string]any); ok {
					addErrorEvidence(evidence, errValue)
				}
				if usage, ok := value["usage"].(map[string]any); ok {
					usageEvents++
					addUsageEvidence(evidence, usage)
					if choices, ok := value["choices"].([]any); ok && len(choices) == 0 {
						evidence["usage_event_empty_choices"] = true
					}
				}
				if choices, ok := value["choices"].([]any); ok {
					for _, rawChoice := range choices {
						choice, ok := rawChoice.(map[string]any)
						if !ok {
							continue
						}
						if reason, ok := choice["finish_reason"].(string); ok && reason != "" {
							finishReason = reason
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
						}
					}
				}
			}
		}
		evidence["sse_events"], evidence["done"] = events, done
		evidence["content_chunks"], evidence["reasoning_chunks"] = contentChunks, reasoningChunks
		evidence["has_content"], evidence["has_reasoning_content"] = contentChunks > 0, reasoningChunks > 0
		if usageEvents > 0 {
			evidence["usage_events"] = usageEvents
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
				if reason, ok := choice["finish_reason"].(string); ok && reason != "" {
					evidence["finish_reason"] = reason
				}
				for _, field := range []string{"message", "delta"} {
					part, ok := choice[field].(map[string]any)
					if !ok {
						continue
					}
					if content, ok := part["content"].(string); ok && strings.TrimSpace(content) != "" {
						evidence["has_content"] = true
						evidence["contains_stop_sequence"] = strings.Contains(content, "banana")
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
					}
					if reasoningContent, ok := logprobs["reasoning_content"].([]any); ok {
						evidence["logprobs_reasoning_content"] = len(reasoningContent)
						evidence["max_reasoning_top_logprobs"] = maxTopLogprobs(reasoningContent)
					}
				}
				if message, ok := choice["message"].(map[string]any); ok {
					if toolCalls, ok := message["tool_calls"].([]any); ok && len(toolCalls) > 0 {
						evidence["has_tool_calls"] = true
						validArguments := true
						for _, rawToolCall := range toolCalls {
							toolCall, ok := rawToolCall.(map[string]any)
							function, functionOK := toolCall["function"].(map[string]any)
							arguments, argumentsOK := function["arguments"].(string)
							if !ok || !functionOK || !hasNonEmptyString(function["name"]) || !argumentsOK || !json.Valid([]byte(arguments)) {
								validArguments = false
							}
						}
						evidence["tool_arguments_json"] = validArguments
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

func addUsageEvidence(evidence map[string]any, usage map[string]any) {
	evidence["usage"] = true
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
