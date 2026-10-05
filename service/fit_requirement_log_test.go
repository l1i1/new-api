package service

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/fitpolicy"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// TestAppendFitRequirementInfoRecordsTheNarrowing pins the request-log
// attribution the operator needs: a request the official-fit policy pinned to
// an official-behaving channel must say so, with the behaviours it required and
// the rules that required them. Without this field the narrowing is silent and
// a log line cannot distinguish "the policy sent it here" from "priority did".
func TestAppendFitRequirementInfoRecordsTheNarrowing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(nil)
	other := model.NewLogOther()

	common.SetContextKey(ctx, constant.ContextKeyFitRequirement, fitpolicy.Requirement{
		Family:        "kimi-k3",
		Model:         "kimi-k3",
		Marks:         []string{"logprobs.present"},
		Rules:         []string{"k3-logprobs"},
		PolicyVersion: 5,
		PolicyHash:    "711aabfe",
		BaselineHash:  "kimi-k3-moonshot-2026-10-03",
	})
	appendFitRequirementInfo(ctx, other)

	snapshot := other.Snapshot()
	fit, ok := snapshot["fit"].(map[string]any)
	require.True(t, ok, "fit must be recorded as public metadata, got %v", snapshot)
	require.Equal(t, "kimi-k3", fit["family"])
	require.Equal(t, []string{"logprobs.present"}, fit["marks"])
	require.Equal(t, []string{"k3-logprobs"}, fit["rules"])
	require.Equal(t, 5, fit["policy"])
	require.Equal(t, "kimi-k3-moonshot-2026-10-03", fit["baseline"])
}

// TestAppendFitRequirementInfoStaysSilentWithoutAnOpinion asserts the field's
// absence is meaningful: a request the policy has no opinion about must not
// carry a narrowing marker, or every log line would claim one.
func TestAppendFitRequirementInfoStaysSilentWithoutAnOpinion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(nil)
	other := model.NewLogOther()

	appendFitRequirementInfo(ctx, other)
	require.NotContains(t, other.Snapshot(), "fit")

	// A requirement with no marks is NoOpinion: the policy ran and declined.
	common.SetContextKey(ctx, constant.ContextKeyFitRequirement, fitpolicy.Requirement{Family: "kimi-k3"})
	appendFitRequirementInfo(ctx, other)
	require.NotContains(t, other.Snapshot(), "fit")

	// A nil other must not panic either: the caller passes whatever it built.
	appendFitRequirementInfo(ctx, nil)
}
