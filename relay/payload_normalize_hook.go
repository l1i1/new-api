package relay

import (
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/pkg/payloadnorm"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/gin-gonic/gin"
)

// normalizePayloadForUpstream applies the operator-configured payload normalization to a
// chat-completions request before ANY channel adaptor converts it.
//
// Why it lives here rather than inside one adaptor: the rewrite exists to give the upstream
// cache a stable prefix (a long document moved out of the last user message into the system
// message), and that property belongs to the request shape, not to one vendor. Keeping the
// hook on the shared OpenAI-format path means an official channel and an aggregator channel
// are rewritten identically, so the cache benefit survives a routing change.
//
// The call is a no-op unless the operator enabled the feature and scoped this token/channel
// (pkg/payloadnorm.Apply decides; it also refuses anything it cannot rewrite safely —
// multi-turn, tool, multimodal, or non-string content). One atomic load of the installed
// immutable snapshot per request: a concurrent option reload cannot change the scope midway
// through, and no lock is taken on the relay hot path.
func normalizePayloadForUpstream(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) *dto.GeneralOpenAIRequest {
	if request == nil || info == nil || info.ChannelMeta == nil {
		return request
	}
	normalized, decision := payloadnorm.Apply(payloadnorm.CurrentConfig(), info.TokenId, info.ChannelId, request)
	if !decision.Apply {
		return request
	}
	logger.LogDebug(c.Request.Context(), "payload_normalize applied: token=%d channel=%d reason=%s",
		info.TokenId, info.ChannelId, decision.Reason)
	return normalized
}
