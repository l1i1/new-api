package operation_setting

import (
	"regexp"
	"sync"
)

const (
	ErrorMessageFilterEnabledOptionKey = "ErrorMessageFilterEnabled"
	ErrorMessageFilterPatternOptionKey = "ErrorMessageFilterPattern"
	ErrorMessageFilterDefaultPattern   = `(?i)\s*\(providers=[^)]*\)|\s*providers=[^()\r\n]*[^\s()]|\s*\(console go\)`
)

var (
	errorMessageFilterMu      sync.RWMutex
	errorMessageFilterEnabled = true
	errorMessageFilterPattern = ErrorMessageFilterDefaultPattern
	errorMessageFilterRegexp  = regexp.MustCompile(ErrorMessageFilterDefaultPattern)
)

func IsErrorMessageFilterEnabled() bool {
	errorMessageFilterMu.RLock()
	defer errorMessageFilterMu.RUnlock()
	return errorMessageFilterEnabled
}

func GetErrorMessageFilterPattern() string {
	errorMessageFilterMu.RLock()
	defer errorMessageFilterMu.RUnlock()
	return errorMessageFilterPattern
}

func ValidateErrorMessageFilterPattern(pattern string) error {
	_, err := regexp.Compile(pattern)
	return err
}

func SetErrorMessageFilterEnabled(enabled bool) {
	errorMessageFilterMu.Lock()
	errorMessageFilterEnabled = enabled
	errorMessageFilterMu.Unlock()
}

func SetErrorMessageFilterPattern(pattern string) error {
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return err
	}

	errorMessageFilterMu.Lock()
	errorMessageFilterPattern = pattern
	if pattern == "" {
		errorMessageFilterRegexp = nil
	} else {
		errorMessageFilterRegexp = compiled
	}
	errorMessageFilterMu.Unlock()
	return nil
}

// FilterErrorMessage removes configured sensitive fragments from a client-bound
// relay error. The original message remains available to the caller for logs.
func FilterErrorMessage(message string) string {
	errorMessageFilterMu.RLock()
	enabled := errorMessageFilterEnabled
	compiled := errorMessageFilterRegexp
	errorMessageFilterMu.RUnlock()

	if !enabled || compiled == nil {
		return message
	}
	return compiled.ReplaceAllString(message, "")
}
