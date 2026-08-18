package common

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestApiErrorI18nIncludesStableMessageCode(t *testing.T) {
	previousTranslateMessage := TranslateMessage
	TranslateMessage = func(_ *gin.Context, _ string, _ ...map[string]any) string {
		return "localized message"
	}
	t.Cleanup(func() {
		TranslateMessage = previousTranslateMessage
	})

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	ApiErrorI18n(context, "user.email_already_taken")

	var response struct {
		Success bool   `json:"success"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	require.NoError(t, Unmarshal(recorder.Body.Bytes(), &response))
	require.False(t, response.Success)
	require.Equal(t, "user.email_already_taken", response.Code)
	require.Equal(t, "localized message", response.Message)
}
