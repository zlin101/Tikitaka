# Round 3.1：Coding Agent 多轮协作 V0 技术设计（故障与复现修订）

> 仓库导入说明（2026-10-07）：保存最新版 Round 3.1 设计提案，来源为 464 行修订稿；恢复契约见 [补充文档](round3_1_collaboration_resume_addendum.md)。原文提到的 ZIP、sources、verification/round3 和 MANIFEST 本次未提供，相关路径保留为文本，不代表本仓库已有这些产物或独立复跑过其 10/24/7 测试。当前仓库独立实验边界见 [原生续接实验](experiments/native-resume.md)。文档导入不表示生产实现已验收。

状态：修订后的实现契约提案 + 有限参考验证；不是生产实现已通过验收的声明。
生产实现方向：**Go**。Python 仅为协调状态的可执行规格；本包另含独立 Go 事件泵参考和假时钟测试。
尚未完成：仓库集成、真实 Codex / Claude 联调、跨资源停止验证、生产数据库迁移与崩溃恢复。
日期：2026-10-07。

## 0. 本次修订与证据等级

本轮仅补齐评审指出的缺口，不改变 Host 驱动受管会话、按轮释放执行槽的主方案。新增的状态和默认故障处理是 **Round 3.1 的设计决定**，不是声称旧文档已包含。

| 评审项 | 修订位置 | 当前验证范围 |
|---|---|---|
| 非法输出只回滚，没有可恢复落点 | §7 的拒绝事务、§11 的状态与恢复 | Python 规格新增故障回归；不模拟真实文件副作用 |
| 先等事件导致第一轮不启动；等待无计时唤醒 | §7 的根请求、bootstrap、deadline、Go 事件泵 | Go 假时钟测试验证循环唤醒；尚未接生产状态库与 Adapter |
| 10 项测试和前序来源不在仓库 | §13、§15、配套目录与运行器 | 原 ZIP 的 10 项测试本次重跑；全部来源副本和运行命令随包提供 |
| Python asyncio 与 Go 方向冲突 | §2、§9 | Go 保持生产方向；Python 不进入生产依赖 |
| 官方网页当次访问失败 | §9、§15 | 本次重新打开官方页面作文档核验；不等于 Runtime 联调 |

这是可放入仓库的交付目录，不是已提交到用户仓库的声明。本文路径均相对本包根目录；从 `Archiving/` 阅读的 Markdown 链接使用相应相对路径。

## 1. 与已有材料的关系

Round 2 是 Execution Layer 架构审查提示词，不是已经实现的协议规范。它的 §18 排除 mailbox/scheduler，§21 Case 5 主要验证单次自主委派，§23 将 peer inbox 等列为默认 non-goals。

当前验收增加了显式需求：A 委派 B；B 向 A 追问；A 回答或向 C 核实；B 接续完成；A 消费结果。无需人为在 Agent 之间转贴消息。

因此保留 Round 2 的 ExecutionRequest / Attempt / Outcome 及权限、输入、取消、UNKNOWN 原则；在其上新增有边界的协作语义。承认这包含小型收件队列、轮次调度和会话所有权，不再称其只是单次执行器。

## 2. V0 的明确选择

单机、单项目、一个前台 **Go Coordination Host**；静态 A/B/C；由 Host 启动并独占推进每个参与者的原生会话。Go 的 owner goroutine 管理控制状态；Runtime I/O 使用受监督 worker goroutine；channel 承载事件或唤醒；timer 提供期限唤醒。SQLite 保存控制状态，普通目录保存原始输出与工件。

Python asyncio 不再作为生产方案候选。本包 `verification/round3/spec/` 的 Python 是有限可执行规格，不需要移植其类结构，也不要求在 Go Host 中嵌入 Python。生产实现保持项目既定 Go 工程边界；本包的独立 Go 示例模块只验证事件泵顺序，并不替换项目 `go.mod`。

A/B/C 是逻辑参与者，不是模型品牌。A 和 C 可以都运行 Codex，但不能因此共用同一原生线程。

运行期间 Host 常驻，但不要求系统级后台服务、不要求脱离宿主退出继续执行。不做动态角色发现、跨机器消息、DAG、业务自动验收、自动重跑 UNKNOWN。

采用非抢占式、轮次边界交接：一轮可以包含多次内部模型和工具调用；Agent 需要外部信息时，以结构化决策结束本轮。后续由 Host 把消息作为下一轮输入续接，不保留一个阻塞等待最终结果的跨 Agent 调用栈。

