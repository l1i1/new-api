package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/require"
)

// enableCapture installs a capture setting for one test and restores the previous one, so the package
// level setting the middleware reads is never left pointing at a test fixture.
func enableCapture(t *testing.T, setting operation_setting.RequestCaptureSetting) {
	t.Helper()
	previous := *operation_setting.GetRequestCaptureSetting()
	*operation_setting.GetRequestCaptureSetting() = setting
	t.Cleanup(func() { *operation_setting.GetRequestCaptureSetting() = previous })
}

func TestMatchRequestCaptureRuleHonoursEachCondition(t *testing.T) {
	enableCapture(t, operation_setting.RequestCaptureSetting{
		Enabled: true,
		Rules: []operation_setting.RequestCaptureRule{
			{
				Name:       "only-token-7-kimi",
				Enabled:    true,
				TokenIDs:   []int{7},
				ModelRegex: []string{"^kimi-k3$"},
				PathRegex:  []string{"^/v1/chat/completions$"},
			},
		},
	})

	got := MatchRequestCaptureRule(7, 1, "kimi-k3", "/v1/chat/completions")
	require.NotNil(t, got, "a request meeting every condition must match")
	require.Equal(t, "only-token-7-kimi", got.Name)

	for name, args := range map[string][4]interface{}{
		"wrong token": {8, 1, "kimi-k3", "/v1/chat/completions"},
		"wrong model": {7, 1, "glm-5.3", "/v1/chat/completions"},
		"wrong path":  {7, 1, "kimi-k3", "/v1/messages"},
	} {
		t.Run(name, func(t *testing.T) {
			require.Nil(t, MatchRequestCaptureRule(args[0].(int), args[1].(int), args[2].(string), args[3].(string)))
		})
	}
}

func TestMatchRequestCaptureRuleStaysOffUntilEnabled(t *testing.T) {
	enableCapture(t, operation_setting.RequestCaptureSetting{
		Enabled: false,
		Rules: []operation_setting.RequestCaptureRule{
			{Name: "broad", Enabled: true},
		},
	})
	require.Nil(t, MatchRequestCaptureRule(1, 1, "kimi-k3", "/v1/chat/completions"),
		"the feature is off by default and a rule must not capture anything while it is")

	enableCapture(t, operation_setting.RequestCaptureSetting{
		Enabled: true,
		Rules: []operation_setting.RequestCaptureRule{
			{Name: "switched-off", Enabled: false},
		},
	})
	require.Nil(t, MatchRequestCaptureRule(1, 1, "kimi-k3", "/v1/chat/completions"),
		"a disabled rule inside an enabled feature captures nothing")
}

// A rule that sets no conditions matches everything, which is allowed - a broad sample is a
// legitimate thing to ask for - but only the first matching rule wins, so a specific rule placed
// before a broad one still narrows the capture.
func TestMatchRequestCaptureRuleTakesTheFirstMatch(t *testing.T) {
	enableCapture(t, operation_setting.RequestCaptureSetting{
		Enabled: true,
		Rules: []operation_setting.RequestCaptureRule{
			{Name: "specific", Enabled: true, ModelRegex: []string{"^kimi-k3$"}},
			{Name: "everything", Enabled: true},
		},
	})
	require.Equal(t, "specific", MatchRequestCaptureRule(1, 1, "kimi-k3", "/v1/chat/completions").Name)
	require.Equal(t, "everything", MatchRequestCaptureRule(1, 1, "glm-5.3", "/v1/chat/completions").Name)
}

func TestWriteAndReadRequestCapture(t *testing.T) {
	dir := t.TempDir()
	setting := &operation_setting.RequestCaptureSetting{Directory: dir, RetentionHours: 24}
	payload := &RequestCapturePayload{
		CapturedAt:  time.Now().Unix(),
		RequestID:   "req_abc",
		RuleName:    "debug k3",
		TokenID:     7,
		Model:       "kimi-k3",
		RequestBody: `{"model":"kimi-k3"}`,
	}

	full, err := WriteRequestCapture(setting, payload)
	require.NoError(t, err)

	info, err := os.Stat(full)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "a capture holds a customer prompt and must not be world readable")

	rel, err := filepath.Rel(dir, full)
	require.NoError(t, err)
	require.Contains(t, rel, time.Now().Format("2006-01-02"), "captures are partitioned by day so retention deletes directories")

	raw, err := ReadRequestCapture(setting, rel)
	require.NoError(t, err)
	var back RequestCapturePayload
	require.NoError(t, json.Unmarshal(raw, &back))
	require.Equal(t, payload.RequestID, back.RequestID)
	require.Equal(t, payload.RuleName, back.RuleName)
}

