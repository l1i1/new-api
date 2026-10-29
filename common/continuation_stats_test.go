package common

import "testing"

func TestContinuationCapabilityCandidate(t *testing.T) {
	cases := []struct {
		name  string
		stats ContinuationChannelStats
		want  bool
	}{
		{"no traffic", ContinuationChannelStats{}, false},
		{"too few attempts", ContinuationChannelStats{Attempts: 2, Successes: 2}, false},
		{"reliable resumer", ContinuationChannelStats{Attempts: 10, Successes: 8}, true},
		{"unreliable", ContinuationChannelStats{Attempts: 10, Successes: 2}, false},
		{"borderline passes", ContinuationChannelStats{Attempts: 10, Successes: 6}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ContinuationCapabilityCandidate(tc.stats, 3, 0.6); got != tc.want {
				t.Fatalf("candidate(%+v) = %v, want %v", tc.stats, got, tc.want)
			}
		})
	}
}

func TestContinuationOutcomeRecorded(t *testing.T) {
	const channelID = 986001
	t.Cleanup(func() {
		continuationStats.Lock()
		delete(continuationStats.byChannel, channelID)
		continuationStats.Unlock()
	})
	RecordContinuationOutcome(channelID, true)
	RecordContinuationOutcome(channelID, false)
	stats, ok := ContinuationCapabilitySnapshot()[channelID]
	if !ok {
		t.Fatal("channel must appear in the snapshot")
	}
	if stats.Attempts != 2 || stats.Successes != 1 || stats.Failures != 1 {
		t.Fatalf("unexpected counters: %+v", stats)
	}
	if stats.LastSuccessAt == 0 || stats.LastFailureAt == 0 {
		t.Fatalf("timestamps must be set: %+v", stats)
	}
}