### 接入边界

本方案默认受管会话。它不意味着任意已经打开的交互终端只加一个 MCP Server 就能被自动唤醒。接入已有会话必须另外验证原生启动下一轮的能力、唯一驱动权和客户端行为。不能偷偷复制一个新会话充当原 Agent。

MCP 可以暴露 start/get/post/cancel 等入口；它不是默认的异构原生会话续接机制。外部 Agent 发起一段受管协作，也不自动变成协作中的受管 A。

## 3. 最小对象与标识

- Collaboration：参与者范围、调用方、根请求、可选 task_ref、预算及运行/暂停/关闭状态；不是 Trellium Task。
- Participant：静态 agent_id、Runtime profile、原生会话引用、活动 execution_id、checkpoint。会话映射键为 (collaboration_id, agent_id)。
- Message：id、范围、sender、recipient、kind、reply_to、body、工件引用、origin_execution。每条单一收件人。
- Execution：复用 Round 2；一轮受监督执行，具有独立证据、原生产出及不确定性。

不另设 attempt_id、question_id、review_id。消息 ID 可由 execution_id + action_index 稳定生成。外部输入使用由可信调用入口提供的幂等键。

### 消息类别

| kind | 意义 | reply_to 规则 |
|---|---|---|
| request | 委派一项工作 | 可关联上游消息，不代表上游完成 |
| question | 请求缺失信息 | 可关联原工作或上游问题，不关闭原 request |
| answer | 对 question 的答复 | 必须指向该 question；只允许原收件人回答原发送人 |
| result | 对 request 形成工作结果 | 必须指向该 request；只允许原收件人回复原发送人 |

result 不等于 accepted；answer 不等于答案真实。V0 一个 request/question 最多一个终结性 reply，补充讨论再开一条关联消息。消息正文可以自然语言，不从正文关键词猜测控制动作。

sender、范围、执行来源和权限不是模型可自行填写的字段，由 Host 根据实际调用上下文绑定。模型只能选择被授权的收件人并提出内容。

## 4. 一轮 Agent 输出的协议

这是我们定义的协议，不是 Codex / Claude 的现成接口：

```json
{
  "outbox": [
    {
      "key": "q",
      "to": "A",
      "kind": "question",
      "body": "该函数是否同时被中断和普通任务调用？",
      "reply_to": "m1"
    }
  ],
  "mode": "wait",
  "wait_for": ["$q"],
  "checkpoint": "原评审尚未完成；当前缺少调用上下文，收到答复后继续检查。"
}
```

`$q` 是本轮局部引用。Host 分配正式消息 ID 后，原子地替换它。checkpoint 只保存显式工作状态、结论、依据和未解决问题，不要求或保存模型私有推理。

mode：
- wait：无自主继续轮次，记录等待关系；任何合法新入站消息仍能使参与者可运行。
- continue：已有工作还需要下一轮，可在没有新入站消息时被调度，但受统一预算约束。
- idle：当前无自主继续意图；后续新消息仍可唤醒。

不提供隐含的“永久完成 Agent”状态。协作关闭由有权 Caller 决定。

当前示例 JSON Schema 未展开工件字段；真实消息的工件引用沿用 Round 2 的 input/artifact 契约并经 ACL 校验。参考模型不实现该权限校验。

## 5. 输入上下文

每轮 Host 生成确定的输入包，记录其内容或摘要：参与者身份、原始目标投影、固定输入引用、本轮新消息、仍未回答的入站与出站请求、最近 checkpoint、获授权的参与者和动作。

正常路径优先续接该参与者自己的 native session；新消息只追加一次。上下文历史仅包含本参与者可见的信息和明确转交的材料，不向所有参与者无条件广播全文。

原生 session 不可用时，V0 暂停并显式报告；不静默创建新会话、假装保留了全部上下文。未来支持显式 rehydrate 时，要记录信息损失和新的 session 绑定。不能因缺少 JSON 就重跑曾经修改过文件的执行。

模型读取或上下文窗口溢出不允许被隐藏成“无发现”。上下文装配完整性与模型实际理解是不同保证。

## 6. 七轮闭环

