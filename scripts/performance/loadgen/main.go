package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type result struct {
	DurationSeconds float64          `json:"duration_seconds"`
	Requests        int64            `json:"requests"`
	Successes       int64            `json:"successes"`
	Errors          int64            `json:"errors"`
	RPS             float64          `json:"rps"`
	P50Milliseconds float64          `json:"p50_ms"`
	P95Milliseconds float64          `json:"p95_ms"`
	P99Milliseconds float64          `json:"p99_ms"`
	StatusCounts    map[string]int64 `json:"status_counts"`
}

type workerResult struct {
	latencies []int64
	statuses  map[int]int64
	successes int64
	errors    int64
}

func main() {
	url := flag.String("url", "http://127.0.0.1:3001/v1/chat/completions", "request URL")
	token := flag.String("token", "", "relay token")
	duration := flag.Duration("duration", 60*time.Second, "measurement duration")
	concurrency := flag.Int("concurrency", 64, "number of concurrent workers")
	stream := flag.Bool("stream", false, "request an SSE response")
	models := flag.Bool("models", false, "request and validate an OpenAI model list")
	payloadBytes := flag.Int("payload-bytes", 128, "approximate user content size")
	flag.Parse()

	if *token == "" {
		fatal("-token is required")
	}
	if *duration <= 0 || *concurrency <= 0 || *payloadBytes < 0 {
		fatal("duration, concurrency, and payload-bytes must be positive or zero as appropriate")
	}

	method := http.MethodPost
	body := requestBody(*stream, *payloadBytes)
	if *models {
		method = http.MethodGet
		body = nil
	}
	transport := &http.Transport{
		MaxIdleConns:        *concurrency * 2,
		MaxIdleConnsPerHost: *concurrency * 2,
		IdleConnTimeout:     90 * time.Second,
	}
	client := &http.Client{Transport: transport}
	client.Timeout = 30 * time.Second
	deadline := time.Now().Add(*duration)
	workers := make([]workerResult, *concurrency)
	var totalRequests atomic.Int64
	var waitGroup sync.WaitGroup
	waitGroup.Add(*concurrency)
	for worker := range *concurrency {
		go func(index int) {
			defer waitGroup.Done()
			workers[index] = runWorker(client, method, *url, *token, body, *stream, *models, deadline, &totalRequests)
		}(worker)
	}
	waitGroup.Wait()
	transport.CloseIdleConnections()

	latencies := make([]int64, 0, totalRequests.Load())
	statusCounts := make(map[string]int64)
	var successes int64
	var errorsCount int64
	for _, worker := range workers {
		latencies = append(latencies, worker.latencies...)
		successes += worker.successes
		errorsCount += worker.errors
		for status, count := range worker.statuses {
			statusCounts[fmt.Sprintf("%d", status)] += count
		}
	}
	requests := totalRequests.Load()
	seconds := duration.Seconds()
	output := result{
		DurationSeconds: seconds,
		Requests:        requests,
		Successes:       successes,
		Errors:          errorsCount,
		RPS:             float64(successes) / seconds,
		P50Milliseconds: percentileMilliseconds(latencies, 0.50),
		P95Milliseconds: percentileMilliseconds(latencies, 0.95),
		P99Milliseconds: percentileMilliseconds(latencies, 0.99),
		StatusCounts:    statusCounts,
	}
	encoded, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		fatal(err.Error())
	}
	fmt.Println(string(encoded))
}

func runWorker(client *http.Client, method, url, token string, body []byte, stream, models bool, deadline time.Time, totalRequests *atomic.Int64) workerResult {
	result := workerResult{statuses: make(map[int]int64)}
	for time.Now().Before(deadline) {
		req, err := http.NewRequest(method, url, bytes.NewReader(body))
		if err != nil {
			result.statuses[0]++
			continue
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if len(body) > 0 {
			req.Header.Set("Content-Type", "application/json")
		}
		if stream {
			req.Header.Set("Accept", "text/event-stream")
		}
		started := time.Now()
		response, err := client.Do(req)
		if err != nil {
			result.statuses[0]++
			result.errors++
			totalRequests.Add(1)
			continue
		}
		responseBody, readErr := io.ReadAll(response.Body)
		status := response.StatusCode
		_ = response.Body.Close()
		if readErr != nil {
			result.statuses[0]++
			result.errors++
		} else {
			result.statuses[status]++
			validOutput := hasEffectiveOutput(responseBody, stream)
			if models {
				validOutput = hasModelList(responseBody)
			}
			if status >= http.StatusOK && status < http.StatusMultipleChoices && validOutput {
				result.successes++
			} else {
				result.errors++
			}
		}
		result.latencies = append(result.latencies, time.Since(started).Nanoseconds())
		totalRequests.Add(1)
	}
	return result
}

func hasModelList(body []byte) bool {
	var response struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &response) != nil {
		return false
	}
	for _, model := range response.Data {
		if strings.TrimSpace(model.ID) != "" {
			return true
		}
	}
	return false
}

func hasEffectiveOutput(body []byte, stream bool) bool {
	if stream {
		return hasEffectiveStreamOutput(body)
	}
	var response struct {
		Choices []struct {
			Text    string `json:"text"`
			Message struct {
				Content   any `json:"content"`
				ToolCalls []struct {
					Type     string `json:"type"`
					Function struct {
						Name string `json:"name"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return false
	}
	for _, choice := range response.Choices {
		if strings.TrimSpace(choice.Text) != "" || hasNonEmptyContent(choice.Message.Content) {
			return true
		}
		for _, toolCall := range choice.Message.ToolCalls {
			if toolCall.Type == "function" && strings.TrimSpace(toolCall.Function.Name) != "" {
				return true
			}
		}
	}
	return false
}

func hasEffectiveStreamOutput(body []byte) bool {
	hasOutput := false
	hasDone := false
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			hasDone = true
			continue
		}
		var event struct {
			Choices []struct {
				Delta struct {
					Content   any `json:"content"`
					ToolCalls []struct {
						Type     string `json:"type"`
						Function struct {
							Name string `json:"name"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(data), &event) != nil {
			continue
		}
		for _, choice := range event.Choices {
			if hasNonEmptyContent(choice.Delta.Content) {
				hasOutput = true
			}
			for _, toolCall := range choice.Delta.ToolCalls {
				if toolCall.Type == "function" && strings.TrimSpace(toolCall.Function.Name) != "" {
					hasOutput = true
				}
			}
		}
	}
	return hasOutput && hasDone
}

func hasNonEmptyContent(content any) bool {
	switch value := content.(type) {
	case string:
		return strings.TrimSpace(value) != ""
	case []any:
		for _, item := range value {
			if hasNonEmptyContent(item) {
				return true
			}
		}
	case map[string]any:
		for _, key := range []string{"text", "content"} {
			if hasNonEmptyContent(value[key]) {
				return true
			}
		}
	}
	return false
}

func requestBody(stream bool, payloadBytes int) []byte {
	content := strings.Repeat("x", payloadBytes)
	payload := map[string]any{
		"model": "gpt-4o-mini",
		"messages": []map[string]string{{
			"role":    "user",
			"content": content,
		}},
		"stream": stream,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		fatal(err.Error())
	}
	return encoded
}

func percentileMilliseconds(values []int64, percentile float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	index := int(float64(len(values)-1) * percentile)
	return float64(values[index]) / float64(time.Millisecond)
}

func fatal(message string) {
	fmt.Println(message)
	panic(message)
}
