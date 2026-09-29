package operation_setting

import "github.com/QuantumNous/new-api/setting/config"

// 额度展示类型
const (
	QuotaDisplayTypeUSD    = "USD"
	QuotaDisplayTypeCNY    = "CNY"
	QuotaDisplayTypeTokens = "TOKENS"
	QuotaDisplayTypeCustom = "CUSTOM"
)

type GeneralSetting struct {
	DocsLink            string `json:"docs_link"`
	PingIntervalEnabled bool   `json:"ping_interval_enabled"`
	PingIntervalSeconds int    `json:"ping_interval_seconds"`
	// 非流式响应的保活（2026-09-30）。非流式请求在整段生成期间不写任何字节，
	// 而按「两次数据之间的空档」计时的中间层会在生成完成前断开：EdgeOne 的回源
	// 响应超时是硬上限 600s（官方文档：可配 5–600，默认 15），没有字节就回 524，
	// 客户看到「非流请求 600s 超时」。开启后，等待上游生成期间按间隔让连接上有数据。
	// 只作用于文本补全类非流式请求；音频/图片/任务型响应绝不写入
	// （见 relay/channel.nonStreamKeepAliveApplies）。默认关闭。
	//
	// Seconds 是**首次探针的延迟**，同时等于「请求在多久之后失败会失去真实状态码」
	// 的窗口：默认 300s 距 600s 上限留 2 倍余量（nginx 侧 proxy_read_timeout
	// 86400s，不构成限制），并把该窗口比 60s 缩小 5 倍。<= 0 视为关闭。
	//
	// Mode 选择机制：whitespace（默认，往正文写 JSON 合法空白；并发上无害，但首个
	// 字节即提交状态，之后失败只能以 200 + 错误 JSON 报出）或 interim（发 103 中间
	// 响应，**不提交最终状态**，迟到失败仍能返回真实状态码；代价是发 1xx 会让
	// net/http 读响应头 map，依赖写守卫把并发窗口压到可忽略）。interim 需先在真实
	// 链路验证一次 EdgeOne 是否把它算作「有数据响应」再启用。
	NonStreamKeepAliveEnabled bool   `json:"non_stream_keep_alive_enabled"`
	NonStreamKeepAliveSeconds int    `json:"non_stream_keep_alive_seconds"`
	NonStreamKeepAliveMode    string `json:"non_stream_keep_alive_mode"`
	// 当前站点额度展示类型：USD / CNY / TOKENS
	QuotaDisplayType string `json:"quota_display_type"`
	// 自定义货币符号，用于 CUSTOM 展示类型
	CustomCurrencySymbol string `json:"custom_currency_symbol"`
	// 自定义货币与美元汇率（1 USD = X Custom）
	CustomCurrencyExchangeRate float64 `json:"custom_currency_exchange_rate"`
}

// 默认配置
var generalSetting = GeneralSetting{
	DocsLink:                   "https://docs.newapi.pro",
	PingIntervalEnabled:        false,
	PingIntervalSeconds:        60,
	NonStreamKeepAliveEnabled:  false,
	NonStreamKeepAliveSeconds:  300,
	NonStreamKeepAliveMode:     "whitespace",
	QuotaDisplayType:           QuotaDisplayTypeUSD,
	CustomCurrencySymbol:       "¤",
	CustomCurrencyExchangeRate: 1.0,
}

func init() {
	// 注册到全局配置管理器
	config.GlobalConfig.Register("general_setting", &generalSetting)
}

func GetGeneralSetting() *GeneralSetting {
	return &generalSetting
}

// IsCurrencyDisplay 是否以货币形式展示（美元或人民币）
func IsCurrencyDisplay() bool {
	return generalSetting.QuotaDisplayType != QuotaDisplayTypeTokens
}

// IsCNYDisplay 是否以人民币展示
func IsCNYDisplay() bool {
	return generalSetting.QuotaDisplayType == QuotaDisplayTypeCNY
}

// GetQuotaDisplayType 返回额度展示类型
func GetQuotaDisplayType() string {
	return generalSetting.QuotaDisplayType
}

// GetCurrencySymbol 返回当前展示类型对应符号
func GetCurrencySymbol() string {
	switch generalSetting.QuotaDisplayType {
	case QuotaDisplayTypeUSD:
		return "$"
	case QuotaDisplayTypeCNY:
		return "¥"
	case QuotaDisplayTypeCustom:
		if generalSetting.CustomCurrencySymbol != "" {
			return generalSetting.CustomCurrencySymbol
		}
		return "¤"
	default:
		return ""
	}
}

// GetUsdToCurrencyRate 返回 1 USD = X <currency> 的 X（TOKENS 不适用）
func GetUsdToCurrencyRate(usdToCny float64) float64 {
	switch generalSetting.QuotaDisplayType {
	case QuotaDisplayTypeUSD:
		return 1
	case QuotaDisplayTypeCNY:
		return usdToCny
	case QuotaDisplayTypeCustom:
		if generalSetting.CustomCurrencyExchangeRate > 0 {
			return generalSetting.CustomCurrencyExchangeRate
		}
		return 1
	default:
		return 1
	}
}
