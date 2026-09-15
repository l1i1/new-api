package officialfit

import (
	"testing"

	"github.com/QuantumNous/new-api/constant"
)

// The family table is the single source of truth for official-fit family
// classification. These tests pin the exact ids and channel types the relay
// validator, the distributor pin and the error renderer all depend on, so a
// table edit cannot silently change routing or wire behavior.

func TestOfClassifiesEveryFamilyAndOnlyThose(t *testing.T) {
	cases := []struct {
		model  string
		family string
	}{
		{"deepseek-v4-flash", "deepseek-v4"},
		{"deepseek-v4-pro", "deepseek-v4"},
		{"deepseek-v4.1-flash", "deepseek-v4"},
		{"deepseek-v4-flash-vision-exp", "deepseek-v4"},
		{"DeepSeek-V4.1-Flash", "deepseek-v4"}, // case-insensitive
		{" deepseek-v4-pro ", "deepseek-v4"},   // trimmed
		{"kimi-k3", "kimi-k3"},
		{"KIMI-K3", "kimi-k3"},
		{"glm-5.3", "glm-5.3"},
		{"glm-5.3-flash", "glm-5.3"},
		// Not official-fit families: the platform's compatible behavior applies.
		{"deepseek-v3", ""},
		{"glm-5.2", ""},
		{"kimi-k2.7-code", ""},
		{"gpt-4o", ""},
		{"qwen3.8-max", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := FamilyOf(tc.model); got != tc.family {
			t.Errorf("FamilyOf(%q) = %q, want %q", tc.model, got, tc.family)
		}
	}
}

func TestChannelTypeMatchesTheFamily(t *testing.T) {
	cases := []struct {
		model string
		want  int
	}{
		{"deepseek-v4.1-flash", constant.ChannelTypeDeepSeek},
		{"kimi-k3", constant.ChannelTypeMoonshot},
		{"glm-5.3-flash", constant.ChannelTypeZhipu_v4},
		{"deepseek-v3", 0},
		{"gpt-4o", 0},
	}
	for _, tc := range cases {
		if got := ChannelType(tc.model); got != tc.want {
			t.Errorf("ChannelType(%q) = %d, want %d", tc.model, got, tc.want)
		}
	}
}

// A model may belong to a family yet not be an accepted official id. That gap
// is what the distributor uses as an unknown-model gate, so it must stay
// expressible.
func TestIsOfficialModelNameDistinguishesFamilyFromAcceptedID(t *testing.T) {
	accepted := []string{
		"deepseek-v4-pro", "deepseek-v4-flash", "deepseek-v4-flash-vision-exp",
		"deepseek-v4.1-flash", "kimi-k3", "glm-5.3", "glm-5.3-flash",
	}
	for _, model := range accepted {
		if !IsOfficialModelName(model) {
			t.Errorf("IsOfficialModelName(%q) = false, want true", model)
		}
	}
	// In a family but not an accepted id (live probe 2026-09-11 rejected this).
	for _, model := range []string{"deepseek-v4.1-pro", "deepseek-v4-notexist", "glm-5.3x", "kimi-k3x"} {
		if IsOfficialModelName(model) {
			t.Errorf("IsOfficialModelName(%q) = true, want false", model)
		}
	}
	// Outside every family.
	if IsOfficialModelName("gpt-4o") {
		t.Error("IsOfficialModelName(gpt-4o) = true, want false")
	}
}

func TestWireShapeIsPerFamily(t *testing.T) {
	cases := []struct {
		model string
		want  WireShape
	}{
		{"deepseek-v4.1-flash", WireShapeOpenAI},
		{"kimi-k3", WireShapeOpenAI},
		// GLM renders {error:{code,message}} with no type/param.
		{"glm-5.3", WireShapeZhipu},
		{"glm-5.3-flash", WireShapeZhipu},
		// Unknown models keep the platform's generic shape.
		{"gpt-4o", WireShapeOpenAI},
	}
	for _, tc := range cases {
		if got := WireShapeOf(tc.model); got != tc.want {
			t.Errorf("WireShapeOf(%q) = %q, want %q", tc.model, got, tc.want)
		}
	}
}

// Models is what the relay helper aliases for its exported model-name lists.
func TestModelsReturnsTheDeclaredList(t *testing.T) {
	if got := Models("deepseek-v4"); len(got) != 4 {
		t.Errorf("Models(deepseek-v4) has %d entries, want 4: %v", len(got), got)
	}
	if got := Models("glm-5.3"); len(got) != 2 {
		t.Errorf("Models(glm-5.3) has %d entries, want 2: %v", len(got), got)
	}
	if got := Models("nope"); got != nil {
		t.Errorf("Models(nope) = %v, want nil", got)
	}
}

// No family may prefix another, or Of() would depend on table order.
func TestNoFamilyPrefixesAnother(t *testing.T) {
	for i, a := range Families {
		for j, b := range Families {
			if i == j {
				continue
			}
			for _, pa := range a.ModelPrefixes {
				for _, pb := range b.ModelPrefixes {
					if len(pb) <= len(pa) && pa[:len(pb)] == pb {
						t.Errorf("family %q prefix %q shadows family %q prefix %q", a.ID, pa, b.ID, pb)
					}
				}
			}
		}
	}
}
