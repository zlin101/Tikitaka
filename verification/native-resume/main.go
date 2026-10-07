// Package main runs the native session resume verification probe.
// Live mode executes four sequential turns and must be selected explicitly;
// offline mode checks synthetic inputs without invoking a model.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ---------- 常量与探针协议 ----------

// 超时/期限用 var：fake Runtime 测试可缩短，不影响 live 默认值。
var (
	turnTimeout    = 120 * time.Second // 每轮时限
	interruptGrace = 15 * time.Second  // 取消请求与停止核对共享的单一宽限窗口
	batchDeadline  = 20 * time.Minute  // 全局批次期限（任务契约）
)

const (
	probeProtocolNote = "Respond ONLY with a single JSON object matching the provided schema. Do not use tools. Do not read or write files."
	round1BodySnippet = "Mars greenhouse" // 用于离线断言：第二轮输入不得含第一轮正文
)

// codexAllowedItemTypes 是完成轮次里允许出现的 item 类型；
// 出现任何其他类型（命令执行、文件修改、MCP 调用等）即视为隔离违例。
var codexAllowedItemTypes = map[string]bool{
	"userMessage":  true,
	"agentMessage": true,
	"reasoning":    true,
}

// errDisconnected 表示 app-server 输出流关闭（进程退出/失联）。
var errDisconnected = errors.New("app-server output closed")

// errDeadline 表示等待响应/通知超过时限；对已发出的请求（如 turn/start）
// 而言远端状态不确定，不得降级为 failed（R3）。
var errDeadline = errors.New("wait deadline exceeded")

// deadlineWait 包装：供 errors.Is 判断等待超时。
func deadlineWait(what string) error { return fmt.Errorf("%w waiting for %s", errDeadline, what) }

// groupObserveWindow 是超时后确认进程组全部退出的观察窗口（有界轮询）。
var groupObserveWindow = 2 * time.Second

// codexItemsIsolation 返回完成轮次中白名单之外的 item 类型（去重）。
func codexItemsIsolation(turn map[string]any) []string {
	items, _ := turn["items"].([]any)
	var bad []string
	seen := map[string]bool{}
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := m["type"].(string)
		if !codexAllowedItemTypes[typ] && !seen[typ] {
			seen[typ] = true
			bad = append(bad, typ)
		}
	}
	return bad
}

// batchExpired 报告批次是否已超出全局期限。
func batchExpired(start, now time.Time) bool { return now.Sub(start) > batchDeadline }

// haltScheduling 报告某轮次状态之后是否必须停止后续调度：
// 停止或状态无法核清（unknown_*）时必须停止；干净的 failed/completed 不触发重试或停摆。
func haltScheduling(status string) bool {
	return strings.HasPrefix(status, "unknown")
}

// RoundResult 是两轮共用的结构化决策。
type RoundResult struct {
	Marker    string `json:"marker"`
	RequestID string `json:"requestId"`
	Decision  string `json:"decision"`
	Question  string `json:"question,omitempty"`
	Answer    string `json:"answer,omitempty"`
}

const schema1 = `{"type":"object","additionalProperties":false,` +
	`"required":["marker","requestId","decision","question"],` +
	`"properties":{` +
	`"marker":{"type":"string","minLength":8},` +
	`"requestId":{"type":"string","minLength":4},` +
	`"decision":{"type":"string","enum":["question"]},` +
	`"question":{"type":"string","minLength":1}}}`

const schema2 = `{"type":"object","additionalProperties":false,` +
	`"required":["marker","requestId","decision","answer"],` +
	`"properties":{` +
	`"marker":{"type":"string","minLength":8},` +
	`"requestId":{"type":"string","minLength":4},` +
	`"decision":{"type":"string","enum":["result"]},` +
	`"answer":{"type":"string","minLength":1}}}`

// round1Prompt 构造第一轮输入：marker 只出现在这里。
func round1Prompt(marker, reqID string) string {
	return fmt.Sprintf(`Turn 1 of 2 in a session-resume capability probe. Synthetic scenario; %s
Session marker (memorize it; never invent one): %s
Tag your output with request id: %s
Unfinished synthetic request: "Draft a haiku-style status line for a %s log."
Protocol: ask exactly ONE clarifying question about this request, then end your turn (wait for the user's answer).`, probeProtocolNote, marker, reqID, round1BodySnippet)
}

// round2Prompt 构造第二轮输入：只含 answer、请求关联标识与协议说明。
// 刻意不含 marker，也不含第一轮正文。
func round2Prompt(reqID, answer string) string {
	return fmt.Sprintf(`Turn 2 of 2. This session was resumed natively from turn 1; the earlier conversation context should already be part of this session. %s
You previously asked a clarifying question tagged %s. The user's answer: "%s"
In the JSON: marker = the session marker from turn 1, taken from your session memory (it is NOT restated in this message); requestId = %s; decision = "result"; answer = one sentence on how you would now complete the original request given the answer.`, probeProtocolNote, reqID, answer, reqID)
}

// parseStrict 解析结构化决策；未知字段即失败（对应 additionalProperties:false）。
func parseStrict(text string) (*RoundResult, error) {
	dec := json.NewDecoder(strings.NewReader(text))
	dec.DisallowUnknownFields()
	var r RoundResult
	if err := dec.Decode(&r); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, fmt.Errorf("trailing content after JSON object")
	}
	return &r, nil
}

