package common

import (
	"sync"
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
	assert.False(t, VerifyCodeWithKey(key, "123456", EmailVerificationPurpose))
}

func TestVerifyCodeWithKeyConsumesCodeOnceUnderConcurrency(t *testing.T) {
	key := "verification-concurrency@example.com"
	RegisterVerificationCodeWithKey(key, "123456", EmailVerificationPurpose)

	const attempts = 16
	results := make(chan bool, attempts)
	var wg sync.WaitGroup
	wg.Add(attempts)
	for i := 0; i < attempts; i++ {
		go func() {
			defer wg.Done()
			results <- VerifyCodeWithKey(key, "123456", EmailVerificationPurpose)
		}()
	}
	wg.Wait()
	close(results)

	successes := 0
	for result := range results {
		if result {
			successes++
		}
	}
	require.Equal(t, 1, successes)
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

func TestRegisterVerificationCodeWithKeyEvictsOldestWhenMapIsFull(t *testing.T) {
	verificationMutex.Lock()
	originalMaxSize := verificationMapMaxSize
	verificationMapMaxSize = 2
	verificationMap = make(map[string]verificationValue)
	verificationMutex.Unlock()
	t.Cleanup(func() {
		verificationMutex.Lock()
		verificationMapMaxSize = originalMaxSize
		verificationMap = make(map[string]verificationValue)
		verificationMutex.Unlock()
	})

	RegisterVerificationCodeWithKey("verification-old@example.com", "111111", EmailVerificationPurpose)
	RegisterVerificationCodeWithKey("verification-new@example.com", "222222", EmailVerificationPurpose)

	verificationMutex.Lock()
	oldest := verificationMap[EmailVerificationPurpose+"verification-old@example.com"]
	oldest.time = time.Now().Add(-time.Minute)
	verificationMap[EmailVerificationPurpose+"verification-old@example.com"] = oldest
	verificationMutex.Unlock()

	RegisterVerificationCodeWithKey("verification-latest@example.com", "333333", EmailVerificationPurpose)

	assert.False(t, VerifyCodeWithKey("verification-old@example.com", "111111", EmailVerificationPurpose))
	assert.True(t, VerifyCodeWithKey("verification-new@example.com", "222222", EmailVerificationPurpose))
	assert.True(t, VerifyCodeWithKey("verification-latest@example.com", "333333", EmailVerificationPurpose))
}
