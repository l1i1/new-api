package middleware

import (
	"bytes"
	"crypto/subtle"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"

	"github.com/gin-gonic/gin"
)

const (
	partnerSignatureHeader  = "X-Partner-Signature"
	partnerTimestampHeader  = "X-Partner-Timestamp"
	partnerKeyIDHeader      = "X-Partner-Key-ID"
	partnerTimestampWindow  = 300 // seconds, anti-replay
	partnerRequestBodyLimit = 1 << 20
)

// SignedPartnerBypass authenticates partner-console service calls by HMAC and
// marks verified requests so the per-path limiter after it can stand down.
// Requests that fail verification fall through to the normal chain untouched,
// so an invalid signature can neither bypass limits nor amplify into an
// oracle: the response is identical to an unsigned request hitting the same
// route.
//
// The signature is hex(hmac_sha256(secret, raw_request_body)) with the secret
// selected by X-Partner-Key-ID from PARTNER_CONSOLE_SECRETS (comma-separated,
// key rotation without downtime). GET requests sign the empty body.
func SignedPartnerBypass() func(c *gin.Context) {
	return func(c *gin.Context) {
		signature := strings.TrimSpace(c.GetHeader(partnerSignatureHeader))
		if signature == "" {
			return
		}
		if !verifyPartnerSignature(c, signature) {
			return
		}
		c.Set("partner_bypass", true)
		c.Set("partner_key_id", strings.TrimSpace(c.GetHeader(partnerKeyIDHeader)))
	}
}

// PartnerLimiter wraps a limiter so verified partner signatures skip it while
// every other request keeps the shared protection. The bypass marker is set
// by SignedPartnerBypass earlier in the same chain.
func PartnerLimiter(limiter func(c *gin.Context)) func(c *gin.Context) {
	return func(c *gin.Context) {
		if PartnerBypassActive(c) {
			return
		}
		limiter(c)
	}
}

// PartnerBypassActive reports whether the request passed signature verification.
func PartnerBypassActive(c *gin.Context) bool {
	active, _ := c.Get("partner_bypass")
	passed, _ := active.(bool)
	return passed
}

func verifyPartnerSignature(c *gin.Context, signature string) bool {
	timestamp, err := strconv.ParseInt(strings.TrimSpace(c.GetHeader(partnerTimestampHeader)), 10, 64)
	if err != nil {
		return false
	}
	now := time.Now().Unix()
	if timestamp < now-partnerTimestampWindow || timestamp > now+partnerTimestampWindow {
		return false
	}
	secret := matchPartnerSecret(strings.TrimSpace(c.GetHeader(partnerKeyIDHeader)))
	if secret == "" {
		return false
	}
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, partnerRequestBodyLimit))
	if err != nil {
		return false
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	expected := common.HmacSha256(string(body), secret)
	if len(signature) != len(expected) {
		return false
	}
	matched := subtle.ConstantTimeCompare([]byte(strings.ToLower(signature)), []byte(expected)) == 1
	if !matched {
		return false
	}
	logger.LogInfo(c.Request.Context(), "partner signed request path="+c.Request.URL.Path)
	return true
}

// matchPartnerSecret selects the signing secret by key ID. Entries use the
// "key-id:secret" form; a bare secret without a colon matches any key ID so a
// single-secret deployment needs no key IDs at all.
func matchPartnerSecret(keyID string) string {
	raw := os.Getenv("PARTNER_CONSOLE_SECRETS")
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	fallback := ""
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		id, secret, found := strings.Cut(entry, ":")
		if !found {
			if fallback == "" {
				fallback = strings.TrimSpace(entry)
			}
			continue
		}
		if strings.TrimSpace(id) == keyID && strings.TrimSpace(secret) != "" {
			return strings.TrimSpace(secret)
		}
	}
	return fallback
}
