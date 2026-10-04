package main

// Multi-round measurement: N independent rounds, one majority decision per row.
//
// A single sample is not a decision. Independent runs of this tool disagreed on
// 18% of the rows they measured, and a row one sample stored as
// supported=false was later measured true, so operators ran three rounds by
// hand and wrote only the rows a majority agreed on. --rounds N moves that work
// into the tool.
//
// The rules, all fail-closed:
//
//   - rounds == 1 is the legacy single-sample path and is unchanged: the one
//     sample decides, no round bookkeeping is attached to the row, and the
//     report claims rounds 1 with cases 1/1.
//   - rounds > 1 decides a row only on a strict majority of the requested
//     rounds: strictly more than half of them must agree on consistent, or on
//     divergence. A tie or a plurality leaves the row undecided, which is
//     carried as an inconclusive row and therefore never serialized.
//   - Every requested round counts in the denominator, including a round whose
//     probe was inconclusive or could not be executed: an abstention makes a
//     majority harder, never easier, and a two-round run needs both rounds to
//     agree.
//   - A majority row carries the weakest evidence tier among the rounds that
//     voted for it. Repetition must not launder an acceptance-tier observation
//     into a structural claim, so one acceptance-tier vote keeps the whole row
//     behind the --acceptance-marks gate.
//   - Rounds are independent and strictly sequential, and each round re-measures
//     the official baseline when one is configured, so a multi-round run costs N
//     times the single-round request count — not just N times the relay probes.

import (
	"fmt"
	"io"
	"strings"
)

// aggregateRounds folds one target's per-round measurements into the single row
// the report carries. base holds the row's static fields (identity, path,
// minimal request); samples holds one record per executed round, in round
// order.
func aggregateRounds(base probeResult, samples []roundRecord, rounds int) probeResult {
	if len(samples) == 0 {
		base.Verdict = verdictInconclusive
		base.Strength = strengthAcceptance
		base.Basis = "no measurement round was executed for this row"
		return base
	}
	if rounds <= 1 {
		return singleRoundRow(base, samples[0])
	}

	base.Rounds = rounds
	base.RoundRecords = samples

	consistent, divergent := 0, 0
	for _, sample := range samples {
		switch sample.Verdict {
		case verdictConsistent:
			consistent++
		case verdictDivergence:
			divergent++
		}
	}
	decided, votes := verdictInconclusive, 0
	switch {
	case consistent*2 > rounds:
		decided, votes = verdictConsistent, consistent
	case divergent*2 > rounds:
		decided, votes = verdictDivergence, divergent
	}

	base.Verdict = decided
	base.Votes = votes
	base.DivergenceShape = ""
	// The row-level signature is the first round that voted for the settled
	// verdict; for an undecided row it is the first executed round. Each round's
	// own signature and status stay in its round record, so nothing is lost.
	representative := samples[0]
	for _, sample := range samples {
		if decided != verdictInconclusive && sample.Verdict == decided {
			representative = sample
			break
		}
	}
	base.HTTPStatus = representative.HTTPStatus
	base.Attempts = representative.Attempts
	base.Transport = representative.Transport
	base.ChannelSignature = representative.Signature
	base.BaselineSignature = representative.Baseline

	if decided == verdictInconclusive {
		base.Strength = strengthAcceptance
		base.Basis = "no strict majority in " + itoa(rounds) + " rounds: " + voteTally(samples, rounds)
		return base
	}
	base.Strength = majorityStrength(samples, decided)
	base.DivergenceShape = representative.Shape
	base.Basis = "decided by a strict majority of " + itoa(rounds) + " rounds: " + voteTally(samples, rounds)
	if base.Strength == "" {
		base.Basis += "; the deciding rounds disagree on the evidence tier, so the row is omitted from the payload"
	}
	return base
}

// singleRoundRow is the legacy shape: the row is the one sample, field for
// field, with no round bookkeeping attached.
func singleRoundRow(base probeResult, sample roundRecord) probeResult {
	base.Verdict = sample.Verdict
	base.Strength = sample.Strength
	base.Basis = sample.Basis
	base.DivergenceShape = sample.Shape
	base.HTTPStatus = sample.HTTPStatus
	base.Attempts = sample.Attempts
	base.Transport = sample.Transport
	base.ChannelSignature = sample.Signature
	base.BaselineSignature = sample.Baseline
	return base
}

// majorityStrength is the weakest evidence tier among the rounds that voted for
// the winning verdict, or "" when one of them carries no known tier. An unknown
// tier is never encodable, so it stays "" rather than being rounded up.
func majorityStrength(samples []roundRecord, decided verdict) string {
	strength := strengthStructural
	for _, sample := range samples {
		if sample.Verdict != decided {
			continue
		}
		switch sample.Strength {
		case strengthStructural:
		case strengthAcceptance:
			strength = strengthAcceptance
		default:
			return ""
		}
	}
	return strength
}

// voteTally renders the vote a row's decision came from: "consistent 2/3
// (rounds 1, 3); divergence 1/3 (round 2); inconclusive 0/3".
func voteTally(samples []roundRecord, rounds int) string {
	parts := make([]string, 0, 3)
	for _, name := range []verdict{verdictConsistent, verdictDivergence, verdictInconclusive} {
		voted := make([]string, 0, len(samples))
		for _, sample := range samples {
			if sample.Verdict == name {
				voted = append(voted, itoa(sample.Round))
			}
		}
		part := string(name) + " " + itoa(len(voted)) + "/" + itoa(rounds)
		if len(voted) > 0 {
			part += " (rounds " + strings.Join(voted, ", ") + ")"
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "; ")
}

// perRoundVerdicts renders "r1 consistent, r2 divergence, r3 inconclusive".
func perRoundVerdicts(samples []roundRecord) string {
	parts := make([]string, 0, len(samples))
	for _, sample := range samples {
		parts = append(parts, "r"+itoa(sample.Round)+" "+string(sample.Verdict))
	}
	return strings.Join(parts, ", ")
}

// reportUndecided names every row a majority left undecided, with its per-round
// verdicts. The payload schema cannot carry an undecided row, so without this
// line the run would omit it silently.
func reportUndecided(rows []probeResult, stderr io.Writer) {
	for _, row := range rows {
		if len(row.RoundRecords) == 0 || row.Verdict != verdictInconclusive {
			continue
		}
		fmt.Fprintf(stderr, "fit-probe: undecided channel=%d family=%s model=%s behavior=%s (%s) - omitted from the payload\n",
			row.ChannelID, row.Family, row.Model, row.Behavior, perRoundVerdicts(row.RoundRecords))
	}
}

// decidedRowCount counts the rows this run decided, whether or not the payload
// encoded them: it is what a curated payload is measured against.
func decidedRowCount(rows []probeResult) int {
	count := 0
	for _, row := range rows {
		if row.Verdict == verdictConsistent || row.Verdict == verdictDivergence {
			count++
		}
	}
	return count
}

// multiRounds is the rounds value the report and the summary carry: 0 for a
// single-round run, so its JSON shape stays exactly what it was, and N above 1.
func multiRounds(rounds int) int {
	if rounds > 1 {
		return rounds
	}
	return 0
}
