package model

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type legacyVendor struct {
	Id   int    `gorm:"primaryKey"`
	Name string `gorm:"size:128;not null"`
}

func (legacyVendor) TableName() string {
	return "vendor_display_name_migration_test"
}

func TestVendorDisplayNameMigrationPreservesLegacyRows(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Table("vendors").AutoMigrate(&legacyVendor{}))
	require.NoError(t, db.Table("vendors").Create(&legacyVendor{Id: 1, Name: "Alibaba"}).Error)

	require.NoError(t, db.AutoMigrate(&Vendor{}))

	var vendor Vendor
	require.NoError(t, db.First(&vendor, 1).Error)
	assert.Equal(t, "Alibaba", vendor.Name)
	assert.Empty(t, vendor.DisplayName)
	assert.True(t, db.Migrator().HasColumn(&Vendor{}, "display_name"))
}

func TestVendorUpdateOnlyUpdatesExistingVendor(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Vendor{}))

	previousDB := DB
	DB = db
	t.Cleanup(func() { DB = previousDB })

	vendor := &Vendor{Name: "Alibaba", DisplayName: "Alibaba", Status: 1}
	require.NoError(t, vendor.Insert())
	createdTime := vendor.CreatedTime

	vendor.DisplayName = `<tnt l="zh">阿里巴巴</tnt><tnt l="en">Alibaba</tnt>`
	vendor.Description = "localized vendor"
	vendor.Icon = "Qwen.Color"
	vendor.Status = 0
	require.NoError(t, vendor.Update())
	require.NoError(t, vendor.Update())

	var persisted Vendor
	require.NoError(t, db.First(&persisted, vendor.Id).Error)
	assert.Equal(t, vendor.Name, persisted.Name)
	assert.Equal(t, vendor.DisplayName, persisted.DisplayName)
	assert.Equal(t, vendor.Description, persisted.Description)
	assert.Equal(t, vendor.Icon, persisted.Icon)
	assert.Zero(t, persisted.Status)
	assert.Equal(t, createdTime, persisted.CreatedTime)

	missing := &Vendor{Id: vendor.Id + 100, Name: "Missing", Status: 1}
	assert.ErrorIs(t, missing.Update(), gorm.ErrRecordNotFound)
	var count int64
	require.NoError(t, db.Model(&Vendor{}).Where("id = ?", missing.Id).Count(&count).Error)
	assert.Zero(t, count)
}

func TestPricingCacheGettersReturnSnapshots(t *testing.T) {
	cacheRatio := 0.5
	updatePricingLock.Lock()
	pricingMap = []Pricing{{
		ModelName:              "snapshot-model",
		EnableGroup:            []string{"default"},
		SupportedEndpointTypes: []constant.EndpointType{constant.EndpointTypeOpenAI},
		CacheRatio:             &cacheRatio,
	}}
	vendorsList = []PricingVendor{{ID: 1, Name: "Alibaba", DisplayName: "Alibaba"}}
	supportedEndpointMap = map[string]common.EndpointInfo{
		"openai": {Path: "/v1/chat/completions", Method: "POST"},
	}
	lastGetPricingTime = time.Now()
	updatePricingLock.Unlock()
	modelEnableGroupsLock.Lock()
	modelEnableGroups = map[string][]string{"snapshot-model": {"default"}}
	modelQuotaTypeMap = map[string]int{"snapshot-model": 0}
	modelEnableGroupsLock.Unlock()
	t.Cleanup(func() {
		InvalidatePricingCache()
		modelEnableGroupsLock.Lock()
		modelEnableGroups = make(map[string][]string)
		modelQuotaTypeMap = make(map[string]int)
		modelEnableGroupsLock.Unlock()
	})

	pricing := GetPricing()
	vendors := GetVendors()
	endpoints := GetSupportedEndpointMap()
	groups := GetModelEnableGroups("snapshot-model")
	pricing[0].EnableGroup[0] = "mutated"
	pricing[0].SupportedEndpointTypes[0] = constant.EndpointTypeGemini
	*pricing[0].CacheRatio = 9
	vendors[0].DisplayName = "mutated"
	endpoints["openai"] = common.EndpointInfo{Path: "/mutated", Method: "GET"}
	groups[0] = "mutated"

	pricingAgain := GetPricing()
	vendorsAgain := GetVendors()
	endpointsAgain := GetSupportedEndpointMap()
	assert.Equal(t, "default", pricingAgain[0].EnableGroup[0])
	assert.Equal(t, constant.EndpointTypeOpenAI, pricingAgain[0].SupportedEndpointTypes[0])
	assert.Equal(t, 0.5, *pricingAgain[0].CacheRatio)
	assert.Equal(t, "Alibaba", vendorsAgain[0].DisplayName)
	assert.Equal(t, "/v1/chat/completions", endpointsAgain["openai"].Path)
	assert.Equal(t, "default", GetModelEnableGroups("snapshot-model")[0])
}
