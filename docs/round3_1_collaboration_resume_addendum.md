# Round 3.1 补充契约：Collaboration 级恢复

> 仓库导入说明（2026-10-07）：与同目录的 [最新版 Round 3.1](round3_collaboration_v0_rfc.md) 配套保存；该链接现在指向 464 行修订稿的仓库副本。下文的“本次复跑/交付”描述原附件环境，不是本仓库的执行记录；C01–C14 仍待实现与验证。原生续接后续结果见 [实验摘要](experiments/native-resume.md)。

状态：**新增设计提案；尚未实现，尚无本补充的可执行测试。**
日期：2026-10-07。
适用基础：[Round 3.1 主文档](round3_collaboration_v0_rfc.md)。
建议合并位置：主文档 §11 增加 §11.4；§7.5、§13 按本补充交叉引用。
本文件不改变 Go 生产方向、受管独立会话、按轮交接或单 Writer 默认。

## 0. 来源与本次新增的边界

主文档 §7.1 同时要求 Collaboration=RUNNING 和 Participant.gate=OPEN；§7.5 规定等待/总期限超时暂停整段协作；§11.2 的 ResumeAfterReview 只明确解除参与者 gate；§11.3 要求重启后暂停未结束协作。上述规则没有明确组成 Collaboration 的恢复入口。

因此，本补充新增 ResumeCollaboration 的前置条件、事务后置条件和预算/期限规则。它们不是声称 Round 3.1 原来已经具备，也不是厂商原生接口。

本次解压并重跑的旧源码仍为三张状态表的有限 Python 规格（agents、executions、messages）及独立 Go 事件泵；没有实现下述 Collaboration 恢复契约。10/24/7 的通过记录不能作为本补充已验证的证据。

## 11.4 Collaboration 的显式恢复

### 11.4.1 两个 gate 分别控制，不相互替代

```text
Host 健康且拥有唯一推进权
AND Collaboration.state = RUNNING
AND Participant.gate = OPEN
AND 本轮会话、资源、期限、预算均允许
AND 存在可分派输入、自主继续意图或已授权恢复包
=> 可以预留一轮新 Execution
```

ResumeAfterReview 只处理某个参与者的故障恢复。ResumeCollaboration 只处理整段协作的运行许可。

先在 PAUSED 的 Collaboration 中执行 ResumeAfterReview 是允许的；该参与者变为 OPEN 后仍不能被调度。之后 ResumeCollaboration 成功，调度器才重新检查它。

反过来，ResumeCollaboration 成功不批量打开其他 participant gate，不消费 HELD 输入，不生成 recovery packet，不重放旧 Execution。

**恢复运行许可，不等于已经启动工作，更不等于业务已完成。**

### 11.4.2 V0 选择“静止点恢复”，不接管活跃轮次

恢复全局调度前，本协作的 STARTING、RUNNING、CANCELLING、UNKNOWN 及未结清占有必须逐项核对。

V0 要求：没有尚在进行的旧执行轮次，没有未核清的 UNKNOWN，没有旧执行尚未满足的会话/冲突写入停止条件。无法满足就返回 blocker，保持 PAUSED。不能提供 force=true 绕过这些条件。

这是保守的 V0 选择：不在全局恢复时收养仍在运行的旧轮次。它不要求退出空闲的 App Server 或销毁原生会话；检查对象是旧轮次及其可能冲突的执行活动。

必要的停止/副作用观察由 Supervisor/Adapter 取得并记录。Caller 只能提供授权与审阅决定，不能用一个未经验证的 stopped=true 替代观测。ReconcileStop 本身也不追认非法协调输出。

原生会话不可恢复的参与者继续 PAUSED(SESSION_UNAVAILABLE)。只要不存在未核清的旧执行或全局安全障碍，不必为恢复其他参与者而强行打开这个 gate；但返回结果必须列出该 blocker。

