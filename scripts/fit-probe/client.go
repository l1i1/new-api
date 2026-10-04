package main

// HTTP surface: admin reads, relay probes and the single write path.
//
// Nothing in this file logs a request body or a credential. The admin and relay
// tokens are read from the environment once and held in memory; only a boolean
// "credential present" fact is ever printed.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// channelInfo is the slice of GET /api/channel/ this tool reads. Keys are never
// requested and never decoded (the endpoint omits them anyway).
type channelInfo struct {
	Id      int    `json:"id"`
	Name    string `json:"name"`
	Type    int    `json:"type"`
	Status  int    `json:"status"`
	Group   string `json:"group"`
	Models  string `json:"models"`
	BaseURL string `json:"base_url"`
}

// modelList splits the comma-separated channel model list.
func (c channelInfo) modelList() []string {
	parts := strings.Split(c.Models, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// fitPolicyView is the live-policy half of GET /api/fit-policy that the report
// binding needs.
type fitPolicyView struct {
	Source            string `json:"source"`
	EffectiveDocument string `json:"effective_document"`
	Live              struct {
		Installed bool   `json:"installed"`
		Version   int    `json:"version"`
		Enabled   bool   `json:"enabled"`
		Shadow    bool   `json:"shadow"`
		Baseline  string `json:"baseline"`
		Hash      string `json:"hash"`
	} `json:"live"`
}

func (v fitPolicyView) binding() policyBinding {
	return policyBinding{Version: v.Live.Version, Hash: v.Live.Hash, Baseline: v.Live.Baseline}
}

// client is a thin JSON HTTP client for the gateway.
type client struct {
	http       *http.Client
	adminBase  string
	adminToken string
	relayBase  string
	relayToken string
}

func newClient(timeout time.Duration, adminBase, adminToken, relayBase, relayToken string) *client {
	return &client{
		http: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(req *http.Request, _ []*http.Request) error {
				return fmt.Errorf("fit-probe refuses redirects")
			},
		},
		adminBase:  strings.TrimRight(adminBase, "/"),
		adminToken: adminToken,
		relayBase:  strings.TrimRight(relayBase, "/"),
		relayToken: relayToken,
	}
}

func (c *client) do(ctx context.Context, method, endpoint, token string, body []byte) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	// Bound the read: a probe needs a fingerprint, not the whole body. 512 KiB
	// is far above any legitimate chat completion envelope.
	payload, err := io.ReadAll(io.LimitReader(response.Body, 512*1024))
	if err != nil {
		return response.StatusCode, nil, err
	}
	return response.StatusCode, payload, nil
}

// listChannels pages through GET /api/channel/ until total is collected.
// page_size is capped at 100 by the server (common/page_info.go:100).
func (c *client) listChannels(ctx context.Context, status string) ([]channelInfo, error) {
	const pageSize = 100
	channels := make([]channelInfo, 0)
	for page := 1; page <= 100; page++ {
		endpoint := fmt.Sprintf("%s/api/channel/?p=%d&page_size=%d&id_sort=true", c.adminBase, page, pageSize)
		if status != "" {
			endpoint += "&status=" + url.QueryEscape(status)
		}
		code, payload, err := c.do(ctx, http.MethodGet, endpoint, c.adminToken, nil)
		if err != nil {
			return nil, fmt.Errorf("GET /api/channel/: %w", err)
		}
		if code != http.StatusOK {
			return nil, fmt.Errorf("GET /api/channel/ answered %d", code)
		}
		var envelope struct {
			Success bool `json:"success"`
			Data    struct {
				Items []channelInfo `json:"items"`
				Total int           `json:"total"`
			} `json:"data"`
		}
		if err := json.Unmarshal(payload, &envelope); err != nil {
			return nil, fmt.Errorf("GET /api/channel/: unreadable envelope: %w", err)
		}
		if !envelope.Success {
			return nil, fmt.Errorf("GET /api/channel/: success=false")
		}
		channels = append(channels, envelope.Data.Items...)
		if len(channels) >= envelope.Data.Total || len(envelope.Data.Items) == 0 {
			return channels, nil
		}
	}
	return channels, nil
}

// fitPolicy reads GET /api/fit-policy.
func (c *client) fitPolicy(ctx context.Context) (fitPolicyView, error) {
	var view fitPolicyView
	code, payload, err := c.do(ctx, http.MethodGet, c.adminBase+"/api/fit-policy", c.adminToken, nil)
	if err != nil {
		return view, fmt.Errorf("GET /api/fit-policy: %w", err)
	}
	if code != http.StatusOK {
		return view, fmt.Errorf("GET /api/fit-policy answered %d", code)
	}
	var envelope struct {
		Success bool          `json:"success"`
		Data    fitPolicyView `json:"data"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return view, fmt.Errorf("GET /api/fit-policy: unreadable envelope: %w", err)
	}
	if !envelope.Success {
		return view, fmt.Errorf("GET /api/fit-policy: success=false")
	}
	return envelope.Data, nil
}

// probeRelay sends one minimal request pinned to one channel.
//
// The pin is the documented admin token form: the relay splits the bearer key
// on "-" and treats the second segment as a specific channel id
// (middleware/auth.go:544-556). It requires the token's user to be an admin, and
// it short-circuits the fit policy (middleware/fitpolicy_shadow.go:86-96), so
// the probe measures the channel, not the policy.
func (c *client) probeRelay(ctx context.Context, channelID int, path string, body []byte) (int, []byte, error) {
	if c.relayToken == "" {
		return 0, nil, fmt.Errorf("relay credential is not set")
	}
	pinned := c.relayToken + "-" + strconv.Itoa(channelID)
	return c.do(ctx, http.MethodPost, c.relayBase+path, pinned, body)
}

// probeOfficial sends the identical minimal request to a real official endpoint.
func (c *client) probeOfficial(ctx context.Context, base, key, path string, body []byte) (int, []byte, error) {
	endpoint := strings.TrimRight(base, "/") + officialPath(base, path)
	return c.do(ctx, http.MethodPost, endpoint, key, body)
}

// officialPath appends the chat path unless the caller already supplied it.
func officialPath(base, path string) string {
	if strings.HasSuffix(strings.TrimRight(base, "/"), "/chat/completions") {
		return ""
	}
	return path
}

// postReport is the ONLY write in this program.
func (c *client) postReport(ctx context.Context, body []byte) (int, []byte, error) {
	return c.do(ctx, http.MethodPost, c.adminBase+"/api/fit-capability/report", c.adminToken, body)
}
