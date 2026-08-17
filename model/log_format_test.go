package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/stretchr/testify/require"
)

// TestFormatUserLogsStripsQuotaSaturation verifies the admin-only quota
// saturation marker (nested under other.admin_info) is removed for non-admin
// log views, since formatUserLogs strips the whole admin_info object.
func TestFormatUserLogsStripsQuotaSaturation(t *testing.T) {
	other := common.MapToJsonStr(map[string]interface{}{
		"model_price": 0.004,
		"admin_info": map[string]interface{}{
			"quota_saturation": map[string]interface{}{
				"op":      "QuotaFromDecimal",
				"kind":    "overflow",
				"clamped": common.MaxQuota,
			},
		},
	})
	logs := []*Log{{Other: other}}

	formatUserLogs(logs, 0)

	parsed, err := common.StrToMap(logs[0].Other)
	require.NoError(t, err)
	_, hasAdminInfo := parsed["admin_info"]
	require.False(t, hasAdminInfo, "admin_info (and nested quota_saturation) must be stripped for non-admin views")
	// Non-admin billing fields remain visible.
	require.Contains(t, parsed, "model_price")
}

func TestFormatUserLogsSanitizesStreamErrors(t *testing.T) {
	other := common.MapToJsonStr(map[string]interface{}{
		"stream_status": map[string]interface{}{
			"status":      "error",
			"end_reason":  "upstream_error",
			"error_count": 2,
			"end_error":   "dial tcp 10.0.0.8:443: connection refused",
			"errors":      []string{"provider response body", "internal panic detail"},
		},
	})
	logs := []*Log{{Other: other}}

	formatUserLogs(logs, 0)

	parsed, err := common.StrToMap(logs[0].Other)
	require.NoError(t, err)
	streamStatus, ok := parsed["stream_status"].(map[string]interface{})
	require.True(t, ok)
	require.Equal(t, "error", streamStatus["status"])
	require.Equal(t, "upstream_error", streamStatus["end_reason"])
	require.EqualValues(t, 2, streamStatus["error_count"])
	require.NotContains(t, streamStatus, "end_error")
	require.NotContains(t, streamStatus, "errors")
}

func TestFormatUserLogsFiltersErrorContentForNonAdminViews(t *testing.T) {
	originalEnabled := operation_setting.IsErrorMessageFilterEnabled()
	originalPattern := operation_setting.GetErrorMessageFilterPattern()
	t.Cleanup(func() {
		operation_setting.SetErrorMessageFilterEnabled(originalEnabled)
		require.NoError(t, operation_setting.SetErrorMessageFilterPattern(originalPattern))
	})

	operation_setting.SetErrorMessageFilterEnabled(true)
	require.NoError(t, operation_setting.SetErrorMessageFilterPattern(operation_setting.ErrorMessageFilterDefaultPattern))

	logs := []*Log{
		{
			Type:    LogTypeError,
			Content: "status_code=503, auth_unavailable: no auth available (providers=private-provider, model=qwen3.8-max) (request id: upstream-1) (request id: upstream-2)",
		},
		{
			Type:    LogTypeConsume,
			Content: "providers=should-remain-in-non-error-log",
		},
	}

	formatUserLogs(logs, 0)

	require.Equal(t, "status_code=503, auth_unavailable: no auth available", logs[0].Content)
	require.Equal(t, "providers=should-remain-in-non-error-log", logs[1].Content)
}