暂停期间仍持续接收原生完成/停止观察、保存合法迟到答复，并处理取消宽限期限。PAUSED 只禁止启动新轮次，不停止监督器和事件泵。

### 11.4.3 新控制操作

以下为语义契约，不是必须照搬的 JSON 或新的领域框架：

```text
ResumeCollaboration(
    collaboration_id,
    operation_id,
    expected_revision,
    reason,
    reconciliation_ref,
    deadline_changes?,
    new_total_limits?
) -> RecoveryReceipt
```

- caller 从可信入口绑定，必须有恢复权限；修改总期限/预算还必须具备相应授权，不能由普通 peer outbox 自授。
- operation_id 在 caller + collaboration 作用域内幂等。同 key、同规范化载荷返回原 receipt；同 key、不同载荷拒绝。
- expected_revision 绑定调用方审阅的状态版本。相关暂停、执行核对、终结答复、期限、预算或 gate 变化都必须改变该版本；过期审阅不得清掉后发生的暂停。
- reconciliation_ref 引用已留存的核对记录，覆盖当前全部协作级暂停原因、相关执行及停止边界、会话映射和已检查副作用。它不因“有一个引用”就被视为充分；Host 仍验证其范围和当前有效性。
- deadline_changes 仅包含显式的新绝对期限；new_total_limits 表示新的累计上限，不表示剩余额度重置。

本操作不更改 Goal、Acceptance Criteria、参与者集合、角色或工作区授权。不通过恢复入口另起任务、注入伪造 answer 或跳过权限审批。

正常幂等重读要先验证 caller 有权读取该记录。一个曾成功的恢复命令，在系统后来再次 PAUSED 后被重发，只返回旧 receipt，**不得再次恢复**。响应应把 applied_revision 与 current_state/current_revision 分开，避免旧成功回执伪装成现在仍在运行。

CLOSED（包括已放弃的协作）不能用这个操作重开；需要继续业务时使用新的 Collaboration 并显式引用旧材料。关闭仍不等于旧进程已经停止。

### 11.4.4 期限与预算不因恢复而“重新开始”

V0 默认暂停时间继续计入绝对期限。保留原 accepted_at、消息 created_at、累计预算使用量及旧超时记录；不做隐含停表。

| 当前情况 | 恢复要求 |
|---|---|
| 总期限未到，累计预算有余量 | 保持原值；仍须完成其他核对 |
| 总期限已到 | 同一次恢复计划必须包含获授权、严格晚于提交时刻的新总期限，否则拒绝 |
| 累计轮次数达到上限 | 明确增加累计上限，保证还有至少一个可预留轮次；不能将已用量清零 |
| request/question 已到期且未获得合法终结答复 | 必须逐项授权延期；不允许只把 processed=true 留着、从而制造永久等待 |
| 合法迟到 answer/result 已提交 | 该等待项已解决，无需再次计时；但原超时暂停仍须本操作明确核对后解除 |
| 原 Execution 超时或已取消 | 不延长/复活这个旧 Execution；按停止核对与参与者恢复规则安排新 execution_id |
| 停止宽限期届满、状态仍不明 | 保持 UNKNOWN 和相应占有，拒绝全局恢复 |

具体要求：

1. 恢复检查重新枚举所有未解决 request/question，包括暂停期间刚刚到期但 timer 尚未处理的项，不只读取某次 wait_for 列表。
2. 对未到期等待项，默认保持原期限。对已到期未解决项，V0 只支持“明确延期”或不恢复/放弃整段协作；本补充不引入通用子任务撤销或伪造答复。
3. 延期沿用原 message_id，不重新发送消息。保存旧期限与 timeout 记录，产生一个新的有效等待期限版本（deadline generation）；新 deadline 必须晚于恢复提交检查时刻，且不超过有效协作总期限。
4. timer 只是唤醒提示。它必须重新读取当前 message 状态与 deadline generation；旧 timer 不能在延期后再次触发旧超时。
5. 新累计上限只能由有权 Caller 增加并审计，不能绕过部署层硬上限。已启用的预算维度都必须可核对；不明使用量不能当作零。
6. 对轮次预算，建议 V0 以成功预留的新 Execution 为计数点，包含后来失败/未知的预留，默认不退款。若生产工程已有明确记账定义，需固定并测试该定义；绝不通过恢复清零。
7. 新轮次预留时再次检查时间、累计预算和占有。恢复成功到真正预留之间到期，应再次停止调度，不能因为刚恢复过就豁免。