| 轮次 | Agent | 输出 | 之后 |
|---|---|---|---|
| 1 | A | m1 request → B | 等 m1 |
| 2 | B | m2 question → A，关联 m1 | 等 m2，保留原评审 |
| 3 | A | m3 question → C，关联 m2 | 等 m1/m3 |
| 4 | C | m4 answer → A，回复 m3 | idle |
| 5 | A | m5 answer → B，回复 m2 | 继续等 m1 |
| 6 | B | m6 result → A，回复 m1 | idle |
| 7 | A | 消费 m6，对根请求形成结果或继续工作 | Caller 后续决定 |

A 等 B 的结果时，仍必须接收 B 的追问。wait_for 是工作依赖描述，不是 Inbox 过滤器。按参与者画出的 A→B→A 环并不自动等于死锁：只要问题已到达且 A 可运行，就存在可推进动作。

即使全局并行槽为 1，这条路径也应正常完成。等待不占活动执行槽，不占数据库事务，不锁住整个协作。

## 7. 调度、事务、启动与期限

### 7.1 可运行条件

```text
collaboration.state = RUNNING
AND participant.gate = OPEN
AND participant 没有活动或未核对的 UNKNOWN 执行
AND 原生会话可按已验证 profile 接续
AND 必要的 workspace/session fence 已解除
AND (有 QUEUED 入站消息 OR mode=continue OR 有已授权 recovery packet)
AND 期限与累计预算均允许
```

`gate=OPEN/PAUSED` 与 `mode=wait/continue/idle` 是不同维度。暂停不是第四种模型 mode。任何合法新消息都可以使未暂停的等待者可运行；暂停者可以继续收件，但不能因此越过恢复审批。

同一原生会话至多一个活动轮次；不同参与者可并行。运行中入站排到下一轮。调度按稳定、公平顺序，不能让一直 continue 的 A 永久饿死 B。新执行实际启动前再次检查全局槽位、预算和资源 fence。

### 7.2 根请求与第一轮启动

`collab_start(caller, idempotency_key, root_recipient, goal, profiles, budgets)` 是可信入口，不由模型伪造 caller。

一个短事务完成：校验 caller/参与者/profile/有限预算；记录幂等键及请求摘要；建立 Collaboration 和静态 Participant；插入一条发给根参与者 A 的 `request`，置 `dispatch_state=QUEUED`；固定整段协作 deadline；提交。

提交后返回 `collaboration_id` 和 `root_message_id`，并发出 wake hint。相同作用域的相同 key 和内容只返回原标识，不能新建根消息或重置预算；相同 key 不同内容拒绝。**accepted 只表示达到本系统约定的受理存储状态，不表示 Runtime 已启动。**

Host 启动时先做一次 bootstrap scan，再进入阻塞等待；正常运行的 Host 接受新根请求后重新扫描。wake hint 即使丢失，也由有界周期扫描发现已提交根消息。该规则不能替代重启核对：重启载入的未结束协作默认 PAUSED，不可直接按旧 RUNNING 状态驱动 Agent。

### 7.3 分派事务

短事务内检查可运行条件；选择有界 QUEUED 消息批次；建立 `Execution(state=STARTING)`；保存不可变 input_ids/input packet 引用；将本批消息标记 RESERVED 并关联 execution_id；占有 participant/session/fence；记录本轮启动/执行期限；提交。

事务外启动 Runtime，观察开始证据后变为 RUNNING。STARTING 到 Runtime start 之间的崩溃按未知处理。明确未启动的失败也必须落盘并暂停；V0 不在这里隐式创建重试。

参考 Python 模型的 `claim()` 没有真实启动阶段，直接使用 running；它只验证分派控制记录，不证明 STARTING 的 crash window 已解决。

### 7.4 完成或拒绝：必须是一个状态落点事务

Adapter 先保存完整原生产出及其归属、轮次边界证据和停止观察。Raw output 是诊断，不是可直接路由的 outbox。

在一个外层事务中锁定/条件检查 execution 的可完成版本：

```text
BEGIN
  检查 execution 身份、当前状态、重复/冲突完成
  SAVEPOINT proposed_decision
  校验整个 schema、收件人、reply_to、ACL、wait_for、预算
  尝试形成全批 outbox 和下一轮状态

  合法：
    一起保存 decision、全部 outbox、checkpoint、native refs、等待关系
    RESERVED 输入 → APPLIED
    decision_status = COMMITTED
    lifecycle = FINISHED（以轮次结束证据为前提）
    只有停止/资源条件满足才释放相应 fence

  非法：
    ROLLBACK TO proposed_decision
    decision_status = REJECTED
    保存 raw_output_ref、validation_errors、观察证据
    本轮 RESERVED 输入 → HELD；保留原 execution_id 关联
    participant.gate = PAUSED(reason=INVALID_OUTPUT)
    不提交 checkpoint、不发布任何 outbox
    已确认允许重入的停止边界：FINISHED，释放执行槽但仍 PAUSED
    未确认停止/仍可能冲突写入：UNKNOWN，保留相应占有与 fence
COMMIT
```

