package middleware

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/nodemetrics"

	"github.com/gin-gonic/gin"
)

// NodeMetrics records one relay request together with its client payload sizes
// into the per-node rolling counters that are reported by the system-instance
// heartbeat (info.extra.traffic).
//
// It only reads after the handler has finished: the cached body storage reports
// its size without being consumed (ContentLength is the fallback), and
// c.Writer.Size() reports the bytes written to the client. c.Writer is never
// wrapped here, so websocket upgrades and http.ResponseController paths keep
// working unchanged.
//
// Register it on relay groups only (never on both a parent and its child
// group), so console/API traffic is not counted.
func NodeMetrics() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()

		requestBytes := int64(0)
		if value, ok := c.Get(common.KeyBodyStorage); ok && value != nil {
			if storage, ok := value.(common.BodyStorage); ok {
				requestBytes = storage.Size()
			}
		}
		if requestBytes <= 0 && c.Request != nil && c.Request.ContentLength > 0 {
			requestBytes = c.Request.ContentLength
		}

		responseBytes := int64(0)
		if c.Writer != nil {
			if size := c.Writer.Size(); size > 0 {
				responseBytes = int64(size)
			}
		}

		nodemetrics.RecordRequest(requestBytes, responseBytes)
	}
}
