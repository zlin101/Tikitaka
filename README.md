# Tikitaka

Tikitaka 是本地 coding agent 协调运行时。目前已建立 Go 工程与原生会话续接验证工具；生产协调器尚未实现。

## 工程入口

- Go module：`github.com/zlin101/Tikitaka`，Go 1.26 或以上；仅标准库，无第三方依赖。
- [续接探针与测试](verification/native-resume/README.md)：两个独立会话各续接两轮。
- [实验方法与结论边界](docs/experiments/native-resume.md)。
- [Round 3.1 执行方案](docs/round3_collaboration_v0_rfc.md)及[协作恢复补充](docs/round3_1_collaboration_resume_addendum.md)：设计提案，生产实现待验证。
- [产品方向归档](Archiving/Polaris.md)：后续实施范围由具体任务确定。

## 离线检查

从仓库根运行；这些命令无需模型认证，不调用真实 Runtime：

```sh
go test ./...
go test -race ./...
go vet ./...
go build ./...
go run ./verification/native-resume -mode=offline
```

当前验证平台为 macOS arm64、Go 1.26.5。探针依赖 POSIX 进程组；其他平台尚未验证，Windows 暂不支持。

## 目录

```text
verification/native-resume/   探针源码、离线与 fake Runtime 测试
docs/experiments/             可公开的实验方法与边界
artifacts/native-resume/      本地 live 输出（Git 忽略）
Archiving/                   既有方向文档
```

项目治理记忆和历史实测证据保存在本机 Private Vault，不是源码或测试的运行依赖。
