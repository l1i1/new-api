package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAbortWithOpenAIMessageUsesPublicErrorEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name       string
		statusCode int
		code       types.ErrorCode
		errorType  string
		publicCode string
	}{
		{name: "authentication", statusCode: http.StatusUnauthorized, errorType: "authentication_error", publicCode: "invalid_request_error"},
		{name: "invalid request", statusCode: http.StatusBadRequest, code: types.ErrorCodeModelNotFound, errorType: "invalid_request_error", publicCode: "model_not_found"},
		{name: "access denied", statusCode: http.StatusForbidden, code: types.ErrorCodeAccessDenied, errorType: "invalid_request_error", publicCode: "access_denied"},
		{name: "server error", statusCode: http.StatusServiceUnavailable, code: types.ErrorCodeGetChannelFailed, errorType: "server_error", publicCode: "server_error"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)

			if test.code == "" {
				abortWithOpenAiMessage(c, test.statusCode, "request failed")
			} else {
				abortWithOpenAiMessage(c, test.statusCode, "request failed", test.code)
			}

			var payload struct {
				Error types.OpenAIError `json:"error"`
			}
			require.NoError(t, common.Unmarshal(w.Body.Bytes(), &payload))
			assert.Equal(t, test.statusCode, w.Code)
			assert.Equal(t, test.errorType, payload.Error.Type)
			assert.Equal(t, test.publicCode, payload.Error.Code)
			assert.Nil(t, payload.Error.Param)
			assert.Contains(t, w.Body.String(), `"param":null`)
			assert.NotContains(t, w.Body.String(), "new_api_error")
		})
	}
}
