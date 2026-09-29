package channel

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// syncRecorder is an http.ResponseWriter that tolerates the test reading it while
// the keep-alive goroutine writes. The production writer needs no lock: the write
// guard waits for the goroutine to exit before the body is written, so the only
// concurrent reader here is the assertion.
type syncRecorder struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncRecorder) Header() http.Header { return http.Header{} }

func (s *syncRecorder) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncRecorder) WriteHeader(int) {}

func (s *syncRecorder) Flush() {}

func (s *syncRecorder) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Len()
}

func (s *syncRecorder) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func newKeepAliveContext(t *testing.T, path string) (*gin.Context, *syncRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := &syncRecorder{}
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, path, nil)
	return c, recorder
}

// The gate protects two different things: the switch (off by default, so nothing
// changes until an operator asks for it) and the payload shape. It keys on
// RelayFormat plus a deny-list of binary/task modes, because an allow-list of
// RelayModes silently missed `/v1/messages` (Claude), whose RelayMode is
// RelayModeUnknown — the longest text path of all.
func TestNonStreamKeepAliveApplies(t *testing.T) {
	enabled := &operation_setting.GeneralSetting{NonStreamKeepAliveEnabled: true, NonStreamKeepAliveSeconds: 60}
	disabled := &operation_setting.GeneralSetting{NonStreamKeepAliveEnabled: false, NonStreamKeepAliveSeconds: 60}
	zeroInterval := &operation_setting.GeneralSetting{NonStreamKeepAliveEnabled: true, NonStreamKeepAliveSeconds: 0}

	openaiChat := func() *relaycommon.RelayInfo {
		return &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAI, RelayMode: relayconstant.RelayModeChatCompletions}
	}

	cases := []struct {
		name     string
		info     *relaycommon.RelayInfo
		settings *operation_setting.GeneralSetting
		want     bool
	}{
		{"off by default", openaiChat(), disabled, false},
		{"nil info", nil, enabled, false},
		{"nil settings", openaiChat(), nil, false},
		{"interval 0 means off, not 10s", openaiChat(), zeroInterval, false},
		{"negative interval means off", openaiChat(), &operation_setting.GeneralSetting{NonStreamKeepAliveEnabled: true, NonStreamKeepAliveSeconds: -5}, false},
		{"chat completions", openaiChat(), enabled, true},
		{"claude messages (RelayModeUnknown)", &relaycommon.RelayInfo{RelayFormat: types.RelayFormatClaude}, enabled, true},
		{"gemini", &relaycommon.RelayInfo{RelayFormat: types.RelayFormatGemini, RelayMode: relayconstant.RelayModeGemini}, enabled, true},
		{"responses", &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAIResponses, RelayMode: relayconstant.RelayModeResponses}, enabled, true},
		{"embeddings", &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAI, RelayMode: relayconstant.RelayModeEmbeddings}, enabled, true},
		{"stream is the SSE ping path's business", &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAI, RelayMode: relayconstant.RelayModeChatCompletions, IsStream: true}, enabled, false},
		{"unknown format is refused", &relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeChatCompletions}, enabled, false},
		{"tts is binary", &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAI, RelayMode: relayconstant.RelayModeAudioSpeech}, enabled, false},
		{"transcription is binary", &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAI, RelayMode: relayconstant.RelayModeAudioTranscription}, enabled, false},
		{"images are excluded", &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAI, RelayMode: relayconstant.RelayModeImagesGenerations}, enabled, false},
		{"alpha/search is a raw passthrough", &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAI, RelayMode: relayconstant.RelayModeAlphaSearch}, enabled, false},
		{"task payloads are excluded", &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAI, RelayMode: relayconstant.RelayModeMidjourneyImagine}, enabled, false},
		{"realtime is a websocket", &relaycommon.RelayInfo{RelayFormat: types.RelayFormatOpenAI, RelayMode: relayconstant.RelayModeRealtime}, enabled, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, nonStreamKeepAliveApplies(tc.info, tc.settings))
		})
	}
}

// Regression test for the review's first finding: the keep-alive used to stop as
// soon as doRequest returned, i.e. when the upstream HEADERS arrived — while the
// body, the part that actually takes minutes, is read afterwards. With an
// upstream that answers headers immediately and only produces the body later, the
// old code left the client silent for the whole wait. This drives the real
// doRequest and asserts probes flow during the body read and stop at the body.
func TestDoRequestKeepsProbingWhileUpstreamBodyIsSlow(t *testing.T) {
	// The relay's outbound client registry is normally initialised at process
	// start; doRequest needs it even for a direct (proxy-less) upstream.
	service.InitHttpClient()

	settings := operation_setting.GetGeneralSetting()
	previousEnabled, previousSeconds := settings.NonStreamKeepAliveEnabled, settings.NonStreamKeepAliveSeconds
	settings.NonStreamKeepAliveEnabled, settings.NonStreamKeepAliveSeconds = true, 1
	defer func() {
		settings.NonStreamKeepAliveEnabled, settings.NonStreamKeepAliveSeconds = previousEnabled, previousSeconds
	}()

	const bodyDelay = 2500 * time.Millisecond
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// Headers now, body much later: exactly the shape the old implementation
		// could not survive.
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		time.Sleep(bodyDelay)
		_, _ = w.Write([]byte(`{"id":"late"}`))
	}))
	defer upstream.Close()

	c, recorder := newKeepAliveContext(t, "/v1/chat/completions")
	// ChannelMeta carries ChannelSetting and RelayInfo embeds it as a pointer, so
	// doRequest dereferences it (production always populates it).
	info := &relaycommon.RelayInfo{
		RelayFormat: types.RelayFormatOpenAI,
		RelayMode:   relayconstant.RelayModeChatCompletions,
		ChannelMeta: &relaycommon.ChannelMeta{},
	}
	// A real body, because doRequest closes req.Body (production always sets one).
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, upstream.URL, strings.NewReader("{}"))
	require.NoError(t, err)

	start := time.Now()
	resp, err := doRequest(c, req, info)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Less(t, time.Since(start), bodyDelay, "doRequest must return at the headers")

	// While the handler waits for the body, the client must not go silent.
	require.Eventually(t, func() bool { return recorder.Len() > 0 }, bodyDelay, 20*time.Millisecond,
		"probes must flow after doRequest returned, while the body is still pending")

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, `{"id":"late"}`, string(body))

	// The handler now writes the response; the guard stops the probes first.
	if _, err := c.Writer.Write(body); err != nil {
		t.Fatalf("write body: %v", err)
	}
	settled := recorder.Len()
	time.Sleep(1500 * time.Millisecond)
	assert.Equal(t, settled, recorder.Len(), "no probe may be written after the real body")

	out := recorder.String()
	assert.True(t, bytes.HasSuffix([]byte(out), body), "the body must be the tail, uninterrupted")
}
