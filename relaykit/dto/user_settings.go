package dto

import (
	"strings"
)

type UserSetting struct {
	NotifyType                       string             `json:"notify_type,omitempty"`                          // QuotaWarningType 额度预警类型
	QuotaWarningThreshold            float64            `json:"quota_warning_threshold,omitempty"`              // QuotaWarningThreshold 额度预警阈值
	WebhookUrl                       string             `json:"webhook_url,omitempty"`                          // WebhookUrl webhook地址
	WebhookSecret                    string             `json:"webhook_secret,omitempty"`                       // WebhookSecret webhook密钥
	NotificationEmail                string             `json:"notification_email,omitempty"`                   // NotificationEmail 通知邮箱地址
	BarkUrl                          string             `json:"bark_url,omitempty"`                             // BarkUrl Bark推送URL
	GotifyUrl                        string             `json:"gotify_url,omitempty"`                           // GotifyUrl Gotify服务器地址
	GotifyToken                      string             `json:"gotify_token,omitempty"`                         // GotifyToken Gotify应用令牌
	GotifyPriority                   int                `json:"gotify_priority"`                                // GotifyPriority Gotify消息优先级
	UpstreamModelUpdateNotifyEnabled bool               `json:"upstream_model_update_notify_enabled,omitempty"` // 是否接收上游模型更新定时检测通知（仅管理员）
	AcceptUnsetRatioModel            bool               `json:"accept_unset_model_ratio_model,omitempty"`       // AcceptUnsetRatioModel 是否接受未设置价格的模型
	RecordIpLog                      bool               `json:"record_ip_log,omitempty"`                        // 是否记录请求和错误日志IP
	SidebarModules                   string             `json:"sidebar_modules,omitempty"`                      // SidebarModules 左侧边栏模块配置
	BillingPreference                string             `json:"billing_preference,omitempty"`                   // BillingPreference 扣费策略（订阅/钱包）
	Language                         string             `json:"language,omitempty"`                             // Language 用户语言偏好 (zh, en)
	OfficialFit                      *OfficialFitConfig `json:"official_fit,omitempty"`                         // OfficialFit 官方一致性模式配置（模型族 × 行为维度）
}

// OfficialFitConfig is the per-user official-fit (官方一致性) configuration.
// Profile keys are model-family match prefixes, e.g. "deepseek-v4" (covers
// both the v4 and v4.1 lines), "kimi-k3" or "*" (fallback for unmatched models).
type OfficialFitConfig struct {
	Profile map[string]OfficialFitProfile `json:"profile,omitempty"`
}

const (
	// deepSeekV4FitKey is the canonical DeepSeek family prefix. It omits the
	// trailing dash so the dotted v4.1 line matches alongside v4.
	deepSeekV4FitKey = "deepseek-v4"
	// legacyDeepSeekV4FitKey is the dash-terminated prefix used before the
	// family widened. It is accepted as an alias so stored profiles keep
	// working without a data migration.
	legacyDeepSeekV4FitKey = "deepseek-v4-"
)

// OfficialFitProfile declares which official-fit behaviors apply to a model
// family. Everything defaults off: a zero profile keeps the platform's
// compatible behavior.
type OfficialFitProfile struct {
	Validate bool `json:"validate,omitempty"` // 官方参数校验：本地按官方拦截（该 400 就 400）
	Errors   bool `json:"errors,omitempty"`   // 错误消息官方原文：不附加网关 request id
	Shape    bool `json:"shape,omitempty"`    // 响应形态拟合：官方 usage 结构/SSE 拼接/剥离扩展字段
	Route    bool `json:"route,omitempty"`    // 官方渠道固定路由：整族请求 pin 到官方渠道
}

// OfficialFitProfileFor returns the most specific profile matching model.
// Match order: exact key > longest prefix key > "*". The legacy DeepSeek key
// "deepseek-v4-" is accepted as an alias of "deepseek-v4".
func (s *UserSetting) OfficialFitProfileFor(model string) (OfficialFitProfile, bool) {
	if s == nil || s.OfficialFit == nil || len(s.OfficialFit.Profile) == 0 {
		return OfficialFitProfile{}, false
	}
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return OfficialFitProfile{}, false
	}
	var prefixProfile OfficialFitProfile
	bestPrefixLen := -1
	bestIsAlias := false
	for key, p := range s.OfficialFit.Profile {
		k := strings.ToLower(strings.TrimSpace(key))
		if k == "" {
			continue
		}
		// The DeepSeek family key used to be dash-terminated, which does not
		// prefix-match the dotted v4.1 names. Treat it as the canonical
		// dash-free key so pre-v4.1 profiles keep covering the whole family.
		isAlias := k == legacyDeepSeekV4FitKey
		if isAlias {
			k = deepSeekV4FitKey
		}
		// An exact key is simply the longest possible prefix, so plain prefix
		// matching covers both cases.
		if !strings.HasPrefix(m, k) {
			continue
		}
		// A longer prefix wins; at equal length the canonical key beats the
		// legacy alias so map iteration order cannot change the result.
		betterLength := len(k) > bestPrefixLen
		betterTieBreak := len(k) == bestPrefixLen && bestIsAlias && !isAlias
		if betterLength || betterTieBreak {
			prefixProfile, bestPrefixLen, bestIsAlias = p, len(k), isAlias
		}
	}
	if bestPrefixLen >= 0 {
		return prefixProfile, true
	}
	if p, ok := s.OfficialFit.Profile["*"]; ok {
		return p, true
	}
	return OfficialFitProfile{}, false
}

var (
	NotifyTypeEmail   = "email"   // Email 邮件
	NotifyTypeWebhook = "webhook" // Webhook
	NotifyTypeBark    = "bark"    // Bark 推送
	NotifyTypeGotify  = "gotify"  // Gotify 推送
)
