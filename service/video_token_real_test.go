package service

import (
	"os"
	"testing"
)

// TestDecodeVideoMetadataRealContainer runs the parser against a real encoder
// output when one is pointed at by CDP_BENCH_VIDEO. The calibration fixtures
// live outside the repository (they are large binary samples), so the test
// skips instead of failing when the variable is unset.
//
// Example:
//
//	CDP_BENCH_VIDEO=../tools/cdp-bench/data/tmp/sample.mp4 go test ./service/ -run RealContainer -v
func TestDecodeVideoMetadataRealContainer(t *testing.T) {
	path := os.Getenv("CDP_BENCH_VIDEO")
	if path == "" {
		t.Skip("set CDP_BENCH_VIDEO to a real mp4 to run this check")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read fixture: %v", err)
	}
	meta, ok := DecodeVideoMetadata(data)
	if !ok {
		t.Fatalf("real container %s did not parse", path)
	}
	t.Logf("parsed %s: %dx%d %.3fs -> %d video tokens",
		path, meta.Width, meta.Height, meta.DurationSeconds, EstimateVideoTokens(meta))
}
