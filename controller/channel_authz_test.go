package controller

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChannelHasSensitiveChanges(t *testing.T) {
	baseURL := "https://api.example.com"
	headerOverride := `{"Authorization":"Bearer {api_key}"}`
	origin := &model.Channel{
		Type:           1,
		Key:            "old-key",
		BaseURL:        &baseURL,
		HeaderOverride: &headerOverride,
		Models:         "gpt-4o",
		Group:          "default",
	}

	t.Run("non-sensitive routing fields", func(t *testing.T) {
		updated := PatchChannel{Channel: *origin}
		updated.Models = "gpt-4o,gpt-4o-mini"
		updated.Group = "vip"

		assert.False(t, channelHasSensitiveChanges(&updated, origin, map[string]any{
			"models": updated.Models,
			"group":  updated.Group,
		}))
	})

	t.Run("key change", func(t *testing.T) {
		updated := PatchChannel{Channel: *origin}
		updated.Key = "new-key"

		assert.True(t, channelHasSensitiveChanges(&updated, origin, map[string]any{"key": updated.Key}))
	})

	t.Run("base url change", func(t *testing.T) {
		updated := PatchChannel{Channel: *origin}
		newBaseURL := "https://leak.example.com"
		updated.BaseURL = &newBaseURL

		assert.True(t, channelHasSensitiveChanges(&updated, origin, map[string]any{"base_url": newBaseURL}))
	})

	t.Run("header override change", func(t *testing.T) {
		updated := PatchChannel{Channel: *origin}
		newHeaderOverride := `{"X-Key":"{api_key}"}`
		updated.HeaderOverride = &newHeaderOverride

		assert.True(t, channelHasSensitiveChanges(&updated, origin, map[string]any{"header_override": newHeaderOverride}))
	})

	t.Run("omitted sensitive fields do not use zero values", func(t *testing.T) {
		updated := PatchChannel{}
		updated.Id = origin.Id
		updated.Priority = origin.Priority

		assert.False(t, channelHasSensitiveChanges(&updated, origin, map[string]any{"priority": 10}))
	})

	t.Run("unknown field fails closed", func(t *testing.T) {
		updated := PatchChannel{Channel: *origin}

		assert.True(t, channelHasSensitiveChanges(&updated, origin, map[string]any{"future_secret_field": "x"}))
	})

	t.Run("status is operational", func(t *testing.T) {
		updated := PatchChannel{Channel: *origin}
		updated.Status = common.ChannelStatusManuallyDisabled

		assert.False(t, channelHasSensitiveChanges(&updated, origin, map[string]any{"status": updated.Status}))
	})

	t.Run("read-only fields are ignored by sensitivity check", func(t *testing.T) {
		updated := PatchChannel{Channel: *origin}
		updated.Balance = 99
		updated.UsedQuota = 100
		updated.ResponseTime = 200

		assert.False(t, channelHasSensitiveChanges(&updated, origin, map[string]any{
			"balance":       updated.Balance,
			"used_quota":    updated.UsedQuota,
			"response_time": updated.ResponseTime,
		}))
	})
}

func TestEffectiveChannelForCredentialParsingUsesUpdatedRepresentation(t *testing.T) {
	vertexJSON := &model.Channel{
		Type:          constant.ChannelTypeVertexAi,
		OtherSettings: `{"vertex_key_type":"json"}`,
	}

	changedType := PatchChannel{Channel: *vertexJSON}
	changedType.Type = constant.ChannelTypeOpenAI
	effective := effectiveChannelForCredentialParsing(
		&changedType,
		vertexJSON,
		map[string]any{"type": constant.ChannelTypeOpenAI},
	)
	assert.False(t, usesLegacyJSONMultiKeyCredentials(effective))

	changedVertexKind := PatchChannel{Channel: *vertexJSON}
	changedVertexKind.OtherSettings = `{"vertex_key_type":"api_key"}`
	effective = effectiveChannelForCredentialParsing(
		&changedVertexKind,
		vertexJSON,
		map[string]any{"settings": changedVertexKind.OtherSettings},
	)
	assert.False(t, usesLegacyJSONMultiKeyCredentials(effective))

	ordinary := &model.Channel{Type: constant.ChannelTypeOpenAI}
	changedToVertexJSON := PatchChannel{Channel: *ordinary}
	changedToVertexJSON.Type = constant.ChannelTypeVertexAi
	changedToVertexJSON.OtherSettings = `{"vertex_key_type":"json"}`
	effective = effectiveChannelForCredentialParsing(
		&changedToVertexJSON,
		ordinary,
		map[string]any{
			"type":     constant.ChannelTypeVertexAi,
			"settings": changedToVertexJSON.OtherSettings,
		},
	)
	assert.True(t, usesLegacyJSONMultiKeyCredentials(effective))
}

