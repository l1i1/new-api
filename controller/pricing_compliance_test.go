package controller

import (
	"crypto/sha256"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateComplianceGeoIPOption(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		value   string
		wantErr bool
	}{
		{name: "enabled", key: setting.ComplianceGeoIPEnabledOptionKey, value: "true"},
		{name: "invalid enabled", key: setting.ComplianceGeoIPEnabledOptionKey, value: "sometimes", wantErr: true},
		{name: "country codes", key: setting.ComplianceGeoIPCountryCodesOptionKey, value: "CN, us"},
		{name: "invalid country code", key: setting.ComplianceGeoIPCountryCodesOptionKey, value: "China", wantErr: true},
		{name: "model keywords", key: setting.ComplianceGeoIPModelKeywordsOptionKey, value: "gpt; gemini"},
		{name: "empty model keywords", key: setting.ComplianceGeoIPModelKeywordsOptionKey, value: "", wantErr: true},
		{name: "group keywords", key: setting.ComplianceGeoIPGroupKeywordsOptionKey, value: "gpt\ngenpic"},
		{name: "retry minimum", key: setting.ComplianceGeoIPRetryBackoffMinutesOptionKey, value: "1"},
		{name: "retry maximum", key: setting.ComplianceGeoIPRetryBackoffMinutesOptionKey, value: "1440"},
		{name: "retry too small", key: setting.ComplianceGeoIPRetryBackoffMinutesOptionKey, value: "0", wantErr: true},
		{name: "retry too large", key: setting.ComplianceGeoIPRetryBackoffMinutesOptionKey, value: "1441", wantErr: true},
		{name: "database path", key: setting.ComplianceGeoIPDatabaseOptionKey, value: "GeoLite2-Country.mmdb"},
		{name: "database path too long", key: setting.ComplianceGeoIPDatabaseOptionKey, value: strings.Repeat("a", 1025), wantErr: true},
		{name: "empty URL uses fallback", key: "compliance_geoip.url", value: ""},
		{name: "HTTPS URL", key: "compliance_geoip.url", value: "https://example.com/country.mmdb"},
		{name: "HTTP URL", key: "compliance_geoip.url", value: "http://example.com/country.mmdb", wantErr: true},
		{name: "empty checksum", key: "compliance_geoip.sha256", value: ""},
		{name: "valid checksum", key: "compliance_geoip.sha256", value: "sha256:" + strings.Repeat("a", 64)},
		{name: "invalid checksum", key: "compliance_geoip.sha256", value: "invalid", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateComplianceGeoIPOption(test.key, test.value)
			if test.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

type complianceOptionSnapshot struct {
	value  string
	exists bool
}

func withComplianceOptions(t *testing.T, values map[string]string) {
	t.Helper()
	originals := make(map[string]complianceOptionSnapshot, len(values))
	common.OptionMapRWMutex.Lock()
	mapWasNil := common.OptionMap == nil
	if mapWasNil {
		common.OptionMap = make(map[string]string)
	}
	for key, value := range values {
		original, exists := common.OptionMap[key]
		originals[key] = complianceOptionSnapshot{value: original, exists: exists}
		common.OptionMap[key] = value
	}
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		common.OptionMapRWMutex.Lock()
		defer common.OptionMapRWMutex.Unlock()
		if mapWasNil {
			common.OptionMap = nil
			return
		}
		for key, original := range originals {
			if original.exists {
				common.OptionMap[key] = original.value
			} else {
				delete(common.OptionMap, key)
			}
		}
	})
}

func TestComplianceClientCountryUsesConfiguredCountryHeaders(t *testing.T) {
	withComplianceOptions(t, map[string]string{
		setting.ComplianceGeoIPEnabledOptionKey:      "true",
		setting.ComplianceGeoIPCountryCodesOptionKey: "US,JP",
	})
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest("GET", "/api/pricing", nil)
	context.Request.RemoteAddr = ""
	context.Request.Header.Set("CF-IPCountry", " us ")
	assert.Equal(t, "US", complianceClientCountry(context))

	context.Request.Header.Set("CF-IPCountry", "CN")
	context.Request.Header.Set("EO-Client-IPCountry", "jp")
	assert.Equal(t, "JP", complianceClientCountry(context))

	context.Request.Header.Del("EO-Client-IPCountry")
	assert.Empty(t, complianceClientCountry(context))
}

func TestComplianceDisabledSkipsCountryLookup(t *testing.T) {
	withComplianceOptions(t, map[string]string{
		setting.ComplianceGeoIPEnabledOptionKey:      "false",
		setting.ComplianceGeoIPCountryCodesOptionKey: "CN",
		setting.ComplianceGeoIPDatabaseOptionKey:     filepath.Join(t.TempDir(), "missing.mmdb"),
	})
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest("GET", "/api/pricing", nil)
	context.Request.Header.Set("CF-IPCountry", "CN")

	assert.Empty(t, complianceClientCountry(context))
}

func TestComplianceConfigurationFallsBackFromInvalidRuntimeValues(t *testing.T) {
	withComplianceOptions(t, map[string]string{
		setting.ComplianceGeoIPEnabledOptionKey:             "invalid",
		setting.ComplianceGeoIPCountryCodesOptionKey:        "not-a-country",
		setting.ComplianceGeoIPModelKeywordsOptionKey:       ",",
		setting.ComplianceGeoIPGroupKeywordsOptionKey:       strings.Repeat("x", 65),
		setting.ComplianceGeoIPRetryBackoffMinutesOptionKey: "0",
	})

	assert.True(t, complianceEnabled())
	assert.Equal(t, []string{"CN"}, complianceCountryCodes())
	assert.Equal(t, []string{"gpt", "gemini", "claude", "grok"}, complianceModelKeywords())
	assert.Equal(t, []string{"gpt", "gemini", "claude", "grok", "genpic"}, complianceGroupKeywords())
	assert.Equal(t, 5*time.Minute, complianceGeoIPRetryBackoff())
}

func TestComplianceClientIPReadsForwardingHeaders(t *testing.T) {
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest("GET", "/api/pricing", nil)
	context.Request.Header.Set("CF-Connecting-IP", "203.0.113.10")
	context.Request.Header.Set("EO-Client-IP", "203.0.113.11")
	context.Request.Header.Set("X-Real-IP", "203.0.113.12")
	context.Request.Header.Set("X-Forwarded-For", "203.0.113.13, 203.0.113.14")
	assert.Equal(t, net.ParseIP("203.0.113.10"), complianceClientIP(context))

	context.Request.Header.Del("CF-Connecting-IP")
	assert.Equal(t, net.ParseIP("203.0.113.11"), complianceClientIP(context))

	context.Request.Header.Del("EO-Client-IP")
	assert.Equal(t, net.ParseIP("203.0.113.12"), complianceClientIP(context))

	context.Request.Header.Del("X-Real-IP")
	assert.Equal(t, net.ParseIP("203.0.113.13"), complianceClientIP(context))
}

func TestDownloadComplianceGeoIPRejectsInvalidDatabaseWithoutInstalling(t *testing.T) {
	database := []byte("test GeoIP database")
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write(database)
	}))
	t.Cleanup(server.Close)

	originalClient := complianceGeoIPHTTPClient
	complianceGeoIPHTTPClient = server.Client()
	t.Cleanup(func() {
		complianceGeoIPHTTPClient = originalClient
	})

	digest := sha256.Sum256(database)
	databasePath := filepath.Join(t.TempDir(), "geoip", "GeoLite2-Country.mmdb")
	err := downloadComplianceGeoIPWithConfig(databasePath, complianceGeoIPConfig{
		downloadURL:  server.URL,
		sha256Digest: strings.ToUpper(fmt.Sprintf("%x", digest)),
	})
	require.ErrorContains(t, err, "invalid MaxMind DB file")
	_, statErr := os.Stat(databasePath)
	assert.True(t, os.IsNotExist(statErr))
}

