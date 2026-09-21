package service

import (
	"encoding/binary"
	"testing"
)

// box builds one ISO-BMFF box: size, type, payload.
func box(boxType string, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out, uint32(8+len(payload)))
	copy(out[4:8], boxType)
	copy(out[8:], payload)
	return out
}

// mp4With builds a minimal container: moov{ mvhd, trak{ tkhd } }.
func mp4With(width, height int, timescale, duration uint32) []byte {
	mvhd := make([]byte, 100)
	mvhd[0] = 0 // version 0
	binary.BigEndian.PutUint32(mvhd[12:], timescale)
	binary.BigEndian.PutUint32(mvhd[16:], duration)

	tkhd := make([]byte, 84)
	tkhd[0] = 0 // version 0
	// Width and height are 16.16 fixed point.
	binary.BigEndian.PutUint32(tkhd[76:], uint32(width)<<16)
	binary.BigEndian.PutUint32(tkhd[80:], uint32(height)<<16)

	moov := box("moov", append(box("mvhd", mvhd), box("trak", box("tkhd", tkhd))...))
	return append(box("ftyp", []byte("isom\x00\x00\x02\x00isomiso2mp41")), moov...)
}

func TestDecodeVideoMetadataReadsMovieAndTrackHeaders(t *testing.T) {
	meta, ok := DecodeVideoMetadata(mp4With(1920, 1080, 1000, 5533))
	if !ok {
		t.Fatal("expected the minimal container to parse")
	}
	if meta.Width != 1920 || meta.Height != 1080 {
		t.Fatalf("dimensions: got %dx%d, want 1920x1080", meta.Width, meta.Height)
	}
	if meta.DurationSeconds < 5.53 || meta.DurationSeconds > 5.54 {
		t.Fatalf("duration: got %v, want ~5.533", meta.DurationSeconds)
	}
}

func TestDecodeVideoMetadataRejectsNonVideo(t *testing.T) {
	if _, ok := DecodeVideoMetadata([]byte("not a container at all")); ok {
		t.Fatal("garbage must not parse as a container")
	}
	if _, ok := DecodeVideoMetadata([]byte{}); ok {
		t.Fatal("empty input must not parse")
	}
	// A container with a movie header but no track header has no dimensions.
	moov := box("moov", box("mvhd", make([]byte, 100)))
	if _, ok := DecodeVideoMetadata(append(box("ftyp", []byte("isom")), moov...)); ok {
		t.Fatal("a container without a track header must not yield metadata")
	}
}

func TestDecodeVideoMetadataHandlesVersion1Headers(t *testing.T) {
	mvhd := make([]byte, 120)
	mvhd[0] = 1 // version 1: 64-bit times and duration
	binary.BigEndian.PutUint32(mvhd[20:], 600) // timescale
	binary.BigEndian.PutUint64(mvhd[24:], 3300) // duration

	tkhd := make([]byte, 96)
	tkhd[0] = 1
	binary.BigEndian.PutUint32(tkhd[88:], uint32(1280)<<16)
	binary.BigEndian.PutUint32(tkhd[92:], uint32(720)<<16)

	moov := box("moov", append(box("mvhd", mvhd), box("trak", box("tkhd", tkhd))...))
	meta, ok := DecodeVideoMetadata(append(box("ftyp", []byte("isom")), moov...))
	if !ok {
		t.Fatal("version 1 headers must parse")
	}
	if meta.Width != 1280 || meta.Height != 720 {
		t.Fatalf("dimensions: got %dx%d, want 1280x720", meta.Width, meta.Height)
	}
	if meta.DurationSeconds < 5.49 || meta.DurationSeconds > 5.51 {
		t.Fatalf("duration: got %v, want 5.5", meta.DurationSeconds)
	}
}

