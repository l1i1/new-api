package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/setting/model_setting"

	"github.com/gin-gonic/gin"
)

func CloseResponseBodyGracefully(httpResponse *http.Response) {
	if httpResponse == nil || httpResponse.Body == nil {
		return
	}
	err := httpResponse.Body.Close()
	if err != nil {
		common.SysError("failed to close response body: " + err.Error())
	}
}

// ShouldCopyUpstreamHeader checks whether a given upstream response header
// should be copied to the client response. It returns false for Content-Length
// (managed separately) and X-Oneapi-Request-Id (to preserve the local instance
// ID). When the upstream header is X-Oneapi-Request-Id, the value is captured
// into the Gin context for later logging.
func ShouldCopyUpstreamHeader(c *gin.Context, k string, v []string) bool {
	if strings.EqualFold(k, "Content-Length") {
		return false
	}
	if strings.EqualFold(k, common.RequestIdKey) {
		if c != nil && len(v) > 0 {
			c.Set(common.UpstreamRequestIdKey, v[0])
		}
		return false
	}
	return true
}

func IOCopyBytesGracefully(c *gin.Context, src *http.Response, data []byte) error {
	if c == nil || c.Writer == nil {
		return fmt.Errorf("context or writer is nil")
	}

	// Shared response writer for every relay format: when the global
	// "hide upstream model name" setting is on and a channel mapped the
	// request model to a different upstream id, rewrite the top-level
	// "model" field before the body reaches the client. The relay info
	// pointer resolves the origin/upstream names after ModelMappedHelper
	// ran; the rewrite re-encodes the JSON (field order is not a contract).
	if shouldMaskUpstreamModelName(c, data) {
		if patched, ok := maskUpstreamModelNameBytes(data, c); ok {
			data = patched
		}
	}

	body := io.NopCloser(bytes.NewBuffer(data))

	// We shouldn't set the header before we parse the response body, because the parse part may fail.
	// And then we will have to send an error response, but in this case, the header has already been set.
	// So the httpClient will be confused by the response.
	// For example, Postman will report error, and we cannot check the response at all.
	if src != nil {
		for k, v := range src.Header {
			if !ShouldCopyUpstreamHeader(c, k, v) {
				continue
			}
			c.Writer.Header().Set(k, v[0])
		}
	}

	// set Content-Length header manually BEFORE calling WriteHeader
	c.Writer.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))

	// Write header with status code (this sends the headers)
	if src != nil {
		c.Writer.WriteHeader(src.StatusCode)
	} else {
		c.Writer.WriteHeader(http.StatusOK)
	}

	_, err := io.Copy(c.Writer, body)
	if err != nil {
		logger.LogError(c, fmt.Sprintf("failed to copy response body: %s", err.Error()))
		return err
	}
	c.Writer.Flush()
	return nil
}

// shouldMaskUpstreamModelName reports whether the caller should rewrite the
// client-facing model field: the global hide-upstream-model setting must be
// on and the active relay must have a channel-mapped model that differs from
// the origin request model. The relay info pointer carries the resolved names.
func shouldMaskUpstreamModelName(c *gin.Context, data []byte) bool {
	if !model_setting.GetGlobalSettings().MaskUpstreamModelName {
		return false
	}
	info, ok := relayInfoFromContext(c)
	if !ok || info == nil {
		return false
	}
	origin := strings.TrimSpace(info.OriginModelName)
	upstream := strings.TrimSpace(info.GetUpstreamModelName())
	if origin == "" || upstream == "" || origin == upstream {
		return false
	}
	// Skip when the body's top-level model already equals the origin: the
	// format handler (e.g. OpenAI chat) may have done a byte-level rewrite
	// already, and re-parsing here would re-encode key order for no gain.
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(data, &payload); err == nil {
		if raw, ok := payload["model"]; ok {
			var current string
			if json.Unmarshal(raw, &current) == nil && current == origin {
				return false
			}
		}
	}
	return bytes.Contains(data, []byte(`"model"`))
}

func relayInfoFromContext(c *gin.Context) (*relaycommon.RelayInfo, bool) {
	anyInfo, ok := c.Get(string(constant.ContextKeyRelayInfoPtr))
	if !ok {
		return nil, false
	}
	info, ok := anyInfo.(*relaycommon.RelayInfo)
	return info, ok
}

// maskUpstreamModelNameBytes rewrites only the top-level "model" field of a
// JSON body with a byte-level splice, preserving every other byte (including
// key order). It is a format-agnostic helper for non-stream response bodies;
// streaming SSE events are handled at each format's writer.
func maskUpstreamModelNameBytes(data []byte, c *gin.Context) ([]byte, bool) {
	info, ok := relayInfoFromContext(c)
	if !ok || info == nil {
		return data, false
	}
	origin := strings.TrimSpace(info.OriginModelName)
	if origin == "" {
		return data, false
	}
	replacement, err := json.Marshal(origin)
	if err != nil {
		return data, false
	}
	return replaceTopLevelJSONValue(data, "model", replacement)
}

// replaceTopLevelJSONValue replaces the value of a top-level key inside a
// JSON object and re-encodes the whole document. It is intentionally simple:
// response-body field order is not part of any client contract, and using the
// standard library avoids hand-rolled JSON scanning bugs.
func replaceTopLevelJSONValue(data []byte, key string, value []byte) ([]byte, bool) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(data, &payload); err != nil {
		return data, false
	}
	if _, ok := payload[key]; !ok {
		return data, false
	}
	payload[key] = value
	encoded, err := json.Marshal(payload)
	if err != nil {
		return data, false
	}
	return encoded, true
}
