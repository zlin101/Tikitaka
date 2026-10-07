package main

// TASK-0002 R3 修复的 fake Runtime 测试：不调用真实 CLI/模型/网络。
// 假可执行文件通过 PATH 注入；超时用缩短的 turnTimeout。

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func fakeOnPath(t *testing.T, name, script string) {
	t.Helper()
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, name), []byte("#!/bin/sh\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", d)
}

// signalAlive 报告是否存在命令行含 needle 的进程。
// 查询本身失败（如 pgrep 不可用）时保守返回 true：无进程断言必须由可用查询证明，
// 不得因查询失败而成立（R3 假阳性教训）。pgrep 退出码 1 = 无匹配。
func signalAlive(t *testing.T, queryPath, needle string) bool {
	t.Helper()
	out, err := exec.Command(queryPath, "-f", needle).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return false
		}
		return true // 查询失败：无法证明无进程
	}
	return len(strings.TrimSpace(string(out))) > 0
}

// R3：claude 超时路径。假 CLI 的后台子进程（sleep 30）继承进程组：
// 若只杀直属进程，子进程将残留 30s 并占住管道；正确实现应整组终止，
// runClaudeTurn 快速返回且无残留 sleep。
func TestClaudeTimeoutTerminatesProcessGroup(t *testing.T) {
	old := turnTimeout
	// The deadline includes executable startup; require a recorded child PID
	// below so an early startup timeout cannot satisfy the stop assertion.
	turnTimeout = time.Second
	defer func() { turnTimeout = old }()

	pidFile := filepath.Join(t.TempDir(), "pids")
	t.Setenv("PROBE_TEST_PID_FILE", pidFile)
	fakeOnPath(t, "claude", `/bin/sleep 30 &
child=$!
printf '%s %s\n' "$$" "$child" > "$PROBE_TEST_PID_FILE"
wait "$child"
`)
	t.Cleanup(func() {
		data, err := os.ReadFile(pidFile)
		if err != nil {
			return
		}
		fields := strings.Fields(string(data))
		if len(fields) != 2 {
			return
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid <= 1 {
			return
		}
		if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			t.Errorf("clean up fake process group: %v", err)
		}
	})
	r := &TurnRecord{}
	start := time.Now()
	runClaudeTurn(context.Background(), r, "synthetic", schema2, "", t.TempDir(), "S1")
	if r.Status != "timeout_stop_confirmed" {
		t.Fatalf("status=%s err=%s, want timeout_stop_confirmed", r.Status, r.Err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("runClaudeTurn returned after %s; likely waited on orphaned children", elapsed)
	}
	// Inspect only this fake's group and child, so unrelated host processes cannot affect the result.
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("fake did not record its process IDs: %v", err)
	}
	fields := strings.Fields(string(data))
	if len(fields) != 2 {
		t.Fatalf("invalid process IDs: %q", data)
	}
	for i, field := range fields {
		pid, err := strconv.Atoi(field)
		if err != nil || pid <= 1 {
			t.Fatalf("invalid process ID: %q", field)
		}
		if i == 0 {
			pid = -pid // The fake shell is the process group leader.
		}
		if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
			t.Fatalf("process/group %d absence unconfirmed: %v", pid, err)
		}
	}
}

// R3：codex turn 进行中失联 → unknown_turn_state，后续轮次全部 not_run，整体 inconclusive。
func TestCodexMidTurnDisconnectIsUnknown(t *testing.T) {
	old := turnTimeout
	turnTimeout = 2 * time.Second
	defer func() { turnTimeout = old }()

	// 假 app-server：依次回应 initialize/thread/start/turn/start 后关闭输出端
	//（模拟进程失联），但保持输入端打开，避免把写入断管与失联混淆。
	fakeOnPath(t, "codex", `echo '{"jsonrpc":"2.0","id":1,"result":{}}'
echo '{"jsonrpc":"2.0","id":2,"result":{"thread":{"id":"t1"}}}'
echo '{"jsonrpc":"2.0","id":3,"result":{"turn":{"id":"u1","status":"inProgress"}}}'
exec 1>&-
/bin/cat > /dev/null
`)
	out := t.TempDir()
	rep := runCodex(out)
	if rep["status"] != "inconclusive" {
		t.Fatalf("status=%v reason=%v, want inconclusive", rep["status"], rep["reason"])
	}
	turns := rep["turns"].([]*TurnRecord)
	if turns[0].Status != "unknown_turn_state" {
		t.Fatalf("turn0=%s, want unknown_turn_state", turns[0].Status)
	}
	for i := 1; i <= 3; i++ {
		if turns[i].Status != "not_run" {
			t.Fatalf("turn%d=%s, want not_run", i, turns[i].Status)
		}
	}
}

// R3：codex 启动阶段失联（thread/start 无响应）→ blocked，不误标轮次失败。
func TestCodexStartupDisconnectIsBlocked(t *testing.T) {
	fakeOnPath(t, "codex", `echo '{"jsonrpc":"2.0","id":1,"result":{}}'
exec 1>&-
/bin/cat > /dev/null
`)
	out := t.TempDir()
	rep := runCodex(out)
	if rep["status"] != "blocked" {
		t.Fatalf("status=%v reason=%v, want blocked", rep["status"], rep["reason"])
	}
	turns := rep["turns"].([]*TurnRecord)
	if turns[0].Status != "blocked" {
		t.Fatalf("turn0=%s, want blocked", turns[0].Status)
	}
}