重启不能恢复进程内的旧单调时钟。按已保存绝对期限和起算记录核对；时钟跳变导致期限是否仍有效无法可靠判断时，保持暂停，要求明确的新期限授权，不能偷偷给予完整新预算。

### 11.4.5 事务边界

耗时的 Runtime 检查在事务外完成，保存带执行/会话版本的观察；事务内校验这些观察仍对应当前状态，不持有数据库事务等待 Runtime。

```text
BEGIN
  校验身份、恢复/预算修改授权
  查询 operation_id：相同载荷返回原 receipt，冲突载荷拒绝
  校验 Collaboration 为 PAUSED，且 revision 与 expected_revision 匹配
  校验 Host 唯一推进权与存储健康
  核验所有协作级暂停原因及对应 reconciliation 记录
  核验旧轮次、UNKNOWN、会话与冲突资源条件
  重新核对当前时刻的总期限、未解决消息期限与累计预算
  验证全部 deadline_changes / new_total_limits

  原子记录恢复决定、前后期限和上限、已处理暂停原因
  应用明确的期限/预算修订，更新 deadline generation
  Collaboration.state = RUNNING
  revision++
  保存 operation_id、规范化请求摘要、RecoveryReceipt
COMMIT
提交后触发 wake hint；周期扫描仍作为漏唤醒后备
```

任何前置条件失败，不修改全局状态、期限、预算或参与者 gate。更新过程中存储失败则整笔回滚并按 §7.4 fail-closed 处理；不能留下“预算改了、恢复失败”的半笔操作。

成功后 participant gate/mode、已提交 outbox、原 execution.input_ids、HELD/RESOLVED 关联均保持原语义。若另有参与者需要恢复，仍使用 ResumeAfterReview，不能由本操作暗中代办。

RecoveryReceipt 至少能定位恢复操作、applied_revision、修改摘要和剩余 blocker。可以另返回基于该版本的可运行候选与最近 deadline；这些只是状态快照，不是“模型已启动”的确认。

RUNNING 且暂时无人可运行可能是正常等待，也可能是所有 gate 仍关闭。返回和 UI 必须区分；不得凭空加入 continue 轮次，也不得把“恢复接口成功”显示为“任务正在进展”。

### 11.4.6 迟到事件不会复活旧工作

旧 completion 继续按 execution_id 与原生 turn 引用去重、归档；已经 REJECTED 的输出不得改为 COMMITTED。不能仅因属于恢复前就丢弃真实停止证据，也不能把旧结果再次投递。

取消请求必须指向原 execution/native turn，不能写成“取消 A 当前那一轮”。否则队列中迟到的取消可能杀掉恢复后新轮次。

恢复后发生新的故障，会产生新的暂停与 revision；旧恢复请求和旧 deadline 提示不能将它清除。

## 13 补充：必须加入生产 Go 状态层的验收

下列为 **待实现、待运行**，不是本次 10/24/7 测试数量的一部分：

