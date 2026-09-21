package operation_setting

import (
	"fmt"
	"math"
	"strconv"

	"github.com/QuantumNous/new-api/setting/config"
)

type QuotaSetting struct {
	EnableFreeModelPreConsume bool    `json:"enable_free_model_pre_consume"` // 是否对免费模型启用预消耗
	TrustQuotaUSD             float64 `json:"trust_quota_usd"`               // 钱包免预扣门槛，0 表示禁用
	PreConsumeMultiplier      float64 `json:"pre_consume_multiplier"`        // 预计输入费用的预扣倍率，仅影响预留
}

// MaxInputPreConsumeMultiplier bounds administrator-controlled reservation
// inflation before it reaches quota arithmetic. A 100x ceiling is deliberately
// generous for conservative estimates while rejecting values that can turn a
// normal request into an avoidable quota overflow.
const MaxInputPreConsumeMultiplier = 100.0

// 默认配置
var quotaSetting = QuotaSetting{
	EnableFreeModelPreConsume: true,
	TrustQuotaUSD:             10,
	PreConsumeMultiplier:      1,
}

func init() {
	// 注册到全局配置管理器
	config.GlobalConfig.Register("quota_setting", &quotaSetting)
}

func GetQuotaSetting() *QuotaSetting {
	return &quotaSetting
}

// ValidateQuotaOption validates reservation settings before they are persisted.
func ValidateQuotaOption(key, value string) error {
	if key != "quota_setting.trust_quota_usd" && key != "quota_setting.pre_consume_multiplier" {
		return nil
	}
	number, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) || number < 0 {
		return fmt.Errorf("%s must be a finite non-negative number", key)
	}
	if key == "quota_setting.pre_consume_multiplier" {
		if err := validateInputPreConsumeMultiplier(number); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
	}
	return nil
}

// InputPreConsumeMultiplier rejects invalid runtime settings as well as invalid saves.
func InputPreConsumeMultiplier() (float64, error) {
	multiplier := quotaSetting.PreConsumeMultiplier
	if err := validateInputPreConsumeMultiplier(multiplier); err != nil {
		return 0, err
	}
	return multiplier, nil
}

func validateInputPreConsumeMultiplier(multiplier float64) error {
	if multiplier <= 0 || multiplier > MaxInputPreConsumeMultiplier || math.IsNaN(multiplier) || math.IsInf(multiplier, 0) {
		return fmt.Errorf("pre-consume multiplier must be finite, greater than zero, and at most %g", MaxInputPreConsumeMultiplier)
	}
	return nil
}
