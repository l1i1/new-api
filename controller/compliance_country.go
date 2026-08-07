package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
	"github.com/oschwald/geoip2-golang"
)

const (
	complianceGeoIPDefaultURL = "https://github.com/P3TERX/GeoLite.mmdb/releases/latest/download/GeoLite2-Country.mmdb"
	complianceGeoIPMaxBytes   = 100 << 20
	complianceGeoIPPathMaxLen = 1024
)

var (
	complianceGeoIPStateMutex sync.Mutex
	complianceGeoIPLoaded     bool
	complianceGeoIPConfigKey  string
	complianceGeoIPReader     *geoip2.Reader
	complianceGeoIPRetryAfter time.Time
	complianceGeoIPHTTPClient = &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(request *http.Request, _ []*http.Request) error {
			if request.URL.Scheme != "https" {
				return fmt.Errorf("GeoIP download redirect must use HTTPS")
			}
			return nil
		},
	}
)

type complianceGeoIPConfig struct {
	path         string
	downloadURL  string
	sha256Digest string
	retryBackoff time.Duration
}

func complianceGeoIPOptionValue(optionKey, envKey string) string {
	common.OptionMapRWMutex.RLock()
	value := common.OptionMap[optionKey]
	common.OptionMapRWMutex.RUnlock()
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return strings.TrimSpace(os.Getenv(envKey))
}

func currentComplianceGeoIPConfig() complianceGeoIPConfig {
	downloadURL := complianceGeoIPOptionValue(
		setting.ComplianceGeoIPDownloadOptionKey,
		setting.ComplianceGeoIPDownloadEnv,
	)
	if _, err := parseComplianceGeoIPDownloadURL(downloadURL); err != nil {
		downloadURL = ""
	}
	sha256Digest := complianceGeoIPOptionValue(
		setting.ComplianceGeoIPSHA256OptionKey,
		setting.ComplianceGeoIPSHA256Env,
	)
	if _, err := parseComplianceGeoIPChecksum(sha256Digest); err != nil {
		sha256Digest = ""
	}
	config := complianceGeoIPConfig{
		path:         complianceGeoIPPath(),
		downloadURL:  downloadURL,
		sha256Digest: sha256Digest,
		retryBackoff: complianceGeoIPRetryBackoff(),
	}
	return config
}

func (config complianceGeoIPConfig) key() string {
	return strings.Join([]string{
		config.path,
		config.downloadURL,
		config.sha256Digest,
		config.retryBackoff.String(),
	}, "\x00")
}

func complianceGeoIPRetryBackoff() time.Duration {
	value := complianceConfigValue(
		setting.ComplianceGeoIPRetryBackoffMinutesOptionKey,
		setting.ComplianceGeoIPRetryBackoffMinutesEnv,
		setting.ComplianceGeoIPRetryBackoffMinutesDefault,
	)
	minutes, err := strconv.Atoi(value)
	if err != nil || minutes < 1 || minutes > 1440 {
		minutes, _ = strconv.Atoi(setting.ComplianceGeoIPRetryBackoffMinutesDefault)
	}
	return time.Duration(minutes) * time.Minute
}

func firstComplianceHeaderIP(value string) net.IP {
	for _, part := range strings.Split(value, ",") {
		candidate := strings.TrimSpace(part)
		if candidate == "" {
			continue
		}
		if host, _, err := net.SplitHostPort(candidate); err == nil {
			candidate = host
		}
		candidate = strings.Trim(candidate, "[]")
		if ip := net.ParseIP(candidate); ip != nil {
			return ip
		}
	}
	return nil
}

func complianceClientIP(c *gin.Context) net.IP {
	if c == nil || c.Request == nil {
		return nil
	}
	for _, header := range []string{
		"CF-Connecting-IP",
		"EO-Client-IP",
		"X-Real-IP",
		"X-Forwarded-For",
	} {
		if ip := firstComplianceHeaderIP(c.GetHeader(header)); ip != nil {
			return ip
		}
	}
	if ip := firstComplianceHeaderIP(c.ClientIP()); ip != nil {
		return ip
	}
	return firstComplianceHeaderIP(c.Request.RemoteAddr)
}