func TestReadRequestCaptureRefusesToEscapeItsDirectory(t *testing.T) {
	dir := t.TempDir()
	setting := &operation_setting.RequestCaptureSetting{Directory: dir}
	for _, name := range []string{"../outside.json", "../../etc/passwd", "a/../../b.json"} {
		_, err := ReadRequestCapture(setting, name)
		require.Error(t, err, "a name that escapes the capture directory must be refused: %s", name)
	}
}

func TestPruneRequestCapturesRemovesExpiredDaysOnly(t *testing.T) {
	dir := t.TempDir()
	setting := &operation_setting.RequestCaptureSetting{Directory: dir, RetentionHours: 24}
	now := time.Now()

	old := filepath.Join(dir, now.AddDate(0, 0, -3).Format("2006-01-02"))
	recent := filepath.Join(dir, now.Format("2006-01-02"))
	require.NoError(t, os.MkdirAll(old, 0o700))
	require.NoError(t, os.MkdirAll(recent, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(old, "x.json"), []byte("{}"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(recent, "y.json"), []byte("{}"), 0o600))

	removed, err := PruneRequestCaptures(setting, now)
	require.NoError(t, err)
	require.Equal(t, 1, removed)
	require.NoDirExists(t, old)
	require.DirExists(t, recent, "a day still inside the retention window must survive")

	// Nothing to prune twice: the loop must be idempotent, because it runs on a timer.
	removed, err = PruneRequestCaptures(setting, now)
	require.NoError(t, err)
	require.Equal(t, 0, removed)
}

func TestListRequestCapturesReturnsNewestFirstAndHonoursTheLimit(t *testing.T) {
	dir := t.TempDir()
	setting := &operation_setting.RequestCaptureSetting{Directory: dir}
	now := time.Now()
	for i := 0; i < 3; i++ {
		payload := &RequestCapturePayload{
			CapturedAt: now.Add(-time.Duration(i) * time.Minute).Unix(),
			RequestID:  "req",
			RuleName:   "rule" + string(rune('a'+i)),
		}
		_, err := WriteRequestCapture(setting, payload)
		require.NoError(t, err)
		time.Sleep(10 * time.Millisecond)
	}
	files, err := ListRequestCaptures(setting, 2)
	require.NoError(t, err)
	require.Len(t, files, 2)
	require.GreaterOrEqual(t, files[0].ModifiedAt, files[1].ModifiedAt)
}

func TestCaptureDefaultsBoundAnUnconfiguredRule(t *testing.T) {
	rule := operation_setting.RequestCaptureRule{}
	require.Equal(t, 20, rule.EffectiveLimit(), "a rule with no limit is still bounded")
	require.Equal(t, 600, rule.EffectiveWindowSeconds())

	setting := operation_setting.RequestCaptureSetting{}
	require.Equal(t, 256<<10, setting.EffectiveMaxBytes())
	require.Equal(t, 24, setting.EffectiveRetentionHours())
	require.NotEmpty(t, setting.EffectiveDirectory())
}

// Day directories alone would keep a file until its whole day leaves the window, which is up to twice
// the configured retention. The window is a promise about the data, so files inside a retained day
// must be swept too.
func TestPruneRequestCapturesSweepsExpiredFilesInsideARetainedDay(t *testing.T) {
	dir := t.TempDir()
	setting := &operation_setting.RequestCaptureSetting{Directory: dir, RetentionHours: 24}
	now := time.Now()

	// One file from two days ago parked in today's directory: the directory is inside the window, the
	// file is not.
	today := filepath.Join(dir, now.Format("2006-01-02"))
	require.NoError(t, os.MkdirAll(today, 0o700))
	stale := filepath.Join(today, "old.json")
	fresh := filepath.Join(today, "new.json")
	require.NoError(t, os.WriteFile(stale, []byte("{}"), 0o600))
	require.NoError(t, os.WriteFile(fresh, []byte("{}"), 0o600))
	old := now.Add(-48 * time.Hour)
	require.NoError(t, os.Chtimes(stale, old, old))

	removed, err := PruneRequestCaptures(setting, now)
	require.NoError(t, err)
	require.Equal(t, 1, removed)
	require.NoFileExists(t, stale, "a file past the retention window must go even though its day is kept")
	require.FileExists(t, fresh)
}
