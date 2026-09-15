// Package officialfit holds the model-family table behind the per-user
// official-fit profile.
//
// Family knowledge used to be spread across the relay validator, the channel
// cache, the distributor and the error renderer, so adding a family meant
// editing a switch in each of those files and keeping four prefix checks
// consistent. The families are declared once here and every consumer
// classifies a model through Of / ChannelType / WireShape, which keeps the
// per-user dimensions (`official_fit.profile`) the only thing that varies per
// request.
//
// The table is deliberately free of relay state: it answers "which family is
// this model id" and "how does that family behave", nothing else.
package officialfit

import (
	"strings"

	"github.com/QuantumNous/new-api/constant"
)

// WireShape selects how a family's validation errors are rendered on the wire.
// The shape belongs to the family because it is a property of the provider's
// API, not of the caller.
type WireShape string

const (
	// WireShapeOpenAI renders {error:{message,type,param,code}}.
	WireShapeOpenAI WireShape = "openai"
	// WireShapeZhipu renders Zhipu v4's {error:{code,message}}: a numeric
	// machine code and no type/param pair.
	WireShapeZhipu WireShape = "zhipu"
)

// Canonical family ids. They are both the table keys and the profile keys
// users write in `official_fit.profile`, so callers must reference these
// constants instead of repeating the literals: a mistyped family id silently
// disables official-fit behavior for that family.
const (
	FamilyDeepSeekV4 = "deepseek-v4"
	FamilyKimiK3     = "kimi-k3"
	FamilyGlm53      = "glm-5.3"
)

// Family describes one officially-fitted model family.
type Family struct {
	// ID is the canonical family key ("deepseek-v4", "kimi-k3", "glm-5.3").
	// It is also the profile key users write in `official_fit.profile`.
	ID string
	// ModelPrefixes select the family from a client-facing model id. The
	// DeepSeek prefix carries no trailing dash so the dotted v4.1 line is
	// covered alongside deepseek-v4-*.
	ModelPrefixes []string
	// OfficialModelNames are the exact model ids the official endpoint
	// accepts. The accepted set is probed, never read from /v1/models: the
	// public list omits ids the endpoint still serves (live probe 2026-09-11
	// accepted deepseek-v4.1-flash while rejecting deepseek-v4.1-pro, neither
	// of which appears in the advertised list).
	OfficialModelNames []string
	// ChannelType is the channel type that counts as official for the family.
	ChannelType int
	// WireShape is how validation errors for the family are rendered.
	WireShape WireShape
}

// Families is the family table. A model matches a family when one of its
// prefixes matches; the first match wins, so no family may prefix another.
var Families = []Family{
	{
		ID:            FamilyDeepSeekV4,
		ModelPrefixes: []string{"deepseek-v4"},
		OfficialModelNames: []string{
			"deepseek-v4-pro",
			"deepseek-v4-flash",
			"deepseek-v4-flash-vision-exp",
			"deepseek-v4.1-flash",
		},
		ChannelType: constant.ChannelTypeDeepSeek,
		WireShape:   WireShapeOpenAI,
	},
	{
		ID:                 FamilyKimiK3,
		ModelPrefixes:      []string{"kimi-k3"},
		OfficialModelNames: []string{"kimi-k3"},
		ChannelType:        constant.ChannelTypeMoonshot,
		WireShape:          WireShapeOpenAI,
	},
	{
		ID:                 FamilyGlm53,
		ModelPrefixes:      []string{"glm-5.3"},
		OfficialModelNames: []string{"glm-5.3", "glm-5.3-flash"},
		ChannelType:        constant.ChannelTypeZhipu_v4,
		WireShape:          WireShapeZhipu,
	},
}

func normalize(model string) string {
	return strings.ToLower(strings.TrimSpace(model))
}

// Of returns the family governing model. ok is false for models outside every
// official-fit family, which keeps official-fit behavior off by default for
// the rest of the model catalog.
func Of(model string) (Family, bool) {
	m := normalize(model)
	if m == "" {
		return Family{}, false
	}
	for _, family := range Families {
		for _, prefix := range family.ModelPrefixes {
			if strings.HasPrefix(m, prefix) {
				return family, true
			}
		}
	}
	return Family{}, false
}

// FamilyOf returns the canonical family id for model, or "" when the model is
// not part of an official-fit family.
func FamilyOf(model string) string {
	family, ok := Of(model)
	if !ok {
		return ""
	}
	return family.ID
}

// Models returns the exact official model ids of the named family, or nil when
// the family is unknown.
func Models(familyID string) []string {
	for _, family := range Families {
		if family.ID == familyID {
			return family.OfficialModelNames
		}
	}
	return nil
}

// ChannelType returns the channel type that is official for model's family, or
// 0 when the model has no official-fit family. Callers use 0 as "this model
// has no official upstream" rather than as a valid channel type.
func ChannelType(model string) int {
	family, ok := Of(model)
	if !ok {
		return 0
	}
	return family.ChannelType
}

// IsOfficialModelName reports whether model is one of the exact ids the
// official endpoint of its family accepts. A model may belong to a family yet
// fail this check (deepseek-v4.1-pro matches the DeepSeek prefix but is not an
// accepted id), which is what makes it usable as an unknown-model gate.
func IsOfficialModelName(model string) bool {
	family, ok := Of(model)
	if !ok {
		return false
	}
	m := normalize(model)
	for _, name := range family.OfficialModelNames {
		if strings.EqualFold(m, name) {
			return true
		}
	}
	return false
}

// WireShapeOf returns the wire shape of model's family. Models outside every
// family get WireShapeOpenAI, the platform's generic error shape.
func WireShapeOf(model string) WireShape {
	family, ok := Of(model)
	if !ok {
		return WireShapeOpenAI
	}
	return family.WireShape
}
