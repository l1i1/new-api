package controller

import (
	"crypto/sha256"
	"fmt"
)

// paymentPayloadDigest is safe to include in operational logs: it lets an
// operator correlate retries without retaining signed payloads or secrets.
func paymentPayloadDigest(payload []byte) string {
	digest := sha256.Sum256(payload)
	return fmt.Sprintf("%x", digest[:])
}

func paymentPayloadDigestString(payload string) string {
	return paymentPayloadDigest([]byte(payload))
}
