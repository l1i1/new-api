package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestVideoEstimateKeyRoundTripsThroughTheOptionsAPI is the operator contract
// end to end: the console saves the key through the options endpoint, the value
// is persisted, a reload from the database (what every node's sync loop does)
// brings it back into memory, and the resolved endpoint uses it. It also pins
// the withholding rule — the options API must never hand the stored secret back
// to the browser.
func TestVideoEstimateKeyRoundTripsThroughTheOptionsAPI(t *testing.T) {
	previousDB := model.DB
	previousType := common.MainDatabaseType()
	previousSQLite := common.SQLitePath
	common.SQLitePath = filepath.Join(t.TempDir(), "video-estimate-option.db")
	require.NoError(t, model.InitDB())
	require.NoError(t, model.DB.AutoMigrate(&model.Option{}))
	// updateOptionMap writes into the shared option map, so it must exist.
	model.InitOptionMap()
	testDB := model.DB
	t.Cleanup(func() {
		if sqlDB, err := testDB.DB(); err == nil {
			_ = sqlDB.Close()
		}
		model.DB = previousDB
		common.SetMainDatabaseType(previousType)
		common.SQLitePath = previousSQLite
	})

	settingKey := operation_setting.VideoEstimateSettingModule + ".api_key"
	baseKey := operation_setting.VideoEstimateSettingModule + ".base_url"

	// Never inherit a value from another test or from the environment.
	t.Setenv(service.VideoEstimateAPIKeyEnv, "")
	t.Setenv(service.VideoEstimateBaseURLEnv, "")
	setting := operation_setting.GetVideoEstimateSetting()
	previousBase, previousKey := setting.BaseURL, setting.APIKey
	setting.BaseURL, setting.APIKey = "", ""
	t.Cleanup(func() { setting.BaseURL, setting.APIKey = previousBase, previousKey })

	put := func(key, value string) {
		t.Helper()
		payload, err := json.Marshal(map[string]any{"key": key, "value": value})
		require.NoError(t, err)
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPut, "/api/option/", strings.NewReader(string(payload)))
		c.Request.Header.Set("Content-Type", "application/json")
		UpdateOption(c)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		var response struct {
			Success bool `json:"success"`
		}
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
		require.True(t, response.Success, recorder.Body.String())
	}

	if _, ok := service.LoadVideoEstimateConfig(); ok {
		t.Fatal("the endpoint must start unconfigured")
	}

	// The console saves a key with the whitespace a paste leaves behind.
	put(settingKey, "  sk-from-console  ")

	// Persisted verbatim (no secret is logged, and the trimming happens on read).
	var stored model.Option
	require.NoError(t, testDB.First(&stored, "key = ?", settingKey).Error)
	require.Equal(t, "  sk-from-console  ", stored.Value)

	// A reload from the database — what SyncOptions does on every node — must be
	// what makes the key live, not the in-process PUT.
	setting.BaseURL, setting.APIKey = "", ""
	model.InitOptionMap()

	cfg, ok := service.LoadVideoEstimateConfig()
	require.True(t, ok, "a saved key must configure the endpoint after a reload")
	require.Equal(t, "sk-from-console", cfg.APIKey)
	require.Equal(t, service.DefaultVideoEstimateBaseURL, cfg.BaseURL)

	// A stored base URL takes precedence over the default.
	put(baseKey, "https://tokenizer.test/v1/")
	model.InitOptionMap()
	cfg, ok = service.LoadVideoEstimateConfig()
	require.True(t, ok)
	require.Equal(t, "https://tokenizer.test/v1", cfg.BaseURL)

	// The options API must not return the stored secret, which is what lets the
	// console render a blank field meaning "unchanged".
	optionsRecorder := httptest.NewRecorder()
	optionsContext, _ := gin.CreateTestContext(optionsRecorder)
	optionsContext.Request = httptest.NewRequest(http.MethodGet, "/api/option/", nil)
	GetOptions(optionsContext)
	require.Equal(t, http.StatusOK, optionsRecorder.Code)
	require.NotContains(t, optionsRecorder.Body.String(), "sk-from-console",
		"the options API must never return the stored key")
	require.Contains(t, optionsRecorder.Body.String(), baseKey,
		"the base URL is not a secret and stays editable")

	// Clearing the key through the same endpoint takes the endpoint dark.
	put(settingKey, "")
	model.InitOptionMap()
	if _, ok := service.LoadVideoEstimateConfig(); ok {
		t.Fatal("clearing the stored key must disable the endpoint when nothing else provides one")
	}
}
