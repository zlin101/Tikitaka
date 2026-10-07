package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReviewTurnStartResponseTimeoutIsUnknown(t *testing.T) {
	old := turnTimeout
	turnTimeout = 100 * time.Millisecond
	defer func() { turnTimeout = old }()
	fakeOnPath(t, "codex", "/bin/cat > /dev/null\n")
	c, err := startCodex(t.TempDir() + "/log")
	if err != nil {
		t.Fatal(err)
	}
	defer c.kill()
	r := &TurnRecord{}
	runCodexTurn(c, r, "known-thread", "synthetic", schema1)
	if !strings.HasPrefix(r.Status, "unknown") || !haltScheduling(r.Status) {
		t.Fatalf("sent turn/start but response timeout yields %s; scheduling permitted=%v", r.Status, !haltScheduling(r.Status))
	}
}

// Query errors must not make a no-process assertion pass.
func TestReviewLivenessCheckActuallyExecutes(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "query-ran")
	t.Setenv("PROBE_TEST_QUERY_MARKER", marker)
	fakeOnPath(t, "pgrep", `printf 'ran' > "$PROBE_TEST_QUERY_MARKER"
exit 3
`)
	query := filepath.Join(os.Getenv("PATH"), "pgrep")
	if !signalAlive(t, query, ".") {
		t.Fatal("failed query incorrectly proved process absence")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("process query did not execute: %v", err)
	}
}
