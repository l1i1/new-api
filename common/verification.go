package common

import (
	"crypto/sha256"
	"crypto/subtle"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type verificationValue struct {
	codeHash [sha256.Size]byte
	time     time.Time
	attempts int
}

const (
	EmailVerificationPurpose = "v"
	PasswordResetPurpose     = "r"
	verificationMaxAttempts  = 5
)

var verificationMutex sync.Mutex
var verificationMap map[string]verificationValue
var verificationMapMaxSize = 10
var VerificationValidMinutes = 10

func GenerateVerificationCode(length int) string {
	code := uuid.New().String()
	code = strings.Replace(code, "-", "", -1)
	if length == 0 {
		return code
	}
	return code[:length]
}

func RegisterVerificationCodeWithKey(key string, code string, purpose string) {
	verificationMutex.Lock()
	defer verificationMutex.Unlock()
	verificationMap[purpose+key] = verificationValue{
		codeHash: sha256.Sum256([]byte(code)),
		time:     time.Now(),
	}
	if len(verificationMap) > verificationMapMaxSize {
		removeExpiredPairs()
	}
}

func VerifyCodeWithKey(key string, code string, purpose string) bool {
	verificationMutex.Lock()
	defer verificationMutex.Unlock()
	value, okay := verificationMap[purpose+key]
	now := time.Now()
	if !okay || int(now.Sub(value.time).Seconds()) >= VerificationValidMinutes*60 {
		delete(verificationMap, purpose+key)
		return false
	}
	providedHash := sha256.Sum256([]byte(code))
	if subtle.ConstantTimeCompare(value.codeHash[:], providedHash[:]) == 1 {
		return true
	}
	value.attempts++
	if value.attempts >= verificationMaxAttempts {
		delete(verificationMap, purpose+key)
	} else {
		verificationMap[purpose+key] = value
	}
	return false
}

func DeleteKey(key string, purpose string) {
	verificationMutex.Lock()
	defer verificationMutex.Unlock()
	delete(verificationMap, purpose+key)
}

// no lock inside, so the caller must lock the verificationMap before calling!
func removeExpiredPairs() {
	now := time.Now()
	for key := range verificationMap {
		if int(now.Sub(verificationMap[key].time).Seconds()) >= VerificationValidMinutes*60 {
			delete(verificationMap, key)
		}
	}
}

func init() {
	verificationMutex.Lock()
	defer verificationMutex.Unlock()
	verificationMap = make(map[string]verificationValue)
}