func TestFormatChannelKeyForRevealPreservesLegacyJSONCredentials(t *testing.T) {
	vertexJSON := &model.Channel{
		Type: constant.ChannelTypeVertexAi,
		Key:  `[{"project_id":"first"},{"project_id":"second"}]`,
		ChannelInfo: model.ChannelInfo{
			IsMultiKey: true,
		},
		Credentials: []model.ChannelCredential{
			{Id: 1, Position: 0, Secret: `{"project_id":"first"}`},
			{Id: 2, Position: 1, Secret: `{"project_id":"second"}`},
		},
	}
	assert.Equal(t, vertexJSON.Key, formatChannelKeyForReveal(vertexJSON))

	ordinary := &model.Channel{
		Key: "key-one\nkey-two",
		ChannelInfo: model.ChannelInfo{
			IsMultiKey: true,
		},
		Credentials: []model.ChannelCredential{
			{Id: 1, Position: 0, Secret: "key-one"},
			{Id: 2, Position: 1, Secret: "key-two", ProxyMode: model.ChannelCredentialProxyModeCustom, ProxyURL: "http://proxy.example:8080"},
		},
	}
	assert.Equal(
		t,
		"key-one\nkey-two\nhttp://proxy.example:8080",
		formatChannelKeyForReveal(ordinary),
	)
}

func TestMergeCodexMultiKeyCredentialsPreservesJSONObjects(t *testing.T) {
	existing := `{"access_token":"token-a","account_id":"account-a"}`
	incoming := "{\n  \"access_token\": \"token-b\",\n  \"account_id\": \"account-b\"\n}"

	merged, err := mergeCodexMultiKeyCredentials(existing, incoming)
	require.NoError(t, err)

	var credentials []map[string]any
	require.NoError(t, common.Unmarshal([]byte(merged), &credentials))
	require.Len(t, credentials, 2)
	assert.Equal(t, "token-a", credentials[0]["access_token"])
	assert.Equal(t, "account-a", credentials[0]["account_id"])
	assert.Equal(t, "token-b", credentials[1]["access_token"])
	assert.Equal(t, "account-b", credentials[1]["account_id"])
}

func TestMergeCodexMultiKeyCredentialsRejectsInvalidJSON(t *testing.T) {
	_, err := mergeCodexMultiKeyCredentials(
		`{"access_token":"token-a","account_id":"account-a"}`,
		"{\"access_token\":\"token-b\"}",
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "account_id")
}

func TestChannelHasSensitiveChangesRecognizesStructuredMultiKeyCredentials(t *testing.T) {
	origin := &model.Channel{}
	updated := PatchChannel{Channel: *origin}

	assert.True(t, channelHasSensitiveChanges(
		&updated,
		origin,
		map[string]any{
			"multi_key_credentials": []any{map[string]any{"secret": "key-one"}},
		},
	))
}

