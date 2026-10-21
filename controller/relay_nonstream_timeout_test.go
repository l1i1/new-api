package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

// The non-stream attempt bound exists so a stalled upstream fails the attempt
// instead of holding a non-stream client for minutes (measured 2026-10-21:
// 262s/314s for a 31-token "hi", reproduced at 190s/185s on a 40-request
// sample). These tests pin the two properties the retry loop depends on: only
// non-stream attempts get the deadline, and the request is restored afterwards
// so the next attempt starts from the original context.
func TestBoundNonStreamAttempt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	newCtx := func() *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		return c
	}
	previous := common.RelayNonStreamTimeout
	t.Cleanup(func() { common.RelayNonStreamTimeout = previous })

	t.Run("non-stream attempt gets a deadline and is restored", func(t *testing.T) {
		common.RelayNonStreamTimeout = 90
		c := newCtx()
		original := c.Request
		restore := boundNonStreamAttempt(c, false)
		if restore == nil {
			t.Fatal("non-stream attempt was not bounded")
		}
		deadline, ok := c.Request.Context().Deadline()
		if !ok {
			t.Fatal("non-stream request has no deadline")
		}
		if remaining := time.Until(deadline); remaining <= 0 || remaining > 95*time.Second {
			t.Fatalf("deadline is %s away, want about 90s", remaining)
		}
		restore()
		if c.Request != original {
			t.Fatal("original request was not restored")
		}
		if _, ok := c.Request.Context().Deadline(); ok {
			t.Fatal("deadline survived the restore")
		}
	})

	t.Run("streaming attempt is untouched", func(t *testing.T) {
		common.RelayNonStreamTimeout = 90
		c := newCtx()
		if restore := boundNonStreamAttempt(c, true); restore != nil {
			t.Fatal("streaming attempt was bounded: a long stream is the answer, not a stall")
		}
		if _, ok := c.Request.Context().Deadline(); ok {
			t.Fatal("streaming request got a deadline")
		}
	})

	t.Run("zero disables the bound", func(t *testing.T) {
		common.RelayNonStreamTimeout = 0
		c := newCtx()
		if restore := boundNonStreamAttempt(c, false); restore != nil {
			t.Fatal("bound applied while RELAY_NONSTREAM_TIMEOUT is 0")
		}
	})

	t.Run("nil request is safe", func(t *testing.T) {
		common.RelayNonStreamTimeout = 90
		c := &gin.Context{}
		if restore := boundNonStreamAttempt(c, false); restore != nil {
			t.Fatal("bound applied without a request")
		}
	})
}
