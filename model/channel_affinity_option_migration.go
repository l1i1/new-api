package model

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"gorm.io/gorm"
)

const channelAffinityRulesOptionKey = "channel_affinity_setting.rules"

// These are the exact rule values shipped between c91d07466 and 1037acae6.
// Missing fields are significant: only untouched defaults may be expanded.
var historicalChannelAffinityRuleValues = []string{
	`[
		{"name":"codex trace","model_regex":["^gpt-.*$"],"path_regex":["/v1/responses"],"key_sources":[{"type":"gjson","path":"prompt_cache_key"}],"value_regex":"","ttl_seconds":0,"include_using_group":true,"include_rule_name":true},
		{"name":"claude code trace","model_regex":["^claude-.*$"],"path_regex":["/v1/messages"],"key_sources":[{"type":"gjson","path":"metadata.user_id"}],"value_regex":"","ttl_seconds":0,"include_using_group":true,"include_rule_name":true}
	]`,
	`[
		{"name":"codex cli trace","model_regex":["^gpt-.*$"],"path_regex":["/v1/responses"],"key_sources":[{"type":"gjson","path":"prompt_cache_key"}],"value_regex":"","ttl_seconds":0,"param_override_template":{"operations":[{"mode":"pass_headers","value":["Originator","Session_id","User-Agent","X-Codex-Beta-Features","X-Codex-Turn-Metadata"],"keep_origin":true}]},"include_using_group":true,"include_rule_name":true},
		{"name":"claude cli trace","model_regex":["^claude-.*$"],"path_regex":["/v1/messages"],"key_sources":[{"type":"gjson","path":"metadata.user_id"}],"value_regex":"","ttl_seconds":0,"param_override_template":{"operations":[{"mode":"pass_headers","value":["X-Stainless-Arch","X-Stainless-Lang","X-Stainless-Os","X-Stainless-Package-Version","X-Stainless-Retry-Count","X-Stainless-Runtime","X-Stainless-Runtime-Version","X-Stainless-Timeout","User-Agent","X-App","Anthropic-Beta","Anthropic-Dangerous-Direct-Browser-Access","Anthropic-Version"],"keep_origin":true}]},"include_using_group":true,"include_rule_name":true}
	]`,
	`[
		{"name":"codex cli trace","model_regex":["^gpt-.*$"],"path_regex":["/v1/responses"],"key_sources":[{"type":"gjson","path":"prompt_cache_key"}],"value_regex":"","ttl_seconds":0,"param_override_template":{"operations":[{"mode":"pass_headers","value":["Originator","Session_id","User-Agent","X-Codex-Beta-Features","X-Codex-Turn-Metadata"],"keep_origin":true}]},"skip_retry_on_failure":true,"include_using_group":true,"include_rule_name":true},
		{"name":"claude cli trace","model_regex":["^claude-.*$"],"path_regex":["/v1/messages"],"key_sources":[{"type":"gjson","path":"metadata.user_id"}],"value_regex":"","ttl_seconds":0,"param_override_template":{"operations":[{"mode":"pass_headers","value":["X-Stainless-Arch","X-Stainless-Lang","X-Stainless-Os","X-Stainless-Package-Version","X-Stainless-Retry-Count","X-Stainless-Runtime","X-Stainless-Runtime-Version","X-Stainless-Timeout","User-Agent","X-App","Anthropic-Beta","Anthropic-Dangerous-Direct-Browser-Access","Anthropic-Version"],"keep_origin":true}]},"skip_retry_on_failure":true,"include_using_group":true,"include_rule_name":true}
	]`,
	`[
		{"name":"codex cli trace","model_regex":["^gpt-.*$"],"path_regex":["/v1/responses"],"key_sources":[{"type":"gjson","path":"prompt_cache_key"}],"value_regex":"","ttl_seconds":0,"param_override_template":{"operations":[{"mode":"pass_headers","value":["Originator","Session_id","User-Agent","X-Codex-Beta-Features","X-Codex-Turn-Metadata"],"keep_origin":true}]},"skip_retry_on_failure":true,"include_using_group":true,"include_model_name":false,"include_rule_name":true},
		{"name":"claude cli trace","model_regex":["^claude-.*$"],"path_regex":["/v1/messages"],"key_sources":[{"type":"gjson","path":"metadata.user_id"}],"value_regex":"","ttl_seconds":0,"param_override_template":{"operations":[{"mode":"pass_headers","value":["X-Stainless-Arch","X-Stainless-Lang","X-Stainless-Os","X-Stainless-Package-Version","X-Stainless-Retry-Count","X-Stainless-Runtime","X-Stainless-Runtime-Version","X-Stainless-Timeout","User-Agent","X-App","Anthropic-Beta","Anthropic-Dangerous-Direct-Browser-Access","Anthropic-Version"],"keep_origin":true}]},"skip_retry_on_failure":true,"include_using_group":true,"include_model_name":false,"include_rule_name":true}
	]`,
	`[
		{"name":"codex cli trace","model_regex":["^gpt-.*$"],"path_regex":["/v1/responses"],"key_sources":[{"type":"gjson","path":"prompt_cache_key"}],"value_regex":"","ttl_seconds":0,"param_override_template":{"operations":[{"mode":"pass_headers","value":["Originator","Session_id","Thread_id","Session-Id","Thread-Id","X-Client-Request-Id","User-Agent","X-Codex-Beta-Features","X-Codex-Turn-State","X-Codex-Turn-Metadata","X-Codex-Window-Id","X-Codex-Parent-Thread-Id","X-OpenAI-Subagent","X-OpenAI-Memgen-Request","X-ResponsesAPI-Include-Timing-Metrics","X-OpenAI-Internal-Codex-Responses-Lite"],"keep_origin":true}]},"skip_retry_on_failure":true,"include_using_group":true,"include_model_name":false,"include_rule_name":true},
		{"name":"claude cli trace","model_regex":["^claude-.*$"],"path_regex":["/v1/messages"],"key_sources":[{"type":"gjson","path":"metadata.user_id"}],"value_regex":"","ttl_seconds":0,"param_override_template":{"operations":[{"mode":"pass_headers","value":["X-Stainless-Arch","X-Stainless-Lang","X-Stainless-Os","X-Stainless-Package-Version","X-Stainless-Retry-Count","X-Stainless-Runtime","X-Stainless-Runtime-Version","X-Stainless-Timeout","User-Agent","X-App","Anthropic-Beta","Anthropic-Dangerous-Direct-Browser-Access","Anthropic-Version"],"keep_origin":true}]},"skip_retry_on_failure":true,"include_using_group":true,"include_model_name":false,"include_rule_name":true}
	]`,
}

