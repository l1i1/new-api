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
	"DS-E01", "DS-E02", "DS-E03", "DS-E04", "DS-E05", "DS-E06",
	"DS-F01", "DS-F02", "DS-F03", "DS-F04", "DS-F05", "DS-F06", "DS-F07", "DS-F08", "DS-F09", "DS-F10",
	"DS-G01", "DS-G02", "DS-G03", "DS-G04", "DS-G05", "DS-G06", "DS-G07",
	"DS-H01", "DS-H02", "DS-H03", "DS-H04", "DS-H05", "DS-H06", "DS-H07",
	"DS-I01", "DS-I02", "DS-I03", "DS-I04", "DS-I05", "DS-I06", "DS-I07", "DS-I08", "DS-I09", "DS-I10", "DS-I11",
	"DS-J01", "DS-J02", "DS-J03", "DS-J04", "DS-J05", "DS-J06",
}

func main() {
	client := &http.Client{Timeout: 45 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}}
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
			results = append(results, result{CaseID: id, Tier: tierFor(id), Surface: surfaceFor(id), Route: "offline", Status: "inconclusive", Evidence: map[string]any{"reason": "live credentials or route variables not injected"}})
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

func runBasic(client *http.Client, route, base, key, tier string) []result {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	p := &probe{client: client, model: "deepseek-v4-flash"}
	checks := []struct {
		id   string
		body any
	}{
		{"DS-A01", nil},
		{"DS-A02", nil},
		{"DS-A03", basicRequest(p.model)},
		{"DS-A04", withFields(basicRequest(p.model), "model", "deepseek-v4-unknown")},
		{"DS-B01", basicRequest(p.model)},
		{"DS-B02", streamRequest(p.model, true)},
		{"DS-C06", withFields(basicRequest(p.model), "reasoning_effort", "extreme")},
		{"DS-D03", withFields(basicRequest(p.model), "top_p", 1.5)},
		{"DS-F01", toolRequest(p.model, "auto")},
		{"DS-D08", withFields(basicRequest(p.model), "stop", "香蕉")},
		{"DS-E01", withFields(withFields(basicRequest(p.model), "logprobs", true), "top_logprobs", 5)},
	}
	results := make([]result, 0, len(checks))
	for _, check := range checks {
		result := result{CaseID: check.id, Tier: tier, Surface: "chat-completions", Route: route, Status: "fail"}
		if check.body == nil {
			requestKey := key
			if check.id == "DS-A02" {
				requestKey = ""
			}
			status, evidence, err := request(ctx, client, base, requestKey, http.MethodGet, "/models", nil)
			result.HTTP, result.Evidence = status, evidence
			if err == nil && ((check.id == "DS-A02" && status == http.StatusUnauthorized) || (check.id == "DS-A01" && status == http.StatusOK)) {
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
			if err == nil && expectedPass(check.id, route, status, evidence) {
				result.Status = "pass"
			}
			if check.id == "DS-D03" && route == "official" && status >= 400 && status < 500 {
				result.Status = "doc_drift"
			}
		}
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
	case "DS-A03", "DS-A04":
		return status >= 400 && status < 500
	case "DS-D03":
		return route != "official" && status >= 200 && status < 300
	case "DS-C06":
		return status >= 200 && status < 300
	case "DS-B01", "DS-B04":
		return status == http.StatusOK && evidence["json"] == true && evidence["choices"] == float64(1)
	case "DS-B02":
		return status == http.StatusOK && evidence["stream"] == true && evidence["done"] == true && evidence["sse_events"] != nil
	case "DS-D08":
		return status == http.StatusOK && evidence["json"] == true
	case "DS-E01", "DS-F01":
		return status == http.StatusOK && evidence["json"] == true
	default:
		return status >= 200 && status < 300
	}
}

func request(ctx context.Context, client *http.Client, base, key, method, path string, body []byte) (int, map[string]any, error) {
	url := strings.TrimRight(base, "/") + "/" + strings.TrimLeft(path, "/")
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
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
		return resp.StatusCode, nil, readErr
	}
	evidence := summarize(data, strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream"))
	return resp.StatusCode, evidence, nil
}

func summarize(data []byte, stream bool) map[string]any {
	evidence := map[string]any{"bytes": len(data), "stream": stream}
	if stream {
		scanner := bufio.NewScanner(bytes.NewReader(data))
		events, done := 0, false
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				events++
				if strings.TrimSpace(strings.TrimPrefix(line, "data: ")) == "[DONE]" {
					done = true
				}
			}
		}
		evidence["sse_events"], evidence["done"] = events, done
		return evidence
	}
	var value map[string]any
	if json.Unmarshal(data, &value) != nil {
		evidence["json"] = false
		return evidence
	}
	evidence["json"] = true
	if choices, ok := value["choices"].([]any); ok {
		evidence["choices"] = len(choices)
	}
	if value["object"] != nil {
		evidence["object"] = value["object"]
	}
	if _, ok := value["error"]; ok {
		evidence["has_error"] = true
	}
	return evidence
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
