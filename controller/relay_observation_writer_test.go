package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// The relay path installs relayObservationWriter around gin's writer for the
// whole request, and relay/helper.ExtendWriteDeadline drives the connection
// write deadline through http.ResponseController on every stream write. That
// only works if every wrapper in the chain implements Unwrap() down to gin's
// responseWriter, which unwraps to the net/http writer that owns the deadline
// (gin v1.9.1 ships that Unwrap). These tests pin the whole chain so a gin
// upgrade or a new wrapper cannot break the deadline silently again (B3).
func TestObservationWriterSetWriteDeadlineReachesConn(t *testing.T) {
	gin.SetMode(gin.TestMode)
	errCh := make(chan error, 1)
	r := gin.New()
	r.GET("/", func(c *gin.Context) {
		original := c.Writer
		c.Writer = &relayObservationWriter{ResponseWriter: original, ctx: c}
		defer func() { c.Writer = original }()
		errCh <- http.NewResponseController(c.Writer).SetWriteDeadline(time.Now().Add(time.Second))
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
			t.Fatalf("SetWriteDeadline through relayObservationWriter failed: %v", err)
		}
	default:
		t.Fatal("handler did not report a deadline result")
	}
}

// TestGinWriterSetWriteDeadlineBaseline is the control for the chain above:
// gin's own writer must reach the deadline too, because every wrapper's
// Unwrap bottoms out at gin's responseWriter.Unwrap.
func TestGinWriterSetWriteDeadlineBaseline(t *testing.T) {
	gin.SetMode(gin.TestMode)
	errCh := make(chan error, 1)
	r := gin.New()
	r.GET("/", func(c *gin.Context) {
		errCh <- http.NewResponseController(c.Writer).SetWriteDeadline(time.Now().Add(time.Second))
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
			t.Fatalf("SetWriteDeadline through gin's writer failed: %v", err)
		}
	default:
		t.Fatal("handler did not report a deadline result")
	}
}
