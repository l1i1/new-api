package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpdateOptionRejectsInvalidInvoicePaymentMethodAllowlist(t *testing.T) {
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(
		http.MethodPut,
		"/api/option/",
		strings.NewReader(`{"key":"`+model.InvoiceAllowedPaymentMethodsOption+`","value":{"alipay":true}}`),
	)

	UpdateOption(context)

	assert.Equal(t, http.StatusOK, response.Code)
	var payload struct {
		Success bool `json:"success"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
	assert.False(t, payload.Success)
}

func TestNormalizeInvoicePaymentMethodAllowlistBeforePersistence(t *testing.T) {
	allowed, err := model.NormalizeInvoiceAllowedPaymentMethods(`[" Stripe ","alipay","stripe"]`)
	require.NoError(t, err)
	encoded, err := common.Marshal(allowed)
	require.NoError(t, err)
	assert.Equal(t, `["alipay","stripe"]`, string(encoded))
}
