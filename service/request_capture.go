package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/setting/operation_setting"
)

// Targeted request capture.
//
// The counters the platform already keeps answer "how many" and "how fast", and nothing else. When
// the question is "which key source did the affinity layer pick", "what does this client's message
// array actually look like" or "does this caller send a session id at all", the only honest way to
// answer is to look at a few real requests. Inferring it from indirect signals is what this file
// exists to stop doing.
//
// Shape of the thing:
//
//	the node that served the request matches the rules, builds a payload, and pushes it onto a Redis
//	list - Redis is already shared by every node, so this needs no new endpoint, no service-to-service
//	auth and no addressing of ephemeral instances;
//	the master drains that list and writes each payload as a file, because the panel can only read
//	files on the machine it runs on;
//	nothing here runs unless an operator enabled the feature and wrote a rule, and every rule is
//	bounded by its own limit, window, byte cap and retention.
type RequestCapturePayload struct {
	CapturedAt int64  `json:"captured_at"`
	RequestID  string `json:"request_id"`
	RuleName   string `json:"rule"`
	TokenID    int    `json:"token_id"`
	UserID     int    `json:"user_id"`
	Model      string `json:"model"`
	Path       string `json:"path"`
	ChannelID  int    `json:"channel_id"`
	Node       string `json:"node,omitempty"`

	RequestHeaders   map[string]string `json:"request_headers,omitempty"`
	RequestBody      string            `json:"request_body,omitempty"`
	RequestTruncated bool              `json:"request_body_truncated,omitempty"`

	ResponseStatus    int               `json:"response_status,omitempty"`
	ResponseHeaders   map[string]string `json:"response_headers,omitempty"`
	ResponseBody      string            `json:"response_body,omitempty"`
	ResponseTruncated bool              `json:"response_body_truncated,omitempty"`
}

var captureRegexCache sync.Map // pattern -> *regexp.Regexp

func captureRegex(pattern string) *regexp.Regexp {
	if v, ok := captureRegexCache.Load(pattern); ok {
		if re, ok := v.(*regexp.Regexp); ok {
			return re
		}
		return nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		captureRegexCache.Store(pattern, nil)
		return nil
	}
	captureRegexCache.Store(pattern, re)
	return re
}

func captureMatchesAny(patterns []string, value string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		if re := captureRegex(p); re != nil && re.MatchString(value) {
			return true
		}
	}
	return false
}

func captureContainsInt(values []int, want int) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

// captureStatusRanges caches the parsed form of a rule's status filter. The filter lives in a
// hot-reloaded setting, so parsing it on every capture decision would repeat the same work.
var captureStatusRanges sync.Map // filter string -> []operation_setting.StatusCodeRange

// CaptureStatusAllowed reports whether a response status passes the rule's filter. An empty filter
// admits everything, and an unreadable one is logged and then treated as empty rather than as
// "capture nothing": a typo in a debugging rule must not silently disable the debugging.
func CaptureStatusAllowed(rule *operation_setting.RequestCaptureRule, status int) bool {
	filter := strings.TrimSpace(rule.ResponseStatusIn)
	if filter == "" {
		return true
	}
	var ranges []operation_setting.StatusCodeRange
	if cached, ok := captureStatusRanges.Load(filter); ok {
		ranges, _ = cached.([]operation_setting.StatusCodeRange)
	} else {
		parsed, err := operation_setting.ParseHTTPStatusCodeRanges(filter)
		if err != nil {
			logger.LogWarn(context.Background(), fmt.Sprintf("request capture rule %q has an unreadable status filter %q: %v", rule.Name, filter, err))
			parsed = nil
		}
		captureStatusRanges.Store(filter, parsed)
		ranges = parsed
	}
	if len(ranges) == 0 {
		return true
	}
	for _, r := range ranges {
		if status >= r.Start && status <= r.End {
			return true
		}
	}
	return false
}

// MatchRequestCaptureRule returns the first enabled rule whose conditions this request satisfies, or
// nil. An empty condition list means "any", so a rule that sets nothing matches everything - which is
// allowed on purpose (an operator may want a broad sample) and bounded by the rule's own limit.
func MatchRequestCaptureRule(tokenID, userID int, model, path string) *operation_setting.RequestCaptureRule {
	setting := operation_setting.GetRequestCaptureSetting()
	if setting == nil || !setting.Enabled {
		return nil
	}
	for i := range setting.Rules {
		rule := &setting.Rules[i]
		if !rule.Enabled || strings.TrimSpace(rule.Name) == "" {
			continue
		}
		if len(rule.TokenIDs) > 0 && !captureContainsInt(rule.TokenIDs, tokenID) {
			continue
		}
		if len(rule.UserIDs) > 0 && !captureContainsInt(rule.UserIDs, userID) {
			continue
		}
		if !captureMatchesAny(rule.ModelRegex, model) {
			continue
		}
		if !captureMatchesAny(rule.PathRegex, path) {
			continue
		}
		return rule
	}
	return nil
}