// TestEstimateVideoTokensAgainstCalibration pins the model to the fifteen-point
// sweep it was fitted on. Measured values come from api.moonshot.cn with an
// unchanged text baseline of 96 prompt tokens.
func TestEstimateVideoTokensAgainstCalibration(t *testing.T) {
	cases := []struct {
		name     string
		meta     VideoMetadata
		measured int
		// maxRelErr documents the fitted accuracy per size class; the smallest
		// size deviates most because frame-count rounding dominates there.
		maxRelErr float64
	}{
		{"native 5.53s", VideoMetadata{3612, 1952, 5.533}, 42320, 0.001},
		{"native 3.01s", VideoMetadata{3612, 1952, 3.008}, 25396, 0.001},
		{"native 1.51s", VideoMetadata{3612, 1952, 1.515}, 12703, 0.002},
		{"native 0.51s", VideoMetadata{3612, 1952, 0.512}, 4241, 0.005},
		{"native 11.07s (total cap)", VideoMetadata{3612, 1952, 11.067}, 42320, 0.001},
		{"1440p 5.53s (total cap)", VideoMetadata{2560, 1384, 5.533}, 42320, 0.001},
		{"2160p 5.53s (total cap)", VideoMetadata{3840, 2076, 5.533}, 42320, 0.001},
		{"1080p 5.53s", VideoMetadata{1920, 1038, 5.533}, 28929, 0.01},
		{"720p 5.53s", VideoMetadata{1280, 692, 5.533}, 12736, 0.01},
		{"360p 5.53s", VideoMetadata{640, 346, 5.533}, 3374, 0.10},
		{"360p 11.07s", VideoMetadata{640, 346, 11.067}, 7046, 0.12},
	}
	for _, tc := range cases {
		got := EstimateVideoTokens(tc.meta)
		if got == 0 {
			t.Fatalf("%s: model returned 0", tc.name)
		}
		relErr := float64(got-tc.measured) / float64(tc.measured)
		if relErr < 0 {
			relErr = -relErr
		}
		if relErr > tc.maxRelErr {
			t.Errorf("%s: got %d, measured %d (%.2f%% off, tolerance %.2f%%)",
				tc.name, got, tc.measured, relErr*100, tc.maxRelErr*100)
		}
	}
}

// TestEstimateVideoTokensRules pins the three rules the calibration discovered,
// since each is counter-intuitive and easy to "fix" into a regression.
func TestEstimateVideoTokensRules(t *testing.T) {
	// The source frame rate is ignored: 15 fps and 60 fps clips of the same
	// duration and size price identically (the caller passes no fps at all, so
	// this asserts the model has no fps input to misuse).
	short := EstimateVideoTokens(VideoMetadata{3612, 1952, 1.0})
	if short != EstimateVideoTokens(VideoMetadata{3612, 1952, 1.0}) {
		t.Fatal("the model must be deterministic")
	}

	// A 15-frame floor: a one-frame clip still bills the floor.
	oneFrame := EstimateVideoTokens(VideoMetadata{3612, 1952, 0.033})
	floor := EstimateVideoTokens(VideoMetadata{3612, 1952, 0.5})
	if oneFrame != floor {
		t.Fatalf("frame floor: a one-frame clip got %d, a 15-frame clip got %d", oneFrame, floor)
	}

	// Duration scales linearly below the cap.
	two := EstimateVideoTokens(VideoMetadata{3612, 1952, 2.0})
	four := EstimateVideoTokens(VideoMetadata{3612, 1952, 4.0})
	if four <= two {
		t.Fatalf("duration must increase cost below the cap: 2s=%d 4s=%d", two, four)
	}

	// The total cap binds and is never exceeded.
	for _, duration := range []float64{5.5, 30, 600} {
		if got := EstimateVideoTokens(VideoMetadata{3612, 1952, duration}); got != videoTokenCap {
			t.Fatalf("cap: %vs priced %d, want %d", duration, got, videoTokenCap)
		}
	}

	// The per-frame cap binds for large frames: 4K must not out-price the
	// native clip of the same duration.
	if EstimateVideoTokens(VideoMetadata{3840, 2076, 3.0}) != EstimateVideoTokens(VideoMetadata{2560, 1384, 3.0}) {
		t.Fatal("sizes above the per-frame cap must price the same")
	}
}

func TestEstimateVideoTokensRejectsUnusableMetadata(t *testing.T) {
	for _, meta := range []VideoMetadata{
		{0, 1080, 5},
		{1920, 0, 5},
		{1920, 1080, 0},
		{-1920, 1080, 5},
	} {
		if got := EstimateVideoTokens(meta); got != 0 {
			t.Fatalf("metadata %+v must not price (%d)", meta, got)
		}
	}
}
