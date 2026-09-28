package middleware

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/fitpolicy"
)

// Observation for the fit-policy application path.
//
// The policy has several silent early returns: no snapshot, snapshot disabled,
// out of scope, an explicit pin, or a request the policy has no opinion about.
// All of them look identical from the outside — nothing happens — which makes a
// fault indistinguishable from a healthy "no opinion" and makes an equivalence
// window unfalsifiable: an empty divergence log reads either as "the policy
// agrees with the shipped predicate" or as "the policy never ran".
//
// These counters separate those cases. They are process-local, so the summary
// below is the only way out of the process; it goes to the ordinary log, which
// on the mainland fleet is readable with DescribeContainerLog and is captured
// over a long window by the drain-side collector. A counter endpoint would be a
// better fit for a permanent instrument and is deliberately not added here:
// this exists to answer one question without widening the API surface.
//
// The observer starts lazily from the first request that reaches the policy, so
// nothing runs while the layer is unused, and removing this file cannot leave a
// goroutine behind.
const fitPolicyObsInterval = 60 * time.Second

type fitPolicyObs struct {
	// Why a request left applyFitPolicy without a requirement.
	noSnapshot  atomic.Int64
	disabled    atomic.Int64
	outOfScope  atomic.Int64
	explicitPin atomic.Int64
	noOpinion   atomic.Int64
	// Reached a decision the policy has an opinion about: the only counter that
	// proves the policy is actually evaluating.
	evaluated atomic.Int64
	// Evaluated in shadow, so the decision was recorded and not acted on. This
	// counted disagreements with the shipped predicate until those were retired;
	// it now counts dry-run decisions, which is what shadow means on its own.
	shadowed atomic.Int64
}

var (
	fitPolicyStats fitPolicyObs
	fitPolicyOnce  sync.Once
)

func startFitPolicyObserver() {
	fitPolicyOnce.Do(func() {
		go func() {
			for {
				time.Sleep(fitPolicyObsInterval)
				reportFitPolicyStats()
			}
		}()
	})
}

// reportFitPolicyStats logs the current snapshot state and the counters since
// the process started. Snapshot identity is included so a summary line alone
// says whether a policy is installed at all and which one.
func reportFitPolicyStats() {
	snapshot := "nil"
	if s := fitpolicy.Current(); s != nil {
		snapshot = fmt.Sprintf("v%d enabled=%t shadow=%t hash=%s",
			s.Version(), s.Enabled(), s.Shadow(), s.Hash())
	}
	common.SysLog(fmt.Sprintf(
		"fitpolicy obs: snapshot=%s no_snapshot=%d disabled=%d out_of_scope=%d explicit_pin=%d no_opinion=%d evaluated=%d shadowed=%d",
		snapshot,
		fitPolicyStats.noSnapshot.Load(),
		fitPolicyStats.disabled.Load(),
		fitPolicyStats.outOfScope.Load(),
		fitPolicyStats.explicitPin.Load(),
		fitPolicyStats.noOpinion.Load(),
		fitPolicyStats.evaluated.Load(),
		fitPolicyStats.shadowed.Load(),
	))
}
