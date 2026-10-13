package billingexpr

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The 2026-10-13 kimi-k3 accumulation incident shapes, from the INC-02
// quantification: cache_tokens = k × modal prefix with small completion, and
// prompt far above the local estimate.
func TestCheckUsageDeviation(t *testing.T) {
	t.Run("normal usage passes", func(t *testing.T) {
		require.Nil(t, CheckUsageDeviation(2707, 100, 0, 2700, 500))
	})
	t.Run("all zeros pass (no baselines, no cache)", func(t *testing.T) {
		require.Nil(t, CheckUsageDeviation(0, 0, 0, 0, 0))
	})
	t.Run("tokenizer difference within tolerance passes", func(t *testing.T) {
		// local estimate 2700, upstream 3100 = +14.8% — inside the 20% band.
		require.Nil(t, CheckUsageDeviation(3100, 100, 0, 2700, 500))
	})

	t.Run("completion over client budget flags (cleanest criterion)", func(t *testing.T) {
		d := CheckUsageDeviation(2707, 450, 0, 2707, 100)
		require.NotNil(t, d)
		assert.Equal(t, DeviationCompletionOverBudget, d.Kind)
		assert.Equal(t, "c", d.Field)
		assert.Equal(t, 450.0, d.Value)
		assert.Equal(t, 100.0, d.Baseline)
	})
	t.Run("completion exactly at budget passes", func(t *testing.T) {
		require.Nil(t, CheckUsageDeviation(2707, 100, 0, 2707, 100))
	})
	t.Run("zero client budget disables criterion 2", func(t *testing.T) {
		// No declared budget: an oversized completion alone is not a verdict.
		require.Nil(t, CheckUsageDeviation(2707, 900, 0, 2707, 0))
	})

	t.Run("prompt exceeding estimate beyond tolerance flags", func(t *testing.T) {
		// INC-02 shape: local estimate ~2707, upstream accumulated 33984.
		d := CheckUsageDeviation(33984, 85, 0, 2707, 500)
		require.NotNil(t, d)
		assert.Equal(t, DeviationPromptExceeded, d.Kind)
		assert.Equal(t, "p", d.Field)
		assert.Equal(t, 33984.0, d.Value)
		assert.Equal(t, 2707.0, d.Baseline)
	})
	t.Run("upstream smaller than estimate passes (cache hit)", func(t *testing.T) {
		require.Nil(t, CheckUsageDeviation(500, 100, 0, 2707, 500))
	})
	t.Run("zero local estimate disables criterion 1", func(t *testing.T) {
		require.Nil(t, CheckUsageDeviation(33984, 85, 0, 0, 500))
	})

	t.Run("cache k-fingerprint flags (row 540085 of the CSV)", func(t *testing.T) {
		// cache=33792 = 12 × 2816, completion=85 <= 300.
		d := CheckUsageDeviation(33984, 85, 33792, 33984, 500)
		require.NotNil(t, d)
		assert.Equal(t, DeviationCacheMultiple, d.Kind)
		assert.Equal(t, "cr", d.Field)
		assert.Equal(t, 33792.0, d.Value)
		assert.Equal(t, 2816.0, d.Baseline)
	})
	t.Run("cache multiple with long completion passes (legitimate)", func(t *testing.T) {
		// Same fingerprint but completion 1200: the S3 trigger condition is
		// what makes the small-cache-multiple shape meaningful; long
		// completions legitimately carry large cache values.
		require.Nil(t, CheckUsageDeviation(33984, 1200, 33792, 33984, 2000))
	})
	t.Run("cache k=1 (single round) passes", func(t *testing.T) {
		require.Nil(t, CheckUsageDeviation(5632, 85, 2816, 5632, 500))
	})
	t.Run("cache not an exact multiple passes", func(t *testing.T) {
		require.Nil(t, CheckUsageDeviation(33985, 85, 33793, 33985, 500))
	})
	t.Run("every prefix in the family is detected", func(t *testing.T) {
		for _, prefix := range cachePrefixFamily {
			d := CheckUsageDeviation(prefix*3, 100, prefix*3, prefix*3, 500)
			require.NotNil(t, d, "prefix %g must be detected", prefix)
			assert.Equal(t, DeviationCacheMultiple, d.Kind)
			assert.Equal(t, prefix, d.Baseline)
		}
	})

	t.Run("criterion order: completion budget wins first", func(t *testing.T) {
		// Both criterion 2 and 1 would match; the budget violation is the
		// more definitive verdict and must be the one reported.
		d := CheckUsageDeviation(33984, 450, 0, 2707, 100)
		require.NotNil(t, d)
		assert.Equal(t, DeviationCompletionOverBudget, d.Kind)
	})
}

func TestUsageDeviationAuditMap(t *testing.T) {
	d := &UsageDeviation{
		Kind:     DeviationCacheMultiple,
		Field:    "cr",
		Value:    33792,
		Baseline: 2816,
	}
	m := d.AuditMap()
	assert.Equal(t, DeviationCacheMultiple, m["kind"])
	assert.Equal(t, "cr", m["field"])
	assert.Equal(t, 33792.0, m["value"])
	assert.Equal(t, 2816.0, m["baseline"])
	assert.Equal(t, "observed", m["action"])
	assert.Equal(t, "usage deviation (cache_integer_multiple): field=cr value=33792 baseline=2816", d.Error())
}
