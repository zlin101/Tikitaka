package main

import "testing"

// Run the existing CLI self-check matrix through the standard test entry point.
func TestOfflineChecks(t *testing.T) {
	if code := offline(); code != 0 {
		t.Fatalf("offline checks exited with %d", code)
	}
}
