package controller

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Calcium-Ion/go-epay/epay"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupEpayNotifyTest(t *testing.T) {
	t.Helper()
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousMainDBType, previousLogDBType := common.MainDatabaseType(), common.LogDatabaseType()
	previousRedisEnabled := common.RedisEnabled
	previousPayAddress := operation_setting.PayAddress
	previousEpayID := operation_setting.EpayId
	previousEpayKey := operation_setting.EpayKey
	previousPayMethods := operation_setting.PayMethods
	paymentSetting := operation_setting.GetPaymentSetting()
	previousComplianceConfirmed := paymentSetting.ComplianceConfirmed
	previousComplianceVersion := paymentSetting.ComplianceTermsVersion

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.User{}, &model.TopUp{}, &model.Log{}))
	model.DB, model.LOG_DB = db, db
	common.SetDatabaseTypes(common.DatabaseTypeSQLite, common.DatabaseTypeSQLite)
	common.RedisEnabled = false
	operation_setting.PayAddress = "https://epay.example.test"
	operation_setting.EpayId = "test-partner"
	operation_setting.EpayKey = "test-key"
	operation_setting.PayMethods = []map[string]string{{"name": "Alipay", "type": "alipay"}}
	paymentSetting.ComplianceConfirmed = true
	paymentSetting.ComplianceTermsVersion = operation_setting.CurrentComplianceTermsVersion

	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.SetDatabaseTypes(previousMainDBType, previousLogDBType)
		common.RedisEnabled = previousRedisEnabled
		operation_setting.PayAddress = previousPayAddress
		operation_setting.EpayId = previousEpayID
		operation_setting.EpayKey = previousEpayKey
		operation_setting.PayMethods = previousPayMethods
		paymentSetting.ComplianceConfirmed = previousComplianceConfirmed
		paymentSetting.ComplianceTermsVersion = previousComplianceVersion
	})
}

func signedEpayNotifyRequest(t *testing.T, tradeNo string, money string) *http.Request {
	t.Helper()
	params := epay.GenerateParams(map[string]string{
		"pid":          operation_setting.EpayId,
		"type":         "alipay",
		"trade_no":     "provider-" + tradeNo,
		"out_trade_no": tradeNo,
		"name":         "topup",
		"money":        money,
		"trade_status": epay.StatusTradeSuccess,
	}, operation_setting.EpayKey)
	form := url.Values{}
	for key, value := range params {
		form.Set(key, value)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/user/epay/notify", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

func TestEpayNotify_AcknowledgesOnlyCommittedSettlement(t *testing.T) {
	setupEpayNotifyTest(t)
	gin.SetMode(gin.TestMode)

	user := &model.User{Id: 701, Username: "epay_notify_user", Status: common.UserStatusEnabled, Quota: 10}
	require.NoError(t, model.DB.Create(user).Error)
	topUp := &model.TopUp{
		UserId:          user.Id,
		Amount:          2,
		Money:           9.99,
		TradeNo:         "epay-notify-success",
		PaymentMethod:   "wxpay",
		PaymentProvider: model.PaymentProviderEpay,
		Status:          common.TopUpStatusPending,
	}
	require.NoError(t, topUp.Insert())

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = signedEpayNotifyRequest(t, topUp.TradeNo, "9.99")
	EpayNotify(context)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "success", recorder.Body.String())
	completed := model.GetTopUpByTradeNo(topUp.TradeNo)
	require.NotNil(t, completed)
	assert.Equal(t, common.TopUpStatusSuccess, completed.Status)
	assert.Positive(t, completed.CompleteTime)

	var storedUser model.User
	require.NoError(t, model.DB.Select("quota").First(&storedUser, user.Id).Error)
	assert.Greater(t, storedUser.Quota, 10)
}

func TestEpayNotify_ReturnsFailAndRollsBackWhenCreditCannotCommit(t *testing.T) {
	setupEpayNotifyTest(t)
	gin.SetMode(gin.TestMode)

	topUp := &model.TopUp{
		UserId:          9999,
		Amount:          2,
		Money:           9.99,
		TradeNo:         "epay-notify-missing-user",
		PaymentMethod:   "alipay",
		PaymentProvider: model.PaymentProviderEpay,
		Status:          common.TopUpStatusPending,
	}
	require.NoError(t, topUp.Insert())

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = signedEpayNotifyRequest(t, topUp.TradeNo, "9.99")
	EpayNotify(context)

	assert.Equal(t, http.StatusOK, recorder.Code)
	assert.Equal(t, "fail", recorder.Body.String())
	unchanged := model.GetTopUpByTradeNo(topUp.TradeNo)
	require.NotNil(t, unchanged)
	assert.Equal(t, common.TopUpStatusPending, unchanged.Status)
	assert.Zero(t, unchanged.CompleteTime)
}
