package helper

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestResponseCommitStateOf(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("not committed", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		require.Equal(t, ResponseNotCommitted, ResponseCommitStateOf(c))
	})

	t.Run("headers flushed without body", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		// gin's Status() only stages the code; Flush commits the header with a
		// zero-byte body, which is still nothing the client can act on.
		c.Writer.Flush()
		require.True(t, c.Writer.Written())
		require.Equal(t, ResponseKeepAliveOnly, ResponseCommitStateOf(c))
	})

	t.Run("keep-alive only", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		require.NoError(t, PingData(c))
		require.Equal(t, ResponseKeepAliveOnly, ResponseCommitStateOf(c))

		// Repeated pings stay in the keep-alive-only state.
		require.NoError(t, PingData(c))
		require.Equal(t, ResponseKeepAliveOnly, ResponseCommitStateOf(c))
	})

	t.Run("payload after keep-alive", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		require.NoError(t, PingData(c))
		_, err := c.Writer.Write([]byte("data: {}\n\n"))
		require.NoError(t, err)
		require.Equal(t, ResponsePayloadWritten, ResponseCommitStateOf(c))
	})

	t.Run("payload without keep-alive", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		_, err := c.Writer.Write([]byte("partial stream"))
		require.NoError(t, err)
		require.Equal(t, ResponsePayloadWritten, ResponseCommitStateOf(c))
	})
}
