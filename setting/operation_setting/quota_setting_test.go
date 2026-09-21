package operation_setting

import (
	"math"
	"testing"
)

func TestValidateQuotaOptionBoundsPreConsumeMultiplier(t *testing.T) {
	const key = "quota_setting.pre_consume_multiplier"

	for _, value := range []string{"0.5", "1", "100"} {
		if err := ValidateQuotaOption(key, value); err != nil {
			t.Errorf("ValidateQuotaOption(%q) returned error for %q: %v", key, value, err)
		}
	}
	for _, value := range []string{"0", "-0.5", "100.0001", "NaN", "Inf", "1e309"} {
		if err := ValidateQuotaOption(key, value); err == nil {
			t.Errorf("ValidateQuotaOption(%q) accepted invalid value %q", key, value)
		}
	}
}

func TestInputPreConsumeMultiplierRejectsInvalidRuntimeValues(t *testing.T) {
	previous := quotaSetting.PreConsumeMultiplier
	t.Cleanup(func() { quotaSetting.PreConsumeMultiplier = previous })

	for _, value := range []float64{0, -0.5, MaxInputPreConsumeMultiplier + 0.001, math.NaN(), math.Inf(1)} {
		quotaSetting.PreConsumeMultiplier = value
		if _, err := InputPreConsumeMultiplier(); err == nil {
			t.Errorf("InputPreConsumeMultiplier accepted invalid runtime value %v", value)
		}
	}

	quotaSetting.PreConsumeMultiplier = MaxInputPreConsumeMultiplier
	if got, err := InputPreConsumeMultiplier(); err != nil || got != MaxInputPreConsumeMultiplier {
		t.Fatalf("InputPreConsumeMultiplier at upper bound = (%v, %v), want (%v, nil)", got, err, MaxInputPreConsumeMultiplier)
	}
}
