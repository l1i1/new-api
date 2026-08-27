package helper

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	coreconstant "github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newEffortTypeErrorTestContext(t *testing.T, body string, profile *dto.OfficialFitConfig) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	if profile != nil {
		common.SetContextKey(c, coreconstant.ContextKeyUserSetting, dto.UserSetting{OfficialFit: profile})
	}
	return c
}

func TestOfficialFitEffortTypeErrorMapsToOfficialFamilyText(t *testing.T) {
	profile := &dto.OfficialFitConfig{Profile: map[string]dto.OfficialFitProfile{
		"deepseek-v4-": {Validate: true},
		"kimi-k3":      {Validate: true},
		"glm-5.3":      {Validate: true},
	}}
	cases := []struct {
		name  string
		model string
		want  string
		code  string
	}{
		{"deepseek", "deepseek-v4-flash", deepSeekV4ReasoningEffortTypeErrorMessage, "invalid_request_error"},
		{"kimi", "kimi-k3", kimiK3ReasoningEffortTypeMessage, "invalid_request_error"},
		{"glm", "glm-5.3", glm53ReasoningEffortTypeMessage, "1210"},
	}
	unmarshalErr := errors.New("json: cannot unmarshal number into Go struct field GeneralOpenAIRequest.reasoning_effort of type string")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"model":"` + tc.model + `","messages":[{"role":"user","content":"hi"}],"reasoning_effort":1}`
			c := newEffortTypeErrorTestContext(t, body, profile)
			err := officialFitEffortTypeError(c, unmarshalErr)
			require.Error(t, err)
			var apiErr *types.NewAPIError
			require.True(t, errors.As(err, &apiErr))
			assert.Equal(t, http.StatusBadRequest, apiErr.StatusCode)
			oaiErr := apiErr.ToOpenAIError()
			assert.Equal(t, tc.want, oaiErr.Message)
			assert.Equal(t, tc.code, oaiErr.Code)
		})
	}
}

func TestOfficialFitEffortTypeErrorLeavesOthersUntouched(t *testing.T) {
	profile := &dto.OfficialFitConfig{Profile: map[string]dto.OfficialFitProfile{
		"deepseek-v4-": {Validate: true},
	}}
	unmarshalEffortErr := errors.New("json: cannot unmarshal number into Go struct field GeneralOpenAIRequest.reasoning_effort of type string")

	t.Run("no fit profile", func(t *testing.T) {
		c := newEffortTypeErrorTestContext(t, `{"model":"deepseek-v4-flash","reasoning_effort":1}`, nil)
		assert.NoError(t, officialFitEffortTypeError(c, unmarshalEffortErr))
	})
	t.Run("error not about reasoning_effort", func(t *testing.T) {
		c := newEffortTypeErrorTestContext(t, `{"model":"deepseek-v4-flash","reasoning_effort":1}`, profile)
		assert.NoError(t, officialFitEffortTypeError(c, errors.New("json: cannot unmarshal number into Go struct field GeneralOpenAIRequest.n of type int")))
	})
	t.Run("model missing from body", func(t *testing.T) {
		c := newEffortTypeErrorTestContext(t, `{"reasoning_effort":1}`, profile)
		assert.NoError(t, officialFitEffortTypeError(c, unmarshalEffortErr))
	})
	t.Run("validate flag off", func(t *testing.T) {
		c := newEffortTypeErrorTestContext(t, `{"model":"deepseek-v4-flash","reasoning_effort":1}`,
			&dto.OfficialFitConfig{Profile: map[string]dto.OfficialFitProfile{"deepseek-v4-": {Errors: true}}})
		assert.NoError(t, officialFitEffortTypeError(c, unmarshalEffortErr))
	})
}
