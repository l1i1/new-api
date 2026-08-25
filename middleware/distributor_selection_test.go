package middleware

import (
	"errors"
	"net/http"
	"testing"

	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/assert"
)

func TestChannelSelectionFailureResponse(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		statusCode int
		errorCode  types.ErrorCode
	}{
		{
			name:       "unknown model",
			err:        &service.ChannelSelectionError{Kind: service.ChannelSelectionModelNotConfigured},
			statusCode: http.StatusBadRequest,
			errorCode:  types.ErrorCodeModelNotFound,
		},
		{
			name:       "temporarily unavailable",
			err:        &service.ChannelSelectionError{Kind: service.ChannelSelectionTemporarilyUnavailable},
			statusCode: http.StatusServiceUnavailable,
			errorCode:  types.ErrorCodeGetChannelFailed,
		},
		{
			name:       "access denied",
			err:        &service.ChannelSelectionError{Kind: service.ChannelSelectionAccessDenied},
			statusCode: http.StatusForbidden,
			errorCode:  types.ErrorCodeAccessDenied,
		},
		{
			name:       "internal selection error",
			err:        &service.ChannelSelectionError{Kind: service.ChannelSelectionInternalError},
			statusCode: http.StatusInternalServerError,
			errorCode:  types.ErrorCodeGetChannelFailed,
		},
		{
			name:       "untyped error",
			err:        errors.New("database unavailable"),
			statusCode: http.StatusInternalServerError,
			errorCode:  types.ErrorCodeGetChannelFailed,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			statusCode, errorCode := channelSelectionFailureResponse(test.err)
			assert.Equal(t, test.statusCode, statusCode)
			assert.Equal(t, test.errorCode, errorCode)
		})
	}
}
