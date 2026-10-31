package nodemetrics

import (
	"sync"
	"testing"
	"time"
)

func at(second int64) time.Time {
	return time.Unix(second, 0)
}

func TestSnapshotEmpty(t *testing.T) {
	m := newMetrics()
	got := m.snapshot(at(1_700_000_000))
	if got.WindowSeconds != WindowSeconds {
		t.Fatalf("window_seconds = %d, want %d", got.WindowSeconds, WindowSeconds)
	}
	if got.RPM != 0 || got.TPM != 0 || got.PromptTPM != 0 || got.CompletionTPM != 0 ||
		got.RequestBytes != 0 || got.ResponseBytes != 0 || got.RequestsTotal != 0 ||
		got.PromptTokensTotal != 0 || got.CompletionTokensTotal != 0 || got.TokensTotal != 0 ||
		got.RequestBytesTotal != 0 || got.ResponseBytesTotal != 0 {
		t.Fatalf("empty snapshot has non-zero fields: %+v", got)
	}
}

func TestWindowSums(t *testing.T) {
	m := newMetrics()
	now := int64(1_700_000_000)
	m.recordRequestAt(at(now-59), 100, 200)
	m.recordRequestAt(at(now-30), 5, 10)
	m.recordRequestAt(at(now), 7, 9)
	m.recordTokensAt(at(now-59), 11, 13)
	m.recordTokensAt(at(now), 17, 19)

	got := m.snapshot(at(now))
	if got.WindowSeconds != WindowSeconds {
		t.Fatalf("window_seconds = %d, want %d", got.WindowSeconds, WindowSeconds)
	}
	if got.RPM != 3 {
		t.Errorf("rpm = %d, want 3", got.RPM)
	}
	if got.PromptTPM != 28 {
		t.Errorf("prompt_tpm = %d, want 28", got.PromptTPM)
	}
	if got.CompletionTPM != 32 {
		t.Errorf("completion_tpm = %d, want 32", got.CompletionTPM)
	}
	if got.TPM != 60 {
		t.Errorf("tpm = %d, want 60", got.TPM)
	}
	if got.RequestBytes != 112 {
		t.Errorf("request_bytes = %d, want 112", got.RequestBytes)
	}
	if got.ResponseBytes != 219 {
		t.Errorf("response_bytes = %d, want 219", got.ResponseBytes)
	}
	if got.RequestsTotal != 3 || got.RequestBytesTotal != 112 || got.ResponseBytesTotal != 219 {
		t.Errorf("cumulative request totals = %+v, want requests=3 request=112 response=219", got)
	}
	if got.PromptTokensTotal != 28 || got.CompletionTokensTotal != 32 || got.TokensTotal != 60 {
		t.Errorf("cumulative token totals = %+v, want prompt=28 completion=32 tokens=60", got)
	}
}

// TestWindowBoundary checks that a bucket exactly 60 seconds old has rolled out
// of the window: the window covers [now-59, now].
func TestWindowBoundary(t *testing.T) {
	m := newMetrics()
	now := int64(1_700_000_000)
	m.recordRequestAt(at(now-60), 1, 1)
	if got := m.snapshot(at(now)); got.RPM != 0 || got.RequestBytes != 0 {
		t.Fatalf("request 60s old must be outside the window, got %+v", got)
	}

	m = newMetrics()
	m.recordRequestAt(at(now-59), 1, 1)
	if got := m.snapshot(at(now)); got.RPM != 1 || got.RequestBytes != 1 || got.ResponseBytes != 1 {
		t.Fatalf("request 59s old must be inside the window, got %+v", got)
	}

	// The cumulative totals are independent of the window.
	if got := m.snapshot(at(now)); got.RequestsTotal != 1 || got.RequestBytesTotal != 1 {
		t.Fatalf("cumulative totals must survive window rollover, got %+v", got)
	}
}

// TestBucketRollover proves that a ring slot is reset before a new second is
// recorded into it, so stale counters can never leak into a later window.
func TestBucketRollover(t *testing.T) {
	m := newMetrics()
	m.recordRequestAt(at(100), 10, 20)
	if got := m.snapshot(at(159)); got.RPM != 1 || got.RequestBytes != 10 || got.ResponseBytes != 20 {
		t.Fatalf("request at second 100 must be inside the window at 159, got %+v", got)
	}

	// Second 160 maps to the same ring slot as second 100.
	m.recordRequestAt(at(160), 7, 9)
	got := m.snapshot(at(160))
	if got.RPM != 1 {
		t.Fatalf("rpm = %d, want 1 (stale bucket must be reset)", got.RPM)
	}
	if got.RequestBytes != 7 || got.ResponseBytes != 9 {
		t.Fatalf("request_bytes/response_bytes = %d/%d, want 7/9 (stale bytes must be reset)", got.RequestBytes, got.ResponseBytes)
	}
	if got.RequestsTotal != 2 || got.RequestBytesTotal != 17 || got.ResponseBytesTotal != 29 {
		t.Fatalf("cumulative totals = %+v, want requests=2 request=17 response=29", got)
	}
}

