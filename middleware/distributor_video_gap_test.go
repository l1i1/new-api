package middleware

import (
	"net/http"
	"testing"

	taskdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/assert"
)

// The distributor chooses a channel before the relay handler runs official-fit
// validation, so a definite media-capability gap must map to the designed
// video_request rejection. Before ChannelSelectionVideoUnsupported existed the
// wrapped error fell into the generic 500 branch, which is what the customer's
// video test received on 2026-10-21.
func TestChannelSelectionFailureResponseMapsVideoCapabilityGap(t *testing.T) {
	err := &service.ChannelSelectError{
		Err: &service.ChannelSelectionError{Kind: service.ChannelSelectionVideoUnsupported},
	}
	status, code := channelSelectionFailureResponse(err)
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, types.ErrorCode(taskdto.FilterVideoRequest), code)

	// The kind is only reachable through the selector's definite gap; an
	// unprovable exhaustion keeps its own honest answer (500 to the writer,
	// which the controller replaces with the last real upstream verdict).
	unprovable := &service.ChannelSelectError{
		Err: &service.ChannelSelectionError{Kind: service.ChannelSelectionUnprovableExhaustion},
	}
	status, _ = channelSelectionFailureResponse(unprovable)
	assert.Equal(t, http.StatusInternalServerError, status)
}
