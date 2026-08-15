package setting

// Waffo Pancake hosted checkout configuration. Gateway is enabled once
// MerchantID + PrivateKey + ProductID are populated (no separate Enabled
// flag, matching Stripe / Creem). StoreID + ProductID are operator-bound
// via SaveWaffoPancakeConfig.
var (
	WaffoPancakeMerchantID string
	WaffoPancakePrivateKey string
	WaffoPancakeReturnURL  string
	// Retained for the USD branch of wallet checkout and for old option rows.
	WaffoPancakeExchangeRate float64
	WaffoPancakeUnitPrice    float64 = 1.0
	WaffoPancakeMinTopUp     int     = 1
	WaffoPancakeStoreID      string
	WaffoPancakeProductID    string
)
