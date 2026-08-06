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
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
	"github.com/oschwald/geoip2-golang"
)

const (
	pricingGeoIPDefaultURL = "https://github.com/P3TERX/GeoLite.mmdb/releases/latest/download/GeoLite2-Country.mmdb"
	pricingGeoIPMaxBytes   = 100 << 20
)

var (
	pricingGeoIPStateMutex sync.Mutex
	pricingGeoIPLoaded     bool
	pricingGeoIPConfigKey  string
	pricingGeoIPReader     *geoip2.Reader
	pricingGeoIPHTTPClient = &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(request *http.Request, _ []*http.Request) error {
			if request.URL.Scheme != "https" {
				return fmt.Errorf("GeoIP download redirect must use HTTPS")
			}
			return nil
		},
	}
)

func isChinaCountry(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "cn", "china", "中国":
		return true
	default:
		return false
	}
}

type pricingGeoIPConfig struct {
	path         string
	downloadURL  string
	sha256Digest string
}

func pricingGeoIPOptionValue(optionKey, envKey string) string {
	common.OptionMapRWMutex.RLock()
	value := common.OptionMap[optionKey]
	common.OptionMapRWMutex.RUnlock()
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return strings.TrimSpace(os.Getenv(envKey))
}

func currentPricingGeoIPConfig() pricingGeoIPConfig {
	config := pricingGeoIPConfig{
		path: pricingGeoIPPath(),
		downloadURL: pricingGeoIPOptionValue(
			setting.PricingGeoIPDownloadOptionKey,
			setting.PricingGeoIPDownloadEnv,
		),
		sha256Digest: pricingGeoIPOptionValue(
			setting.PricingGeoIPSHA256OptionKey,
			setting.PricingGeoIPSHA256Env,
		),
	}
	return config
}

func (config pricingGeoIPConfig) key() string {
	return strings.Join([]string{config.path, config.downloadURL, config.sha256Digest}, "\x00")
}

func firstPricingHeaderIP(value string) net.IP {
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

func pricingClientIP(c *gin.Context) net.IP {
	if c == nil || c.Request == nil {
		return nil
	}
	for _, header := range []string{
		"CF-Connecting-IP",
		"EO-Client-IP",
		"X-Real-IP",
		"X-Forwarded-For",
	} {
		if ip := firstPricingHeaderIP(c.GetHeader(header)); ip != nil {
			return ip
		}
	}
	if ip := firstPricingHeaderIP(c.ClientIP()); ip != nil {
		return ip
	}
	return firstPricingHeaderIP(c.Request.RemoteAddr)
}

func pricingGeoIPPath() string {
	if path := pricingGeoIPOptionValue(setting.PricingGeoIPDatabaseOptionKey, setting.PricingGeoIPDatabaseEnv); path != "" {
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

func parsePricingGeoIPChecksum(raw string) ([]byte, error) {
	raw = strings.TrimSpace(strings.TrimPrefix(strings.ToLower(raw), "sha256:"))
	if raw == "" {
		return nil, nil
	}
	if len(raw) != sha256.Size*2 {
		return nil, fmt.Errorf("%s must be a SHA-256 hex digest", setting.PricingGeoIPSHA256Env)
	}
	checksum, err := hex.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("%s must be a SHA-256 hex digest: %w", setting.PricingGeoIPSHA256Env, err)
	}
	return checksum, nil
}

func pricingGeoIPChecksum() ([]byte, error) {
	return parsePricingGeoIPChecksum(pricingGeoIPOptionValue(setting.PricingGeoIPSHA256OptionKey, setting.PricingGeoIPSHA256Env))
}

func parsePricingGeoIPDownloadURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = pricingGeoIPDefaultURL
	}
	downloadURL, err := url.Parse(raw)
	if err != nil || downloadURL.Scheme != "https" || downloadURL.Host == "" {
		return nil, fmt.Errorf("%s must be an HTTPS URL", setting.PricingGeoIPDownloadEnv)
	}
	return downloadURL, nil
}

func pricingGeoIPDownloadURL() (*url.URL, error) {
	return parsePricingGeoIPDownloadURL(pricingGeoIPOptionValue(setting.PricingGeoIPDownloadOptionKey, setting.PricingGeoIPDownloadEnv))
}

