package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func fakeCLI(t *testing.T, payload string) string {
	t.Helper()
	d := t.TempDir()
	body := "#!/bin/sh\nprintf '%s\\n' '" + payload + "'\n"
	if err := os.WriteFile(filepath.Join(d, "claude"), []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", d)
	return t.TempDir()
}
func TestReviewStructuredOutputIsUsed(t *testing.T) {
	p := `{"type":"result","subtype":"success","is_error":false,"session_id":"probe-session","result":"plain text summary","structured_output":{"marker":"MKR-aaaaaaaaaaaaaaaa","requestId":"RQ-01","decision":"result","answer":"ok"}}`
	d := fakeCLI(t, p)
	r := &TurnRecord{}
	runClaudeTurn(context.Background(), r, "synthetic", schema2, "probe-session", d, "S1")
	if r.Status != "completed" {
		t.Fatalf("valid structured_output rejected: status=%s error=%s", r.Status, r.Err)
	}
}
func TestReviewFailureEnvelopeCannotPass(t *testing.T) {
	p := `{"type":"result","subtype":"error_max_turns","terminal_reason":"failed","is_error":false,"session_id":"probe-session","result":"{\"marker\":\"MKR-aaaaaaaaaaaaaaaa\",\"requestId\":\"RQ-01\",\"decision\":\"result\",\"answer\":\"ok\"}"}`
	d := fakeCLI(t, p)
	r := &TurnRecord{}
	runClaudeTurn(context.Background(), r, "synthetic", schema2, "probe-session", d, "S1")
	if r.Status == "completed" {
		t.Fatal("failure envelope incorrectly classified completed")
	}
}
