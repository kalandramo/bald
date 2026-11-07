# AGENTS.md

本仓库是 **bald**：Go 服务声明式装配框架（多 module 库 + 官方 CLI）。给在本仓工作的 AI/人的最小作业指引。
规范全文走 `bald convention`（本仓自己的蒸馏卡就住在 `cli/internal/convention/cards/`）：

```bash
bald convention overview        # 先看有哪些规范卡
bald convention get <卡id>      # 卡全文（judgments / reuse / catalog / codegen / gates / commit）
```

## 仓库事实（动手前必读）

- **多 module 仓**：每个 `go.mod` 是独立 module，`go test ./...` 必须在各 module 目录内跑；根模块无可导入包。
- **CLI 是零 replace 独立 module**（`cli/`）：`go install github.com/kalandramo/bald/cli/cmd/bald@latest` 可装；
  同仓库只能以已发布 tag 依赖（如 `bconf v0.23.0`），**禁止给 cli 加 replace**（那是它存在的意义）。
- **生成树是声明的函数**：`_example/bald` 的 owned 产物（`appspec.manifest.json` 清单内 DO NOT EDIT 文件）
  禁止手改，改 `appspec.json` 后 `bald gen app --adopt` 重新生成，`--check` 必须零差异。

## 硬门禁（提交前全过）

| 闸门 | 命令 | 什么时候 |
|---|---|---|
| lint | `task lint`（golangci 五类：errcheck/govet/ineffassign/staticcheck/unused） | 每次提交 |
| race | `task race`（模块清单 `scripts/list-modules.sh` 派生） | 每次提交 |
| 漏洞 | `task vuln`（govulncheck，只拦代码可达） | 每次提交 |
| 文档 | `go test ./internal/docscheck/` | 改了 docs/ 或架构制品 |
| workflow | `task ci-workflow-lint` | 改了 `.github/workflows/*.yml` |
| 生成物 | `task example-bald-vet` / `task acceptance` | 改了生成器或 AppSpec |
| 规范卡 | `task convention` | 改了 `cli/internal/convention/` |

## 设计判据（J1–J9）

取舍问题的裁决依据在 [docs/devel/zh-CN/设计判据.md](docs/devel/zh-CN/设计判据.md)：
零调用不是删除依据（J1）、重复税抽单一闸门（J2）、出站客户端默认超时（J3）、身份不完整 fail-closed（J4）、
显式声明不推断（J5）、协议只承诺协议形状（J6）、未实现配置段 fail-fast（J7）、契约不超承诺（J8）、
配置取值只有契约一条通道（J9）。做「保留/删除/收敛」类决定前先对照。

## 写代码纪律

- **复用优先**：落笔前查 [docs/devel/zh-CN/框架契约总览.md](docs/devel/zh-CN/框架契约总览.md)（能力地图），
  已有能力一律复用；同一不变量出现第二份实现 = 重复税，回 appkit/docscheck 收闸门。
- **贡献/修改蒸馏卡**：卡放 `cli/internal/convention/cards/*.md`，规则行带 `<卡id>-rNN` ID 与
  `[强制]/[建议]/[参考]` 前缀；`sources:` 指向真实母文档；`checks:` 只能声明注册表内 checker
  （docscheck 闸门全部校验）。好规则三问：在什么场景下、做什么、做到什么程度。
- **提交**：Conventional Commits，中文 subject；发布走 `bald release`，不手工打 tag。
