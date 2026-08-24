package main

import (
	"strings"
	"testing"
)

func TestFeatureProbeMatrixIsCompleteAndUnique(t *testing.T) {
	seen := make(map[string]struct{}, len(allCases))
	for _, id := range allCases {
		if !strings.HasPrefix(id, "DS-") {
			t.Fatalf("invalid case id %q", id)
		}
		if _, exists := seen[id]; exists {
			t.Fatalf("duplicate case id %s", id)
		}
		seen[id] = struct{}{}
	}
	if len(seen) != 84 {
		t.Fatalf("feature matrix has %d cases, want 84", len(seen))
	}
}

func TestSafeErrorDoesNotExposeMessage(t *testing.T) {
	if got := safeError(assertionError("secret-key-or-prompt")); got != "main.assertionError" {
		t.Fatalf("safeError leaked or changed error shape: %q", got)
	}
}

type assertionError string

func (assertionError) Error() string { return "secret-key-or-prompt" }
