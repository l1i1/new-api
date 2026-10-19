package operation_setting

import "github.com/QuantumNous/new-api/setting/config"

// PayloadNormalizeSetting controls a narrow, opt-in rewrite of the chat payload before it is
// relayed: the stable long block a client puts in the last user message is moved into the
// system message, which is the layout that reliably hits the upstream prompt cache.
//
// Measurements behind it (智谱 glm-5.3-flash, 315 KB document, 300 requests, concurrency 20,
// replaying the customer's own captured bytes): document in the last user message hits the
// cache 40-60% of the time; the same content in the system message hits 99%+. The provider's
// cache is prefix based, so the placement - not the relay path - decides the hit rate.
//
// This is not a formatting preference. Moving text between roles changes the model-visible
// authority of that text and can change the answer, so the feature is off by default and
// must be scoped to explicit token/channel IDs before it does anything at all. An empty
// scope means "nobody", never "everybody".
type PayloadNormalizeSetting struct {
	Enabled bool `json:"enabled"`
	// TokenIDs and ChannelIDs are allowlists. Both empty means no request matches.
	TokenIDs   []int `json:"token_ids,omitempty"`
	ChannelIDs []int `json:"channel_ids,omitempty"`
	// MinUserChars is the size floor of the user message (default 100000).
	MinUserChars int `json:"min_user_chars,omitempty"`
	// FingerprintChars is how many leading characters of the user message the fingerprint
	// covers (default 4096). FingerprintSHA256 is the hex sha256 of that prefix; an empty
	// digest disables the check and leaves the allowlists as the only guard.
	FingerprintChars  int    `json:"fingerprint_chars,omitempty"`
	FingerprintSHA256 string `json:"fingerprint_sha256,omitempty"`
	// DocumentChars marks where the stable block ends inside the user message. Zero moves the
	// whole user message into the system message and keeps the original system text as the
	// user turn.
	DocumentChars int `json:"document_chars,omitempty"`
}

var payloadNormalizeSetting = PayloadNormalizeSetting{
	Enabled: false,
}

func init() {
	config.GlobalConfig.Register("payload_normalize", &payloadNormalizeSetting)
}

func GetPayloadNormalizeSetting() *PayloadNormalizeSetting {
	return &payloadNormalizeSetting
}