这里的 SAVEPOINT 是可用实现方式，不是新增领域组件。也可先在事务内完整验证、再选择成功分支或失败分支；关键是不能“整笔回滚返回错误”后就结束处理。JSON 解码在事务前失败时，用独立的**拒绝状态事务**记录同样的 REJECTED/HELD/PAUSED；此前本就未发布协调动作。

只有 COMMITTED 的 outbox 能路由；只有成功提交后才能唤醒接收者。相同完成内容幂等返回原提交结果；冲突完成拒绝。已 REJECTED 的 execution 不允许下一份“修好 JSON”冒充原输出并自动覆盖，修复是显式新轮次。

I/O 错误、DB 错误和程序异常不是“模型输出非法”。拒绝事务也无法提交时，Host 必须进入内存 fail-closed 状态、停止新调度、报告存储故障、保留旧活动占有并核对；不能假称暂停已持久化。崩溃前未提交的完成继续保持待核对状态。

消息状态只描述 Host 控制事实：QUEUED 未分派；RESERVED 已关联某轮输入；APPLIED 该轮协调决策已提交；HELD 该轮失败待恢复；RESOLVED 已由显式恢复安排接续。它们都不证明模型理解或业务完成。原始 execution.input_ids 不可因恢复被抹掉。

### 7.5 时间必须产生独立唤醒

Host 配置有限期限；模型不能通过反复 question/wait 自行延长整段协作预算。

| 期限 | 起算点与过期处理 |
|---|---|
| collaboration_deadline | 根请求受理提交时固定；到期暂停整段协作，禁止新轮次，请求取消活动执行；不自动声明业务失败或完成 |
| execution_deadline | 预留 STARTING 时开始，覆盖启动卡住；到期记录 CANCEL_REQUESTED，向 Adapter 请求取消并进入停止宽限期；不能直接认定停止 |
| reply_deadline | 每条 request/question 首次发布时按 Host policy 固定；回答或结果提交后关闭该等待项；再次输出 wait、收到无关消息、重启均不能重新计时 |
| stop_grace_deadline | 首次取消请求时固定；没有充分停止证据则转 UNKNOWN，保留会话与冲突资源 fence |

V0 对等待超时采用保守默认：记录是哪条 request/question 过期，Collaboration 进入 `PAUSED(WAIT_TIMEOUT)`，请求取消仍活动的本协作执行；保留未解决的问题。**超时生成 Host 状态事件，不冒充 peer 的 answer/result，不自动重发原请求。**

迟到 answer/result 仍可作为合法证据提交和保存，但不能自动清除已提交的 timeout pause。终结答复与超时按单 Host 的事务提交次序线性化；timeout handler 必须重新检查等待项是否已解决。不要用模型自报时间改变顺序。

`NextDeadline()` 忽略已经解决或已经处理的期限；每个 deadline 只处理一次，避免过期 timer 导致零延迟忙循环。全部等待但有未过期的 request 时是正常 WAITING，不马上判定死锁；到期或预算耗尽才触发明确诊断。

单进程定时使用单调时间，持久记录保存可解释的绝对期限和起算记录。重启先暂停并核对，再评估已到期项，不重新给完整预算。授权延期要有独立审计记录；本包没有实现生产重启和时钟核对。

### 7.6 Go 事件泵

执行顺序固定为：**处理当前已到期事实 → 重新调度 → 装配最早 deadline 或周期扫描 timer → 等待输入/Runtime 事件/timer。**

```go
for {
    // 单 owner；内部只有短事务与异步启动，不 await peer 最终结果。
    reconcileDueDeadlinesAndRunnableState(now())
    scheduleWithinCapacityAndFences()

    delay := min(timeUntilNextDeadline(), maxRescanInterval)
    select {
    case event := <-events:
        applyObservedEvent(event)
    case <-newTimer(delay):
        // 仅唤醒；循环顶部依据当前已提交状态再次判断。
    case <-shutdownRequested:
        beginBoundedShutdownAndReconciliation()
        return
    }
}
```

