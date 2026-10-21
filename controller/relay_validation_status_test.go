package controller

import (
	"errors"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// relayValidationError is the single mapping from a request-validation failure to
// the error the response writer renders. It must keep an official status a
// validator chose (the DeepSeek content-part enum failure is 422), while the
// historical defaults stay: plain errors and status-less wrappers are 400, and
// an oversized body is 413.
func TestRelayValidationErrorKeepsTheValidatorsStatus(t *testing.T) {
	official422 := types.WithOpenAIError(types.OpenAIError{
		Message: "Failed to deserialize the JSON body into the target type: messages[0]: unknown variant `video_url`, expected one of `text`, `image_url`, `file`",
		Type:    "invalid_request_error",
		Code:    "invalid_request_error",
	}, http.StatusUnprocessableEntity)

	got := relayValidationError(official422)
	require.NotNil(t, got)
	assert.Equal(t, http.StatusUnprocessableEntity, got.StatusCode,
		"the official 422 must reach the client unchanged")
	assert.Contains(t, got.ToOpenAIError().Message, "unknown variant `video_url`")

	t.Run("an explicit 400 stays 400", func(t *testing.T) {
		official400 := types.WithOpenAIError(types.OpenAIError{Message: "Empty input messages"}, http.StatusBadRequest)
		assert.Equal(t, http.StatusBadRequest, relayValidationError(official400).StatusCode)
	})

	t.Run("a plain error keeps the historical 400", func(t *testing.T) {
		got := relayValidationError(errors.New("field messages is required"))
		assert.Equal(t, http.StatusBadRequest, got.StatusCode)
	})

	t.Run("a status-less NewAPIError keeps the historical 400", func(t *testing.T) {
		// types.NewError without a status option leaves the constructor default,
		// which is the "no status chosen" marker; the mapping must not leak it.
		got := relayValidationError(types.NewError(errors.New("bad shape"), types.ErrorCodeInvalidRequest))
		assert.Equal(t, http.StatusBadRequest, got.StatusCode)
	})

	t.Run("an oversized body stays 413", func(t *testing.T) {
		got := relayValidationError(common.ErrRequestBodyTooLarge)
		assert.Equal(t, http.StatusRequestEntityTooLarge, got.StatusCode)
	})

	t.Run("the mapped error is usable as an OpenAI error body", func(t *testing.T) {
		got := relayValidationError(official422)
		oai := got.ToOpenAIError()
		assert.Equal(t, "invalid_request_error", oai.Type)
	})
}