func validatePricingGeoIPOption(key, value string) error {
	switch key {
	case setting.PricingGeoIPDownloadOptionKey:
		if strings.TrimSpace(value) == "" {
			return nil
		}
		_, err := parsePricingGeoIPDownloadURL(value)
		return err
	case setting.PricingGeoIPSHA256OptionKey:
		_, err := parsePricingGeoIPChecksum(value)
		return err
	default:
		return nil
	}
}

func downloadPricingGeoIPWithConfig(path string, config pricingGeoIPConfig) error {
	downloadURL, err := parsePricingGeoIPDownloadURL(config.downloadURL)
	if err != nil {
		return err
	}
	expectedChecksum, err := parsePricingGeoIPChecksum(config.sha256Digest)
	if err != nil {
		return err
	}

	requestContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, downloadURL.String(), nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", "new-api-pricing-geoip/1.0")
	response, err := pricingGeoIPHTTPClient.Do(request)
	if err != nil {
		return fmt.Errorf("download GeoIP database: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download GeoIP database: HTTP %d", response.StatusCode)
	}
	if response.ContentLength > pricingGeoIPMaxBytes {
		return fmt.Errorf("download GeoIP database exceeds %d bytes", pricingGeoIPMaxBytes)
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
	written, err := io.Copy(io.MultiWriter(temporary, hash), io.LimitReader(response.Body, pricingGeoIPMaxBytes+1))
	if err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write GeoIP database: %w", err)
	}
	if written > pricingGeoIPMaxBytes {
		_ = temporary.Close()
		return fmt.Errorf("download GeoIP database exceeds %d bytes", pricingGeoIPMaxBytes)
	}
	if expectedChecksum != nil && !equalBytes(hash.Sum(nil), expectedChecksum) {
		_ = temporary.Close()
		return fmt.Errorf("downloaded GeoIP database SHA-256 does not match %s", setting.PricingGeoIPSHA256Env)
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

func downloadPricingGeoIP(path string) error {
	return downloadPricingGeoIPWithConfig(path, currentPricingGeoIPConfig())
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

func loadPricingGeoIPLocked(config pricingGeoIPConfig) *geoip2.Reader {
	configKey := config.key()
	if pricingGeoIPLoaded && pricingGeoIPConfigKey == configKey {
		return pricingGeoIPReader
	}
	if pricingGeoIPReader != nil {
		_ = pricingGeoIPReader.Close()
		pricingGeoIPReader = nil
	}
	pricingGeoIPLoaded = true
	pricingGeoIPConfigKey = configKey

	if _, err := os.Stat(config.path); os.IsNotExist(err) {
		if err := downloadPricingGeoIPWithConfig(config.path, config); err != nil {
			log.Printf("pricing GeoIP disabled: %v", err)
			return nil
		}
	} else if err != nil {
		log.Printf("pricing GeoIP disabled: cannot access %s: %v", config.path, err)
		return nil
	}
	reader, err := geoip2.Open(config.path)
	if err != nil {
		log.Printf("pricing GeoIP disabled: failed to open %s: %v", config.path, err)
		return nil
	}
	pricingGeoIPReader = reader
	return pricingGeoIPReader
}

func pricingGeoIP() *geoip2.Reader {
	config := currentPricingGeoIPConfig()
	pricingGeoIPStateMutex.Lock()
	defer pricingGeoIPStateMutex.Unlock()
	return loadPricingGeoIPLocked(config)
}

func lookupPricingGeoIP(ip net.IP) (*geoip2.Country, error) {
	config := currentPricingGeoIPConfig()
	pricingGeoIPStateMutex.Lock()
	defer pricingGeoIPStateMutex.Unlock()
	reader := loadPricingGeoIPLocked(config)
	if reader == nil {
		return nil, fmt.Errorf("GeoIP database unavailable")
	}
	return reader.Country(ip)
}

func isChinaPricingClient(c *gin.Context) bool {
	for _, header := range []string{"CF-IPCountry", "EO-Client-IPCountry"} {
		if isChinaCountry(c.GetHeader(header)) {
			return true
		}
	}

	ip := pricingClientIP(c)
	if ip == nil {
		return false
	}
	record, err := lookupPricingGeoIP(ip)
	if err != nil {
		return false
	}
	return isChinaCountry(record.Country.IsoCode) ||
		isChinaCountry(record.Country.Names["en"]) ||
		isChinaCountry(record.Country.Names["zh-CN"])
}
