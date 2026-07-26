package constant

type MultiKeyMode string

const (
	MultiKeyModeRandom   MultiKeyMode = "random"   // 随机
	MultiKeyModePolling  MultiKeyMode = "polling"  // 轮询
	MultiKeyModeAffinity MultiKeyMode = "affinity" // 按令牌稳定选择
)

func (mode MultiKeyMode) IsValid() bool {
	switch mode {
	case MultiKeyModeRandom, MultiKeyModePolling, MultiKeyModeAffinity:
		return true
	default:
		return false
	}
}