// MigrateChannelAffinityDefaultRules expands an untouched two-rule default to
// the current six-rule default. Any malformed or edited value is left alone.
// The migration is intentionally master-only at the startup call site because
// all nodes read the same option database.
func MigrateChannelAffinityDefaultRules() error {
	if DB == nil {
		return errors.New("database is not initialized")
	}

	return DB.Transaction(func(tx *gorm.DB) error {
		var option Option
		err := lockForUpdate(tx).Where(&Option{Key: channelAffinityRulesOptionKey}).First(&option).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read %s: %w", channelAffinityRulesOptionKey, err)
		}

		if !isHistoricalChannelAffinityRules(option.Value) {
			return nil
		}

		encoded, err := common.Marshal(operation_setting.GetChannelAffinitySetting().Rules)
		if err != nil {
			return fmt.Errorf("encode current channel affinity rules: %w", err)
		}
		_, err = replaceChannelAffinityDefaultRulesIfUnchanged(tx, option, string(encoded))
		return err
	})
}

func isHistoricalChannelAffinityRules(value string) bool {
	compacted, err := common.CompactJson(common.StringToByteSlice(value))
	if err != nil {
		return false
	}
	for _, historicalValue := range historicalChannelAffinityRuleValues {
		historicalCompacted, err := common.CompactJson(common.StringToByteSlice(historicalValue))
		if err != nil {
			return false
		}
		if bytes.Equal(compacted, historicalCompacted) {
			return true
		}
	}
	return false
}

func replaceChannelAffinityDefaultRulesIfUnchanged(tx *gorm.DB, option Option, value string) (bool, error) {
	result := tx.Model(&Option{}).
		Where(&Option{Key: option.Key, Value: option.Value}).
		Update("value", value)
	if result.Error != nil {
		return false, fmt.Errorf("update %s: %w", channelAffinityRulesOptionKey, result.Error)
	}
	// A zero-row CAS means an administrator saved a newer value after the
	// read. Preserve it and let the next startup evaluate that value again.
	return result.RowsAffected > 0, nil
}
