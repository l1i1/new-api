package middleware

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// request_capture and admin audit both wrap gin's writer on live request
// paths, and the write-deadline chain (http.ResponseController -> Unwrap
// hops -> gin's responseWriter -> net/http writer) must survive both
// wrappers (B3). A wrapper that forgets Unwrap turns
// helper.ExtendWriteDeadline into a silent no-op.
func TestCaptureWriterSetWriteDeadlineReachesConn(t *testing.T) {
	gin.SetMode(gin.TestMode)
	errCh := make(chan error, 1)
	r := gin.New()
	r.GET("/", func(c *gin.Context) {
		c.Writer = &captureResponseWriter{ResponseWriter: c.Writer, limit: 1024}
		errCh <- http.NewResponseController(c.Writer).SetWriteDeadline(time.Now().Add(time.Second))
		c.String(http.StatusOK, "ok")
	})
	srv := httptest.NewServer(r)
	defer srv.Close()
	resp, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("SetWriteDeadline through captureResponseWriter failed: %v", err)
		}
	default:
		t.Fatal("handler did not report a deadline result")
	}
}

func TestAuditWriterSetWriteDeadlineReachesConn(t *testing.T) {
	gin.SetMode(gin.TestMode)
	errCh := make(chan error, 1)
	r := gin.New()
	r.GET("/", func(c *gin.Context) {
		c.Writer = &auditResponseWriter{ResponseWriter: c.Writer, body: bytes.NewBuffer(nil), maxSize: 64 * 1024}
		errCh <- http.NewResponseController(c.Writer).SetWriteDeadline(time.Now().Add(time.Second))
		c.String(http.StatusOK, "ok")
	})
	srv := httptest.NewServer(r)
	defer srv.Close()
	resp, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("SetWriteDeadline through auditResponseWriter failed: %v", err)
		}
	default:
		t.Fatal("handler did not report a deadline result")
	}
}
