---
id: gates
title: 质量门禁
level: 强制
category: codegen
sources:
  - README.md
  - Taskfile.yml
  - .github/workflows/ci.yml
checks: []
---
# 质量门禁

提交前的机械化底线（多 module 仓库，没有根目录一把梭的 test）。门禁清单以 README「质量门禁」节与 Taskfile 为准。

- [gates-r01] [强制] 每个 Go module 在其 go.mod 所在目录内 `go test ./...`；主循环跑 `go test -short`，非 -short 集成用例由 CI 的 integration 作业注入真 etcd/consul/redis/mongo 跑。
- [gates-r02] [强制] 提交前过三闸：`task lint`（golangci 五类正确性检查）、`task race`（全量 -race，模块清单由 scripts/list-modules.sh 派生）、`task vuln`（govulncheck，只拦代码可达漏洞）。
- [gates-r03] [强制] 改 `.github/workflows/*.yml` 后先跑 `task ci-workflow-lint`：GitHub 在编译期解析作业文件，一个未登记的键会让整个文件被拒，本地 YAML 解析器看不出任何问题。
- [gates-r04] [强制] 改 docs/ 或架构制品后跑 `go test ./internal/docscheck/`：文档与架构制品有机器闸门（含蒸馏卡的来源链接与规则 ID 校验）。
- [gates-r05] [强制] 生成物改动以零差异为准入：CI 的 example/acceptance 作业跑生成器 `--check`（本地对应 `task example-bald-vet`、`task acceptance`）。
- [gates-r06] [强制] 非测试代码新增环境变量读取或手写 flag 路由必须登记理由：判据 J9 的双向对账闸门（docscheck + bootstrap）会判红未登记路由与已失效的死登记。
