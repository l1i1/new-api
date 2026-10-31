package service

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/nodemetrics"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildSystemInstanceInfoReportsTrafficExtra pins the heartbeat payload:
// the existing resource fields are untouched and info.extra.traffic carries the
// rolling 60-second counters with exactly the contract's JSON keys.
func TestBuildSystemInstanceInfoReportsTrafficExtra(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	identity := common.NodeIdentity{
		Name:                    "i-2ze0abcdef",
		Source:                  common.NodeNameSourceManual,
		ManuallyConfigured:      true,
		ShouldConfigureManually: false,
	}
	info := buildSystemInstanceInfo(
		identity,
		"node-host",
		common.SystemStatus{CPUUsage: 12.5, MemoryUsage: 34.5, DiskUsage: 56},
		common.DiskSpaceInfo{Total: 1_000, Used: 400, Free: 600, UsedPercent: 40},
		now,
	)

	assert.Equal(t, 1, info.SchemaVersion)
	assert.Equal(t, identity, info.Node)
	assert.Equal(t, "node-host", info.Host.Hostname)
	assert.InDelta(t, 12.5, info.Resources.CPU.UsagePercent, 1e-9)
	assert.InDelta(t, 34.5, info.Resources.Memory.UsagePercent, 1e-9)
	assert.Equal(t, uint64(1_000), info.Resources.Storage.TotalBytes)
	assert.Equal(t, uint64(400), info.Resources.Storage.UsedBytes)
	assert.Equal(t, uint64(600), info.Resources.Storage.FreeBytes)
	assert.InDelta(t, 40.0, info.Resources.Storage.UsedPercent, 1e-9)

	require.NotNil(t, info.Extra, "heartbeat must carry extra")
	traffic, ok := info.Extra["traffic"].(nodemetrics.Traffic)
	require.True(t, ok, "extra.traffic must be a nodemetrics.Traffic (got %T)", info.Extra["traffic"])
	assert.Equal(t, nodemetrics.WindowSeconds, traffic.WindowSeconds)

	raw, err := json.Marshal(info)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))

	extra, ok := decoded["extra"].(map[string]any)
	require.True(t, ok, "marshalled heartbeat must contain extra")
	trafficMap, ok := extra["traffic"].(map[string]any)
	require.True(t, ok, "marshalled heartbeat must contain extra.traffic")

	expectedTrafficKeys := []string{
		"window_seconds",
		"rpm",
		"tpm",
		"prompt_tpm",
		"completion_tpm",
		"request_bytes",
		"response_bytes",
		"requests_total",
		"prompt_tokens_total",
		"completion_tokens_total",
		"tokens_total",
		"request_bytes_total",
		"response_bytes_total",
	}
	for _, key := range expectedTrafficKeys {
		assert.Contains(t, trafficMap, key, "extra.traffic is missing contract key %q", key)
	}
	assert.Len(t, trafficMap, len(expectedTrafficKeys), "extra.traffic must not carry keys outside the contract")
	assert.Equal(t, float64(nodemetrics.WindowSeconds), trafficMap["window_seconds"])

	// The pre-existing resource fields keep their JSON shape.
	resources, ok := decoded["resources"].(map[string]any)
	require.True(t, ok)
	storage, ok := resources["storage"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(1_000), storage["total_bytes"])
	assert.Equal(t, float64(400), storage["used_bytes"])
	assert.Equal(t, float64(600), storage["free_bytes"])
}
