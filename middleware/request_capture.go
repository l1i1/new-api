package middleware

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// captureResponseWriter tees the response without changing it. Every byte still reaches the client
// as it is written; the copy stops at the cap, so a large or endless stream costs bounded memory.
type captureResponseWriter struct {
	gin.ResponseWriter
	limit     int
	status    int
	body      []byte
	truncated bool
}

func (w *captureResponseWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *captureResponseWriter) WriteHeaderNow() {
	if w.status == 0 {
		w.status = w.ResponseWriter.Status()
	}
	w.ResponseWriter.WriteHeaderNow()
}

func (w *captureResponseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if len(w.body) < w.limit {
		room := w.limit - len(w.body)
		if room >= len(data) {
			w.body = append(w.body, data...)
		} else {
			w.body = append(w.body, data[:room]...)
			w.truncated = true
		}
	} else if len(data) > 0 {
		w.truncated = true
	}
	return w.ResponseWriter.Write(data)
}

// RequestCaptureMiddleware is installed after authentication and before the relay handlers, so the
// token is known, the body has not been consumed yet, and every protocol on the group is covered by
// one implementation instead of one per relay format.
func RequestCaptureMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		setting := operation_setting.GetRequestCaptureSetting()
		if setting == nil || !setting.Enabled || len(setting.Rules) == 0 {
			c.Next()
			return
		}
		tokenID := c.GetInt("token_id")
		userID := c.GetInt("id")
		path := ""
		if c.Request != nil && c.Request.URL != nil {
			path = c.Request.URL.Path
		}
		model, body := capturePeek(c)
		rule := service.MatchRequestCaptureRule(tokenID, userID, model, path)
		if rule == nil {
			c.Next()
			return
		}
		writer := &captureResponseWriter{ResponseWriter: c.Writer, limit: setting.EffectiveMaxBytes()}
		c.Writer = writer
		c.Next()

		// The status filter and the budget are applied after the response, not before, for one
		// reason: a rule that watches failures must not spend its limit on successes. Deciding
		// before the request runs would consume the window on traffic the operator never asked for
		// and leave nothing for the errors it exists to catch.
		if !service.CaptureStatusAllowed(rule, writer.status) {
			return
		}
		budgetCtx, cancelBudget := context.WithTimeout(context.Background(), 3*time.Second)
		allowed := service.CaptureBudgetAllows(budgetCtx, rule)
		cancelBudget()
		if !allowed {
			return
		}

		payload := &service.RequestCapturePayload{
			CapturedAt: time.Now().Unix(),
			RequestID:  c.GetString(common.RequestIdKey),
			RuleName:   rule.Name,
			TokenID:    tokenID,
			UserID:     userID,
			Model:      model,
			Path:       path,
			ChannelID:  c.GetInt("channel_id"),
		}
		if rule.Include.RequestHeaders {
			payload.RequestHeaders = captureRequestHeaders(c, rule)
		}
		if rule.Include.RequestBody {
			payload.RequestBody, payload.RequestTruncated = captureTruncate(body, setting.EffectiveMaxBytes())
		}
		if rule.Include.ResponseHeaders {
			payload.ResponseHeaders = captureResponseHeaders(writer)
		}
		if rule.Include.ResponseBody {
			payload.ResponseBody = string(writer.body)
			payload.ResponseTruncated = writer.truncated
		}
		payload.ResponseStatus = writer.status
		go func() {
			sendCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := service.EnqueueRequestCapture(sendCtx, payload); err != nil {
				logger.LogWarn(sendCtx, fmt.Sprintf("request capture enqueue failed for rule %q: %v", rule.Name, err))
			}
		}()
	}
}

func captureTruncate(body []byte, limit int) (string, bool) {
	if limit <= 0 || len(body) <= limit {
		return string(body), false
	}
	return string(body[:limit]), true
}

func captureRequestHeaders(c *gin.Context, rule *operation_setting.RequestCaptureRule) map[string]string {
	out := map[string]string{}
	if c == nil || c.Request == nil {
		return out
	}
	redact := map[string]bool{"authorization": true, "x-api-key": true, "api-key": true}
	for _, h := range rule.RedactHeaders {
		redact[strings.ToLower(strings.TrimSpace(h))] = true
	}
	for name, values := range c.Request.Header {
		lower := strings.ToLower(name)
		if redact[lower] {
			out[name] = "[redacted]"
			continue
		}
		out[name] = strings.Join(values, ", ")
	}
	return out
}

func captureResponseHeaders(w *captureResponseWriter) map[string]string {
	out := map[string]string{}
	if w == nil {
		return out
	}
	for name, values := range w.Header() {
		out[name] = strings.Join(values, ", ")
	}
	return out
}

// capturePeek reads the body through the shared body storage, which the relay reads later for its own
// purposes; peeking must not consume it. It also returns the model, because the model only exists
// after the body is parsed and rule matching happens before the handler runs.
func capturePeek(c *gin.Context) (string, []byte) {
	if c == nil || c.Request == nil {
		return "", nil
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return "", nil
	}
	body, err := storage.Bytes()
	if err != nil || len(body) == 0 {
		return "", nil
	}
	// gjson reads the one field matching needs without decoding the whole body. Capture runs on the
	// request path, and a chat request can carry a long conversation; unmarshalling all of it into a
	// map to read a single string would cost the relay real time for nothing.
	return strings.TrimSpace(gjson.GetBytes(body, "model").String()), body
}