上面是顺序说明，不是可直接编译代码。可编译的小循环在 `verification/round3/go_pump/pump.go`，调用方通过 Driver 提供状态事务/启动/取消；它不实现 Driver，也不实现原生 Runtime。

wake hint 可合并，真实 completion payload 不可无证据丢弃。Runtime I/O 必须持续 drain，不占数据库事务等待；事件发送应处理反压。状态故障或 Runtime 事件源关闭不能被当成普通 idle。

Go context 取消只是请求放弃工作，不证明 goroutine/子进程/后代停止。该循环返回之后，生产宿主仍要有限时地 cancel、drain、观察和标记 UNKNOWN。[W6]

## 8. 存储选择

仍采用 collaborations、participants、messages、executions 四类控制状态；参与者 gate/pause_reason、消息 dispatch_state、execution input_ids/decision_status/stop_observation/recovery_record 和 deadlines 可作为字段实现，不因此引入通用事件溯源平台。

SQLite 承担短控制事务，不承担 Runtime 工具副作用事务。[W4] 成功和非法输出都必须有一条可检查的状态迁移。普通工件目录保存原始输出、输入包、日志；数据库引用只指向已经完整发布的文件，工件写入失败时不得声称诊断已经保存。

单 Host 必须独占控制库的推进权；该限制与 SQLite 文件能否被打开不是同一件事。状态数据库、监督记录和 raw outputs 不应由 Agent 改写。Native Session 存储由 Runtime 自己使用，通常需要在受控私有位置写入；不能笼统把它规定为对整个 Runtime 不可写。

有限的本地收件与调度状态不等于跨机器 Broker，不承诺自动 redelivery、自动崩溃接管或跨文件/数据库/网络副作用恰好一次。

本包 Python SQLite 结构只用于新的测试数据库；不提供旧库 ALTER/migration，不可直接对生产控制库运行。

## 9. 原生 Adapter 绑定与 Go 实现关系

### 9.1 当前文档核验结果

本次在 2026-10-07 能重新读取以下官方页面；这不否认评审者此前访问失败，也不能反向证明此前已跑过 Adapter。**DOC_CHECKED 与 RUNTIME_VERIFIED 必须分列。**

| Runtime | 本次官方文档支持的候选接口 | 实测状态 |
|---|---|---|
| Codex App Server | initialize/initialized；thread/start、thread/resume；turn/start 的 outputSchema；turn/completed 含 completed/interrupted/failed | DOC_CHECKED；未在此包运行 Codex |
| Claude Code CLI | `-p --output-format json --json-schema`；读取 `structured_output`；保存 session_id 并以 `--resume` 指定会话 | DOC_CHECKED；未在此包运行 Claude |

Go Host 可直接作为 Codex 原生协议客户端、Claude 非交互子进程的监督者；不需要在核心中嵌入 Python SDK。[W1][W2] 这是我们选定的实现边界，不是厂商交付的跨 Runtime 协调产品。

绑定时必须固定真实 CLI/profile 版本，保存 thread/session/turn 引用，明确结构化结果所在的实际原生字段；completion event 的存在和 status 都要检查。进程退出、JSON 合法、turn completed、停止冲突写入是不同观察，不能相互替代。

Codex 的 outputSchema 按轮指定，不能假定第一轮配置自动覆盖后续轮次。[W1] Claude 不能用“最近的会话”代替记录下来的 session_id。[W2]

### 9.2 最小接口

```text
run_turn(execution_request, session_ref, input_packet) -> ExecutionOutcome
request_cancel(execution_id) -> cancellation_observation
```

Go 侧 RunTurn 可由受监督 goroutine 执行，结果回到 owner 处理；它不能阻塞 owner 的状态事务。Outcome 必须把 TurnDecision 与完成/停止证据分开。对真实副作用和停止证据的检查不是 Python 规格中的一个布尔值可以替代的。

认证按真实用户环境核验，不默认从订阅切换 API。自然语言作为 argv/JSON 数据，不拼进 shell。问 peer 的业务问题走 question 消息，不借用面向真正授权者的审批通道。

### 9.3 实现门槛：仍未通过

两个 Runtime 各自完成“第一轮提问并结束 → 用准确 session/thread ID 续接 → 回答后给出结果”的真实测试；再做错误结构化结果、取消/超时、会话失效和工作区约束测试。必须保存命令/协议版本、原始事件与实际 observed state。只有完成这些才标记对应 profile 为 RUNTIME_VERIFIED。

