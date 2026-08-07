package setting

const (
	ComplianceGeoIPEnabledEnv             = "COMPLIANCE_GEOIP_ENABLED"
	ComplianceGeoIPCountryCodesEnv        = "COMPLIANCE_GEOIP_COUNTRY_CODES"
	ComplianceGeoIPModelKeywordsEnv       = "COMPLIANCE_GEOIP_MODEL_KEYWORDS"
	ComplianceGeoIPGroupKeywordsEnv       = "COMPLIANCE_GEOIP_GROUP_KEYWORDS"
	ComplianceGeoIPRetryBackoffMinutesEnv = "COMPLIANCE_GEOIP_RETRY_BACKOFF_MINUTES"
	ComplianceGeoIPDatabaseEnv            = "COMPLIANCE_GEOIP_DB"
	ComplianceGeoIPDownloadEnv            = "COMPLIANCE_GEOIP_URL"
	ComplianceGeoIPSHA256Env              = "COMPLIANCE_GEOIP_SHA256"

	ComplianceGeoIPEnabledOptionKey             = "compliance_geoip.enabled"
	ComplianceGeoIPCountryCodesOptionKey        = "compliance_geoip.country_codes"
	ComplianceGeoIPModelKeywordsOptionKey       = "compliance_geoip.model_keywords"
	ComplianceGeoIPGroupKeywordsOptionKey       = "compliance_geoip.group_keywords"
	ComplianceGeoIPRetryBackoffMinutesOptionKey = "compliance_geoip.retry_backoff_minutes"
	ComplianceGeoIPDatabaseOptionKey            = "compliance_geoip.db"
	ComplianceGeoIPDownloadOptionKey            = "compliance_geoip.url"
	ComplianceGeoIPSHA256OptionKey              = "compliance_geoip.sha256"

	ComplianceGeoIPEnabledDefault             = "true"
	ComplianceGeoIPCountryCodesDefault        = "CN"
	ComplianceGeoIPModelKeywordsDefault       = "gpt,gemini,claude,grok"
	ComplianceGeoIPGroupKeywordsDefault       = "gpt,gemini,claude,grok,genpic"
	ComplianceGeoIPRetryBackoffMinutesDefault = "5"
)
