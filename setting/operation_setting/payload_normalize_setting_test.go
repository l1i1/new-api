package operation_setting

import (
	"testing"

	"github.com/QuantumNous/new-api/setting/config"
)

// TestPayloadNormalizeSettingAcceptsOptionKeys guards the wiring, not the rewrite: the admin
// option API splits "payload_normalize.token_ids" on the dot and looks the remainder up
// verbatim against the json tag. A ",omitempty" suffix in that tag silently turns every
// runtime update into a no-op, which would leave the feature impossible to scope - and
// therefore impossible to enable - while every unit test still passed.
func TestPayloadNormalizeSettingAcceptsOptionKeys(t *testing.T) {
	var setting PayloadNormalizeSetting
	err := config.UpdateConfigFromMap(&setting, map[string]string{
		"enabled":            "true",
		"token_ids":          "[88]",
		"channel_ids":        "[7]",
		"min_user_chars":     "100000",
		"fingerprint_chars":  "4096",
		"fingerprint_sha256": "3f56ce8e",
		"document_chars":     "315569",
	})
	if err != nil {
		t.Fatalf("update from option map: %v", err)
	}
	if !setting.Enabled {
		t.Fatalf("enabled option was not applied")
	}
	if len(setting.TokenIDs) != 1 || setting.TokenIDs[0] != 88 {
		t.Fatalf("token_ids option was not applied: %v", setting.TokenIDs)
	}
	if len(setting.ChannelIDs) != 1 || setting.ChannelIDs[0] != 7 {
		t.Fatalf("channel_ids option was not applied: %v", setting.ChannelIDs)
	}
	if setting.MinUserChars != 100000 || setting.FingerprintChars != 4096 ||
		setting.FingerprintSHA256 != "3f56ce8e" || setting.DocumentChars != 315569 {
		t.Fatalf("scalar options were not applied: %+v", setting)
	}
}
