package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRelayDoesNotAppendJSONErrorAfterCommittedStream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{"))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	_, err := c.Writer.Write([]byte("data: partial\n\n"))
	require.NoError(t, err)

	Relay(c, types.RelayFormatOpenAI)

	assert.Equal(t, "data: partial\n\n", recorder.Body.String())
	assert.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
}
