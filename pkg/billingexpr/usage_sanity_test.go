package billingexpr

import (
	"math"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckTokenParamsSanity(t *testing.T) {
	t.Run("normal values pass", func(t *testing.T) {
		params := TokenParams{
			P: 1388, C: 342, Len: 1388, CR: 322, CC: 100,
			CC1h: 50, Img: 200, ImgCR: 80, ImgO: 10, AI: 0, AO: 0,
		}
		require.Nil(t, CheckTokenParamsSanity(params))
	})

	t.Run("all zero passes", func(t *testing.T) {
		require.Nil(t, CheckTokenParamsSanity(TokenParams{}))
	})

	t.Run("boundary at threshold passes", func(t *testing.T) {
		require.Nil(t, CheckTokenParamsSanity(TokenParams{P: MaxPlausibleTokensPerField}))
	})

	t.Run("one above threshold is anomalous", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			params TokenParams
			field  string
		}{
			// 2026-10-13 kimi-k3 incident: upstream garbage usage.
			{"prompt tokens 1.39e15", TokenParams{P: 1388347719831046, C: 562949953421056, Len: 1388347719831046, CR: 322313980642816}, "p"},
			{"completion 2^49-256", TokenParams{P: 100, C: 562949953421056}, "c"},
			{"cache read 3.2e14", TokenParams{P: 100, CR: 322313980642816}, "cr"},
			{"len slightly over", TokenParams{P: 100, Len: MaxPlausibleTokensPerField + 1}, "len"},
			{"cache creation 1h", TokenParams{P: 100, CC1h: MaxPlausibleTokensPerField * 10}, "cc1h"},
			{"image tokens", TokenParams{P: 100, Img: math.MaxFloat64}, "img"},
			{"image cache read", TokenParams{P: 100, ImgCR: MaxPlausibleTokensPerField + 0.5}, "img_cr"},
			{"image output", TokenParams{P: 100, ImgO: MaxPlausibleTokensPerField + 1}, "img_o"},
			{"audio input", TokenParams{P: 100, AI: MaxPlausibleTokensPerField + 1}, "ai"},
			{"audio output", TokenParams{P: 100, AO: MaxPlausibleTokensPerField + 1}, "ao"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				anomaly := CheckTokenParamsSanity(tc.params)
				require.NotNil(t, anomaly)
				assert.Equal(t, UsageAnomalyOutOfRange, anomaly.Kind)
				assert.Equal(t, tc.field, anomaly.Field)
				assert.Equal(t, float64(MaxPlausibleTokensPerField), anomaly.Threshold)
				assert.True(t, anomaly.Value < 0 || anomaly.Value > MaxPlausibleTokensPerField || math.IsNaN(anomaly.Value))
			})
		}
	})

	t.Run("negative is anomalous", func(t *testing.T) {
		anomaly := CheckTokenParamsSanity(TokenParams{P: -1})
		require.NotNil(t, anomaly)
		assert.Equal(t, "p", anomaly.Field)
		assert.Equal(t, -1.0, anomaly.Value)
	})

	t.Run("NaN is anomalous", func(t *testing.T) {
		anomaly := CheckTokenParamsSanity(TokenParams{C: math.NaN()})
		require.NotNil(t, anomaly)
		assert.Equal(t, "c", anomaly.Field)
	})

	t.Run("+Inf is anomalous", func(t *testing.T) {
		anomaly := CheckTokenParamsSanity(TokenParams{CR: math.Inf(1)})
		require.NotNil(t, anomaly)
		assert.Equal(t, "cr", anomaly.Field)
	})
}

func TestSaturationAnomaly(t *testing.T) {
	t.Run("nil clamp yields nil", func(t *testing.T) {
		require.Nil(t, SaturationAnomaly(nil))
	})

	t.Run("clamp converts to anomaly", func(t *testing.T) {
		clamp := &common.QuotaClamp{
			Op:       "QuotaRound",
			Kind:     common.QuotaClampOverflow,
			Original: 1.8782471540917400e16,
			Clamped:  common.MaxQuota,
		}
		anomaly := SaturationAnomaly(clamp)
		require.NotNil(t, anomaly)
		assert.Equal(t, UsageAnomalyQuotaSaturation, anomaly.Kind)
		assert.Equal(t, "quota", anomaly.Field)
		assert.Equal(t, clamp.Original, anomaly.Value)
		assert.Equal(t, float64(common.MaxQuota), anomaly.Threshold)
	})

	t.Run("audit map shape", func(t *testing.T) {
		anomaly := &UsageAnomaly{
			Kind:      UsageAnomalyOutOfRange,
			Field:     "p",
			Value:     1388347719831046,
			Threshold: MaxPlausibleTokensPerField,
		}
		m := anomaly.AuditMap()
		assert.Equal(t, UsageAnomalyOutOfRange, m["kind"])
		assert.Equal(t, "p", m["field"])
		assert.Equal(t, float64(1388347719831046), m["value"])
		assert.Equal(t, float64(MaxPlausibleTokensPerField), m["threshold"])
	})
}