// CaptureBudgetAllows counts this capture against the rule's window and reports whether the rule may
// still take it. The count lives in Redis rather than in process memory because the fleet serves the
// same traffic from several nodes, and a limit of twenty that each node applies separately is a
// limit of twenty per node.
func CaptureBudgetAllows(ctx context.Context, rule *operation_setting.RequestCaptureRule) bool {
	if !common.RedisEnabled || common.RDB == nil {
		// Without Redis the shared budget cannot be enforced; the writer still bounds what lands on
		// disk, and a capture that is enqueued but never drained costs nothing.
		return true
	}
	window := int64(rule.EffectiveWindowSeconds())
	bucket := time.Now().Unix() / window
	key := fmt.Sprintf("request_capture:count:%s:%d", rule.Name, bucket)
	pipe := common.RDB.TxPipeline()
	incr := pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, time.Duration(window)*time.Second)
	if _, err := pipe.Exec(ctx); err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("request capture budget failed for rule %q: %v", rule.Name, err))
		return false
	}
	return incr.Val() <= int64(rule.EffectiveLimit())
}

// EnqueueRequestCapture hands a payload to the master through the shared Redis list. It is called on
// the request path, so it never blocks on the network longer than the queue write itself, and the
// caller runs it off the critical path.
func EnqueueRequestCapture(ctx context.Context, payload *RequestCapturePayload) error {
	if !common.RedisEnabled || common.RDB == nil {
		return fmt.Errorf("request capture needs redis to carry payloads to the master")
	}
	setting := operation_setting.GetRequestCaptureSetting()
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	key := setting.EffectiveQueueKey()
	pipe := common.RDB.TxPipeline()
	pipe.RPush(ctx, key, raw)
	// A queue that only grows is a way to exhaust Redis when the master is down or not draining.
	// Keeping the newest entries and expiring the key bounds the damage: a capture payload is roughly
	// its bodies plus headers, so the worst case here is 500 x (2 x MaxBytes + headers) - about 300MB
	// at the default 256KB cap, and far less in practice, where a chat body is kilobytes.
	pipe.LTrim(ctx, key, -500, -1)
	pipe.Expire(ctx, key, time.Duration(setting.EffectiveRetentionHours())*time.Hour)
	if _, err := pipe.Exec(ctx); err != nil {
		return err
	}
	return nil
}

// captureFileName is date-partitioned so retention can delete whole directories, and carries the
// request id and rule so a reader knows what a file is without opening it.
func captureFileName(payload *RequestCapturePayload) string {
	ts := time.Unix(payload.CapturedAt, 0)
	name := fmt.Sprintf("%s-%s.json", ts.Format("150405"), sanitizeCaptureName(payload.RequestID))
	if payload.RuleName != "" {
		name = fmt.Sprintf("%s-%s.json", ts.Format("150405"), sanitizeCaptureName(payload.RuleName+"-"+payload.RequestID))
	}
	return filepath.Join(ts.Format("2006-01-02"), name)
}

func sanitizeCaptureName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "capture"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if len(out) > 96 {
		out = out[:96]
	}
	return out
}

// WriteRequestCapture stores one payload under the capture directory. The file is written 0600 and
// named with a .tmp suffix first, so a reader never sees a half-written capture.
func WriteRequestCapture(setting *operation_setting.RequestCaptureSetting, payload *RequestCapturePayload) (string, error) {
	dir := filepath.Join(setting.EffectiveDirectory(), filepath.Dir(captureFileName(payload)))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	raw, err := json.MarshalIndent(payload, "", " ")
	if err != nil {
		return "", err
	}
	final := filepath.Join(setting.EffectiveDirectory(), captureFileName(payload))
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return final, nil
}

