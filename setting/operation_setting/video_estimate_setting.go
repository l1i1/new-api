package operation_setting

import (
	"strings"

	"github.com/QuantumNous/new-api/setting/config"
)

// VideoEstimateSetting configures the optional provider tokenizer used to price
// video requests on channels whose upstream reports a prompt count that ignores
// the media (ChannelOtherSettings.VideoUsageMode == "estimate").
//
// It is an operator decision, not a default: calling the endpoint hands the
// user's clip to a second supplier for tokenization, so the feature stays inert
// until a key is configured here or in the environment.
type VideoEstimateSetting struct {
	// BaseURL is the tokenizer's API root, e.g. https://api.moonshot.cn/v1.
	// Empty falls back to the environment, then to the built-in default.
	BaseURL string `json:"base_url"`
	// APIKey authenticates the estimate call. It is stored like the other
	// administrator secrets (the options API never returns values whose key ends
	// in "Key"), so the browser sees a blank field and only a blank submit
	// leaves it untouched.
	APIKey string `json:"api_key"`
}

// VideoEstimateSettingModule is the option-module prefix: the persisted keys are
// "video_estimate_setting.base_url" and "video_estimate_setting.api_key".
const VideoEstimateSettingModule = "video_estimate_setting"

var videoEstimateSetting = VideoEstimateSetting{}

func init() {
	config.GlobalConfig.Register(VideoEstimateSettingModule, &videoEstimateSetting)
}

func GetVideoEstimateSetting() *VideoEstimateSetting {
	return &videoEstimateSetting
}

// TrimmedVideoEstimateBaseURL / TrimmedVideoEstimateAPIKey return the stored
// values with surrounding whitespace removed, so a pasted value with a trailing
// newline is usable as-is.
func TrimmedVideoEstimateBaseURL() string {
	return strings.TrimSpace(videoEstimateSetting.BaseURL)
}

func TrimmedVideoEstimateAPIKey() string {
	return strings.TrimSpace(videoEstimateSetting.APIKey)
}
