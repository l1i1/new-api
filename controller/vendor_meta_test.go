package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpdateVendorMetaRefreshesLocalizedPricingVendor(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	vendor := &model.Vendor{
		Name:        "阿里巴巴",
		DisplayName: `<tnt l="zh">阿里巴巴</tnt><tnt l="en">Alibaba</tnt>`,
		Status:      1,
	}
	require.NoError(t, vendor.Insert())
	require.NoError(t, db.Create(&model.Model{
		ModelName: "qwen-localized-vendor-test",
		VendorID:  vendor.Id,
		Status:    1,
		NameRule:  model.NameRuleExact,
	}).Error)
	require.NoError(t, db.Create(&model.Channel{
		Id:     901,
		Type:   constant.ChannelTypeOpenAI,
		Key:    "pricing-vendor-test-key",
		Name:   "pricing-vendor-test-channel",
		Status: common.ChannelStatusEnabled,
	}).Error)
	require.NoError(t, db.Create(&model.Ability{
		Group:     "default",
		Model:     "qwen-localized-vendor-test",
		ChannelId: 901,
		Enabled:   true,
	}).Error)

	model.InitChannelCache()
	model.InvalidatePricingCache()
	require.Equal(t, vendor.DisplayName, model.GetVendors()[0].DisplayName)

	updatedDisplayName := `<tnt l="zh">阿里云</tnt><tnt l="en">Alibaba Cloud</tnt>`
	payload, err := common.Marshal(&model.Vendor{
		Id:          vendor.Id,
		Name:        vendor.Name,
		DisplayName: updatedDisplayName,
		Status:      1,
	})
	require.NoError(t, err)

	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPut, "/api/vendors/", strings.NewReader(string(payload)))
	context.Request.Header.Set("Content-Type", "application/json")

	UpdateVendorMeta(context)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"success":true`)
	pricingRecorder := httptest.NewRecorder()
	pricingContext, _ := gin.CreateTestContext(pricingRecorder)
	pricingContext.Request = httptest.NewRequest(http.MethodGet, "/api/pricing", nil)
	GetPricing(pricingContext)

	require.Equal(t, http.StatusOK, pricingRecorder.Code)
	var pricingResponse struct {
		Success bool                  `json:"success"`
		Vendors []model.PricingVendor `json:"vendors"`
	}
	require.NoError(t, common.Unmarshal(pricingRecorder.Body.Bytes(), &pricingResponse))
	require.True(t, pricingResponse.Success)
	require.Len(t, pricingResponse.Vendors, 1)
	assert.Equal(t, "阿里巴巴", pricingResponse.Vendors[0].Name)
	assert.Equal(t, updatedDisplayName, pricingResponse.Vendors[0].DisplayName)
}

func TestEnsureVendorIDUsesCanonicalNameInsteadOfDisplayName(t *testing.T) {
	db := setupModelListControllerTestDB(t)
	vendor := &model.Vendor{
		Name:        "智谱",
		DisplayName: `<tnt l="zh">智谱</tnt><tnt l="en">Zhipu</tnt>`,
		Status:      1,
	}
	require.NoError(t, vendor.Insert())

	createdVendors := 0
	vendorID := ensureVendorID(
		"智谱",
		map[string]upstreamVendor{
			"智谱": {Name: "智谱", Description: "upstream description"},
		},
		map[string]int{},
		&createdVendors,
	)

	assert.Equal(t, vendor.Id, vendorID)
	assert.Zero(t, createdVendors)
	var vendors []model.Vendor
	require.NoError(t, db.Find(&vendors).Error)
	require.Len(t, vendors, 1)
	assert.Equal(t, vendor.DisplayName, vendors[0].DisplayName)
}
