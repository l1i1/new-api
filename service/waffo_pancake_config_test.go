package service

import (
	"errors"
	"testing"

	"github.com/QuantumNous/new-api/setting"
	"github.com/stretchr/testify/require"
	pancake "github.com/waffo-com/waffo-pancake-sdk-go"
)

func validWaffoPancakeCatalog() *WaffoPancakeCatalog {
	return &WaffoPancakeCatalog{Stores: []WaffoPancakeCatalogStore{{
		ID:          "store-1",
		Status:      "active",
		ProdEnabled: true,
		OnetimeProducts: []WaffoPancakeCatalogProduct{{
			ID:             "product-1",
			Status:         "active",
			HasProdVersion: true,
		}},
	}}}
}

func TestValidateWaffoPancakeBinding(t *testing.T) {
	require.NoError(t, validateWaffoPancakeBinding(validWaffoPancakeCatalog(), "store-1", "product-1"))

	tests := []struct {
		name      string
		mutate    func(*WaffoPancakeCatalog)
		storeID   string
		productID string
		contains  string
	}{
		{name: "missing store", storeID: "store-2", productID: "product-1", contains: "store was not found"},
		{name: "inactive store", storeID: "store-1", productID: "product-1", mutate: func(c *WaffoPancakeCatalog) { c.Stores[0].Status = "archived" }, contains: "store is not active"},
		{name: "production disabled", storeID: "store-1", productID: "product-1", mutate: func(c *WaffoPancakeCatalog) { c.Stores[0].ProdEnabled = false }, contains: "production enabled"},
		{name: "wrong product", storeID: "store-1", productID: "product-2", contains: "does not belong"},
		{name: "inactive product", storeID: "store-1", productID: "product-1", mutate: func(c *WaffoPancakeCatalog) { c.Stores[0].OnetimeProducts[0].Status = "archived" }, contains: "product is not active"},
		{name: "unpublished product", storeID: "store-1", productID: "product-1", mutate: func(c *WaffoPancakeCatalog) { c.Stores[0].OnetimeProducts[0].HasProdVersion = false }, contains: "not published"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			catalog := validWaffoPancakeCatalog()
			if tc.mutate != nil {
				tc.mutate(catalog)
			}
			err := validateWaffoPancakeBinding(catalog, tc.storeID, tc.productID)
			require.ErrorContains(t, err, tc.contains)
		})
	}
}

func TestValidateWaffoPancakeProductionCurrencies(t *testing.T) {
	require.NoError(t, validateWaffoPancakeProductionCurrencies([]waffoPancakeProductVersion{{
		IsProdVersion: true,
		Prices: []WaffoPancakeCatalogPrice{
			{Currency: "cny"},
			{Currency: "USD"},
		},
	}}))

	err := validateWaffoPancakeProductionCurrencies([]waffoPancakeProductVersion{
		{IsProdVersion: false, Prices: []WaffoPancakeCatalogPrice{{Currency: "CNY"}, {Currency: "USD"}}},
		{IsProdVersion: true, Prices: []WaffoPancakeCatalogPrice{{Currency: "USD"}}},
	})
	require.ErrorContains(t, err, "missing currencies: CNY")

	err = validateWaffoPancakeProductionCurrencies([]waffoPancakeProductVersion{{
		IsProdVersion: false,
		Prices:        []WaffoPancakeCatalogPrice{{Currency: "CNY"}, {Currency: "USD"}},
	}})
	require.ErrorContains(t, err, "no production version")
}

func TestResolveWaffoPancakeSavePrivateKey(t *testing.T) {
	previousMerchantID := setting.WaffoPancakeMerchantID
	previousPrivateKey := setting.WaffoPancakePrivateKey
	t.Cleanup(func() {
		setting.WaffoPancakeMerchantID = previousMerchantID
		setting.WaffoPancakePrivateKey = previousPrivateKey
	})

	setting.WaffoPancakeMerchantID = "merchant-1"
	setting.WaffoPancakePrivateKey = "persisted-key"

	key, err := resolveWaffoPancakeSavePrivateKey("merchant-1", "")
	require.NoError(t, err)
	require.Equal(t, "persisted-key", key)

	_, err = resolveWaffoPancakeSavePrivateKey("merchant-2", "")
	require.ErrorContains(t, err, "merchant id changes")

	key, err = resolveWaffoPancakeSavePrivateKey("merchant-2", "new-key")
	require.NoError(t, err)
	require.Equal(t, "new-key", key)
}

func TestIsWaffoPancakeUnsupportedCurrencyError(t *testing.T) {
	err := &pancake.Error{
		Status: 400,
		Errors: []pancake.APIError{{
			Message: "Currency CNY is not supported for this product",
		}},
	}
	require.True(t, IsWaffoPancakeUnsupportedCurrencyError(err, "CNY"))
	require.True(t, IsWaffoPancakeUnsupportedCurrencyError(errors.Join(errors.New("checkout failed"), err), "CNY"))

	require.False(t, IsWaffoPancakeUnsupportedCurrencyError(&pancake.Error{
		Status: 500,
		Errors: []pancake.APIError{{Message: "Currency CNY is not supported"}},
	}, "CNY"))
	require.False(t, IsWaffoPancakeUnsupportedCurrencyError(errors.New("request timeout"), "CNY"))
	require.False(t, IsWaffoPancakeUnsupportedCurrencyError(&pancake.Error{
		Status: 400,
		Errors: []pancake.APIError{{Message: "Payment method is not supported"}},
	}, "CNY"))
}
