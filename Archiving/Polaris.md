# Goal: Build Tikitaka — Local Coordination Runtime for Coding Agents

你正在开发 **Tikitaka**。

Tikitaka 的长期定位是：

> A local coordination runtime for coding agents.

它负责在 Codex、Claude Code 以及未来其他 Agent Runtime 之间可靠地传递任务、上下文、事件和执行结果，并管理 Agent Session、任务执行、消息投递和本地进程生命周期。

Tikitaka **不是 Agent Governance 系统，也不是 Prompt Framework**。

与 Trellium 的边界必须长期保持清晰：

- Trellium owns:
  - Goal
  - Task Contract
  - Authority
  - Acceptance Criteria
  - Review Ledger
  - Handoff
  - Project Memory
  - Governance

- Tikitaka owns:
  - Agent Adapter
  - Agent Session
  - Task Execution
  - Message Delivery
  - Routing
  - Retry
  - Timeout / Cancellation
  - Process Supervision
  - Local Runtime State

Tikitaka 可以与 Trellium 深度集成，但不得依赖 Trellium 才能工作。

**Trellium without Tikitaka must still work.  
Tikitaka without Trellium must still work.**

二者未来通过稳定的 Task Contract / Execution Event 接口集成，而不是共享内部状态。

---

# 1. Architecture Direction

不要把 Tikitaka 实现成：

```text
Codex stdout -> Claude stdin
```

也不要简单复制 AgentBridge。

目标架构应逐步收敛到：

```text
                  Tikitaka
                     │
        ┌────────────┼────────────┐
        │            │            │
      Router      Sessions      Store
        │            │            │
        │            │        Durable State
        │            │
   ┌────┴────┐   ┌───┴────┐
   │         │   │        │
 Codex     Claude ...    Session N
Adapter    Adapter
   │         │
 Codex     Claude
 Runtime   Runtime
```

核心原则：

1. Core 必须 agent-agnostic。
2. Claude/Codex 的特殊逻辑只能存在于 Adapter 层。
3. Task 和 Message 是两个不同层次的对象。
4. Agent execution completed != Task accepted。
5. Runtime 状态和 Governance 状态严格分离。
6. 所有长生命周期操作必须支持 context cancellation。
7. 默认追求 deterministic behavior，而不是依赖 Agent 自觉遵守 Prompt。
8. 不为了未来假想需求提前建设复杂分布式架构。

---

# 2. Core Domain Model

从一开始就避免“只有 Message，没有 Task”的设计。

至少应逐步形成这些核心概念：

```text
Agent
Session
Task
Message / Envelope
Execution
Result
Event
Adapter
Store
```

推荐语义：

## Agent

表示一种可执行 Agent Runtime，例如：

```text
codex
claude
```

未来可以扩展：

```text
gemini
opencode
custom
```

不要在 Core 中 hard-code 两个 Agent。

---

## Session

表示 Tikitaka 与某一个 Agent Runtime 的持续交互上下文。

至少需要表达：

```text
session_id
agent
workspace
role
runtime_session_id
state
created_at
updated_at
```

未来应允许同一个 workspace：

```text
Codex writer
Claude reviewer
Claude planner
...
```

同时存在。

不要设计成：

```text
one workspace = one claude + one codex
```

---

## Task

Task 是一级对象。

它不是一条 message。

Task 至少需要支持：

```text
task_id
parent_task_id
from
to
role
workspace
state
created_at
deadline
result
```

生命周期应显式建模，例如：

```text
queued
running
completed
failed
cancelled
```

Tikitaka 的 `completed` 只表示：

> Agent execution has completed.

不代表上层任务已经通过验收。

---

## Message / Envelope

用于 Agent 间通信。

建议具有：

```text
message_id
task_id
from
to
sequence
payload
created_at
delivery_state
```

未来要能够实现：

```text
PENDING
DELIVERING
ACKED
RETRY
FAILED
```

---

# 3. Reliability Model

可靠性是 Tikitaka 与普通 CLI wrapper 的核心差异之一。

长期目标：

> Process-lifetime delivery reliability is insufficient.

最终应支持 durable mailbox：

```text
Agent A
   ↓
Envelope
   ↓
Persistent Store
   ↓
Deliver
   ↓
Agent B
   ↓
ACK
   ↓
ACKED
```

daemon crash / restart 后：