| 编号 | 场景 | 必须断言 |
|---|---|---|
| C01 | participant OPEN，Collaboration PAUSED | 不能预留轮次 |
| C02 | 全局恢复，某 participant 仍 PAUSED | 该 gate 不变；其他合格参与者才可能运行 |
| C03 | 总期限已过但未授权延期 | 拒绝恢复，期限/上限/状态不变 |
| C04 | 延期与增加累计上限后恢复 | 同一事务生效；已用量、历史超时、原受理时间不变 |
| C05 | 任一旧执行活跃、UNKNOWN 或仍有冲突占有 | 全局恢复被阻止，无新轮次 |
| C06 | 未答复的超时问题未延期 | 拒绝；不能借 processed 标记永久跳过 |
| C07 | 已提交合法迟到答复 | 该问题无需延期；但必须显式解除全局暂停 |
| C08 | 延期后旧 timer 到达 | 旧 generation 无效；不重复旧超时 |
| C09 | 恢复成功后再次暂停，再重发旧 operation_id | 返回原 receipt；当前暂停保持 |
| C10 | 新暂停或核对变更发生在人工检查之后 | expected_revision 不匹配，恢复拒绝 |
| C11 | 事务中存储失败/提交后响应丢失 | 前者无半提交且 fail-closed；后者幂等读回，不重复启动 |
| C12 | 重启时旧期限已到、预算已耗尽 | 不自动重置；核对/授权后才允许恢复 |
| C13 | 无可运行者、但恢复条件满足 | 回执显示真实 blocker/等待，不能自动合成工作 |
| C14 | 旧取消事件迟到 | 不能取消恢复后产生的新 execution |

需在实际 Go 存储、事件泵和 Supervisor 组合中运行这些测试。可以使用假 Runtime 注入观察来验证状态层，但真实停止与会话保留还需原生实验。

## 下一步：先做小型原生续接实验，而不是再设计主架构

先让 Codex 和 Claude 各自完成“两轮同会话”探针，再接 A/B/C 七轮协作。第一步只验证输入/输出/续接，不默认赋予项目写权限或切换用户认证方式。

建议探针：第一轮给一个只出现在这轮输入中的随机标记和未完成请求，要求返回 question + wait；观察完整结构化产出和轮次结束。第二轮只传对应 answer 与必要协议说明，通过准确 session/thread 引用续接，检查结果保留第一轮标记并关联原请求。不要把第一轮完整历史或标记重新塞进第二轮输入，也不要把标记放进模型可搜索的公共文件。该探针检验有限的上下文续接，不证明长上下文无损。

同时建立两个同 Runtime 的独立会话使用不同标记，交错续接，检查不会串线。每一轮都检查结构化决策，而不是仅验证第一轮 schema 成功。

然后用真实模型输出完成：A 委派 B → B 问 A → A 问 C → C 答 A → A 答 B → B 返回 A → A 接续。全局执行槽为 1，Host 自动转交消息；脚本可以断言结果，不能代替 Agent 产生各轮决策。

最后单独验证取消、错误结构化输出、会话不可恢复、进程或连接异常。安全通过条件是状态诚实、不会双开或重复副作用；不是强制每种故障都自动恢复。

本次官方文档核验支持下列候选接口，但没有运行这些 Runtime：
- [Codex App Server](https://learn.chatgpt.com/docs/app-server)：thread/start、thread/resume、turn/start、turn/completed；outputSchema 仅适用于当前轮。
- [Claude Code 非交互 CLI](https://code.claude.com/docs/en/headless)：保存 session_id，用 --resume 指定续接；--output-format json 与 --json-schema 对应 structured_output。

原始 protocol/argv、版本、原生 session/turn 引用、输入摘要、结构化输出、结束/停止观察必须留存，凭据必须排除。模型恢复了一个测试标记不代表已实现沙箱、持久性或自动崩溃恢复。

## 本次实际交付与未做事项

- 原 Round 3.1 的源码、运行器与历史文档保持原字节；附上新的独立复跑记录。
- 本次复跑结果：legacy 10、Python spec 24、Go pump 7，均通过；七轮脚本演示通过。
- 本补充仅新增规范与验收条目，不增加新的通过测试数量。
- 没有读取或修改用户的 /Users/... 本地路径，也没有提交仓库；核对对象为本对话附件。
- 本环境 PATH 未发现 codex/claude，因此没有执行原生续接实验；这不是对用户电脑安装状态的判断。
