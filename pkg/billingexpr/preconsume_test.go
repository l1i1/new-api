package billingexpr_test

import (
	"math"
	"testing"

	"github.com/QuantumNous/new-api/pkg/billingexpr"
)

func TestEstimatePreConsumeUsesFullOutputAndInputComponent(t *testing.T) {
	expr := `c > 1000 ? tier("large", p * 2 + c * 20) : tier("small", p * 2 + c * 3)`
	result, err := billingexpr.EstimatePreConsume(
		expr,
		billingexpr.ExprHashString(expr),
		billingexpr.TokenParams{P: 100, C: 8192, Len: 100},
		2.5,
		billingexpr.RequestInput{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.FullCost != 164040 {
		t.Fatalf("full cost = %v, want 164040", result.FullCost)
	}
	if result.ReservationCost != 164340 {
		t.Fatalf("reservation cost = %v, want 164340", result.ReservationCost)
	}
	if result.Trace.MatchedTier != "large" {
		t.Fatalf("matched tier = %q, want large", result.Trace.MatchedTier)
	}
}

func TestEstimatePreConsumeDoesNotReduceFullEstimate(t *testing.T) {
	expr := `tier("base", p * 3 + c * 15)`
	result, err := billingexpr.EstimatePreConsume(
		expr,
		billingexpr.ExprHashString(expr),
		billingexpr.TokenParams{P: 1000, C: 8192, Len: 1000},
		0.5,
		billingexpr.RequestInput{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.ReservationCost != result.FullCost {
		t.Fatalf("reservation cost = %v, want full cost %v", result.ReservationCost, result.FullCost)
	}
}

func TestEstimatePreConsumeTreatsZeroMultiplierAsLegacyOne(t *testing.T) {
	expr := `tier("base", p * 3 + c * 15)`
	result, err := billingexpr.EstimatePreConsume(
		expr,
		billingexpr.ExprHashString(expr),
		billingexpr.TokenParams{P: 1000, C: 100, Len: 1000},
		0,
		billingexpr.RequestInput{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.ReservationCost != 4500 {
		t.Fatalf("reservation cost = %v, want 4500", result.ReservationCost)
	}
}

func TestEstimatePreConsumeLeavesRequestPriceUnscaled(t *testing.T) {
	expr := `tier("request", fixed(0.01))`
	result, err := billingexpr.EstimatePreConsume(
		expr,
		billingexpr.ExprHashString(expr),
		billingexpr.TokenParams{P: 1000, C: 8192, Len: 1000},
		2.5,
		billingexpr.RequestInput{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.FullCost != 10000 || result.ReservationCost != 10000 {
		t.Fatalf("costs = (%v, %v), want (10000, 10000)", result.FullCost, result.ReservationCost)
	}
	if result.Trace.BillingUnit != billingexpr.BillingUnitRequest {
		t.Fatalf("billing unit = %q, want request", result.Trace.BillingUnit)
	}
}

func TestEstimatePreConsumeIgnoresRequestFeeInputProbe(t *testing.T) {
	expr := `c > 100 ? tier("token", p * 2 + c * 3) : tier("request", fixed(0.01))`
	result, err := billingexpr.EstimatePreConsume(
		expr,
		billingexpr.ExprHashString(expr),
		billingexpr.TokenParams{P: 100, C: 200, Len: 100},
		2.5,
		billingexpr.RequestInput{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.FullCost != 800 || result.ReservationCost != 800 {
		t.Fatalf("costs = (%v, %v), want (800, 800)", result.FullCost, result.ReservationCost)
	}
}

func TestEstimatePreConsumeRejectsInvalidInput(t *testing.T) {
	expr := `tier("base", p * 2)`
	for _, multiplier := range []float64{-1, math.NaN(), math.Inf(1)} {
		_, err := billingexpr.EstimatePreConsume(
			expr,
			billingexpr.ExprHashString(expr),
			billingexpr.TokenParams{P: 100, C: 0, Len: 100},
			multiplier,
			billingexpr.RequestInput{},
		)
		if err == nil {
			t.Fatalf("multiplier %v: expected error", multiplier)
		}
	}

	for _, expr := range []string{`tier("negative", p * -1)`, `tier("nan", p / 0)`} {
		_, err := billingexpr.EstimatePreConsume(
			expr,
			billingexpr.ExprHashString(expr),
			billingexpr.TokenParams{P: 100, C: 0, Len: 100},
			1,
			billingexpr.RequestInput{},
		)
		if err == nil {
			t.Fatalf("expression %q: expected error", expr)
		}
	}
}