func TestClearChannelReadOnlyFields(t *testing.T) {
	channel := PatchChannel{Channel: model.Channel{
		CreatedTime:        11,
		TestTime:           22,
		ResponseTime:       33,
		Balance:            44.5,
		BalanceUpdatedTime: 55,
		UsedQuota:          66,
		Models:             "gpt-4o",
		Group:              "default",
	}}

	clearChannelReadOnlyFields(&channel, map[string]any{
		"created_time":         channel.CreatedTime,
		"test_time":            channel.TestTime,
		"response_time":        channel.ResponseTime,
		"balance":              channel.Balance,
		"balance_updated_time": channel.BalanceUpdatedTime,
		"used_quota":           channel.UsedQuota,
		"models":               channel.Models,
		"group":                channel.Group,
	})

	assert.Zero(t, channel.CreatedTime)
	assert.Zero(t, channel.TestTime)
	assert.Zero(t, channel.ResponseTime)
	assert.Zero(t, channel.Balance)
	assert.Zero(t, channel.BalanceUpdatedTime)
	assert.Zero(t, channel.UsedQuota)
	assert.Equal(t, "gpt-4o", channel.Models)
	assert.Equal(t, "default", channel.Group)
}

func TestUpdateChannelRejectsStatusField(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(
		http.MethodPut,
		"/api/channel/",
		bytes.NewBufferString(`{"id":1,"status":2}`),
	)
	ctx.Request.Header.Set("Content-Type", "application/json")

	UpdateChannel(ctx)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.False(t, response.Success)
}

func TestAddChannelRejectsInvalidMultiKeyMode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(
		http.MethodPost,
		"/api/channel/",
		bytes.NewBufferString(`{"mode":"multi_to_single","multi_key_mode":"typo","channel":{}}`),
	)
	ctx.Request.Header.Set("Content-Type", "application/json")

	AddChannel(ctx)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.False(t, response.Success)
	assert.Contains(t, response.Message, "multi_key_mode")
}

func TestUpdateChannelRejectsInvalidMultiKeyMode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(
		http.MethodPut,
		"/api/channel/",
		bytes.NewBufferString(`{"id":1,"multi_key_mode":"typo"}`),
	)
	ctx.Request.Header.Set("Content-Type", "application/json")

	UpdateChannel(ctx)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.False(t, response.Success)
	assert.Contains(t, response.Message, "multi_key_mode")
}

func TestChannelStatusValidation(t *testing.T) {
	assert.True(t, isManageableChannelStatus(common.ChannelStatusEnabled))
	assert.True(t, isManageableChannelStatus(common.ChannelStatusManuallyDisabled))
	assert.False(t, isManageableChannelStatus(common.ChannelStatusAutoDisabled))
	assert.False(t, isManageableChannelStatus(0))
}

// TestChannelFieldsAreClassified guards the fail-closed sensitivity check: every
// JSON field of PatchChannel (including the embedded model.Channel) must be listed
// in channelSensitiveFields, channelNonSensitiveFields, or
// channelOperationalFields. A newly added field that is left unclassified will
// fail this test, forcing a conscious permission decision instead of silently
// defaulting either way.
func TestChannelFieldsAreClassified(t *testing.T) {
	classified := func(name string) bool {
		if _, ok := channelSensitiveFields[name]; ok {
			return true
		}
		if _, ok := channelNonSensitiveFields[name]; ok {
			return true
		}
		if _, ok := channelOperationalFields[name]; ok {
			return true
		}
		_, ok := channelReadOnlyFields[name]
		return ok
	}

	var collect func(rt reflect.Type) []string
	collect = func(rt reflect.Type) []string {
		var names []string
		for i := 0; i < rt.NumField(); i++ {
			field := rt.Field(i)
			if field.Anonymous && field.Type.Kind() == reflect.Struct {
				names = append(names, collect(field.Type)...)
				continue
			}
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "" || name == "-" {
				continue
			}
			names = append(names, name)
		}
		return names
	}

	for _, name := range collect(reflect.TypeOf(PatchChannel{})) {
		assert.Truef(t, classified(name),
			"channel field %q is not classified; add it to channelSensitiveFields, channelNonSensitiveFields, channelOperationalFields, or channelReadOnlyFields in channel_authz.go", name)
	}
}