func TestComplianceGeoIPRetriesFailedDownloadAfterBackoff(t *testing.T) {
	var requestCount atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		requestCount.Add(1)
		response.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)

	originalClient := complianceGeoIPHTTPClient
	complianceGeoIPHTTPClient = server.Client()
	t.Cleanup(func() {
		complianceGeoIPHTTPClient = originalClient
	})

	complianceGeoIPStateMutex.Lock()
	originalLoaded := complianceGeoIPLoaded
	originalConfigKey := complianceGeoIPConfigKey
	originalReader := complianceGeoIPReader
	originalRetryAfter := complianceGeoIPRetryAfter
	complianceGeoIPLoaded = false
	complianceGeoIPConfigKey = ""
	complianceGeoIPReader = nil
	complianceGeoIPRetryAfter = time.Time{}
	complianceGeoIPStateMutex.Unlock()
	t.Cleanup(func() {
		complianceGeoIPStateMutex.Lock()
		if complianceGeoIPReader != nil && complianceGeoIPReader != originalReader {
			_ = complianceGeoIPReader.Close()
		}
		complianceGeoIPLoaded = originalLoaded
		complianceGeoIPConfigKey = originalConfigKey
		complianceGeoIPReader = originalReader
		complianceGeoIPRetryAfter = originalRetryAfter
		complianceGeoIPStateMutex.Unlock()
	})

	config := complianceGeoIPConfig{
		path:         filepath.Join(t.TempDir(), "GeoLite2-Country.mmdb"),
		downloadURL:  server.URL,
		retryBackoff: 17 * time.Minute,
	}
	load := func() {
		complianceGeoIPStateMutex.Lock()
		defer complianceGeoIPStateMutex.Unlock()
		assert.Nil(t, loadComplianceGeoIPLocked(config))
	}

	load()
	assert.Equal(t, int32(1), requestCount.Load())
	complianceGeoIPStateMutex.Lock()
	assert.WithinDuration(t, time.Now().Add(17*time.Minute), complianceGeoIPRetryAfter, time.Second)
	complianceGeoIPStateMutex.Unlock()
	load()
	assert.Equal(t, int32(1), requestCount.Load())

	complianceGeoIPStateMutex.Lock()
	complianceGeoIPRetryAfter = time.Now().Add(-time.Second)
	complianceGeoIPStateMutex.Unlock()
	load()
	assert.Equal(t, int32(2), requestCount.Load())
}

