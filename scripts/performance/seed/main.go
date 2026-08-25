package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

type apiResponse struct {
	Success bool            `json:"success"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

type loginResponse struct {
	AccessToken string `json:"access_token"`
}

type pageResponse struct {
	Items []struct {
		ID int `json:"id"`
	} `json:"items"`
}

type keyResponse struct {
	Key string `json:"key"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	baseURL := strings.TrimRight(envOrDefault("BASE_URL", "http://127.0.0.1:3001"), "/")
	adminUser := envOrDefault("ADMIN_USER", "perf-admin")
	adminPassword := os.Getenv("ADMIN_PASSWORD")
	if adminPassword == "" {
		return errors.New("ADMIN_PASSWORD is required")
	}
	upstreamURL := envOrDefault("UPSTREAM_URL", "http://mock-upstream:8080")
	perfToken := envOrDefault("PERF_TOKEN_NAME", "perf-token")

	client := &http.Client{}
	if err := ensureSetup(client, baseURL, adminUser, adminPassword); err != nil {
		return err
	}
	accessToken, err := login(client, baseURL, adminUser, adminPassword)
	if err != nil {
		return err
	}
	// The performance monitor middleware reads host-level /proc/stat inside the
	// container, so load-generated host CPU trips its 90% threshold and turns
	// every relay request into a 503. Load-test stacks must keep it disabled.
	if err := disablePerformanceMonitor(client, baseURL, accessToken); err != nil {
		return err
	}
	if err := createChannel(client, baseURL, accessToken, upstreamURL); err != nil {
		return err
	}
	token, err := createToken(client, baseURL, accessToken, perfToken)
	if err != nil {
		return err
	}
	// stdout is consumed by the runner; do not add diagnostics here.
	fmt.Print(token)
	return nil
}

func ensureSetup(client *http.Client, baseURL, username, password string) error {
	response, err := requestJSON(client, http.MethodGet, baseURL+"/api/setup", "", nil)
	if err != nil {
		return err
	}
	var setup struct {
		Status bool `json:"status"`
	}
	if err := json.Unmarshal(response.Data, &setup); err != nil {
		return fmt.Errorf("decode setup response: %w", err)
	}
	if setup.Status {
		return nil
	}
	_, err = requestJSON(client, http.MethodPost, baseURL+"/api/setup", "", map[string]any{
		"username":           username,
		"password":           password,
		"confirmPassword":    password,
		"SelfUseModeEnabled": true,
	})
	return err
}

func login(client *http.Client, baseURL, username, password string) (string, error) {
	response, err := requestJSON(client, http.MethodPost, baseURL+"/api/user/login", "", map[string]string{
		"username": username,
		"password": password,
	})
	if err != nil {
		return "", err
	}
	var loginData loginResponse
	if err := json.Unmarshal(response.Data, &loginData); err != nil {
		return "", fmt.Errorf("decode login response: %w", err)
	}
	if loginData.AccessToken == "" {
		return "", errors.New("login response did not contain an access token")
	}
	return loginData.AccessToken, nil
}

func disablePerformanceMonitor(client *http.Client, baseURL, accessToken string) error {
	_, err := requestJSON(client, http.MethodPut, baseURL+"/api/option/", accessToken, map[string]any{
		"key":   "performance_setting.monitor_enabled",
		"value": false,
	})
	return err
}

func createChannel(client *http.Client, baseURL, accessToken, upstreamURL string) error {
	_, err := requestJSON(client, http.MethodPost, baseURL+"/api/channel/", accessToken, map[string]any{
		"mode": "single",
		"channel": map[string]any{
			"name":     "perf-mock-upstream",
			"type":     1,
			"key":      "perf-upstream-key",
			"status":   1,
			"weight":   100,
			"priority": 0,
			"base_url": upstreamURL,
			"models":   "gpt-4o-mini",
			"group":    "default",
			"auto_ban": 0,
		},
	})
	return err
}

func createToken(client *http.Client, baseURL, accessToken, tokenName string) (string, error) {
	_, err := requestJSON(client, http.MethodPost, baseURL+"/api/token/", accessToken, map[string]any{
		"name":                 tokenName,
		"expired_time":         -1,
		"remain_quota":         1000000000,
		"unlimited_quota":      true,
		"model_limits_enabled": false,
		"group":                "default",
	})
	if err != nil {
		return "", err
	}
	page, err := requestJSON(client, http.MethodGet, baseURL+"/api/token/?size=100", accessToken, nil)
	if err != nil {
		return "", err
	}
	var pageData pageResponse
	if err := json.Unmarshal(page.Data, &pageData); err != nil {
		return "", fmt.Errorf("decode token page: %w", err)
	}
	if len(pageData.Items) == 0 {
		return "", errors.New("token creation returned no token")
	}
	key, err := requestJSON(client, http.MethodPost, fmt.Sprintf("%s/api/token/%d/key", baseURL, pageData.Items[0].ID), accessToken, nil)
	if err != nil {
		return "", err
	}
	var keyData keyResponse
	if err := json.Unmarshal(key.Data, &keyData); err != nil {
		return "", fmt.Errorf("decode token key: %w", err)
	}
	if keyData.Key == "" {
		return "", errors.New("token key response was empty")
	}
	return keyData.Key, nil
}

func requestJSON(client *http.Client, method, url, accessToken string, body any) (apiResponse, error) {
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return apiResponse{}, err
		}
		payload = bytes.NewReader(encoded)
	}
	req, err := http.NewRequest(method, url, payload)
	if err != nil {
		return apiResponse{}, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+accessToken)
	}
	res, err := client.Do(req)
	if err != nil {
		return apiResponse{}, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		return apiResponse{}, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return apiResponse{}, fmt.Errorf("%s %s returned HTTP %d: %s", method, url, res.StatusCode, strings.TrimSpace(string(data)))
	}
	var response apiResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return apiResponse{}, fmt.Errorf("decode %s response: %w", url, err)
	}
	if !response.Success {
		return apiResponse{}, fmt.Errorf("%s failed: %s", url, response.Message)
	}
	return response, nil
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
