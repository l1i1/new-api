package controller

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
)

const (
	complianceListMaxItems     = 64
	complianceKeywordMaxLength = 64
)

func splitComplianceList(raw string) []string {
	parts := strings.FieldsFunc(raw, func(character rune) bool {
		switch character {
		case ',', '，', ';', '\n', '\r':
			return true
		default:
			return false
		}
	})
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			values = append(values, value)
		}
	}
	return values
}

func parseComplianceCountryCodes(raw string) ([]string, error) {
	values := splitComplianceList(raw)
	if len(values) == 0 || len(values) > complianceListMaxItems {
		return nil, fmt.Errorf("country codes must contain between 1 and %d entries", complianceListMaxItems)
	}

	seen := make(map[string]struct{}, len(values))
	codes := make([]string, 0, len(values))
	for _, value := range values {
		code := strings.ToUpper(value)
		if len(code) != 2 || code[0] < 'A' || code[0] > 'Z' || code[1] < 'A' || code[1] > 'Z' {
			return nil, fmt.Errorf("country code %q must contain exactly two ASCII letters", value)
		}
		if _, exists := seen[code]; exists {
			continue
		}
		seen[code] = struct{}{}
		codes = append(codes, code)
	}
	return codes, nil
}

func parseComplianceKeywords(raw string) ([]string, error) {
	values := splitComplianceList(raw)
	if len(values) == 0 || len(values) > complianceListMaxItems {
		return nil, fmt.Errorf("keywords must contain between 1 and %d entries", complianceListMaxItems)
	}

	seen := make(map[string]struct{}, len(values))
	keywords := make([]string, 0, len(values))
	for _, value := range values {
		keyword := strings.ToLower(value)
		if utf8.RuneCountInString(keyword) > complianceKeywordMaxLength {
			return nil, fmt.Errorf("keyword %q exceeds %d characters", value, complianceKeywordMaxLength)
		}
		if _, exists := seen[keyword]; exists {
			continue
		}
		seen[keyword] = struct{}{}
		keywords = append(keywords, keyword)
	}
	return keywords, nil
}

func complianceConfigValue(optionKey, envKey, defaultValue string) string {
	value := complianceGeoIPOptionValue(optionKey, envKey)
	if value == "" {
		return defaultValue
	}
	return value
}

func complianceEnabled() bool {
	value := complianceConfigValue(
		setting.ComplianceGeoIPEnabledOptionKey,
		setting.ComplianceGeoIPEnabledEnv,
		setting.ComplianceGeoIPEnabledDefault,
	)
	enabled, err := strconv.ParseBool(value)
	if err != nil {
		return true
	}
	return enabled
}

func complianceCountryCodes() []string {
	value := complianceConfigValue(
		setting.ComplianceGeoIPCountryCodesOptionKey,
		setting.ComplianceGeoIPCountryCodesEnv,
		setting.ComplianceGeoIPCountryCodesDefault,
	)
	codes, err := parseComplianceCountryCodes(value)
	if err == nil {
		return codes
	}
	codes, _ = parseComplianceCountryCodes(setting.ComplianceGeoIPCountryCodesDefault)
	return codes
}

func complianceModelKeywords() []string {
	value := complianceConfigValue(
		setting.ComplianceGeoIPModelKeywordsOptionKey,
		setting.ComplianceGeoIPModelKeywordsEnv,
		setting.ComplianceGeoIPModelKeywordsDefault,
	)
	keywords, err := parseComplianceKeywords(value)
	if err == nil {
		return keywords
	}
	keywords, _ = parseComplianceKeywords(setting.ComplianceGeoIPModelKeywordsDefault)
	return keywords
}

func complianceGroupKeywords() []string {
	value := complianceConfigValue(
		setting.ComplianceGeoIPGroupKeywordsOptionKey,
		setting.ComplianceGeoIPGroupKeywordsEnv,
		setting.ComplianceGeoIPGroupKeywordsDefault,
	)
	keywords, err := parseComplianceKeywords(value)
	if err == nil {
		return keywords
	}
	keywords, _ = parseComplianceKeywords(setting.ComplianceGeoIPGroupKeywordsDefault)
	return keywords
}

func containsComplianceKeyword(value string, keywords []string) bool {
	text := strings.ToLower(value)
	for _, keyword := range keywords {
		if strings.Contains(text, keyword) {
			return true
		}
	}
	return false
}

func isComplianceRestrictedModel(modelName string) bool {
	return containsComplianceKeyword(modelName, complianceModelKeywords())
}

func isComplianceRestrictedGroup(groupName string) bool {
	return containsComplianceKeyword(groupName, complianceGroupKeywords())
}

func filterComplianceGroups(groups []string) []string {
	filtered := make([]string, 0, len(groups))
	for _, group := range groups {
		if !isComplianceRestrictedGroup(group) {
			filtered = append(filtered, group)
		}
	}
	return filtered
}

func filterComplianceModels(models []string) []string {
	filtered := make([]string, 0, len(models))
	for _, modelName := range models {
		if !isComplianceRestrictedModel(modelName) {
			filtered = append(filtered, modelName)
		}
	}
	return filtered
}

func markComplianceFiltered(c *gin.Context, countryCode string) {
	c.Header("X-Compliance-Filtered", strings.ToLower(countryCode))
}

func setDiscoveryComplianceHeaders(c *gin.Context, countryCode string) {
	c.Header("Cache-Control", "no-store, no-cache, must-revalidate, private, max-age=0")
	c.Header("Pragma", "no-cache")
	c.Header("Expires", "0")
	if countryCode != "" {
		markComplianceFiltered(c, countryCode)
	}
}
