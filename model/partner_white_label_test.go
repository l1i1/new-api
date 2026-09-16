package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewWhiteLabelConfigExpiresSixMonthsAfterRegistration(t *testing.T) {
	registered := time.Date(2026, time.September, 15, 12, 0, 0, 0, time.UTC).Unix()
	config := newWhiteLabelConfig("tommy", registered)
	require.NotNil(t, config)
	assert.True(t, config.HideReferral)
	assert.Equal(t, "tommy", config.PartnerID)
	want := time.Date(2027, time.March, 15, 12, 0, 0, 0, time.UTC).Unix()
	assert.Equal(t, want, config.Until)
}

func TestNewWhiteLabelConfigFallsBackToNow(t *testing.T) {
	before := time.Now().Unix()
	config := newWhiteLabelConfig("tommy", 0)
	require.NotNil(t, config)
	assert.GreaterOrEqual(t, config.Until, before)
}

func TestStampWhiteLabelForInviterIgnoresNonPartners(t *testing.T) {
	// No partner_setting is configured in tests, so no inviter can match.
	// The stamp must be a silent no-op rather than a registration failure.
	stampWhiteLabelForInviter(0, 0)
	stampWhiteLabelForInviter(1, 0)
	stampWhiteLabelForInviter(1, 999999)
}