func TestFilterPricingForComplianceRemovesRestrictedCatalogEntries(t *testing.T) {
	pricing := []model.Pricing{
		{ModelName: "safe-model", VendorID: 1, EnableGroup: []string{"public"}},
		{ModelName: "gpt-5", VendorID: 2, EnableGroup: []string{"public"}},
		{ModelName: "safe-gemini-group", VendorID: 3, EnableGroup: []string{"gemini-public"}},
		{ModelName: "safe-model-2", VendorID: 4, EnableGroup: []string{"private"}},
		{ModelName: "genpic-model", VendorID: 5, EnableGroup: []string{"private"}},
	}
	vendors := []model.PricingVendor{
		{ID: 1, Name: "safe-vendor"},
		{ID: 2, Name: "gpt-vendor"},
		{ID: 4, Name: "private-vendor"},
	}
	groupRatio := map[string]float64{"public": 1, "private": 2, "gpt-public": 3}
	usableGroup := map[string]string{"public": "Public", "private": "Private", "gpt-public": "GPT"}
	autoGroups := []string{"gpt-public", "public", "private"}
	supportedEndpoint := map[string]common.EndpointInfo{
		"openai":    {Path: "/v1/chat/completions", Method: "POST"},
		"anthropic": {Path: "/v1/messages", Method: "POST"},
	}

	filteredPricing, filteredVendors, filteredRatios, filteredUsable, filteredAuto, filteredEndpoints := filterPricingForCompliance(
		pricing,
		vendors,
		groupRatio,
		usableGroup,
		autoGroups,
		supportedEndpoint,
	)

	assert.Equal(t, []string{"safe-model", "safe-model-2"}, []string{filteredPricing[0].ModelName, filteredPricing[1].ModelName})
	assert.Equal(t, []int{1, 4}, []int{filteredVendors[0].ID, filteredVendors[1].ID})
	assert.Equal(t, map[string]float64{"public": 1, "private": 2}, filteredRatios)
	assert.Equal(t, map[string]string{"public": "Public", "private": "Private"}, filteredUsable)
	assert.Equal(t, []string{"public", "private"}, filteredAuto)
	assert.Equal(t, map[string]common.EndpointInfo{
		"openai": {Path: "/v1/chat/completions", Method: "POST"},
	}, filteredEndpoints)
}