func complianceGeoIPPath() string {
	if path := complianceGeoIPOptionValue(setting.ComplianceGeoIPDatabaseOptionKey, setting.ComplianceGeoIPDatabaseEnv); path != "" && len(path) <= complianceGeoIPPathMaxLen {
		return path
	}
	for _, path := range []string{
		"/usr/share/GeoIP/GeoLite2-Country.mmdb",
		"/usr/share/GeoIP/GeoIP2-Country.mmdb",
	} {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return "GeoLite2-Country.mmdb"
}

func parseComplianceGeoIPChecksum(raw string) ([]byte, error) {
	raw = strings.TrimSpace(strings.TrimPrefix(strings.ToLower(raw), "sha256:"))
	if raw == "" {
		return nil, nil
	}
	if len(raw) != sha256.Size*2 {
		return nil, fmt.Errorf("%s must be a SHA-256 hex digest", setting.ComplianceGeoIPSHA256Env)
	}
	checksum, err := hex.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("%s must be a SHA-256 hex digest: %w", setting.ComplianceGeoIPSHA256Env, err)
	}
	return checksum, nil
}

func parseComplianceGeoIPDownloadURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = complianceGeoIPDefaultURL
	}
	downloadURL, err := url.Parse(raw)
	if err != nil || downloadURL.Scheme != "https" || downloadURL.Host == "" {
		return nil, fmt.Errorf("%s must be an HTTPS URL", setting.ComplianceGeoIPDownloadEnv)
	}
	return downloadURL, nil
}

func validateComplianceGeoIPOption(key, value string) error {
	switch key {
	case setting.ComplianceGeoIPEnabledOptionKey:
		_, err := strconv.ParseBool(strings.TrimSpace(value))
		return err
	case setting.ComplianceGeoIPCountryCodesOptionKey:
		_, err := parseComplianceCountryCodes(value)
		return err
	case setting.ComplianceGeoIPModelKeywordsOptionKey, setting.ComplianceGeoIPGroupKeywordsOptionKey:
		_, err := parseComplianceKeywords(value)
		return err
	case setting.ComplianceGeoIPRetryBackoffMinutesOptionKey:
		minutes, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || minutes < 1 || minutes > 1440 {
			return fmt.Errorf("GeoIP retry backoff must be an integer from 1 through 1440 minutes")
		}
		return nil
	case setting.ComplianceGeoIPDatabaseOptionKey:
		if len(strings.TrimSpace(value)) > complianceGeoIPPathMaxLen {
			return fmt.Errorf("GeoIP database path must not exceed %d characters", complianceGeoIPPathMaxLen)
		}
		return nil
	case setting.ComplianceGeoIPDownloadOptionKey:
		if strings.TrimSpace(value) == "" {
			return nil
		}
		_, err := parseComplianceGeoIPDownloadURL(value)
		return err
	case setting.ComplianceGeoIPSHA256OptionKey:
		_, err := parseComplianceGeoIPChecksum(value)
		return err
	default:
		return nil
	}
}

