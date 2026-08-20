package model

import (
	"errors"
	"testing"

	"github.com/QuantumNous/new-api/common"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestUpdateOptionDoesNotUpdateMemoryWhenDatabaseSaveFails(t *testing.T) {
	previousDB := DB
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Option{}))
	DB = db
	t.Cleanup(func() { DB = previousDB })
	const key = "content_moderation.persistence_test"
	require.NoError(t, DB.Where("key = ?", key).Delete(&Option{}).Error)
	require.NoError(t, DB.Create(&Option{Key: key, Value: "old"}).Error)
	common.OptionMapRWMutex.Lock()
	previousMap := common.OptionMap
	if common.OptionMap == nil {
		common.OptionMap = make(map[string]string)
	}
	common.OptionMap[key] = "old"
	common.OptionMapRWMutex.Unlock()

	const callbackName = "test:fail_content_moderation_option_save"
	require.NoError(t, DB.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == "options" {
			tx.AddError(errors.New("forced option save failure"))
		}
	}))
	t.Cleanup(func() {
		_ = DB.Callback().Update().Remove(callbackName)
		_ = DB.Where("key = ?", key).Delete(&Option{}).Error
		common.OptionMapRWMutex.Lock()
		common.OptionMap = previousMap
		common.OptionMapRWMutex.Unlock()
	})

	err = UpdateOption(key, "new")
	require.ErrorContains(t, err, "forced option save failure")
	common.OptionMapRWMutex.RLock()
	require.Equal(t, "old", common.OptionMap[key])
	common.OptionMapRWMutex.RUnlock()
	var stored Option
	require.NoError(t, DB.First(&stored, "key = ?", key).Error)
	require.Equal(t, "old", stored.Value)
}
