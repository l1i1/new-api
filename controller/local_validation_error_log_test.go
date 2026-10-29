package controller

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// setupLocalValidationLogTest points LOG_DB at an in-memory sqlite database and
// turns error logging on, mirroring production where ERROR_LOG_ENABLED=true.
func setupLocalValidationLogTest(t *testing.T) *gorm.DB {
	t.Helper()
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousLogDatabaseType := common.LogDatabaseType()
	previousErrorLogEnabled := constant.ErrorLogEnabled

	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := database.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, database.AutoMigrate(&model.User{}, &model.Log{}))
	model.DB, model.LOG_DB = database, database
	common.SetDatabaseTypes(common.MainDatabaseType(), common.DatabaseTypeSQLite)
	constant.ErrorLogEnabled = true
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(common.MainDatabaseType(), previousLogDatabaseType)
		constant.ErrorLogEnabled = previousErrorLogEnabled
		require.NoError(t, sqlDB.Close())
	})
	return database
}

// TestRelayRecordsLocalValidationRejection pins the reason the row exists: a
// request rejected by the local K3 validator never reaches a channel, so
// without this write its request id is unsearchable (2026-10-29: the customer
// quoted two 400 ids of the same shape, only the one a channel rejected could
// be found). It also pins the row's shape — local, unbilled, channel 0, masked
// content — so it cannot drift into looking like a channel fault.
func TestRelayRecordsLocalValidationRejection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	database := setupLocalValidationLogTest(t)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		`{"model":"kimi-k3.1-pro","messages":[{"role":"user","content":"hi"}],"temperature":0.6}`))
	c.Request.Header.Set("Content-Type", "application/json")
	// Values the auth middlewares set before Relay runs.
	c.Set(common.RequestIdKey, "req-local-validation-1")
	c.Set("id", 7)
	c.Set("username", "log-owner")
	c.Set("token_name", "cdp-kimi-k31")
	c.Set("token_id", 246)
	c.Set("group", "Kimi")
	common.SetContextKey(c, constant.ContextKeyOriginalModel, "kimi-k3.1-pro")
	common.SetContextKey(c, constant.ContextKeyRequestStartTime, time.Now().Add(-time.Second))
	// Thinking stays enabled (the body carries no thinking control), so the
	// official fixed-temperature contract pins 1.0 and 0.6 is rejected.
	common.SetContextKey(c, constant.ContextKeyUserSetting, dto.UserSetting{
		OfficialFit: &dto.OfficialFitConfig{Profile: map[string]dto.OfficialFitProfile{
			"kimi-k3": {Validate: true},
		}},
	})

	Relay(c, types.RelayFormatOpenAI)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Contains(t, recorder.Body.String(), "invalid temperature: only 1 is allowed for this model")

	var stored model.Log
	require.NoError(t, database.First(&stored).Error)
	assert.Equal(t, model.LogTypeError, stored.Type)
	assert.Equal(t, 0, stored.ChannelId, "a local rejection belongs to no channel")
	assert.Equal(t, "kimi-k3.1-pro", stored.ModelName)
	assert.Equal(t, "cdp-kimi-k31", stored.TokenName)
	assert.Equal(t, 246, stored.TokenId)
	assert.Equal(t, "Kimi", stored.Group)
	assert.Equal(t, "req-local-validation-1", stored.RequestId)
	assert.Equal(t, 0, stored.Quota, "locally rejected requests are never billed")
	assert.Equal(t, "status_code=400, invalid temperature: only 1 is allowed for this model", stored.Content)

	storedOther, err := common.StrToMap(stored.Other)
	require.NoError(t, err)
	assert.Equal(t, "local", storedOther["error_source"])
	assert.Equal(t, true, storedOther["local_validation"])
	assert.Equal(t, float64(http.StatusBadRequest), storedOther["status_code"])
	assert.Equal(t, "/v1/chat/completions", storedOther["request_path"])
}

// TestRecordLocalValidationErrorFallsBackToBodyModel covers the case where the
// body cannot be decoded at all (here: invalid JSON): the row must still be
// written, just without a model name, because the request id is the whole point.
func TestRecordLocalValidationErrorFallsBackToBodyModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	database := setupLocalValidationLogTest(t)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{"))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(common.RequestIdKey, "req-local-validation-2")
	c.Set("id", 7)
	c.Set("token_name", "broken-body")
	common.SetContextKey(c, constant.ContextKeyRequestStartTime, time.Now())

	invalidBody := types.NewErrorWithStatusCode(errors.New("failed to unmarshal body"), types.ErrorCodeInvalidRequest,
		http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	recordLocalValidationError(c, invalidBody)

	var stored model.Log
	require.NoError(t, database.First(&stored).Error)
	assert.Empty(t, stored.ModelName)
	assert.Equal(t, "req-local-validation-2", stored.RequestId)
	assert.Equal(t, 0, stored.ChannelId)
}

// TestRecordLocalValidationErrorSkipsWhenDisabled keeps the row behind the same
// switch as every other error row: operators who turn error logging off must
// not get local validation rows back.
func TestRecordLocalValidationErrorSkipsWhenDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	database := setupLocalValidationLogTest(t)
	constant.ErrorLogEnabled = false

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"kimi-k3.1-pro"}`))
	c.Request.Header.Set("Content-Type", "application/json")

	recordLocalValidationError(c, types.NewErrorWithStatusCode(assert.AnError, types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry()))

	var count int64
	require.NoError(t, database.Model(&model.Log{}).Count(&count).Error)
	assert.Zero(t, count)
}