// TestClockJumpBackwards records after the wall clock moved backwards: buckets
// stamped in the "future" must not be counted, and recording must not panic or
// corrupt the bucket being written.
func TestClockJumpBackwards(t *testing.T) {
	m := newMetrics()
	m.recordRequestAt(at(1000), 3, 4)
	m.recordRequestAt(at(500), 5, 6)

	got := m.snapshot(at(500))
	if got.RPM != 1 {
		t.Fatalf("rpm = %d, want 1 (future bucket must be ignored)", got.RPM)
	}
	if got.RequestBytes != 5 || got.ResponseBytes != 6 {
		t.Fatalf("window bytes = %d/%d, want 5/6", got.RequestBytes, got.ResponseBytes)
	}
	if got.RequestsTotal != 2 || got.RequestBytesTotal != 8 || got.ResponseBytesTotal != 10 {
		t.Fatalf("cumulative totals = %+v, want requests=2 request=8 response=10", got)
	}
}

func TestNegativeAndZeroInputSafety(t *testing.T) {
	m := newMetrics()
	now := int64(1_700_000_000)
	m.recordRequestAt(at(now), -5, -7)
	m.recordTokensAt(at(now), -11, -13)

	got := m.snapshot(at(now))
	if got.RPM != 1 {
		t.Fatalf("rpm = %d, want 1 (a request is counted even when its sizes are unknown)", got.RPM)
	}
	if got.RequestBytes != 0 || got.ResponseBytes != 0 || got.RequestBytesTotal != 0 || got.ResponseBytesTotal != 0 {
		t.Fatalf("negative byte sizes must clamp to 0, got %+v", got)
	}
	if got.PromptTPM != 0 || got.CompletionTPM != 0 || got.TPM != 0 || got.PromptTokensTotal != 0 || got.CompletionTokensTotal != 0 {
		t.Fatalf("negative tokens must clamp to 0, got %+v", got)
	}

	m.recordRequestAt(at(now), 0, 0)
	m.recordTokensAt(at(now), 0, 0)
	got = m.snapshot(at(now))
	if got.RPM != 2 || got.TPM != 0 {
		t.Fatalf("zero-value records: rpm = %d, tpm = %d, want 2/0", got.RPM, got.TPM)
	}
}

func TestSnapshotIsReadOnly(t *testing.T) {
	m := newMetrics()
	m.recordRequestAt(at(1000), 1, 2)
	first := m.snapshot(at(1000))
	second := m.snapshot(at(1000))
	if first != second {
		t.Fatalf("snapshot is not stable: %+v != %+v", first, second)
	}
}

// TestPackageLevelHelpers exercises the exported API used by production code.
func TestPackageLevelHelpers(t *testing.T) {
	before := Snapshot(time.Now())
	RecordRequest(11, 22)
	RecordTokens(3, 4)
	after := Snapshot(time.Now())

	if delta := after.RequestsTotal - before.RequestsTotal; delta != 1 {
		t.Errorf("requests_total delta = %d, want 1", delta)
	}
	if delta := after.RequestBytesTotal - before.RequestBytesTotal; delta != 11 {
		t.Errorf("request_bytes_total delta = %d, want 11", delta)
	}
	if delta := after.ResponseBytesTotal - before.ResponseBytesTotal; delta != 22 {
		t.Errorf("response_bytes_total delta = %d, want 22", delta)
	}
	if delta := after.PromptTokensTotal - before.PromptTokensTotal; delta != 3 {
		t.Errorf("prompt_tokens_total delta = %d, want 3", delta)
	}
	if delta := after.CompletionTokensTotal - before.CompletionTokensTotal; delta != 4 {
		t.Errorf("completion_tokens_total delta = %d, want 4", delta)
	}
	if after.TPM != after.PromptTPM+after.CompletionTPM {
		t.Errorf("tpm = %d, want prompt_tpm+completion_tpm = %d", after.TPM, after.PromptTPM+after.CompletionTPM)
	}
	if after.TokensTotal != after.PromptTokensTotal+after.CompletionTokensTotal {
		t.Errorf("tokens_total = %d, want %d", after.TokensTotal, after.PromptTokensTotal+after.CompletionTokensTotal)
	}
}

// TestConcurrentRecords is a race-detector smoke test: concurrent writers must
// not lose increments.
func TestConcurrentRecords(t *testing.T) {
	m := newMetrics()
	now := at(1_700_000_000)
	const workers, perWorker = 8, 100
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perWorker; j++ {
				m.recordRequestAt(now, 1, 1)
				m.recordTokensAt(now, 1, 1)
			}
		}()
	}
	wg.Wait()

	got := m.snapshot(now)
	want := int64(workers * perWorker)
	if got.RPM != want || got.RequestsTotal != want {
		t.Fatalf("rpm/total = %d/%d, want %d", got.RPM, got.RequestsTotal, want)
	}
	if got.PromptTPM != want || got.CompletionTPM != want || got.TPM != 2*want {
		t.Fatalf("token sums = prompt:%d completion:%d tpm:%d, want %d/%d/%d", got.PromptTPM, got.CompletionTPM, got.TPM, want, want, 2*want)
	}
}
