package fitpolicy

import (
	"os"
	"testing"
)

// Temporary helper: print the compiled hash of a candidate document so an
// operator can pre-bind capability marks before installing it (zero-gap flip).
func TestZZPrintCandidateHash(t *testing.T) {
	path := os.Getenv("FITPOLICY_CANDIDATE")
	if path == "" {
		t.Skip("FITPOLICY_CANDIDATE not set")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	policy, err := ParsePolicy(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	snapshot, err := Compile(policy)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	t.Logf("CANDIDATE_HASH=%s", snapshot.Hash())
	t.Logf("CANDIDATE_VERSION=%d", snapshot.Version())
}
