package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestDistributeAllowsSunoFetchWithoutRequestModelForTokenModelLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/suno/fetch", Distribute(), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	request := httptest.NewRequest(http.MethodPost, "/suno/fetch", nil)
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = request
	common.SetContextKey(context, constant.ContextKeyTokenModelLimitEnabled, true)
	common.SetContextKey(context, constant.ContextKeyTokenModelLimit, map[string]bool{})

	router.HandleContext(context)

	require.Equal(t, http.StatusNoContent, response.Code)
}
