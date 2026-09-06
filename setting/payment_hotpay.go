package setting

// HotPay gateway connection settings. These live in the options table so the
// cutover can be operated from the admin billing settings UI without touching
// per-node environment files; the settlement signing secret stays env-only
// because it is the single credential that can mint quota.
var (
	HotPayGatewayURL         string
	HotPayGatewayAPIKey      string
	HotPayGatewayAllowedHost string
	HotPayAlipayAccountID    string
)

const (
	HotPayGatewayURLOptionKey      = "HotPayGatewayURL"
	HotPayGatewayAPIKeyOptionKey   = "HotPayGatewayAPIKey"
	HotPayGatewayAllowedHostOption = "HotPayGatewayAllowedHosts"
	HotPayAlipayAccountIDOptionKey = "HotPayAlipayAccountID"
)
