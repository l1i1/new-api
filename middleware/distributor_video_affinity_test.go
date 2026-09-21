package middleware

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	appI18n "github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	operation_setting "github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// setupVideoAffinityDB gives this test a migrated SQLite database and, via
// model.InitDB, the dialect-specific column names the ability queries build
// their SQL with. The existing setupOriginTaskDB helper opens its own database
// and never initializes those names, which is fine for the pin tests (they
// never query abilities) but breaks video narrowing, whose whole point is an
// ability query filtered by channel settings.
func setupVideoAffinityDB(t *testing.T) {
	t.Helper()
	previousDB := model.DB
	previousType := common.MainDatabaseType()
	previousRedis := common.RedisEnabled
	previousMemoryCache := common.MemoryCacheEnabled
	previousPath := common.SQLitePath
	common.RedisEnabled = false
	// The overseas fleet runs with the memory cache off, so channel selection
	// goes through the database path — the path this regression lives on.
	common.MemoryCacheEnabled = false
	common.SQLitePath = filepath.Join(t.TempDir(), "video-affinity.db")
	require.NoError(t, model.InitDB())
	require.NoError(t, model.DB.AutoMigrate(&model.Channel{}, &model.Ability{}, &model.GroupAccessPolicy{}))
	testDB := model.DB
	t.Cleanup(func() {
		// Close before TempDir cleanup: an open handle makes the directory
		// removal fail on Windows.
		if sqlDB, err := testDB.DB(); err == nil {
			_ = sqlDB.Close()
		}
		model.DB = previousDB
		common.SetMainDatabaseType(previousType)
		common.RedisEnabled = previousRedis
		common.MemoryCacheEnabled = previousMemoryCache
		common.SQLitePath = previousPath
	})
}

// insertVideoCapableChannel inserts a channel serving kimi-k3 in the default
// group, optionally declaring the video capability the way an operator would.
func insertVideoCapableChannel(t *testing.T, name string, priority int64, supportsVideo bool) *model.Channel {
	t.Helper()
	settings := "{}"
	if supportsVideo {
		settings = `{"supports_video":true}`
	}
	channel := &model.Channel{
		Name:          name,
		Key:           "sk-" + name,
		Status:        common.ChannelStatusEnabled,
		Type:          constant.ChannelTypeOpenAI,
		Models:        "kimi-k3",
		Group:         "default",
		OtherSettings: settings,
	}
	priorityCopy := priority
	channel.Priority = &priorityCopy
	require.NoError(t, model.DB.Create(channel).Error)
	require.NoError(t, model.DB.Create(&model.Ability{
		Group:     "default",
		Model:     "kimi-k3",
		ChannelId: channel.Id,
		Enabled:   true,
		Priority:  &priorityCopy,
	}).Error)
	return channel
}

// TestDistributeVideoRequestFallsThroughMediaBlindAffinity is the regression
// for the overseas incident: a conversation had a text-traffic affinity binding
// to a channel with no video capability, and a later video request on the same
// conversation hit that binding. Because the bound channel fails the video
// constraint filter, the request must fall through to free selection and land on
// a channel that declared the capability — not fail with "no available
// channel", and not be pinned to the media-blind channel.
func TestDistributeVideoRequestFallsThroughMediaBlindAffinity(t *testing.T) {
	require.NoError(t, appI18n.Init())
	setupVideoAffinityDB(t)
	// Affinity keeps prompt caches warm, so the binding is normally the right
	// answer; this test is about the case where it is not.
	blind := insertVideoCapableChannel(t, "blind", 50, false)
	capable := insertVideoCapableChannel(t, "capable", 10, true)

	setting := operation_setting.GetChannelAffinitySetting()
	previousEnabled := setting.Enabled
	previousRules := setting.Rules
	setting.Enabled = true
	// One rule keyed by the calling token, matching every chat completion —
	// the shape the production setting ships.
	setting.Rules = []operation_setting.ChannelAffinityRule{{
		Name:              "chat completion affinity",
		ModelRegex:        []string{".*"},
		PathRegex:         []string{"^/v1/chat/completions"},
		KeySources:        []operation_setting.ChannelAffinityKeySource{{Type: "context_int", Key: "token_id"}},
		IncludeRuleName:   true,
		IncludeUsingGroup: true,
		IncludeModelName:  true,
	}}
	t.Cleanup(func() {
		setting.Enabled = previousEnabled
		setting.Rules = previousRules
	})

	newRequest := func() (*gin.Context, *httptest.ResponseRecorder) {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
			`{"model":"kimi-k3","messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"video_url","video_url":{"url":"data:video/mp4;base64,AAAA"}}]}]}`,
		))
		c.Request.Header.Set("Content-Type", "application/json")
		common.SetContextKey(c, constant.ContextKeyTokenId, 4242)
		common.SetContextKey(c, constant.ContextKeyUserGroup, "default")
		common.SetContextKey(c, constant.ContextKeyUsingGroup, "default")
		return c, recorder
	}

	// Record the binding the way text traffic would have: the lookup sets the
	// affinity context, and the recorder stores the channel it selected.
	seed, _ := newRequest()
	_, found := service.GetPreferredChannelByAffinity(seed, "kimi-k3", "default")
	require.False(t, found, "the binding must not exist before it is recorded")
	service.RecordChannelAffinity(seed, blind.Id)
	seeded, foundAgain := service.GetPreferredChannelByAffinity(seed, "kimi-k3", "default")
	require.True(t, foundAgain, "the text binding must be in place for this test to mean anything")
	require.Equal(t, blind.Id, seeded)

	// The video request must ignore that binding and select the capable channel.
	c, _ := newRequest()
	Distribute()(c)
	require.False(t, c.IsAborted(), "video request must not fail with no available channel")
	require.Equal(t, capable.Id, common.GetContextKeyInt(c, constant.ContextKeyChannelId),
		"a media-blind affinity binding must not capture a video request")

	// The text request keeps using its binding: capability narrowing only
	// applies to requests that carry video.
	textCtx, _ := newRequest()
	textCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"kimi-k3","messages":[{"role":"user","content":"hello"}]}`))
	textCtx.Request.Header.Set("Content-Type", "application/json")
	common.SetContextKey(textCtx, constant.ContextKeyTokenId, 4242)
	common.SetContextKey(textCtx, constant.ContextKeyUserGroup, "default")
	common.SetContextKey(textCtx, constant.ContextKeyUsingGroup, "default")
	Distribute()(textCtx)
	require.False(t, textCtx.IsAborted())
	require.Equal(t, blind.Id, common.GetContextKeyInt(textCtx, constant.ContextKeyChannelId),
		"text traffic must keep its affinity binding")
}
