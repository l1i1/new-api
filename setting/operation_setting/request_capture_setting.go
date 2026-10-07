package operation_setting

import "github.com/QuantumNous/new-api/setting/config"

// RequestCaptureRule describes one targeted capture: which requests to keep a copy of, for how long
// the rule may keep capturing, and how much of each request may be kept.
//
// This is a debugging tool, not an observability feature. It exists because the questions that
// actually cost time - which key source the affinity layer picked, what shape a client's messages
// really have, whether a caller sends a session id at all - cannot be answered from counters, and
// the alternative is inferring them from indirect evidence and being wrong. Because the payload is a
// customer's prompt and the model's answer, every guard here is deliberate: the feature is off until
// an operator writes a rule, each rule stops after its own limit inside its own window, one capture
// cannot exceed MaxBytes, and the files are pruned after RetentionHours.
type RequestCaptureRule struct {
	Name          string   `json:"name"`
	Enabled       bool     `json:"enabled"`
	TokenIDs      []int    `json:"token_ids,omitempty"`
	UserIDs       []int    `json:"user_ids,omitempty"`
	ModelRegex    []string `json:"model_regex,omitempty"`
	PathRegex     []string `json:"path_regex,omitempty"`
	WindowSeconds int      `json:"window_seconds,omitempty"`
	Limit         int      `json:"limit,omitempty"`
	// ResponseStatusIn filters on the response status using the same range syntax the rest of the
	// platform uses ("400-599", "429,500-503"). Empty keeps every response the other conditions
	// already admitted. It exists because the interesting capture is usually a failure: a rule that
	// watches only errors stays useful at a small limit, while the same limit spent on successful
	// traffic documents nothing.
	ResponseStatusIn string                `json:"response_status_in,omitempty"`
	Include          RequestCaptureInclude `json:"include"`
	// RedactHeaders replaces the value of these request headers. Authorization is always added: a
	// capture that leaks a live API key into a file on disk is worse than the bug it was taken for.
	RedactHeaders []string `json:"redact_headers,omitempty"`
}

type RequestCaptureInclude struct {
	RequestHeaders  bool `json:"request_headers"`
	RequestBody     bool `json:"request_body"`
	ResponseHeaders bool `json:"response_headers"`
	ResponseBody    bool `json:"response_body"`
}

// EffectiveLimit is the number of captures a rule may take inside one window. A rule that does not
// set one is bounded anyway - an unbounded capture rule is a way to fill a disk by accident.
func (r RequestCaptureRule) EffectiveLimit() int {
	if r.Limit > 0 {
		return r.Limit
	}
	return defaultCaptureLimit
}

func (r RequestCaptureRule) EffectiveWindowSeconds() int {
	if r.WindowSeconds > 0 {
		return r.WindowSeconds
	}
	return defaultCaptureWindowSeconds
}

const (
	defaultCaptureLimit         = 20
	defaultCaptureWindowSeconds = 600
	defaultCaptureMaxBytes      = 256 << 10
	defaultCaptureRetentionHour = 24
)

type RequestCaptureSetting struct {
	Enabled bool                 `json:"enabled"`
	Rules   []RequestCaptureRule `json:"rules"`
	// Directory is where the master writes captures. It is a path inside the container unless the
	// host bind-mounts one over it; the operator-facing note in the docs says so, because a capture
	// directory that silently disappears on the next release is a debugging tool that lies.
	Directory string `json:"directory"`
	// MaxBytes caps each captured body. A larger body is truncated and the payload records that it
	// was, so a reader never mistakes a partial capture for a complete one.
	MaxBytes int `json:"max_bytes"`
	// RetentionHours is how long captured files live before the writer prunes them.
	RetentionHours int `json:"retention_hours"`
	// QueueKey is the shared Redis list that carries captures from whichever node served the
	// request to the master, which owns the files.
	QueueKey string `json:"queue_key"`
}

func (s RequestCaptureSetting) EffectiveDirectory() string {
	if s.Directory != "" {
		return s.Directory
	}
	return "/data/request-captures"
}

func (s RequestCaptureSetting) EffectiveMaxBytes() int {
	if s.MaxBytes > 0 {
		return s.MaxBytes
	}
	return defaultCaptureMaxBytes
}

func (s RequestCaptureSetting) EffectiveRetentionHours() int {
	if s.RetentionHours > 0 {
		return s.RetentionHours
	}
	return defaultCaptureRetentionHour
}

func (s RequestCaptureSetting) EffectiveQueueKey() string {
	if s.QueueKey != "" {
		return s.QueueKey
	}
	return "request_capture:queue"
}

var requestCaptureSetting = RequestCaptureSetting{
	// Off until an operator says otherwise. Capture is off by absence: no rule means no matching
	// work on the request path at all.
	Enabled: false,
	Rules:   []RequestCaptureRule{},
}

func init() {
	config.GlobalConfig.Register("request_capture", &requestCaptureSetting)
}

func GetRequestCaptureSetting() *RequestCaptureSetting {
	return &requestCaptureSetting
}
