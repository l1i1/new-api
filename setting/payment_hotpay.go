package setting

// HotPay gateway connection settings. These live in the options table so the
// cutover can be operated from the admin billing settings UI without touching
// per-node environment files.
var (
	HotPayGatewayURL         string
	HotPayGatewayAPIKey      string
	HotPayGatewayAllowedHost string
	// HotPaySettlementURL is this deployment's signed settlement receiver,
	// sent with every checkout so HotPay settles to the owning application's
	// endpoint instead of a deployment-wide value.
	HotPaySettlementURL       string
	HotPaySettlementSecret    string
	HotPaySettlementMaxAgeSec int
)

const (
	HotPayGatewayURLOptionKey          = "HotPayGatewayURL"
	HotPayGatewayAPIKeyOptionKey       = "HotPayGatewayAPIKey"
	HotPayGatewayAllowedHostOption     = "HotPayGatewayAllowedHosts"
	HotPaySettlementURLOptionKey       = "HotPaySettlementURL"
	HotPaySettlementSecretOptionKey    = "HotPaySettlementSecret"
	HotPaySettlementMaxAgeSecOptionKey = "HotPaySettlementMaxAgeSeconds"
)