Python Agent SDK 不是当前 Go 生产路线的必选依赖；旧文档中同一 Python 宿主的 SDK 建议移除。旧 SDK 网页仅作历史来源，不据此建立本轮接口断言。

## 10. MCP 的准确位置

MCP 是可选的外部工具入口。可以提供以下自定义工具：collab_start、collab_get、collab_post、collab_cancel。start 返回受理标识而不是阻塞等整段协作完成。

MCP 文档描述的 Host/Client/Server 职责，并不直接给出“自动推进任意已有 Agent 会话”的统一保证。即使存在 sampling，也需要对应客户端支持与控制；不要把通知传输等价成对指定原生会话的续接。[W5]

受管 V0 不要求 Agent 先学会调用 MCP 来发出每条问题：最终结构化 outbox 就是控制入口。只有确实需要边运行边投递时，再增加短返回的工具式消息提交，并明确与 turn commit 的一致性；V0 不混用两个可能重复投递的控制来源。

## 11. 故障状态与显式恢复

### 11.1 分开的状态维度

Execution 使用 lifecycle（STARTING/RUNNING/CANCELLING/FINISHED/UNKNOWN）、decision_status（NONE/COMMITTED/REJECTED）、stop_observation（带范围的 CONFIRMED/UNCONFIRMED）。Participant 有 OPEN/PAUSED gate 和 pause_reason；wait/continue/idle 仍表示工作意图。

UNKNOWN 是无法确认关键执行事实，不能表示“停了，只是不满意结果”。确认原生轮次结束也不自动证明后代进程、外部写入或整个世界停止；只有经 profile 定义的冲突资源边界满足才可释放对应 fence。日志和结果可以保留，但协调结果与停止许可仍分别判断。

| 情况 | Execution / 输入 | Participant / 调度 |
|---|---|---|
| 决策合法且允许安全重入 | FINISHED + COMMITTED；RESERVED→APPLIED | 清 active，按 mode/入站重新调度 |
| 输出非法且停止边界已确认 | FINISHED + REJECTED；RESERVED→HELD | 清 active、释放该执行槽；PAUSED(INVALID_OUTPUT) |
| 输出非法且停止边界不明 | UNKNOWN + REJECTED；输入 HELD | 保留未知执行与资源 fence；PAUSED |
| 有文件副作用后失联 | UNKNOWN，保留原生产出/输入/副作用观察 | 禁止同会话和冲突 Writer 新执行；不能因超时过期释放写权 |
| 完成事件重复 | 同内容读原结果；不同内容拒绝，不覆盖历史 | 不追加重复消息、不启动新 Agent |
| 存储失败导致完成/拒绝不能提交 | 不虚构已持久化终态；旧分派仍保留 | Host 全局 fail-closed，停止新调度并核对 |
| 原生 session 不可恢复 | 保留失败诊断；不假冒续接成功 | PAUSED(SESSION_UNAVAILABLE)；不自动另开会话 |
| 等待/总预算超时 | 记录 Host timeout；保留未完成请求 | 暂停协作；对活动执行发 cancel，再按停止证据处理 |
| 用户取消或 Host 关闭 | 先阻止新轮次，发 cancel；宽限后无法确认则 UNKNOWN | 不把“用户界面取消成功”写成进程全部停止 |

### 11.2 非法输出后的恢复动作

V0 保留小而明确的受权操作，不采用“catch error 后再跑一次”：

**Inspect：** 读取 raw_output、校验错误、原 input_ids、已提交 checkpoint、原生会话引用、停止与副作用记录。读取可重复，不改变执行。

**ReconcileStop：** 根据真实证据更新停止观察；UNKNOWN 在充分证据下可结束，但这一步不自动解除参与者暂停，也不追认原非法 outbox。

**ResumeAfterReview：** 有权 Caller 提供幂等 operation_id、原因和恢复说明；先确认旧执行不再冲突、检查已发生副作用、验证会话引用/上下文、核对预算和授权。恢复记录与解除 gate 在一个事务中提交。它只授权下一轮，不立即重跑旧执行。

下一轮使用**新的 execution_id**，输入包含恢复包：旧 execution_id、全部原输入消息引用、拒绝原因、已检查的副作用、已提交 checkpoint、未完成问题和后续指令。尤其不能只提供 open_requests：旧轮已收到的 answer 可能不在 open_requests 中。

