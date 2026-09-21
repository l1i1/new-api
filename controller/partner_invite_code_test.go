package controller

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupPartnerControllerDB(t *testing.T) {
	t.Helper()
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousType, previousLogType := common.MainDatabaseType(), common.LogDatabaseType()
	previousMaster, previousCache, previousRedis, previousSQLite := common.IsMasterNode, common.MemoryCacheEnabled, common.RedisEnabled, common.SQLitePath
	previousOptionMap := common.OptionMap
	common.OptionMap = make(map[string]string)
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousType, previousLogType)
		common.IsMasterNode, common.MemoryCacheEnabled, common.RedisEnabled, common.SQLitePath = previousMaster, previousCache, previousRedis, previousSQLite
		common.OptionMap = previousOptionMap
	})
	t.Setenv("SQL_DSN", "")
	t.Setenv("LOG_SQL_DSN", "")
	common.IsMasterNode, common.MemoryCacheEnabled, common.RedisEnabled = false, false, false
	common.SQLitePath = filepath.Join(t.TempDir(), "partner.db")
	require.NoError(t, model.InitDB())
	database := model.DB
	sqlDB, err := database.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	model.LOG_DB = database
	common.SetLogDatabaseType(common.MainDatabaseType())
	require.NoError(t, database.AutoMigrate(&model.User{}, &model.Log{}, &model.Option{}))
}

// partnerRequest runs one partner handler with the signature gate satisfied:
// the HMAC middleware sets this flag, and the handlers only ever read it.
func partnerRequest(t *testing.T, method, path string, body any, handler gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Handle(method, path, func(c *gin.Context) {
		c.Set("partner_bypass", true)
		handler(c)
	})
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(method, path, bytes.NewReader(raw)))
	return recorder
}

func TestPartnerInviteCodeValidateEndpoint(t *testing.T) {
	setupPartnerControllerDB(t)
	require.NoError(t, model.DB.Create(&model.User{Username: "code-owner", AffCode: "TAKEN"}).Error)

	var response struct {
		Results []struct {
			Code   string `json:"code"`
			OK     bool   `json:"ok"`
			Reason string `json:"reason"`
		} `json:"results"`
	}
	recorder := partnerRequest(t, http.MethodPost, "/internal/v1/partner/invite-code/validate",
		map[string]any{"codes": []string{"CTRL-FREE-1", "TAKEN", "O30E"}}, PartnerInviteCodeValidate)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Len(t, response.Results, 3)
	assert.True(t, response.Results[0].OK)
	assert.False(t, response.Results[1].OK)
	assert.Equal(t, "user_aff_code", response.Results[1].Reason)
	assert.False(t, response.Results[2].OK)
	assert.Equal(t, "format", response.Results[2].Reason)

	empty := partnerRequest(t, http.MethodPost, "/internal/v1/partner/invite-code/validate",
		map[string]any{"codes": []string{}}, PartnerInviteCodeValidate)
	assert.Equal(t, http.StatusBadRequest, empty.Code)

	// The gate still applies: without a verified signature nothing runs.
	unsigned := httptest.NewRecorder()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/internal/v1/partner/invite-code/validate", PartnerInviteCodeValidate)
	router.ServeHTTP(unsigned, httptest.NewRequest(http.MethodPost, "/internal/v1/partner/invite-code/validate",
		bytes.NewReader([]byte(`{"codes":["CTRL-FREE-1"]}`))))
	assert.Equal(t, http.StatusUnauthorized, unsigned.Code)
}

// TestPartnerConfigPushesInviteCodes covers the console's push: codes are
// validated against the site's own aff codes, persisted, and immediately
// resolvable by registration.
func TestPartnerConfigPushesInviteCodes(t *testing.T) {
	setupPartnerControllerDB(t)
	require.NoError(t, model.DB.Create(&model.User{Username: "code-owner-2", AffCode: "TAKEN"}).Error)

	synthetic := operation_setting.PartnerSyntheticInviterIDFloor + 30
	recorder := partnerRequest(t, http.MethodPut, "/internal/v1/partner/config", map[string]any{
		"partner_id": "ctrl-codes",
		"contact":    "c",
		"notice":     "n",
		"invite_codes": []map[string]any{
			{"code": "CTRL-CODES-1", "inviter_id": synthetic},
		},
	}, PartnerConfig)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	inviterID, found := operation_setting.FindInviterIDByPartnerCode("CTRL-CODES-1")
	require.True(t, found)
	assert.Equal(t, synthetic, inviterID)
	assert.Equal(t, synthetic, model.ResolveInviterByAffCode("CTRL-CODES-1"))
	assert.True(t, operation_setting.IsPartnerCodeInviter(synthetic))

	// A code the site already hands out as a real aff code is refused, so the
	// bucket's link can never silently attribute to that account.
	clash := partnerRequest(t, http.MethodPut, "/internal/v1/partner/config", map[string]any{
		"partner_id": "ctrl-codes",
		"contact":    "c",
		"notice":     "n",
		"invite_codes": []map[string]any{
			{"code": "TAKEN", "inviter_id": synthetic},
		},
	}, PartnerConfig)
	assert.Equal(t, http.StatusConflict, clash.Code, clash.Body.String())
	// The refused push left the previous list intact.
	if _, found := operation_setting.FindInviterIDByPartnerCode("CTRL-CODES-1"); !found {
		t.Fatal("rejected push dropped the stored codes")
	}

	// A code that could shadow the four-character site namespace is refused too.
	short := partnerRequest(t, http.MethodPut, "/internal/v1/partner/config", map[string]any{
		"partner_id": "ctrl-codes",
		"contact":    "c",
		"notice":     "n",
		"invite_codes": []map[string]any{
			{"code": "O30E", "inviter_id": synthetic},
		},
	}, PartnerConfig)
	assert.Equal(t, http.StatusBadRequest, short.Code, short.Body.String())
}
