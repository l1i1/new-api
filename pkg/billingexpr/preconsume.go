package billingexpr

import (
	"fmt"
	"math"
)

// PreConsumeEstimate contains the full-output trace and the conservative raw
// expression cost reserved before an upstream response is available.
type PreConsumeEstimate struct {
	FullCost        float64
	ReservationCost float64
	Trace           TraceResult
}

// EstimatePreConsume evaluates the full-output estimate and the input-only
// estimate used by the reservation multiplier. The multiplier is applied only
// to the input component and may not reduce the full-output reservation.
// Request-priced expressions reserve their full estimate without scaling.
func EstimatePreConsume(exprStr, hash string, params TokenParams, multiplier float64, request RequestInput) (PreConsumeEstimate, error) {
	fullCost, fullTrace, err := RunExprByHashWithRequest(exprStr, hash, params, request)
	if err != nil {
		return PreConsumeEstimate{}, err
	}
	if err := validatePreConsumeCost(fullCost); err != nil {
		return PreConsumeEstimate{}, err
	}
	if fullTrace.BillingUnit == BillingUnitRequest {
		return PreConsumeEstimate{FullCost: fullCost, ReservationCost: fullCost, Trace: fullTrace}, nil
	}

	inputParams := params
	inputParams.C = 0
	inputCost, inputTrace, err := RunExprByHashWithRequest(exprStr, hash, inputParams, request)
	if err != nil {
		return PreConsumeEstimate{}, err
	}
	// A conditional expression may select a request-priced leaf only for the
	// input probe. A request fee is independent of input tokens, so it must not
	// be multiplied as if it were an input-priced component.
	if inputTrace.BillingUnit == BillingUnitRequest {
		inputCost = 0
	} else if err := validatePreConsumeCost(inputCost); err != nil {
		return PreConsumeEstimate{}, err
	}
	if multiplier == 0 {
		// Snapshots written before the multiplier field existed decode as zero.
		multiplier = 1
	}
	if multiplier <= 0 || math.IsNaN(multiplier) || math.IsInf(multiplier, 0) {
		return PreConsumeEstimate{}, fmt.Errorf("pre-consume multiplier must be finite and greater than zero")
	}

	reservationCost := fullCost + (multiplier-1)*inputCost
	if reservationCost < fullCost {
		reservationCost = fullCost
	}
	if math.IsNaN(reservationCost) || math.IsInf(reservationCost, 0) {
		return PreConsumeEstimate{}, fmt.Errorf("tiered pre-consume estimate is not finite")
	}

	return PreConsumeEstimate{FullCost: fullCost, ReservationCost: reservationCost, Trace: fullTrace}, nil
}

func validatePreConsumeCost(cost float64) error {
	if cost < 0 || math.IsNaN(cost) || math.IsInf(cost, 0) {
		return fmt.Errorf("tiered pre-consume expression result must be finite and non-negative")
	}
	return nil
}