HELD 输入不自动改回 QUEUED；旧 execution.input_ids 和 dispatch 关联留在历史。可把其恢复安排标记 RESOLVED，同时由恢复包明确引用；这不是模型已理解或业务已经完成。相同恢复命令重复只返回原记录，冲突命令拒绝。

不能修改旧 REJECTED 决策为“当时其实成功”。需要修正输出或重新执行时，以新轮次进行，并由权限/profile 限制其行为。提示“不要重复副作用”只是恢复说明，不能代替幂等或隔离的实际保证。

**Abort：** 有权 Caller 可放弃整段协作，停止新轮次并处理活动执行；未完成输入和 UNKNOWN 作为诊断保留。放弃不等于确认旧执行已停止。V0 不要求单独删除参与者或自动解决其他参与者对它的依赖。

### 11.3 不重跑与不永久卡死同时成立

对正常 WAITING，合法入站仍触发下一轮；对 PAUSED，入站只保存，必须显式恢复。非法输出导致的是可检查、可授权解除的暂停，不是伪装 RUNNING 的死角。

Host 重启载入历史时，未结束协作统一暂停；STARTING/RUNNING/CANCELLING 和未最终提交的执行逐个核对。不能因为库中有可读消息就自动接管原生会话或重新启动 Writer。

本包规格验证拒绝/保留/受权续接的有限状态逻辑。真实进程停止、重启核对、全局资源 fence、ACL、完整预算和生产恢复 API 仍是实施与验收项。

## 12. 权限与治理边界

消息可以提出请求，不能扩大接收者权限。Host 固定 sender，不让模型冒充 peer。越范围的 reply_to、未授权 artifact_ref、未注册收件人均拒绝。

在 Coding 场景沿用单 Writer A，B/C 默认读取获授权固定输入。消息附带输入版本；旧版 Review 不自动证明新版已评审。暂停等待本身不自动把工作区写权限交给其他 Agent。

决定是否询问 C、如何解释发现、是否继续修改，由 A 或已有治理系统决定；Host 只推进这些明确动作、限制资源、转交结果。它不做任务拆解模型、不复制业务验收。

无需人为转贴消息不等于永远不需要人。缺失的真实业务条件、权限审批和无法消除的冲突可以按已授权策略升级给人。

## 13. 复现位置、测试声明与实现门槛

### 13.1 可定位的交付结构

```text
Archiving/
  round3_collaboration_v0_rfc.md                 # 本修订契约
  sources/round3_original.md                    # 原文，不覆盖
  sources/round2_execution_review_prompt.md
  sources/architecture_review.md
  sources/original_problem.md
verification/round3/
  README.md
  run_all.py                                   # 唯一总入口
  legacy/                                      # 原 ZIP 内容，不修正其旧测试
  spec/coordination_model.py                    # 修订后的有限 Python 规格
  spec/test_coordination_model.py
  spec/test_failure_recovery.py
  spec/turn_decision.schema.json
  go_pump/go.mod                               # 独立示例，不改生产 go.mod
  go_pump/pump.go
  go_pump/pump_test.go
  evidence/                                    # 本次运行记录、缺陷重现、环境
```

旧文件所谓“本包 10 项测试”指对话中的 `agent_collaboration_v0_reference.zip`，不是已在你的仓库找到相同代码。本次把原 ZIP 内容放入 legacy/；legacy 中包含的旧 RFC 和 README 只作历史证据，不作为 3.1 当前契约。前序附件已按上表复制并记录来源映射。

### 13.2 复现命令

在本交付包根目录运行：

```bash
python3 verification/round3/run_all.py
```

或分别执行：

```bash
(cd verification/round3/legacy && python3 -m unittest -v test_coordination_model)
(cd verification/round3/spec && python3 -m unittest discover -v)
(cd verification/round3/go_pump && GOWORK=off GOTOOLCHAIN=local go test -count=1 -v ./...)
```

依赖只有 Python 标准库与 Go 标准库。代码按 Python 3.10+ 语法、独立 Go 示例 module 声明 Go 1.20；本次实际测试环境见 evidence/environment.json。没有验证每个兼容版本，也不改变项目既定工具链版本。总入口离线运行，不调用模型、不访问账号、不写项目代码；缺 Go 时以不完整状态退出，不把跳过当成功。

### 13.3 本次实际运行结果

