package helper

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// StartNonStreamKeepAlive installs keepAliveGuardedWriter on c.Writer while
// non-stream keep-alive probes run, and relay/channel's sendKeepAliveProbe
// extends the connection write deadline through http.ResponseController on
// every probe beat. The guard must not break the Unwrap chain, or a slow
// client can block the probe goroutine forever - exactly what the bounded
// write is there to prevent (B3 gap).
func TestKeepAliveGuardedWriterSetWriteDeadlineReachesConn(t *testing.T) {
	gin.SetMode(gin.TestMode)
	errCh := make(chan error, 1)
	r := gin.New()
	r.GET("/", func(c *gin.Context) {
		c.Writer = &keepAliveGuardedWriter{ResponseWriter: c.Writer, ctx: c}
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
			t.Fatalf("SetWriteDeadline through keepAliveGuardedWriter failed: %v", err)
		}
	default:
		t.Fatal("handler did not report a deadline result")
	}
}
