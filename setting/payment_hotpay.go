package setting

// HotPay gateway connection settings. These live in the options table so the
// cutover can be operated from the admin billing settings UI without touching
// per-node environment files.
var (
	HotPayGatewayURL          string
	HotPayGatewayAPIKey       string
	HotPayGatewayAllowedHost  string
	HotPaySettlementSecret    string
	HotPaySettlementMaxAgeSec int
)

const (
	HotPayGatewayURLOptionKey          = "HotPayGatewayURL"
	HotPayGatewayAPIKeyOptionKey       = "HotPayGatewayAPIKey"
	HotPayGatewayAllowedHostOption     = "HotPayGatewayAllowedHosts"
	HotPaySettlementSecretOptionKey    = "HotPaySettlementSecret"
	HotPaySettlementMaxAgeSecOptionKey = "HotPaySettlementMaxAgeSeconds"
)
