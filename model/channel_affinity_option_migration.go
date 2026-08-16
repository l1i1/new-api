package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"gorm.io/gorm"
)

const channelAffinityRulesOptionKey = "channel_affinity_setting.rules"

var channelAffinityRuleJSONFields = map[string]struct{}{
	"name":                    {},
	"model_regex":             {},
	"path_regex":              {},
	"user_agent_include":      {},
	"key_sources":             {},
	"value_regex":             {},
	"ttl_seconds":             {},
	"param_override_template": {},
	"skip_retry_on_failure":   {},
	"include_using_group":     {},
	"include_model_name":      {},
	"include_rule_name":       {},
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
		err := tx.Where(&Option{Key: channelAffinityRulesOptionKey}).First(&option).Error
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
		return tx.Model(&option).Update("value", string(encoded)).Error
	})
}

func isHistoricalChannelAffinityRules(value string) bool {
	var rawRules []map[string]json.RawMessage
	if err := common.UnmarshalJsonStr(strings.TrimSpace(value), &rawRules); err != nil || len(rawRules) != 2 {
		return false
	}
	for _, rawRule := range rawRules {
		if !historicalChannelAffinityRuleFields(rawRule) {
			return false
		}
	}

	var rules []operation_setting.ChannelAffinityRule
	if err := common.UnmarshalJsonStr(strings.TrimSpace(value), &rules); err != nil || len(rules) != 2 {
		return false
	}
	normalizeChannelAffinityRules(rules)

	for _, candidate := range operation_setting.HistoricalDefaultChannelAffinityRules() {
		normalizeChannelAffinityRules(candidate)
		if channelAffinityRulesEqual(rules, candidate) {
			return true
		}
	}
	return false
}

func historicalChannelAffinityRuleFields(rawRule map[string]json.RawMessage) bool {
	for key := range rawRule {
		if _, ok := channelAffinityRuleJSONFields[key]; !ok {
			return false
		}
	}

	// All non-omitempty fields plus the template are required by the old
	// defaults. This prevents null or omitted values from being treated as an
	// untouched rule after typed decoding drops that distinction.
	for _, key := range []string{
		"name",
		"model_regex",
		"path_regex",
		"key_sources",
		"value_regex",
		"ttl_seconds",
		"param_override_template",
		"skip_retry_on_failure",
		"include_using_group",
		"include_model_name",
		"include_rule_name",
	} {
		if _, ok := rawRule[key]; !ok {
			return false
		}
	}

	var keySources []map[string]json.RawMessage
	if err := common.Unmarshal(rawRule["key_sources"], &keySources); err != nil {
		return false
	}
	for _, keySource := range keySources {
		for key := range keySource {
			if key != "type" && key != "key" && key != "path" {
				return false
			}
		}
	}

	var template map[string]json.RawMessage
	if err := common.Unmarshal(rawRule["param_override_template"], &template); err != nil {
		return false
	}
	if len(template) != 1 {
		return false
	}
	var operations []map[string]json.RawMessage
	if err := common.Unmarshal(template["operations"], &operations); err != nil {
		return false
	}
	for _, operation := range operations {
		for key := range operation {
			if key != "mode" && key != "value" && key != "keep_origin" {
				return false
			}
		}
	}
	return true
}

func normalizeChannelAffinityRules(rules []operation_setting.ChannelAffinityRule) {
	for index := range rules {
		if len(rules[index].UserAgentInclude) == 0 {
			rules[index].UserAgentInclude = nil
		}
	}
}

func channelAffinityRulesEqual(left, right []operation_setting.ChannelAffinityRule) bool {
	leftJSON, err := common.Marshal(left)
	if err != nil {
		return false
	}
	rightJSON, err := common.Marshal(right)
	if err != nil {
		return false
	}
	return string(leftJSON) == string(rightJSON)
}
