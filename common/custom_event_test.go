package common

import (
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCustomEventWriteContentTypeDefaultsToEventStream(t *testing.T) {
	recorder := httptest.NewRecorder()

	(CustomEvent{}).WriteContentType(recorder)

	assert.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
}

func TestCustomEventWriteContentTypePreservesEventStreamParameters(t *testing.T) {
	recorder := httptest.NewRecorder()
	recorder.Header().Set("Content-Type", "text/event-stream; charset=utf-8")

	(CustomEvent{}).WriteContentType(recorder)

	assert.Equal(t, "text/event-stream; charset=utf-8", recorder.Header().Get("Content-Type"))
}

func TestCustomEventWriteContentTypeReplacesUnrelatedContentType(t *testing.T) {
	recorder := httptest.NewRecorder()
	recorder.Header().Set("Content-Type", "application/json")

	(CustomEvent{}).WriteContentType(recorder)

	assert.Equal(t, "text/event-stream", recorder.Header().Get("Content-Type"))
}
