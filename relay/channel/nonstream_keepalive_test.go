package channel

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// syncRecorder is an http.ResponseWriter that tolerates the test reading it while
// the keep-alive goroutine writes. The production writer needs no lock: doRequest
// waits for the goroutine to exit before anything else touches the response, so
// the only concurrent reader here is the assertion.
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

func newKeepAliveContext(t *testing.T) (*gin.Context, *syncRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := &syncRecorder{}
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	return c, recorder
}

// The gate protects two different things: the switch (off by default, so nothing
// changes until an operator asks for it) and the payload shape (a leading space
// is only safe in a JSON text response, never in audio or a task payload).
func TestNonStreamKeepAliveApplies(t *testing.T) {
	enabled := &operation_setting.GeneralSetting{NonStreamKeepAliveEnabled: true, NonStreamKeepAliveSeconds: 60}
	disabled := &operation_setting.GeneralSetting{NonStreamKeepAliveEnabled: false, NonStreamKeepAliveSeconds: 60}

	cases := []struct {
		name     string
		info     *relaycommon.RelayInfo
		settings *operation_setting.GeneralSetting
		want     bool
	}{
		{"off by default", &relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeChatCompletions}, disabled, false},
		{"nil info", nil, enabled, false},
		{"nil settings", &relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeChatCompletions}, nil, false},
		{"chat completions", &relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeChatCompletions}, enabled, true},
		{"responses", &relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeResponses}, enabled, true},
		{"embeddings", &relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeEmbeddings}, enabled, true},
		{"stream is handled by the SSE ping path", &relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeChatCompletions, IsStream: true}, enabled, false},
		{"tts is binary", &relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeAudioSpeech}, enabled, false},
		{"transcription is multipart/binary", &relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeAudioTranscription}, enabled, false},
		{"images are excluded", &relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeImagesGenerations}, enabled, false},
		{"task payloads are excluded", &relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeMidjourneyImagine}, enabled, false},
		{"realtime is a websocket", &relaycommon.RelayInfo{RelayMode: relayconstant.RelayModeRealtime}, enabled, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, nonStreamKeepAliveApplies(tc.info, tc.settings))
		})
	}
}

// A fast response must be byte-identical to one served with the feature off: the
// first probe is only due after the interval, and the handler stops the writer
// before writing the body.
func TestNonStreamKeepAliveWritesNothingBeforeInterval(t *testing.T) {
	c, recorder := newKeepAliveContext(t)

	stop, done := startNonStreamKeepAlive(c, time.Hour)
	stop()
	<-done

	assert.Empty(t, recorder.String(), "no probe may be written before the first interval")
}

// The probe has to actually reach the client (unflushed bytes would defeat the
// purpose), and it has to stop once the response is being written.
func TestNonStreamKeepAliveWritesProbesUntilStopped(t *testing.T) {
	c, recorder := newKeepAliveContext(t)

	stop, done := startNonStreamKeepAlive(c, 20*time.Millisecond)
	require.Eventually(t, func() bool { return recorder.Len() > 0 }, time.Second, 5*time.Millisecond,
		"a probe must appear once the interval elapses")

	stop()
	<-done
	settled := recorder.Len()
	time.Sleep(80 * time.Millisecond)
	assert.Equal(t, settled, recorder.Len(), "no probe may be written after stop()")

	// The probes are a prefix, so the real body still parses together with them.
	_, err := recorder.Write([]byte(`{"ok":true}`))
	require.NoError(t, err)
	assert.Regexp(t, `^ +\{\"ok\":true\}$`, recorder.String())
}
