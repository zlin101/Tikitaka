# 原生会话续接探针

此目录是探针与测试的唯一源码入口。`main.go` 保留单文件标准库实现，实验阶段暂不拆出生产 Adapter。实验方法和已知边界见 [公开说明](../../docs/experiments/native-resume.md)。

## 默认离线运行

从仓库根执行：

```sh
go run ./verification/native-resume
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
go build ./...
```

默认模式为 offline，不寻找真实 Codex/Claude，也不需要认证。CLI 自测有 21 项；标准 `go test` 运行该矩阵及 7 项 fake Runtime 测试，共 8 个顶层 Test。

- `offline_test.go`：既有输入隔离、决策验证、envelope、item 白名单和 UNKNOWN/期限检查。
- `probe_test.go`：fake Claude 超时进程组退出；fake Codex 启动及轮中失联。
- `review_test.go`：Claude `structured_output` 优先；失败 envelope 拒绝。
- `rereview_test.go`：`turn/start` 响应超时后 UNKNOWN 停调；进程查询失败不能证明无进程。

Fake CLI 由测试写入临时目录并通过 PATH 注入；测试使用合成输入，只核对自己启动的 PID/进程组，不查询全机进程。测试修改共享期限参数和 PATH，因此保持串行。

## 显式 live 运行

Live 会使用本机已配置的 Runtime、认证与模型，每个 Runtime 执行 4 轮；本次工程化没有执行 live。需要另行安排成本额度和隔离验收。

```sh
go run ./verification/native-resume -mode=live -runtime=codex -out=artifacts/native-resume/codex-new-batch
go run ./verification/native-resume -mode=live -runtime=claude -out=artifacts/native-resume/claude-new-batch
```

每次使用新的输出目录。CLI 通过 PATH 选择，本机配置决定模型；新版本/模型需要重验。约定输出位于 Git 忽略的 `artifacts/native-resume/`；指定其他路径时应自行保证不会提交原始日志和会话资料。

Live 的退出码不代表验收通过：读取 `summary.json` 的 status、每轮 status/validate 与日志。此处保留既有 CLI 行为，尚未设计自动化 live 验收接口。

## 生命周期边界

S1R1 → S2R1 → S1R2 → S2R2 顺序执行，单槽、不重试。每轮 120s，取消/核对宽限 15s，批次期限 20min。Codex 超时尝试原 thread/turn 的 interrupt；Claude 超时杀进程组并有界观察退出。任何 `unknown_*` 停止后续调度，整体 inconclusive。

已验证平台：macOS arm64、Go 1.26.5；当前实现使用 POSIX syscall，不支持 Windows。停止/失联断言来自 fake Runtime，不能推导真实 Runtime 的取消成功。
