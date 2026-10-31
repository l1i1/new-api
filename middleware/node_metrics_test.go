package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/nodemetrics"

	"github.com/gin-gonic/gin"
)

func nodeMetricsNow() nodemetrics.Traffic {
	return nodemetrics.Snapshot(time.Now())
}

// TestNodeMetricsRecordsCachedBodyAndWriterSizes proves the middleware records
// the cached body storage size (not the stale ContentLength) after the handler
// and the number of bytes written to the client.
func TestNodeMetricsRecordsCachedBodyAndWriterSizes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const payload = `{"model":"gpt-4o","messages":[]}`
	const responseBody = "hello world"

	var storage common.BodyStorage
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		var err error
		storage, err = common.CreateBodyStorage([]byte(payload))
		if err != nil {
			t.Fatalf("create body storage: %v", err)
		}
		c.Set(common.KeyBodyStorage, storage)
		c.Next()
	})
	engine.Use(NodeMetrics())
	engine.POST("/v1/chat/completions", func(c *gin.Context) {
		c.String(http.StatusOK, responseBody)
	})
	defer func() {
		if storage != nil {
			_ = storage.Close()
		}
	}()

	before := nodeMetricsNow()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(payload))
	// A stale/compressed ContentLength must not win over the decompressed cache.
	req.ContentLength = 1
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)
	after := nodeMetricsNow()

	if got := recorder.Body.String(); got != responseBody {
		t.Fatalf("handler response = %q, want %q", got, responseBody)
	}
	if delta := after.RequestsTotal - before.RequestsTotal; delta != 1 {
		t.Errorf("requests_total delta = %d, want 1", delta)
	}
	if delta := after.RequestBytesTotal - before.RequestBytesTotal; delta != int64(len(payload)) {
		t.Errorf("request_bytes_total delta = %d, want %d (cached storage size)", delta, len(payload))
	}
	if delta := after.ResponseBytesTotal - before.ResponseBytesTotal; delta != int64(len(responseBody)) {
		t.Errorf("response_bytes_total delta = %d, want %d", delta, len(responseBody))
	}
	if delta := after.RPM - before.RPM; delta != 1 {
		t.Errorf("window rpm delta = %d, want 1", delta)
	}
}

// TestNodeMetricsFallsBackToContentLength covers a body that was never cached
// into a body storage: ContentLength is the only size available.
func TestNodeMetricsFallsBackToContentLength(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const payload = "abcdef"

	engine := gin.New()
	engine.Use(NodeMetrics())
	engine.POST("/v1/embeddings", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	before := nodeMetricsNow()
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(payload))
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)
	after := nodeMetricsNow()

	if delta := after.RequestsTotal - before.RequestsTotal; delta != 1 {
		t.Errorf("requests_total delta = %d, want 1", delta)
	}
	if delta := after.RequestBytesTotal - before.RequestBytesTotal; delta != int64(len(payload)) {
		t.Errorf("request_bytes_total delta = %d, want %d (ContentLength fallback)", delta, len(payload))
	}
	if delta := after.ResponseBytesTotal - before.ResponseBytesTotal; delta != 0 {
		t.Errorf("response_bytes_total delta = %d, want 0 for an empty response", delta)
	}
}

// TestNodeMetricsBodylessRequest proves a request without a body or a cached
// storage is counted as one request with zero bytes (and never double counted).
func TestNodeMetricsBodylessRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	engine.Use(NodeMetrics())
	engine.GET("/v1/models", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	before := nodeMetricsNow()
	engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	after := nodeMetricsNow()

	if delta := after.RequestsTotal - before.RequestsTotal; delta != 1 {
		t.Errorf("requests_total delta = %d, want 1", delta)
	}
	if delta := after.RequestBytesTotal - before.RequestBytesTotal; delta != 0 {
		t.Errorf("request_bytes_total delta = %d, want 0", delta)
	}
	if delta := after.ResponseBytesTotal - before.ResponseBytesTotal; delta != 2 {
		t.Errorf("response_bytes_total delta = %d, want 2", delta)
	}
}

// TestNodeMetricsNilContextDoesNotPanic covers a context without a request or a
// response writer (c.Writer.Size() unavailable): only the request is counted.
func TestNodeMetricsNilContextDoesNotPanic(t *testing.T) {
	before := nodeMetricsNow()
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				t.Fatalf("NodeMetrics panicked without a request/writer: %v", recovered)
			}
		}()
		NodeMetrics()(&gin.Context{})
	}()
	after := nodeMetricsNow()

	if delta := after.RequestsTotal - before.RequestsTotal; delta != 1 {
		t.Errorf("requests_total delta = %d, want 1", delta)
	}
	if delta := after.RequestBytesTotal - before.RequestBytesTotal; delta != 0 {
		t.Errorf("request_bytes_total delta = %d, want 0", delta)
	}
	if delta := after.ResponseBytesTotal - before.ResponseBytesTotal; delta != 0 {
		t.Errorf("response_bytes_total delta = %d, want 0", delta)
	}
}