// validateRound1Res：基线注意力检查（marker/requestId 回显 + question 决策）。
func validateRound1Res(r *RoundResult, marker, reqID string) error {
	if r.Decision != "question" {
		return fmt.Errorf("decision=%q want question", r.Decision)
	}
	if r.Marker != marker {
		return fmt.Errorf("marker echo mismatch")
	}
	if r.RequestID != reqID {
		return fmt.Errorf("requestId mismatch")
	}
	if strings.TrimSpace(r.Question) == "" {
		return fmt.Errorf("empty question")
	}
	return nil
}

// validateRound1：文本入口（Codex 路径：先解析再检查）。
func validateRound1(text, marker, reqID string) error {
	r, err := parseStrict(text)
	if err != nil {
		return fmt.Errorf("schema: %w", err)
	}
	return validateRound1Res(r, marker, reqID)
}

// validateRound2Res：核心续接判定的结构检查。
// marker 必须等于本会话 marker（第二轮输入从未提供它）；
// requestId 必须指向本会话原请求。
func validateRound2Res(r *RoundResult, marker, reqID string) error {
	if r.Decision != "result" {
		return fmt.Errorf("decision=%q want result", r.Decision)
	}
	if r.Marker != marker {
		return fmt.Errorf("marker mismatch: session memory not retained")
	}
	if r.RequestID != reqID {
		return fmt.Errorf("requestId=%q want %q (wrong association)", r.RequestID, reqID)
	}
	if strings.TrimSpace(r.Answer) == "" {
		return fmt.Errorf("empty answer")
	}
	return nil
}

