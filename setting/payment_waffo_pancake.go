package setting

// Waffo Pancake hosted checkout configuration. Gateway is enabled once
// MerchantID + PrivateKey + ProductID are populated (no separate Enabled
// flag, matching Stripe / Creem). StoreID + ProductID are operator-bound
// via SaveWaffoPancakeConfig.
var (
	WaffoPancakeMerchantID string
	WaffoPancakePrivateKey string
	WaffoPancakeReturnURL  string
	// CNY per USD used to convert the local wallet amount to Pancake's USD
	// checkout currency. A non-positive value falls back to the generic top-up
	// price at runtime for compatibility with existing deployments.
	WaffoPancakeExchangeRate float64
	WaffoPancakeUnitPrice    float64 = 1.0
	WaffoPancakeMinTopUp     int     = 1
	WaffoPancakeStoreID      string
	WaffoPancakeProductID    string
)
