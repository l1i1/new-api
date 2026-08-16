package operation_setting

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFilterErrorMessage_DefaultPattern(t *testing.T) {
	require.NoError(t, SetErrorMessageFilterPattern(ErrorMessageFilterDefaultPattern))
	SetErrorMessageFilterEnabled(true)

	message := "status_code=503, auth_unavailable: no auth available (providers=provider-a,provider-b, model=qwen3.8-max)"
	require.Equal(t, "status_code=503, auth_unavailable: no auth available", FilterErrorMessage(message))
	require.Equal(t, "status_code=400, Error from provider: upstream failed", FilterErrorMessage("status_code=400, Error from provider (Console Go): upstream failed"))
	require.Equal(t, "status_code=503, (request id: upstream-1)", FilterErrorMessage("status_code=503, providers=private-provider (request id: upstream-1)"))
}

func TestFilterErrorMessage_DisabledAndCustomPattern(t *testing.T) {
	originalEnabled := IsErrorMessageFilterEnabled()
	originalPattern := GetErrorMessageFilterPattern()
	t.Cleanup(func() {
		SetErrorMessageFilterEnabled(originalEnabled)
		require.NoError(t, SetErrorMessageFilterPattern(originalPattern))
	})

	message := "status_code=503, providers=private-provider (request id: local-1)"
	SetErrorMessageFilterEnabled(false)
	require.Equal(t, message, FilterErrorMessage(message))

	SetErrorMessageFilterEnabled(true)
	require.NoError(t, SetErrorMessageFilterPattern(`providers=[^ ]+`))
	require.Equal(t, "status_code=503,  (request id: local-1)", FilterErrorMessage(message))

	require.NoError(t, SetErrorMessageFilterPattern(""))
	require.Equal(t, message, FilterErrorMessage(message))
}

func TestSetErrorMessageFilterPatternRejectsInvalidExpression(t *testing.T) {
	original := GetErrorMessageFilterPattern()
	t.Cleanup(func() { require.NoError(t, SetErrorMessageFilterPattern(original)) })

	require.Error(t, SetErrorMessageFilterPattern("["))
	require.Equal(t, original, GetErrorMessageFilterPattern())
}
