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
	// 流式首字超时（秒）覆盖 RELAY_STREAM_RESPONSE_HEADER_TIMEOUT：流式尝试
	// 等待上游响应头/首字节的预算。上游接受请求后迟迟不开流，到点即按传输失败
	// 处理并换道重试。0 = 沿用启动时读到的环境变量（默认 300）。做成 DB 选项是
	// 因为上游首字延迟会随资源状况变化，运维需要在故障当晚调大、恢复后收紧，
	// 而容器环境变量不可热改（ECI 需换实例）。见 docs/stream-timeouts.md。
	StreamFirstByteTimeoutSeconds int `json:"stream_first_byte_timeout_seconds"`
	// 流式空闲超时（秒）覆盖 STREAMING_TIMEOUT：SSE 逐行重置的空闲看门狗
	// （也是「响应头已到但迟迟没有第一个数据帧」的兜底）。0 = 沿用启动时读到的
	// 环境变量（默认 300）。与上一个字段分开是因为两者代价不同：首字预算只影响
	// 换道前的等待，空闲超时还决定「流中途静默」多久判定为死流。
	StreamIdleTimeoutSeconds int `json:"stream_idle_timeout_seconds"`
	// 传输失败重试预算（秒）覆盖 RELAY_TRANSPORT_RETRY_BUDGET_SECONDS：一次请求
	// 在 do_request_failed（上游接受请求后既不回首字也不给响应）这类传输失败上
	// 最多能烧掉的墙钟时长。它决定「首字预算 × 可尝试次数」能否真正用满重试配额：
	// 首字预算 420s 时，600s 预算只容得下一次重试，第二条渠道会被预算提前掐断。
	// 0 = 沿用启动时读到的环境变量（默认 1800，约 4 次首字等待）。与首字/空闲超时
	// 分开，是因为它限制的是「整条重试链的时间」，不是单次尝试的等待。
	TransportRetryBudgetSeconds int `json:"transport_retry_budget_seconds"`
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
