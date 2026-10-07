# 原生会话续接实验

## 要收敛的问题

不同 Runtime 能否在第二轮原生续接第一轮会话，同时保持两个独立会话的身份和请求关联？这是后续多轮协作的前置能力验证。

## 方法

每个 Runtime 开两个独立会话，各两轮，按 S1R1 → S2R1 → S1R2 → S2R2 交错执行。

1. 第一轮提供随机 marker、requestId 和合成请求，要求输出一个结构化 question。
2. 第二轮原生 resume；只提供 answer、requestId 和协议说明，不重述 marker 或第一轮正文。
3. 检查取回本会话 marker、正确 requestId、result 决策和对第一轮请求的延续；不出现另一会话 marker。
4. Runtime 失败 envelope、无效输出、隔离违例不能计 PASS；UNKNOWN 停调，零自动重试。

对照信号是交换 marker、错误 requestId、缺失/未知字段、失败 envelope、非白名单工具 item 和断连/超时的离线反例。移除原生会话记忆或混淆会话应使对应断言失败；marker 取回只是有限记忆信号，不证明全部上下文无损。

## 已有实测与证据等级

2026-10-07 两批各 8 轮，累计 16/16，无重试。CLI 版本：Codex 0.159.3、Claude Code 2.1.236。验证对象是各 CLI 当时配置的模型（Codex gpt-6.1-sol；Claude CLI glm-5.2[1m]），不是所有同名产品或模型。

- 首批两轮续接和身份分离有观察信号；隔离配置及 Claude 输出检查器有缺陷，因此保留为观察性结果。
- 修复后第二批每个 Runtime 4/4 PASS。Claude 用 `--tools ""`、严格空 MCP 配置及证据目录外空 cwd，已有结构性工具隔离配置与 live 证据。
- Codex 用空 cwd、read-only sandbox、完成 item 白名单及日志核对；这属于行为性证据。0.159.3 的 read-only 读权限为 fullAccess，不能声称结构性读隔离。
- TASK-0002 对 Codex 行为性证据的验收降档仅限该实验；七轮协作和生产任务须独立确立隔离门槛。

本说明是已验收实验的公开摘要。原始批次报告与脱敏证据保存在本机 Private 存储，没有随 Git 发布；他人可复跑代码，但无法仅凭本仓库独立审计上述历史 live 数字。

## 本次工程化验证

源码入口：[verification/native-resume](../../verification/native-resume/README.md)。标准测试包含 21 项纯离线检查与 7 项 fake Runtime 测试；不调用真实模型。工程化不增加 live 证据，也不改变既有结论。

超时/停止/失联路径只在 fake Runtime 中验证，两批 live 未触发。七轮异构协作、并发调度、长上下文完整性、持久化恢复与生产隔离均未验证。

下一阶段应建立独立七轮任务契约，定义最小 Host 闭环、协议映射、输入边界、停止/UNKNOWN 处理和固定调用预算，再实施与验收。
