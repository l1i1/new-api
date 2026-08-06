package controller

import (
	"net"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidatePricingGeoIPOption(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		value   string
		wantErr bool
	}{
		{name: "empty URL uses fallback", key: "pricing_geoip.url", value: ""},
		{name: "HTTPS URL", key: "pricing_geoip.url", value: "https://example.com/country.mmdb"},
		{name: "HTTP URL", key: "pricing_geoip.url", value: "http://example.com/country.mmdb", wantErr: true},
		{name: "empty checksum", key: "pricing_geoip.sha256", value: ""},
		{name: "valid checksum", key: "pricing_geoip.sha256", value: "sha256:" + strings.Repeat("a", 64)},
		{name: "invalid checksum", key: "pricing_geoip.sha256", value: "invalid", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validatePricingGeoIPOption(test.key, test.value)
			if test.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestIsChinaPricingClientUsesCloudflareCountryHeader(t *testing.T) {
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest("GET", "/api/pricing", nil)
	context.Request.Header.Set("CF-IPCountry", " cn ")
	assert.True(t, isChinaPricingClient(context))

	context.Request.Header.Set("CF-IPCountry", "US")
	context.Request.Header.Set("EO-Client-IPCountry", "cn")
	assert.True(t, isChinaPricingClient(context))

	context.Request.Header.Del("EO-Client-IPCountry")
	context.Request.RemoteAddr = ""
	assert.False(t, isChinaPricingClient(context))
}

func TestPricingClientIPReadsForwardingHeaders(t *testing.T) {
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest("GET", "/api/pricing", nil)
	context.Request.Header.Set("CF-Connecting-IP", "203.0.113.10")
	context.Request.Header.Set("X-Real-IP", "203.0.113.11")
	context.Request.Header.Set("X-Forwarded-For", "203.0.113.12, 203.0.113.13")
	assert.Equal(t, net.ParseIP("203.0.113.10"), pricingClientIP(context))

	context.Request.Header.Del("CF-Connecting-IP")
	assert.Equal(t, net.ParseIP("203.0.113.11"), pricingClientIP(context))

	context.Request.Header.Del("X-Real-IP")
	assert.Equal(t, net.ParseIP("203.0.113.12"), pricingClientIP(context))
}

func TestFilterPricingForChinaRemovesRestrictedCatalogEntries(t *testing.T) {
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

	filteredPricing, filteredVendors, filteredRatios, filteredUsable, filteredAuto, filteredEndpoints := filterPricingForChina(
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
