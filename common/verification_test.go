package common

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVerifyCodeWithKeyRejectsAfterMaximumFailedAttempts(t *testing.T) {
	key := "verification-attempts@example.com"
	RegisterVerificationCodeWithKey(key, "123456", EmailVerificationPurpose)

	for attempt := 1; attempt <= verificationMaxAttempts; attempt++ {
		assert.False(t, VerifyCodeWithKey(key, "000000", EmailVerificationPurpose))
	}
	assert.False(t, VerifyCodeWithKey(key, "123456", EmailVerificationPurpose))
}

func TestVerifyCodeWithKeyUsesOneStoredDigestAndAllowsCorrectCode(t *testing.T) {
	key := "verification-success@example.com"
	RegisterVerificationCodeWithKey(key, "123456", EmailVerificationPurpose)

	require.True(t, VerifyCodeWithKey(key, "123456", EmailVerificationPurpose))
	DeleteKey(key, EmailVerificationPurpose)
	assert.False(t, VerifyCodeWithKey(key, "123456", EmailVerificationPurpose))
}

func TestVerifyCodeWithKeyDeletesExpiredCode(t *testing.T) {
	key := "verification-expired@example.com"
	RegisterVerificationCodeWithKey(key, "123456", EmailVerificationPurpose)

	verificationMutex.Lock()
	value := verificationMap[EmailVerificationPurpose+key]
	value.time = time.Now().Add(-time.Duration(VerificationValidMinutes*60+1) * time.Second)
	verificationMap[EmailVerificationPurpose+key] = value
	verificationMutex.Unlock()

	assert.False(t, VerifyCodeWithKey(key, "123456", EmailVerificationPurpose))
	assert.False(t, VerifyCodeWithKey(key, "123456", EmailVerificationPurpose))
}