// PruneRequestCaptures removes whole date directories older than the retention. Deleting by directory
// rather than by file keeps the work proportional to days, not to captures.
func PruneRequestCaptures(setting *operation_setting.RequestCaptureSetting, now time.Time) (int, error) {
	root := setting.EffectiveDirectory()
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	cutoff := now.Add(-time.Duration(setting.EffectiveRetentionHours()) * time.Hour)
	removed := 0
	for _, entry := range entries {
		dir := filepath.Join(root, entry.Name())
		if !entry.IsDir() {
			continue
		}
		day, err := time.ParseInLocation("2006-01-02", entry.Name(), time.Local)
		if err != nil {
			continue
		}
		// A directory whose whole day has left the window goes in one call.
		if !day.Add(24 * time.Hour).After(cutoff) {
			if err := os.RemoveAll(dir); err != nil {
				return removed, err
			}
			removed++
			continue
		}
		// Day directories alone would let a file live until its day leaves the window - up to twice
		// the configured retention. The window is the promise, so sweep inside the directory too.
		fileErr := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			info, statErr := d.Info()
			if statErr != nil {
				return nil
			}
			if info.ModTime().Before(cutoff) {
				if removeErr := os.Remove(path); removeErr == nil {
					removed++
				}
			}
			return nil
		})
		if fileErr != nil {
			return removed, fileErr
		}
	}
	return removed, nil
}

// ListRequestCaptures returns the captures on this machine, newest first. It reads the directory the
// master writes, so it answers only for the node it runs on - which is the point of routing every
// capture to the master.
func ListRequestCaptures(setting *operation_setting.RequestCaptureSetting, limit int) ([]CaptureFileInfo, error) {
	root := setting.EffectiveDirectory()
	var out []CaptureFileInfo
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".json") {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		out = append(out, CaptureFileInfo{Name: rel, Size: info.Size(), ModifiedAt: info.ModTime().Unix()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModifiedAt > out[j].ModifiedAt })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

type CaptureFileInfo struct {
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	ModifiedAt int64  `json:"modified_at"`
}

// ReadRequestCapture returns one capture by its relative name. The name is resolved inside the
// capture directory and rejected if it escapes, because a path that comes from a request is a path
// an attacker chooses.
func ReadRequestCapture(setting *operation_setting.RequestCaptureSetting, name string) ([]byte, error) {
	root := setting.EffectiveDirectory()
	full := filepath.Join(root, filepath.Clean("/"+name))
	rel, err := filepath.Rel(root, full)
	if err != nil || strings.HasPrefix(rel, "..") {
		return nil, fmt.Errorf("capture name escapes the capture directory")
	}
	return os.ReadFile(full)
}

// StartRequestCaptureWriter drains the shared queue into files. Only the master runs it: the panel
// reads files from the machine it runs on, so exactly one node must own the disk.
func StartRequestCaptureWriter(ctx context.Context) {
	if !common.IsMasterNode {
		return
	}
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		pruneTicker := time.NewTicker(10 * time.Minute)
		defer pruneTicker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				drainRequestCaptureQueue(ctx)
			case <-pruneTicker.C:
				setting := operation_setting.GetRequestCaptureSetting()
				if setting == nil || !setting.Enabled {
					continue
				}
				if removed, err := PruneRequestCaptures(setting, time.Now()); err != nil {
					logger.LogWarn(ctx, fmt.Sprintf("request capture prune failed: %v", err))
				} else if removed > 0 {
					logger.LogInfo(ctx, fmt.Sprintf("request capture pruned %d expired day(s)", removed))
				}
			}
		}
	}()
}

func drainRequestCaptureQueue(ctx context.Context) {
	if !common.RedisEnabled || common.RDB == nil {
		return
	}
	setting := operation_setting.GetRequestCaptureSetting()
	if setting == nil || !setting.Enabled {
		return
	}
	key := setting.EffectiveQueueKey()
	for i := 0; i < 200; i++ {
		raw, err := common.RDB.LPop(ctx, key).Bytes()
		if err != nil {
			return // empty queue, or Redis is unhappy; the next tick tries again
		}
		var payload RequestCapturePayload
		if err := json.Unmarshal(raw, &payload); err != nil {
			logger.LogWarn(ctx, fmt.Sprintf("request capture payload undecodable: %v", err))
			continue
		}
		if _, err := WriteRequestCapture(setting, &payload); err != nil {
			logger.LogWarn(ctx, fmt.Sprintf("request capture write failed: %v", err))
			// Put it back at the head so a transient disk problem does not lose the capture, and
			// stop this tick to avoid spinning on the same failure.
			_ = common.RDB.LPush(ctx, key, raw).Err()
			return
		}
	}
}