```text
unacked message
      ↓
recover
      ↓
redeliver
```

第一阶段不要求一次完成完整 durable mailbox，但架构不能堵死这条路。

核心要求：

- stable message ID
- idempotency
- bounded retry
- ACK semantics
- duplicate tolerance
- FIFO where required
- crash recovery eventually possible

不要把：

```text
write() success
```

等价为：

```text
message consumed
```

---

# 4. Concurrency Model

Go 是 Tikitaka 的核心实现语言。

优先利用 Go 的结构化并发能力，而不是简单把 TypeScript 逻辑逐行翻译成 Go。

推荐方向：

```text
Session
   ↓
one owner goroutine
   ↓
inbox channel
```

每个 Session 尽量有清晰的 state ownership。

使用：

```text
context.Context
goroutine
channel
sync primitives
```

实现：

- cancellation
- timeout
- process lifecycle
- session ownership
- async events

避免：

- 无边界 goroutine
- 全局 mutable state
- 一个超级 daemon.go 管理所有逻辑
- 依赖 sleep 解决同步问题

---

# 5. Adapter Boundary

必须建立稳定 Adapter 接口。

示意：

```go
type AgentAdapter interface {
    Start(ctx context.Context, cfg SessionConfig) error
    Send(ctx context.Context, req Request) error
    Interrupt(ctx context.Context) error
    Close(ctx context.Context) error

    Events() <-chan Event
    Capabilities() Capabilities
}
```

具体接口可以根据实际实现调整，不要求机械照搬。

但原则不变：

```text
Core
 ↓
AgentAdapter
 ↓
CodexAdapter / ClaudeAdapter
 ↓
external CLI/runtime protocol
```

Codex 或 Claude CLI 的：

- JSON shape
- session identifier
- app-server protocol
- command flags
- streaming format
- version differences

不得泄漏到 Router / Task / Store 核心逻辑。

未来 CLI protocol 发生变化，只应该主要修改 Adapter。

---

# 6. Process Supervision

Tikitaka 不只是一个 MCP wrapper。

长期需要成为可靠的 local runtime。

因此逐步建立：

```text
process start
process health
process exit
interrupt
timeout
kill
cleanup
restart/recovery
```

所有进程生命周期必须受：

```text
context.Context
```

控制。

未来注意跨平台：

```text
Linux
macOS
Windows
```

可以通过平台文件隔离，例如：

```text
process_unix.go
process_windows.go
transport_unix.go
transport_windows.go
```

但 v0.1 不需要为了 Windows 阻塞核心闭环。

---

# 7. Storage

初期保持简单。

优先考虑：

```text
SQLite
```

用于未来承载：

```text
sessions
tasks
messages
delivery state
execution metadata
```

如果 v0.1 暂时只需要内存实现，也必须通过 Store interface 隔离。

例如：

```text
Store
 ├── MemoryStore
 └── SQLiteStore
```

不要让业务逻辑直接散落 SQL。

---

# 8. CLI UX

Tikitaka 最终应首先是一款优秀的 CLI 工具。

项目名：

```text
Tikitaka
```

binary 可以使用：

```text
taka
```

目标体验：

```bash
taka doctor
taka agents
taka ask claude "review current git diff"
taka ask codex "analyze this issue"
taka sessions
taka tasks
taka status
```

后续：

```bash
taka task status <id>
taka task cancel <id>
taka session resume <id>
```

CLI 应只是 Core 的一个入口。

不要把业务逻辑写进 cobra command handler。

---

# 9. MCP

MCP 是重要入口，但不是 Tikitaka Core。

长期结构：

```text
               Tikitaka Core
               /           \
             CLI           MCP
```

未来向 Codex / Claude 暴露：

```text
delegate_task
get_task
cancel_task
list_agents
list_sessions
```

Agent 可以主动：

```text
Codex
  ↓
delegate_task(agent=claude)
  ↓
Tikitaka
  ↓
Claude
```

但不要在第一阶段为了 MCP 协议本身延迟核心 Runtime 建设。

---

# 10. Hooks

Hooks 是 trigger，不是 Runtime。

未来组合：

```text
Hook
 ↓
Tikitaka
 ↓
Agent
```

例如：

```text
Codex Stop
 ↓
Hook
 ↓
delegate Claude reviewer
```

或者：

