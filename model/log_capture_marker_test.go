/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
package model

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestAttachLogCaptureMarkerMergesAdminInfoIntoTheRow(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Log{}))

	previousLogDB := LOG_DB
	previousType := common.LogDatabaseType()
	LOG_DB = db
	common.SetLogDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		LOG_DB = previousLogDB
		common.SetLogDatabaseType(previousType)
	})

	require.NoError(t, db.Create(&Log{
		UserId:    1,
		CreatedAt: 100,
		Type:      LogTypeConsume,
		RequestId: "req-capture-1",
		Other:     `{"admin_info":{"node_name":"new-api-ml000"},"model_ratio":1}`,
	}).Error)

	require.NoError(t, AttachLogCaptureMarker("req-capture-1", map[string]any{
		"rule":        "kimi-200-sample",
		"file":        "2026-10-31/120000-kimi-200-sample-req-capture-1.json",
		"captured_at": 1793440000,
		"status":      200,
	}))

	var stored Log
	require.NoError(t, db.Where("request_id = ?", "req-capture-1").First(&stored).Error)
	other := map[string]any{}
	require.NoError(t, common.UnmarshalJsonStr(stored.Other, &other))

	adminInfo, ok := other[logOtherAdminInfoKey].(map[string]any)
	require.True(t, ok, "admin_info must stay an object")
	assert.Equal(t, "new-api-ml000", adminInfo["node_name"], "existing admin fields must be preserved")
	capture, ok := adminInfo["capture"].(map[string]any)
	require.True(t, ok, "capture marker must be written under admin_info")
	assert.Equal(t, "kimi-200-sample", capture["rule"])
	assert.EqualValues(t, 200, capture["status"])
	assert.EqualValues(t, 1, other["model_ratio"], "top-level public fields must be preserved")
}

func TestAttachLogCaptureMarkerIgnoresAnUnknownRequest(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&Log{}))

	previousLogDB := LOG_DB
	previousType := common.LogDatabaseType()
	LOG_DB = db
	common.SetLogDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		LOG_DB = previousLogDB
		common.SetLogDatabaseType(previousType)
	})

	require.NoError(t, AttachLogCaptureMarker("missing", map[string]any{"rule": "r"}))
	require.NoError(t, AttachLogCaptureMarker("", map[string]any{"rule": "r"}))
	require.NoError(t, AttachLogCaptureMarker("missing", nil))
}