| 测试套件 | 本次结果 | 能证明/不能证明 |
|---|---|---|
| legacy 原样测试 | 10/10 通过 | 旧功能测试可在附件中复现；旧非法批次测试仍容忍 active 卡住，不能当作故障恢复证据 |
| spec 修订状态测试 | 24/24 通过 | 包括原 10 项（其中非法输出断言已修正）+14 项故障/恢复测试；使用脚本输出和测试提供的停止观察 |
| Go event-pump | 7/7 通过 | bootstrap、无消息 timer 唤醒、入站后重扫、丢 wake 周期扫描、过期期限、状态错误、事件源关闭；使用 fake clock 和 fake Driver |

三个数量不可相加宣传成 41 项独立产品验收。新规格共 24 项，包含旧 10 项的改订版；Go 7 项只验证循环本身。

SQL 回滚和错误状态测试使用内存 SQLite，不验证电源故障、fsync、生产连接池或重启恢复。reject_output 测试验证 Adapter 上报解码错误后的落点，不实现完整 JSON Schema 解析器。Go 测试只证明 timer 会唤醒 Reconcile，不证明 Driver 已实现 request expiry/cancel/grace 的全套状态迁移。

### 13.4 仍需进入 Go 主线的验收

将本文状态约束落实到项目自己的 Go 存储与 Supervisor，再验证：根受理幂等；所有等待下实际期限迁移；timeout/late reply 竞争与幂等；取消宽限后 UNKNOWN；真实 STARTING crash window；完成提交崩溃恢复；ACL/fence；精确原生会话续接；最终真实 A→B→A→C→A→B→A。

不先做 UI、动态路由、通用 Broker，也不以“单次 Reviewer 返回正常”代替 B 主动追问的产品验收。

## 14. 设计消融

删除 Native Session 可以换成完整显式交接，但不能删除上下文连续性。删除进程常驻可以逐轮重启，但不能删除下一轮驱动者。删除通用 Broker 没有问题，但不能删除消息关联和已到达未处理状态。删除 SQLite 可以用内存单线程原子修改做实验，但要放弃跨进程崩溃后的控制记录，或另行实现文件事务。

最不能删的机制是：明确收件人与回复关系；可释放执行槽的等待；负责下一轮驱动的宿主；与输入和执行绑定的结果。轮次调度是当前明确需求，不再以“过度设计”为由放回一个没有实现的 Caller。

## 15. 资料依据与证据边界

### 本地来源

- S1：Round 2 审查提示词：`sources/round2_execution_review_prompt.md`（原附件路径，尚未提供）：原文件 `astra_round2_execution_layer_one_shot_review(1).md`；是审查要求，不是既定实现。
- S2：第一轮架构评审：`sources/architecture_review.md`（原附件路径，尚未提供）：原文件 `agent_coordination_architecture_review(1).md`。
- S3：原始问题：`sources/original_problem.md`（原附件路径，尚未提供）：原文件 `agent_coordination_problem_for_astra(1).md`。
- S4：Round 3 原文：`sources/round3_original.md`（原附件路径，尚未提供）：本轮修订前的对话附件，保存以便比较。
- 运行入口与限制：`../verification/round3/README.md`（原附件路径，尚未提供）。文件摘要见包根目录 `MANIFEST.sha256`。

### 官方文档

本轮实际网页核验日期：2026-10-07。只核验 W1/W2/W6/W7 中本文使用的接口描述；不从“页面可访问”推导 Runtime 行为已实测。

[W1] OpenAI Codex App Server，官方入口及实际跳转：
`https://developers.openai.com/codex/app-server/`
`https://learn.chatgpt.com/docs/app-server`

[W2] Claude Code，Run Claude Code programmatically：
`https://code.claude.com/docs/en/headless`

[W4] SQLite Transactions：
`https://www.sqlite.org/lang_transaction.html`
本轮仅保留原文的事务背景来源；生产持久性不因这一来源而视为通过。

[W5] MCP architecture / sampling：原 Round 3 的历史资料，见 S4。本轮未重新核验，不将其中版本 URL 作为 Go Adapter 依赖或新保证。

[W6] Go context，CancelFunc 不是停止完成确认：
`https://pkg.go.dev/context`

[W7] Go time，Timer/NewTimer：
`https://pkg.go.dev/time`

所有 request/question、TurnDecision、拒绝事务、暂停/恢复、期限策略均为本项目的设计决定，不是这些厂商已提供的跨 Runtime 协作协议。