```text
Trellium acceptance incomplete
 ↓
Hook blocks stop
 ↓
Tikitaka dispatches reviewer
```

Hook 逻辑不得成为 Tikitaka Core 的必要依赖。

---

# 11. Trellium Integration

当前阶段只设计边界，不进行强耦合集成。

未来目标：

```text
Trellium
   ↓
Task Contract
   ↓
Tikitaka
   ↓
Agent execution
   ↓
Execution Event / Result
   ↓
Trellium
```

Tikitaka 不解析 Trellium 内部 Markdown 作为长期接口。

未来应优先消费：

```text
trellium ... --format json
```

等 machine-readable contract。

禁止 Tikitaka：

- 修改 Trellium lifecycle ownership
- 自己维护第二份 Task acceptance 状态
- 将 execution completed 自动转换成 task accepted

---

# 12. Security / Authority

从设计之初保留 authority / capability boundary。

最安全的默认开发协作模型：

```text
Single Writer
Multiple Reviewers
```

例如：

```text
Codex = writer
Claude = reviewer
```

reviewer 默认只允许：

```text
read
grep
git diff
test
review
```

不应默认拥有代码修改权限。

未来多个 writer 并行工作时，再考虑：

```text
git worktree
workspace isolation
```

不要让两个 Agent 默认同时修改同一个 working tree。

---

# 13. Loop Prevention

多 Agent 系统必须防止：

```text
Codex → Claude → Codex → Claude → ...
```

Task 模型未来应支持类似：

```text
parent_task_id
depth
max_hops
deadline
```

并提供明确的 recursion / delegation limit。

任何 Agent 都不能无限创建下游任务。

---

# 14. Observability

从早期开始保证 runtime 可调试。

至少统一：

```text
structured logging
task_id
session_id
message_id
agent
event
```

日志应能够回答：

```text
谁发起了任务？
发给谁？
哪个 session？
什么时候开始？
消息是否投递？
是否 retry？
Agent 是否退出？
最终 result 是什么？
```

未来再考虑 metrics / tracing。

不要 v0.1 就引入复杂 observability stack。

---

# 15. Development Roadmap

按以下阶段推进。

不要同时铺开所有阶段。

---

## Phase 0 — Repository Foundation

先审查当前 repository。

建立或确认：

```text
cmd/
internal/
pkg/     # 仅真正需要外部消费时使用
docs/
tests/
```

明确 package boundaries。

建立：

```text
go fmt
go vet
go test ./...
```

基础 CI。

补充：

```text
ARCHITECTURE.md
ROADMAP.md
```

不要为了目录“好看”建立大量空 package。

---

## Phase 1 — First Vertical Slice

这是当前最重要的阶段。

目标：

> Tikitaka 可以通过统一 Core 调用 Claude 和 Codex，并获得结构化 Result。

完成：

```text
Agent abstraction
Adapter abstraction
Task basic model
Result model
Process runner
Codex adapter
Claude adapter
CLI
```

最低可用闭环：

```bash
taka ask claude "review current git diff"
```

以及：

```bash
taka ask codex "explain current repository architecture"
```

要求：

- cwd/workspace 支持
- timeout
- cancellation
- exit code
- stdout/stderr
- structured result
- errors 可区分
- adapter 不污染 core

这一阶段可以仍然是 synchronous execution。

不要提前实现 daemon、SQLite durable mailbox、distributed runtime。

Phase 1 完成后，必须能够真实使用。

---

## Phase 2 — Sessions

增加：

```text
Session Manager
persistent runtime session identifier
resume
multiple sessions
```

目标：

```text
workspace + agent + role
```

可以存在独立 Session。

CLI：

```bash
taka sessions
taka session resume ...
```

Codex / Claude 的 session 实现细节必须封装在 Adapter。

---

## Phase 3 — Task Runtime

将：

```text
ask
```

提升为：

```text
task
```

加入：

```text
queued
running
completed
failed
cancelled
```

支持：

```text
async execution
task ID
status
result
cancel
```

CLI：

```bash
taka task start ...
taka task status ...
taka task result ...
taka task cancel ...
```

---

## Phase 4 — Daemon

在 Task Runtime 已稳定后，再引入常驻 daemon。

daemon 负责：

```text
routing
sessions
task execution
process supervision
events
```

CLI 成为 daemon client。

优先 local IPC。

不要第一版就做网络服务。

---

