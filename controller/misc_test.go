package controller

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/i18n"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestEmailVerificationMessageUsesRequestLanguage(t *testing.T) {
	require.NoError(t, i18n.Init())
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/api/verification?email=user@example.com", nil)
	c.Request.Header.Set("Accept-Language", "fr-FR,fr;q=0.9")

	lang := i18n.GetLangFromContext(c)
	subject, title, content := buildEmailVerificationMessage(lang, "123456")

	require.Equal(t, i18n.LangFr, lang)
	require.Contains(t, subject, "Vérification d'e-mail")
	require.Contains(t, title, "Code de vérification")
	require.Contains(t, content, "123456")
	require.NotContains(t, subject, i18n.MsgEmailVerificationSubject)
}
