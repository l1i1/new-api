package controller

import (
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

// GetContinuationCapabilities reports what real continuation rounds did per
// channel and the whitelist those counters support. The continuation whitelist is
// otherwise curated by hand from captures; this turns that into evidence:
// a channel with enough successful rounds is a candidate, one that keeps
// producing nothing is not.
//
// It is read-only. Applying the recommendation stays an explicit operator action
// (deployment/tokeness-cn/continuation-whitelist.py --apply), because widening the
// whitelist changes how customer answers are stitched.
func GetContinuationCapabilities(c *gin.Context) {
	snapshot := common.ContinuationCapabilitySnapshot()
	recommended := common.RecommendedContinuationChannels(3, 0.6)

	channels := make(map[int]gin.H, len(snapshot))
	for channelID, stats := range snapshot {
		entry := gin.H{
			"attempts":        stats.Attempts,
			"successes":       stats.Successes,
			"failures":        stats.Failures,
			"last_success_at": stats.LastSuccessAt,
			"last_failure_at": stats.LastFailureAt,
			"recommended":     common.ContinuationCapabilityCandidate(stats, 3, 0.6),
		}
		if channel, err := model.CacheGetChannel(channelID); err == nil && channel != nil {
			entry["name"] = channel.Name
		}
		channels[channelID] = entry
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data": gin.H{
			"channels":    channels,
			"recommended": recommended,
			"current":     common.ChatContinuationChannelsRaw,
			"criteria":    gin.H{"min_attempts": 3, "min_success_rate": 0.6},
		},
	})
}