## Phase 5 — Durable Store / Mailbox

增加 SQLite。

实现：

```text
persistent task
persistent session metadata
durable messages
ACK
retry
crash recovery
```

重点测试：

```text
daemon crash
restart
unacked message survives
task status survives
```

这是 Tikitaka 从 wrapper 变成 runtime 的关键阶段。

---

## Phase 6 — MCP

向 Agent 暴露：

```text
delegate_task
get_task
get_result
cancel_task
list_agents
```

实现：

```text
Codex → Tikitaka → Claude
Claude → Tikitaka → Codex
```

此时才真正形成 Agent-to-Agent delegation。

---

## Phase 7 — Multi-Agent Coordination

增加：

```text
roles
capabilities
delegation depth
max hops
single-writer policy
reviewer mode
workspace isolation
```

支持：

```text
Codex writer
Claude reviewer
```

以及更复杂的多 Agent topology。

---

## Phase 8 — Trellium Integration

在两边接口稳定后加入可选 integration。

第一阶段只读取 machine-readable Task Contract。

例如：

```text
Trellium Task
 ↓
Tikitaka execution
 ↓
Result
 ↓
Trellium review / acceptance
```

Tikitaka 不接管 Trellium lifecycle。

---

# 16. Explicit Non-Goals for Early Versions

在 v0.x 前期不要实现：

- distributed multi-machine cluster
- Kubernetes deployment platform
- Web UI
- cloud control plane
- custom LLM API gateway
- full workflow DSL
- autonomous planner
- complex RBAC
- arbitrary plugin marketplace
- Trellium embedded runtime
- generic message broker
- Kafka/NATS dependency
- premature abstraction for dozens of agents

目标是把：

```text
local coding agent coordination
```

做到可靠、清晰、可扩展。

---

# 17. Engineering Rules

整个开发过程中坚持：

1. 优先小而完整的 vertical slice。
2. 每一个 abstraction 必须有当前真实需求。
3. 不为了“以后可能支持”建立巨大 interface。
4. Core 不依赖 Claude/Codex 特有协议。
5. Adapter 可以依赖具体 runtime。
6. 状态 ownership 必须唯一。
7. async operation 必须可取消。
8. persistent state 必须有明确 state machine。
9. external process behavior 必须有 integration tests。
10. 不把 shell success 当成业务 success。
11. 不把 delivery 当成 consumption。
12. 不把 execution completion 当成 governance acceptance。
13. 避免全局 mutable singleton。
14. 避免超级 daemon 文件。
15. 优先 Go 标准库，谨慎增加依赖。

---

# 18. Reference Project

可以深入研究：

```text
raysonmeng/agent-bridge
```

重点学习其已经验证过的：

- Claude/Codex integration
- persistent peer communication
- watchdog
- reconnect
- busy guard
- idempotency
- quota coordination
- Codex app-server compatibility handling
- integration/E2E testing

同时重点关注其已经暴露的架构问题：

- daemon responsibility growth
- large adapter complexity
- process-lifetime mailbox
- single pair/session assumptions
- protocol drift
- background task persistence
- Windows/runtime distribution friction

不要逐文件翻译。

目标是：

> Learn from its behavior and failures, not copy its architecture mechanically.

如果参考具体源码或设计，请在 Tikitaka 文档中注明设计来源和我们的差异。

---

# 19. Immediate Execution

现在开始工作。

首先：

1. 审查 Tikitaka 当前 repository。
2. 判断已有代码与上述目标的差距。
3. 创建或更新 `docs/ARCHITECTURE.md`。
4. 创建或更新 `docs/ROADMAP.md`。
5. 将 Phase 1 拆成可执行 TASK。
6. 立即开始 Phase 1 的第一个 vertical slice。
7. 每一步保持 build/test green。

不要只输出方案。

在完成必要设计后立即进入代码实施。

第一阶段最终必须真实实现：

```bash
taka ask claude "..."
taka ask codex "..."
```

并通过统一的：

```text
Task → Adapter → Runtime → Result
```

链路运行，而不是 CLI command 内直接 `exec.Command()` 硬编码完成。

如果当前 repository 已经存在合理实现，则演进现有代码，不为了符合本文而无意义重写。

做架构决策时遵循一个最高原则：

> **Tikitaka should become a durable local coordination runtime, not merely a bridge between two CLIs.**