func downloadComplianceGeoIPWithConfig(path string, config complianceGeoIPConfig) error {
	downloadURL, err := parseComplianceGeoIPDownloadURL(config.downloadURL)
	if err != nil {
		return err
	}
	expectedChecksum, err := parseComplianceGeoIPChecksum(config.sha256Digest)
	if err != nil {
		return err
	}

	requestContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, downloadURL.String(), nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", "new-api-compliance-geoip/1.0")
	response, err := complianceGeoIPHTTPClient.Do(request)
	if err != nil {
		return fmt.Errorf("download GeoIP database: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download GeoIP database: HTTP %d", response.StatusCode)
	}
	if response.ContentLength > complianceGeoIPMaxBytes {
		return fmt.Errorf("download GeoIP database exceeds %d bytes", complianceGeoIPMaxBytes)
	}

	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0750); err != nil {
		return fmt.Errorf("create GeoIP database directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".GeoLite2-Country-*.mmdb.tmp")
	if err != nil {
		return fmt.Errorf("create temporary GeoIP database: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(temporary, hash), io.LimitReader(response.Body, complianceGeoIPMaxBytes+1))
	if err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write GeoIP database: %w", err)
	}
	if written > complianceGeoIPMaxBytes {
		_ = temporary.Close()
		return fmt.Errorf("download GeoIP database exceeds %d bytes", complianceGeoIPMaxBytes)
	}
	if expectedChecksum != nil && !equalBytes(hash.Sum(nil), expectedChecksum) {
		_ = temporary.Close()
		return fmt.Errorf("downloaded GeoIP database SHA-256 does not match %s", setting.ComplianceGeoIPSHA256Env)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync GeoIP database: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary GeoIP database: %w", err)
	}
	if err := os.Chmod(temporaryPath, 0600); err != nil {
		return fmt.Errorf("restrict GeoIP database permissions: %w", err)
	}

	reader, err := geoip2.Open(temporaryPath)
	if err != nil {
		return fmt.Errorf("validate downloaded GeoIP database: %w", err)
	}
	if err := reader.Close(); err != nil {
		return fmt.Errorf("close validated GeoIP database: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("install GeoIP database: %w", err)
	}
	return nil
}

func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func loadComplianceGeoIPLocked(config complianceGeoIPConfig) *geoip2.Reader {
	configKey := config.key()
	if complianceGeoIPLoaded && complianceGeoIPConfigKey == configKey {
		if complianceGeoIPReader != nil || time.Now().Before(complianceGeoIPRetryAfter) {
			return complianceGeoIPReader
		}
	}
	if complianceGeoIPReader != nil {
		_ = complianceGeoIPReader.Close()
		complianceGeoIPReader = nil
	}
	complianceGeoIPLoaded = true
	complianceGeoIPConfigKey = configKey
	complianceGeoIPRetryAfter = time.Time{}

	if _, err := os.Stat(config.path); os.IsNotExist(err) {
		if err := downloadComplianceGeoIPWithConfig(config.path, config); err != nil {
			complianceGeoIPRetryAfter = time.Now().Add(config.retryBackoff)
			log.Printf("compliance GeoIP disabled: %v", err)
			return nil
		}
	} else if err != nil {
		complianceGeoIPRetryAfter = time.Now().Add(config.retryBackoff)
		log.Printf("compliance GeoIP disabled: cannot access %s: %v", config.path, err)
		return nil
	}
	reader, err := geoip2.Open(config.path)
	if err != nil {
		complianceGeoIPRetryAfter = time.Now().Add(config.retryBackoff)
		log.Printf("compliance GeoIP disabled: failed to open %s: %v", config.path, err)
		return nil
	}
	complianceGeoIPReader = reader
	return complianceGeoIPReader
}

func lookupComplianceGeoIP(ip net.IP) (*geoip2.Country, error) {
	config := currentComplianceGeoIPConfig()
	complianceGeoIPStateMutex.Lock()
	defer complianceGeoIPStateMutex.Unlock()
	reader := loadComplianceGeoIPLocked(config)
	if reader == nil {
		return nil, fmt.Errorf("GeoIP database unavailable")
	}
	return reader.Country(ip)
}

func complianceClientCountry(c *gin.Context) string {
	if !complianceEnabled() || c == nil {
		return ""
	}
	countryCodes := complianceCountryCodes()
	for _, header := range []string{"CF-IPCountry", "EO-Client-IPCountry"} {
		if countryCode := matchComplianceCountry(c.GetHeader(header), countryCodes); countryCode != "" {
			return countryCode
		}
	}

	ip := complianceClientIP(c)
	if ip == nil {
		return ""
	}
	record, err := lookupComplianceGeoIP(ip)
	if err != nil {
		return ""
	}
	return matchComplianceCountry(record.Country.IsoCode, countryCodes)
}

func matchComplianceCountry(value string, countryCodes []string) string {
	countryCode := strings.ToUpper(strings.TrimSpace(value))
	for _, configuredCode := range countryCodes {
		if countryCode == configuredCode {
			return configuredCode
		}
	}
	return ""
}