// validateRound2：文本入口；全文不得出现另一会话的 marker。
func validateRound2(rawText, marker, reqID, otherMarker string) error {
	if otherMarker != "" && strings.Contains(rawText, otherMarker) {
		return fmt.Errorf("crosstalk: foreign marker %q present", otherMarker)
	}
	r, err := parseStrict(rawText)
	if err != nil {
		return fmt.Errorf("schema: %w", err)
	}
	return validateRound2Res(r, marker, reqID)
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// ---------- 轮次记录 ----------

// TurnRecord records a single probe turn and its observed runtime outcome.
type TurnRecord struct {
	Label     string         `json:"label"` // S1R1 / S2R1 / S1R2 / S2R2
	Start     time.Time      `json:"start"`
	End       time.Time      `json:"end"`
	Status    string         `json:"status"` // completed | completed_invalid_output | failed | interrupted | timeout_stop_confirmed | unknown_* | blocked | not_run
	SessionID string         `json:"sessionId"`
	Prompt    string         `json:"prompt"`
	Schema    string         `json:"schema"`
	RawOut    string         `json:"rawOut"`
	Parsed    *RoundResult   `json:"parsed,omitempty"`
	Validate  string         `json:"validate,omitempty"` // 空 = 通过
	Err       string         `json:"error,omitempty"`
	Extra     map[string]any `json:"extra,omitempty"`
}

func (t *TurnRecord) short() string {
	if t.Validate == "" && t.Status == "completed" {
		return t.Label + ":PASS"
	}
	return t.Label + ":" + t.Status
}

// ---------- Codex App Server 驱动（JSON-RPC over stdio, newline-JSON） ----------

type rpcMsg struct {
	ID     *json.Number    `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Err    *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message) }

// disconnect 包装：供 errors.Is 判断失联（进程退出导致输出流关闭）。
func disconnect(while string) error { return fmt.Errorf("%w while %s", errDisconnected, while) }

type codexClient struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	msgs    chan rpcMsg
	logFile *os.File
	nextID  int
	mu      sync.Mutex

	pendingMu sync.Mutex
	pending   []rpcMsg // call() 期间收到的通知，等待方先取
}

func (c *codexClient) stash(m rpcMsg) {
	c.pendingMu.Lock()
	c.pending = append(c.pending, m)
	c.pendingMu.Unlock()
}

func (c *codexClient) takePending() []rpcMsg {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	p := c.pending
	c.pending = nil
	return p
}

func startCodex(logPath string) (*codexClient, error) {
	f, err := os.Create(logPath)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("codex", "app-server")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		f.Close()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		f.Close()
		return nil, err
	}
	cmd.Stderr = f
	if err := cmd.Start(); err != nil {
		f.Close()
		return nil, err
	}
	c := &codexClient{cmd: cmd, stdin: stdin, msgs: make(chan rpcMsg, 256), logFile: f}
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 1<<20), 64<<20)
		for sc.Scan() {
			line := bytes.TrimSpace(sc.Bytes())
			if len(line) == 0 {
				continue
			}
			fmt.Fprintln(f, string(line))
			var m rpcMsg
			if json.Unmarshal(line, &m) == nil {
				c.msgs <- m
			}
		}
		close(c.msgs)
	}()
	return c, nil
}

func (c *codexClient) call(method string, params any, deadline time.Time) (json.RawMessage, error) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	c.mu.Unlock()
	b, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	if _, err := fmt.Fprintln(c.stdin, string(b)); err != nil {
		// 写入失败（如 EPIPE）：对端进程已不在，无法确认任何远端状态，按断连处理（R3）。
		return nil, disconnect("writing " + method)
	}
	fmt.Fprintf(c.logFile, "> %s\n", b)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, deadlineWait(method + " response")
		}
		select {
		case m, ok := <-c.msgs:
			if !ok {
				return nil, disconnect("waiting for " + method + " response")
			}
			if m.ID != nil && m.ID.String() == fmt.Sprint(id) {
				if m.Err != nil {
					return nil, m.Err
				}
				return m.Result, nil
			}
			if m.ID == nil {
				c.stash(m) // call 期间到达的通知（如 interrupt 后的 turn/completed）转交等待方
			}
		case <-time.After(remaining):
			return nil, deadlineWait(method + " response")
		}
	}
}

// waitNotification 等待 method 匹配且谓词成立的通知。
func (c *codexClient) waitNotification(method string, pred func(json.RawMessage) bool, deadline time.Time) (json.RawMessage, error) {
	for {
		for _, m := range c.takePending() {
			if m.Method == method && (pred == nil || pred(m.Params)) {
				return m.Params, nil
			}
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, deadlineWait("notification " + method)
		}
		select {
		case m, ok := <-c.msgs:
			if !ok {
				return nil, disconnect("waiting for " + method)
			}
			if m.ID != nil {
				continue // 其他请求的响应，不消费
			}
			if m.Method == method && (pred == nil || pred(m.Params)) {
				return m.Params, nil
			}
		case <-time.After(remaining):
			return nil, deadlineWait("notification " + method)
		}
	}
}

func (c *codexClient) kill() {
	if c.cmd.Process != nil {
		c.cmd.Process.Kill()
	}
	c.cmd.Wait()
	c.logFile.Close()
}

// extractThreadID 从 thread/start | thread/resume 响应中取 thread id（防御多代形状）。
func extractThreadID(raw json.RawMessage) (string, error) {
	var top map[string]any
	if err := json.Unmarshal(raw, &top); err != nil {
		return "", err
	}
	if th, ok := top["thread"].(map[string]any); ok {
		if id, ok := th["id"].(string); ok && id != "" {
			return id, nil
		}
	}
	if id, ok := top["id"].(string); ok && id != "" {
		return id, nil
	}
	if id, ok := top["conversationId"].(string); ok && id != "" {
		return id, nil
	}
	return "", fmt.Errorf("no thread id in response")
}

// turnParams 构造 turn/start 参数。
func turnParams(threadID, prompt, schema string) map[string]any {
	return map[string]any{
		"threadId":     threadID,
		"input":        []map[string]string{{"type": "text", "text": prompt}},
		"outputSchema": json.RawMessage(schema),
		"effort":       "low",
	}
}

// extractAgentText 从 turn.items 里取最后一条 agentMessage 文本。
func extractAgentText(turnObj map[string]any) string {
	items, _ := turnObj["items"].([]any)
	var last string
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := m["type"].(string)
		if typ == "agentMessage" || typ == "agent_message" {
			if s, ok := m["text"].(string); ok {
				last = s
			}
		}
	}
	return last
}

// runCodexTurn 执行单轮：turn/start → 等 turn/completed；超时在单一宽限窗口内
// 发取消（限定原 thread/turn）并核对停止；失联按 UNKNOWN 处理并停止后续调度（R3）。
func runCodexTurn(c *codexClient, rec *TurnRecord, threadID, prompt, schema string) {
	deadline := time.Now().Add(turnTimeout)
	rec.Start = time.Now()
	rec.Prompt = prompt
	rec.Schema = schema
	res, err := c.call("turn/start", turnParams(threadID, prompt, schema), deadline)
	if err != nil {
		if errors.Is(err, errDisconnected) || errors.Is(err, errDeadline) {
			// 响应失联或超时：请求可能已被服务端受理，轮次状态不确定，
			// 按 UNKNOWN 处理并停止后续调度，不得降级为 failed（R3）。
			rec.Status, rec.Err = "unknown_turn_start_unconfirmed", "turn/start: "+err.Error()
		} else {
			rec.Status, rec.Err = "failed", "turn/start: "+err.Error()
		}
		rec.End = time.Now()
		return
	}
	var startResp struct {
		Turn map[string]any `json:"turn"`
	}
	_ = json.Unmarshal(res, &startResp)
	turnID, _ := startResp.Turn["id"].(string)
	if turnID == "" {
		rec.Status, rec.Err = "failed", "no turn id in turn/start response"
		rec.End = time.Now()
		return
	}
	rec.Extra = map[string]any{"turnId": turnID}

	matchCompleted := func(p json.RawMessage) bool {
		var n struct {
			ThreadID string `json:"threadId"`
			Turn     struct {
				ID string `json:"id"`
			} `json:"turn"`
		}
		if json.Unmarshal(p, &n) != nil {
			return false
		}
		return n.ThreadID == threadID && n.Turn.ID == turnID
	}
	params, err := c.waitNotification("turn/completed", matchCompleted, deadline)
	if err != nil {
		if errors.Is(err, errDisconnected) {
			rec.Status = "unknown_turn_state"
			rec.Err = "connection lost while turn in flight; remote stop state unverifiable"
			rec.End = time.Now()
			return
		}
		// 超时：取消请求与停止核对共享同一个宽限窗口，不叠加 15+15（R3）。
		graceDeadline := time.Now().Add(interruptGrace)
		if _, ierr := c.call("turn/interrupt", map[string]string{"threadId": threadID, "turnId": turnID}, graceDeadline); ierr != nil {
			if errors.Is(ierr, errDisconnected) {
				rec.Status = "unknown_turn_state"
				rec.Err = "connection lost during interrupt"
			} else {
				rec.Status = "unknown_timeout_unconfirmed"
				rec.Err = "timeout; interrupt failed: " + ierr.Error()
			}
			rec.End = time.Now()
			return
		}
		p2, cerr := c.waitNotification("turn/completed", func(p json.RawMessage) bool {
			var n struct {
				Turn struct {
					ID string `json:"id"`
				} `json:"turn"`
			}
			if json.Unmarshal(p, &n) != nil {
				return false
			}
			return n.Turn.ID == turnID
		}, graceDeadline)
		if cerr != nil {
			if errors.Is(cerr, errDisconnected) {
				rec.Status = "unknown_turn_state"
				rec.Err = "connection lost during interrupt grace"
			} else {
				rec.Status = "unknown_timeout_unconfirmed"
				rec.Err = "timeout; interrupt sent but no completion observed within shared grace window"
			}
			rec.End = time.Now()
			return
		}
		params = p2
		rec.Status = "interrupted"
		rec.Err = "turn timed out; interrupt confirmed within shared grace window"
	} else {
		rec.Status = "completed"
	}
	var completed map[string]any
	_ = json.Unmarshal(params, &completed)
	turn, _ := completed["turn"].(map[string]any)
	if turn == nil {
		rec.Status, rec.Err = "failed", "turn/completed without turn object"
		rec.End = time.Now()
		return
	}
	if st, _ := turn["status"].(string); st != "" && rec.Status == "completed" && st != "completed" {
		rec.Status = st // failed / interrupted
	}
	rec.End = time.Now()
	rec.RawOut = extractAgentText(turn)
	if rec.Status == "completed" && rec.RawOut == "" {
		rec.Status, rec.Err = "completed_invalid_output", "no agentMessage text in completed turn"
	}
	// 隔离白名单：出现命令执行/文件修改/MCP 等 item 即违例（R1）。
	if viol := codexItemsIsolation(turn); len(viol) > 0 && rec.Status == "completed" {
		rec.Status = "completed_invalid_output"
		rec.Validate = "isolation: unexpected item types " + strings.Join(viol, ",")
	}
}

// runCodex 完整执行一个 Runtime 的 4 轮交错探针。
func runCodex(outDir string) map[string]any {
	logPath := filepath.Join(outDir, "app_server_run.log")
	c, err := startCodex(logPath)
	report := map[string]any{"runtime": "codex"}
	if err != nil {
		report["status"] = "blocked"
		report["reason"] = "start app-server: " + err.Error()
		writeJSON(filepath.Join(outDir, "summary.json"), report)
		return report
	}
	defer c.kill()

	mkr := map[string]string{"S1": "MKR-" + randomHex(8), "S2": "MKR-" + randomHex(8)}
	rq := map[string]string{"S1": "RQ-" + randomHex(2), "S2": "RQ-" + randomHex(2)}
	answer := map[string]string{"S1": "Use exactly 5 words per line and mention the basil plant.", "S2": "Keep it under 12 words and mention the airlock door."}

	if _, err := c.call("initialize", map[string]any{"clientInfo": map[string]string{"name": "tikitaka-native-resume-probe", "title": "probe", "version": "0.1"}}, time.Now().Add(30*time.Second)); err != nil {
		report["status"] = "blocked"
		report["reason"] = "initialize: " + err.Error()
		writeJSON(filepath.Join(outDir, "summary.json"), report)
		return report
	}

	threadID := map[string]string{}
	sessionDirs := map[string]string{}
	defer func() {
		// 空临时 cwd，批后清理；模型在会话 cwd 内无可读的项目/证据材料（R1）。
		for _, d := range sessionDirs {
			os.RemoveAll(d)
		}
	}()
	startSession := func(s string) bool {
		dir, err := os.MkdirTemp("", "probe-codex-"+s+"-")
		if err != nil {
			report["status"], report["reason"] = "blocked", "temp cwd "+s+": "+err.Error()
			return false
		}
		sessionDirs[s] = dir
		res, err := c.call("thread/start", map[string]any{
			"cwd": dir, "sandbox": "read-only", "approvalPolicy": "never",
		}, time.Now().Add(30*time.Second))
		if err != nil {
			report["status"], report["reason"] = "blocked", "thread/start "+s+": "+err.Error()
			return false
		}
		id, err := extractThreadID(res)
		if err != nil {
			report["status"], report["reason"] = "blocked", err.Error()
			return false
		}
		threadID[s] = id
		return true
	}
	resumeSession := func(s string) bool {
		_, err := c.call("thread/resume", map[string]any{
			"threadId": threadID[s], "cwd": sessionDirs[s], "sandbox": "read-only", "approvalPolicy": "never",
		}, time.Now().Add(30*time.Second))
		if err != nil {
			report["status"], report["reason"] = "fail", "thread/resume "+s+": "+err.Error()
			return false
		}
		return true
	}

	type step struct {
		label, s, round string
		start           bool
	}
	steps := []step{
		{"S1R1", "S1", "R1", true}, {"S2R1", "S2", "R1", true},
		{"S1R2", "S1", "R2", false}, {"S2R2", "S2", "R2", false},
	}
	turns := []*TurnRecord{}
	batchStart := time.Now()
	abort := false
	for _, st := range steps {
		rec := &TurnRecord{Label: st.label, SessionID: threadID[st.s]}
		turns = append(turns, rec)
		switch {
		case abort:
			rec.Status = "not_run"
		case batchExpired(batchStart, time.Now()):
			rec.Status, rec.Err = "not_run", "batch deadline exceeded"
			abort = true
			report["reason"] = "batch deadline exceeded; remaining turns not started"
		default:
			if st.start {
				if !startSession(st.s) {
					rec.Status = "blocked"
					abort = true
					break
				}
				rec.SessionID = threadID[st.s]
			} else if !resumeSession(st.s) {
				rec.Status = "blocked"
				abort = true
				break
			}
			if st.round == "R1" {
				rec.Prompt = round1Prompt(mkr[st.s], rq[st.s])
				runCodexTurn(c, rec, threadID[st.s], rec.Prompt, schema1)
				if rec.Status == "completed" {
					if err := validateRound1(rec.RawOut, mkr[st.s], rq[st.s]); err != nil {
						rec.Status, rec.Validate = "completed_invalid_output", err.Error()
					}
				}
			} else {
				rec.Prompt = round2Prompt(rq[st.s], answer[st.s])
				other := "S1"
				if st.s == "S1" {
					other = "S2"
				}
				runCodexTurn(c, rec, threadID[st.s], rec.Prompt, schema2)
				if rec.Status == "completed" {
					if err := validateRound2(rec.RawOut, mkr[st.s], rq[st.s], mkr[other]); err != nil {
						rec.Status, rec.Validate = "completed_invalid_output", err.Error()
					}
				}
			}
		}
		if !abort && haltScheduling(rec.Status) {
			abort = true // 停止/状态未核清：停止后续调度
			report["reason"] = "unconfirmed stop/state after " + st.label + "; scheduling halted"
		}
		writeJSON(filepath.Join(outDir, st.label+".json"), rec)
	}

	if threadID["S1"] != "" && threadID["S1"] == threadID["S2"] {
		report["reason"] = "session isolation violated: identical thread ids"
	}
	report["threadIds"] = threadID
	report["turns"] = turns
	report["status"] = overallStatus(turns, report["reason"] == nil)
	writeJSON(filepath.Join(outDir, "summary.json"), report)
	return report
}

// ---------- Claude Code 驱动（headless 单命令一轮） ----------

// claudeEnvelope 是 claude CLI --output-format json 的顶层结果对象（关键字段）。
type claudeEnvelope struct {
	Type              string          `json:"type"`
	Subtype           string          `json:"subtype"`
	IsError           bool            `json:"is_error"`
	APIErrorStatus    *int            `json:"api_error_status"`
	TerminalReason    string          `json:"terminal_reason"`
	StopReason        string          `json:"stop_reason"`
	Result            string          `json:"result"`
	StructuredOutput  json.RawMessage `json:"structured_output"`
	SessionID         string          `json:"session_id"`
	PermissionDenials []any           `json:"permission_denials"`
	NumTurns          *int            `json:"num_turns"`
	ModelUsage        map[string]any  `json:"modelUsage"`
}

// classifyClaudeEnvelope 是纯函数：从 CLI envelope 判定轮次状态，不发进程、不访问文件。
// 判 completed 必须同时满足：type=result、subtype=success、is_error=false、
// api_error_status 缺失或 0、terminal_reason 缺失或 "completed"、structured_output
// 存在且可解析为探针决策。任一不满足保守拒绝；result 文本可解析不构成完成依据（R2）。
func classifyClaudeEnvelope(raw []byte) (status string, env *claudeEnvelope, parsed *RoundResult, errMsg string) {
	var e claudeEnvelope
	if err := json.Unmarshal(raw, &e); err != nil {
		return "failed", nil, nil, "unparseable CLI output: " + err.Error()
	}
	env = &e
	switch {
	case e.Type != "result":
		return "failed", env, nil, "type=" + e.Type + " want result"
	case e.Subtype != "success":
		return "failed", env, nil, "subtype=" + e.Subtype + " want success"
	case e.IsError:
		return "failed", env, nil, "is_error=true"
	case e.APIErrorStatus != nil && *e.APIErrorStatus != 0:
		return "failed", env, nil, fmt.Sprintf("api_error_status=%d", *e.APIErrorStatus)
	case e.TerminalReason != "" && e.TerminalReason != "completed":
		return "failed", env, nil, "terminal_reason=" + e.TerminalReason
	case len(e.StructuredOutput) == 0:
		return "completed_invalid_output", env, nil, "success envelope without structured_output"
	}
	r, err := parseStrict(string(e.StructuredOutput))
	if err != nil {
		return "completed_invalid_output", env, nil, "structured_output: " + err.Error()
	}
	return "completed", env, r, ""
}

func runClaudeTurn(ctx context.Context, rec *TurnRecord, prompt, schema string, resumeSessionID string, _ string, sessTag string) (newSessionID string) {
	// 参数 _（原 outDir）：会话 cwd 已改为独立空临时目录（R1），保留占位以兼容既有调用方与 review 夹具。
	rec.Start = time.Now()
	rec.Prompt = prompt
	rec.Schema = schema
	// 隔离（R1）：--tools "" 关闭全部内置工具；--strict-mcp-config + 空 mcp-config
	// 排除一切外部 MCP 来源（本机 claude --help 已核对两个开关的语义）。
	args := []string{"-p", prompt, "--output-format", "json", "--json-schema", schema,
		"--tools", "",
		"--strict-mcp-config",
		"--mcp-config", `{"mcpServers":{}}`,
	}
	if resumeSessionID != "" {
		args = append(args, "--resume", resumeSessionID)
	}
	cctx, cancel := context.WithTimeout(ctx, turnTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "claude", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // 独立进程组：超时可整组终止（R3）
	cmd.WaitDelay = 5 * time.Second                       // 直属进程死后不永久等被孙进程占住的管道
	dir, err := os.MkdirTemp("", "probe-claude-"+sessTag+"-")
	if err != nil {
		rec.Status, rec.Err = "blocked", "temp cwd: "+err.Error()
		rec.End = time.Now()
		return ""
	}
	defer os.RemoveAll(dir) // 空临时 cwd，不在证据目录内（R1）
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	rec.End = time.Now()
	if ctx.Err() != nil {
		rec.Status = "not_run"
		return ""
	}
	rec.Extra = map[string]any{"stderrTail": tail(stderr.String(), 800), "argv": args}
	if runErr != nil {
		if cctx.Err() == context.DeadlineExceeded {
			// CommandContext 只杀直属进程；对整个进程组补 SIGKILL 并回收。
			if cmd.Process != nil {
				if kerr := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); kerr != nil && !errors.Is(kerr, syscall.ESRCH) {
					rec.Status = "unknown_timeout_unconfirmed"
					rec.Err = "turn timeout; group kill failed: " + kerr.Error()
					return ""
				}
			}
			_ = cmd.Wait()
			// 停止确认必须观察：轮询 kill(-pgid,0) 直到 ESRCH（组内无存活成员）；
			// 观察窗口内仍存活则按 UNKNOWN 处理（R3：信号发送成功≠全部退出）。
			pgid := -cmd.Process.Pid
			observeDeadline := time.Now().Add(groupObserveWindow)
			for {
				if kerr := syscall.Kill(pgid, 0); errors.Is(kerr, syscall.ESRCH) {
					rec.Status = "timeout_stop_confirmed"
					rec.Err = "turn timeout; process group fully exited (observed via kill(-pgid,0)=ESRCH)"
					return ""
				}
				if time.Now().After(observeDeadline) {
					rec.Status = "unknown_timeout_unconfirmed"
					rec.Err = "turn timeout; group members still observable after kill"
					return ""
				}
				time.Sleep(20 * time.Millisecond) // 有界观察轮询，非时序掩盖
			}
		}
		rec.Status = "failed"
		rec.Err = "run: " + runErr.Error()
		return ""
	}
	status, env, parsed, errMsg := classifyClaudeEnvelope(stdout.Bytes())
	rec.RawOut = stdout.String()
	if env != nil {
		rec.SessionID = env.SessionID
		rec.Extra["stopReason"] = env.StopReason
		rec.Extra["terminalReason"] = env.TerminalReason
		if env.NumTurns != nil {
			rec.Extra["numTurns"] = *env.NumTurns
		}
		rec.Extra["permissionDeniedCount"] = len(env.PermissionDenials)
		if len(env.ModelUsage) > 0 {
			models := make([]string, 0, len(env.ModelUsage))
			for k := range env.ModelUsage {
				models = append(models, k)
			}
			rec.Extra["models"] = models
		}
	}
	rec.Status, rec.Err, rec.Parsed = status, errMsg, parsed
	if status == "completed" && resumeSessionID != "" && env.SessionID != resumeSessionID {
		// --resume 不带 --fork-session 时应保持同一 session id。
		rec.Status = "completed_invalid_output"
		rec.Validate = fmt.Sprintf("session id changed: %s -> %s (no --fork-session)", resumeSessionID, env.SessionID)
	}
	return rec.SessionID
}

func runClaude(outDir string) map[string]any {
	report := map[string]any{"runtime": "claude"}
	mkr := map[string]string{"S1": "MKR-" + randomHex(8), "S2": "MKR-" + randomHex(8)}
	rq := map[string]string{"S1": "RQ-" + randomHex(2), "S2": "RQ-" + randomHex(2)}
	answer := map[string]string{"S1": "Use exactly 5 words per line and mention the basil plant.", "S2": "Keep it under 12 words and mention the airlock door."}

	sess := map[string]string{}
	type step struct {
		label, s, round string
	}
	steps := []step{{"S1R1", "S1", "R1"}, {"S2R1", "S2", "R1"}, {"S1R2", "S1", "R2"}, {"S2R2", "S2", "R2"}}
	turns := []*TurnRecord{}
	ctx := context.Background()
	batchStart := time.Now()
	halted := false
	for _, st := range steps {
		rec := &TurnRecord{Label: st.label}
		turns = append(turns, rec)
		switch {
		case halted:
			rec.Status = "not_run"
		case batchExpired(batchStart, time.Now()):
			rec.Status, rec.Err = "not_run", "batch deadline exceeded"
			halted = true
			report["reason"] = "batch deadline exceeded; remaining turns not started"
		default:
			var sid string
			if st.round == "R1" {
				sid = runClaudeTurn(ctx, rec, round1Prompt(mkr[st.s], rq[st.s]), schema1, "", outDir, st.s)
				if rec.Status == "completed" && rec.Parsed != nil {
					if err := validateRound1Res(rec.Parsed, mkr[st.s], rq[st.s]); err != nil {
						rec.Status, rec.Validate = "completed_invalid_output", err.Error()
					}
				}
			} else {
				other := "S1"
				if st.s == "S1" {
					other = "S2"
				}
				prompt := round2Prompt(rq[st.s], answer[st.s])
				sid = runClaudeTurn(ctx, rec, prompt, schema2, sess[st.s], outDir, st.s)
				if rec.Status == "completed" && rec.Parsed != nil {
					if err := validateRound2Res(rec.Parsed, mkr[st.s], rq[st.s]); err != nil {
						rec.Status, rec.Validate = "completed_invalid_output", err.Error()
					}
					// 串线检查：本会话任何输出字段不得出现另一会话 marker。
					if strings.Contains(rec.RawOut, mkr[other]) {
						rec.Status, rec.Validate = "completed_invalid_output",
							fmt.Sprintf("crosstalk: foreign marker %q in CLI output", mkr[other])
					}
				}
			}
			if sid != "" && st.round == "R1" {
				sess[st.s] = sid
			}
		}
		if haltScheduling(rec.Status) && report["reason"] == nil {
			halted = true
			report["reason"] = "unconfirmed stop/state after " + st.label + "; scheduling halted"
		}
		writeJSON(filepath.Join(outDir, st.label+".json"), rec)
	}
	report["sessionIds"] = sess
	if sess["S1"] != "" && sess["S1"] == sess["S2"] {
		report["reason"] = "session isolation violated: identical session ids"
	}
	report["turns"] = turns
	report["status"] = overallStatus(turns, report["reason"] == nil)
	writeJSON(filepath.Join(outDir, "summary.json"), report)
	return report
}

// ---------- 汇总与工具 ----------

func overallStatus(turns []*TurnRecord, noReason bool) string {
	anyBad := false
	for _, t := range turns {
		if t.Status == "completed" && t.Validate == "" {
			continue
		}
		anyBad = true
		if strings.HasPrefix(t.Status, "unknown") {
			return "inconclusive" // 停止/状态无法核清：如实报告为无法定论
		}
	}
	if !anyBad && noReason {
		return "pass"
	}
	for _, t := range turns {
		if t.Status == "blocked" || t.Status == "not_run" {
			return "blocked"
		}
	}
	return "fail"
}

func writeJSON(path string, v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return
	}
	os.WriteFile(path, b, 0o600)
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func main() {
	mode := flag.String("mode", "offline", "offline | live")
	runtime := flag.String("runtime", "", "codex | claude (live)")
	outDir := flag.String("out", "", "evidence output dir (live, required)")
	flag.Parse()

	switch *mode {
	case "offline":
		os.Exit(offline())
	case "live":
		if *runtime != "codex" && *runtime != "claude" {
			fmt.Fprintln(os.Stderr, "live 需要 -runtime=codex|claude")
			os.Exit(2)
		}
		if *outDir == "" {
			fmt.Fprintln(os.Stderr, "live 需要 -out=<evidence dir>")
			os.Exit(2)
		}
		if err := os.MkdirAll(*outDir, 0o755); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		var rep map[string]any
		if *runtime == "codex" {
			rep = runCodex(*outDir)
		} else {
			rep = runClaude(*outDir)
		}
		b, _ := json.MarshalIndent(rep["status"], "", "")
		fmt.Println("LIVE RESULT:", string(b))
		for _, t := range rep["turns"].([]*TurnRecord) {
			fmt.Println(" ", t.short(), validateNote(t))
		}
	default:
		fmt.Fprintln(os.Stderr, "-mode 必须 offline|live")
		os.Exit(2)
	}
}

func validateNote(t *TurnRecord) string {
	if t.Validate != "" {
		return "[" + t.Validate + "]"
	}
	return ""
}

// offline 运行全部正/负用例；返回退出码。
func offline() int {
	mkr1, mkr2 := "MKR-aaaaaaaaaaaaaaaa", "MKR-bbbbbbbbbbbbbbbb"
	rq := "RQ-01"
	checks := []struct {
		name string
		ok   bool
	}{
		{"r2_prompt_不含_own_marker", !strings.Contains(round2Prompt(rq, "ans"), mkr1)},
		{"r2_prompt_不含_other_marker", !strings.Contains(round2Prompt(rq, "ans"), mkr2)},
		{"r2_prompt_不含_r1_body", !strings.Contains(round2Prompt(rq, "ans"), round1BodySnippet)},
		{"r1_prompt_含_marker_一次", strings.Count(round1Prompt(mkr1, rq), mkr1) == 1},
	}
	r2valid := `{"marker":"MKR-aaaaaaaaaaaaaaaa","requestId":"RQ-01","decision":"result","answer":"ok"}`
	r1valid := `{"marker":"MKR-aaaaaaaaaaaaaaaa","requestId":"RQ-01","decision":"question","question":"how long?"}`
	r2swapped := strings.Replace(r2valid, mkr1, mkr2, 1)
	r2wrongRQ := strings.Replace(r2valid, "RQ-01", "RQ-99", 1)
	r2missingDecision := `{"marker":"MKR-aaaaaaaaaaaaaaaa","requestId":"RQ-01","answer":"ok"}`
	r2unknownField := `{"marker":"MKR-aaaaaaaaaaaaaaaa","requestId":"RQ-01","decision":"result","answer":"ok","extra":1}`
	r1wrongDecision := `{"marker":"MKR-aaaaaaaaaaaaaaaa","requestId":"RQ-01","decision":"result","question":"x"}`

	// R2 修复用例：评审 fake CLI 两个反例 + envelope 门控矩阵（纯函数，无进程）。
	envOK := `{"type":"result","subtype":"success","is_error":false,"session_id":"s","result":"plain text","structured_output":{"marker":"MKR-aaaaaaaaaaaaaaaa","requestId":"RQ-01","decision":"result","answer":"ok"}}`
	envBadSub := `{"type":"result","subtype":"error_max_turns","terminal_reason":"failed","is_error":false,"session_id":"s","structured_output":{"marker":"MKR-aaaaaaaaaaaaaaaa","requestId":"RQ-01","decision":"result","answer":"ok"}}`
	envIsErr := `{"type":"result","subtype":"success","is_error":true,"structured_output":{"marker":"MKR-aaaaaaaaaaaaaaaa","requestId":"RQ-01","decision":"result","answer":"ok"}}`
	envAPIErr := `{"type":"result","subtype":"success","is_error":false,"api_error_status":5,"structured_output":{"marker":"MKR-aaaaaaaaaaaaaaaa","requestId":"RQ-01","decision":"result","answer":"ok"}}`
	envNoSO := `{"type":"result","subtype":"success","is_error":false,"result":"{\"marker\":\"MKR-aaaaaaaaaaaaaaaa\",\"requestId\":\"RQ-01\",\"decision\":\"result\",\"answer\":\"ok\"}"}`
	s1, _, p1, m1 := classifyClaudeEnvelope([]byte(envOK))
	s2, _, _, m2 := classifyClaudeEnvelope([]byte(envBadSub))
	s3, _, _, _ := classifyClaudeEnvelope([]byte(envIsErr))
	s4, _, _, _ := classifyClaudeEnvelope([]byte(envAPIErr))
	s5, _, _, _ := classifyClaudeEnvelope([]byte(envNoSO))
	// R1 用例：codex item 类型白名单。
	cleanTurn := map[string]any{"items": []any{map[string]any{"type": "userMessage"}, map[string]any{"type": "agentMessage"}, map[string]any{"type": "reasoning"}}}
	dirtyTurn := map[string]any{"items": []any{map[string]any{"type": "agentMessage"}, map[string]any{"type": "commandExecution"}}}
	vClean, vDirty := codexItemsIsolation(cleanTurn), codexItemsIsolation(dirtyTurn)
	// R3 用例：调度判定与批次期限纯函数。
	base := time.Now()

	ok := func(err error) bool { return err == nil }
	checks = append(checks,
		struct {
			name string
			ok   bool
		}{"r2_有效_通过", ok(validateRound2(r2valid, mkr1, rq, mkr2))},
		struct {
			name string
			ok   bool
		}{"r2_交换marker_不通过", validateRound2(r2swapped, mkr1, rq, mkr2) != nil},
		struct {
			name string
			ok   bool
		}{"r2_错误replyTo_不通过", validateRound2(r2wrongRQ, mkr1, rq, mkr2) != nil},
		struct {
			name string
			ok   bool
		}{"r2_缺失decision_不通过", validateRound2(r2missingDecision, mkr1, rq, mkr2) != nil},
		struct {
			name string
			ok   bool
		}{"r2_未知字段_不通过", validateRound2(r2unknownField, mkr1, rq, mkr2) != nil},
		struct {
			name string
			ok   bool
		}{"r1_有效_通过", ok(validateRound1(r1valid, mkr1, rq))},
		struct {
			name string
			ok   bool
		}{"r1_错误decision_不通过", validateRound1(r1wrongDecision, mkr1, rq) != nil},
		struct {
			name string
			ok   bool
		}{"R2_structured_output_优先_通过", s1 == "completed" && p1 != nil && m1 == ""},
		struct {
			name string
			ok   bool
		}{"R2_失败subtype_不通过", s2 == "failed" && m2 != ""},
		struct {
			name string
			ok   bool
		}{"R2_is_error_不通过", s3 == "failed"},
		struct {
			name string
			ok   bool
		}{"R2_api_error_status_不通过", s4 == "failed"},
		struct {
			name string
			ok   bool
		}{"R2_无structured_output_不判完成", s5 == "completed_invalid_output"},
		struct {
			name string
			ok   bool
		}{"R1_items_白名单_干净", len(vClean) == 0},
		struct {
			name string
			ok   bool
		}{"R1_items_命令执行_违例", len(vDirty) == 1 && vDirty[0] == "commandExecution"},
		struct {
			name string
			ok   bool
		}{"R3_未核清_停止调度", haltScheduling("unknown_turn_state") && haltScheduling("unknown_timeout_unconfirmed") && haltScheduling("unknown_turn_start_unconfirmed")},
		struct {
			name string
			ok   bool
		}{"R3_干净失败_不误停", !haltScheduling("failed") && !haltScheduling("completed") && !haltScheduling("timeout_stop_confirmed")},
		struct {
			name string
			ok   bool
		}{"R3_批次期限_超时判定", batchExpired(base, base.Add(21*time.Minute)) && !batchExpired(base, base.Add(19*time.Minute))},
	)
	passed := 0
	for _, c := range checks {
		s := "PASS"
		if !c.ok {
			s = "FAIL"
		} else {
			passed++
		}
		fmt.Printf("%-6s %s\n", s, c.name)
	}
	fmt.Printf("OFFLINE %d/%d\n", passed, len(checks))
	if passed != len(checks) {
		return 1
	}
	return 0
}
