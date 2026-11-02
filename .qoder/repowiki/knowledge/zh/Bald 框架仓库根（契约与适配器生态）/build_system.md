## 1. 构建工具链

- **任务编排**：根目录 `Taskfile.yml`，使用 [go-task](https://taskfile.dev)（v3），替代 Makefile。Windows/macOS/Linux 跨平台兼容，所有跨目录命令用 `dir:` 字段而非 `cd`。
- **Protobuf 生成**：通过 `buf generate`，契约代码在 `bconf/` 下按 `buf.gen.yaml` / `buf.gen.local.yaml` 双模板生成到 `bconf/gen/go`；示例 gRPC/gateway 代码在 `_example/bald/api/` 下按 `api/buf.gen.yaml` 生成。
- **依赖管理**：每个子包是独立 Go module（各含 `go.mod`/`go.sum`），仓库是多 module 仓库，无单仓聚合 build。
- **版本固定**：`Taskfile.yml` 的 `vars` 段集中 pin 工具版本（`PROTOC_GEN_DEFAULTS_VERSION=v0.0.2`、`GOLANGCI_LINT_VERSION=v2.15.0`、`GOVULNCHECK_VERSION=v1.9.0`、`ACTIONLINT_VERSION=v1.7.12`），CI 侧以相同版本号安装，避免上游规则集漂移。

## 2. 模块清单派生机制

- **唯一真相源**：`scripts/list-modules.sh` 用 `find . -name go.mod` 自动发现全部模块（排除 `.git/*` 与 `_example*`），输出每行一个模块路径，字典序排列；若派生为空则非零退出，防止“闸门静默失效”。
- **CI 与本地一致**：`.github/workflows/ci.yml` 与本文件（`Taskfile.yml`）的 lint/vuln/race/verify 都消费同一脚本，保证本地与 CI 覆盖范围不漂移。
- **_example 与 cobramcp/examples** 作为验证用独立 module，由 Taskfile 的 `example-*` / `cobramcp-example-verify` 任务单独覆盖，不参与主循环的 `go build ./...`。

## 3. CI 流水线（GitHub Actions）

### 主 CI（`.github/workflows/ci.yml`）
- **触发**：push 到 `main` 与所有 PR。
- **Go 版本**：`setup-go@v5` 固定 `go-version: "1.27.3"`。
- **GOPROXY**：`https://goproxy.io|https://proxy.golang.org,direct`，依赖下载失败重试 3 次（间隔 15s），之后全链路离线稳定。
- **test job**：对 `scripts/list-modules.sh` 派生的每个模块执行 `go build ./... && go vet ./... && go test -short -timeout 5m ./... && go test -bench . -benchtime=1x -run '^$' ./...`。根模块额外加 `-copylocks=false`（appkit hotReload 的 proto 顶层浅拷贝属既定设计）。
- **race job**：`go test -short -race -timeout 10m`，排除列表来自 `.github/race-skip.txt`；派生为空时直接报错。
- **integration job**：拉起 etcd v3.5.14、redis:7-alpine、mongo:7 三个 service，跑 `registry/etcd`、`contrib/store-mongo`、`transport/asynq` 的非 -short 测试，并通过 `EXPECTED` 表逐条 grep `--- PASS` 断言用例真的被执行（防 Skip/改名/未编译导致的静默绿）。
- **lint job**：按模块循环运行 `golangci-lint run ./...`（根 `.golangci.yml`，子模块自动向上发现）。
- **vuln job**：按模块循环运行 `govulncheck ./...`，只报告代码可达漏洞。
- **concurrency**：同分支并发任务 `cancel-in-progress: true`。

### Windows 周界守卫（`.github/workflows/windows.yml`）
- **触发**：每周 UTC 周一 03:00 cron + 手动 `workflow_dispatch`。
- **Go 版本**：`go-version: "1.27.1"`（与 Linux 不同，专门覆盖 Windows 特有分支）。
- **范围**：仅 root、bootstrap、contrib/authz-casbin 三个 module；root 额外跑 `TestTree_EndToEndTimed` 覆盖 CTRL_BREAK 优雅停机路径。

### Workflow 自检查门（`.github/workflows/workflow-lint.yml`）
- 独立于 ci.yml 的文件，用 `actionlint@v1.7.12` 校验所有 workflow YAML schema 及内嵌 shell 语法；避免 ci.yml 自身写错时“连检查都不会触发”的自欺问题。

### BSR 发布（`.github/workflows/publish-bconf.yml`）
- **触发**：推送 `bconf/v*` tag 或手动触发。
- **流程**：`buf lint` → `buf build` → `buf push --create --create-visibility public --git-metadata`，需要 `BUF_TOKEN` secret。
- **约定**：Go module tag 与 proto 发布同源同版本，避免两者漂移。

## 4. 辅助脚本

- `scripts/list-modules.sh`：模块清单派生（见上）。
- `scripts/multi-major.sh`：只读扫描每个 module 的 `go mod edit -json`，列出同一基础路径下出现多个 major 的传递依赖组合，供接手者观测，不做任何修改，退出码恒为 0。

## 5. 关键约定与约束

- **模块清单单一来源**：`scripts/list-modules.sh` 是 CI 与本地 Taskfile 共享的唯一派生点，新增模块无需改动 CI 或 Taskfile。（强制力：脚本空结果非零退出）
- **短测 + 集成分离**：主 CI 一律 `-short` 跳过集成/计时测试；`integration` job 显式点名套件并断言 PASS 记录，防止 Skip 伪装成通过。
- **基准冒烟**：每个模块追加 `go test -bench . -benchtime=1x -run '^$'`，只编译并跑一轮基准，拦截基准编译回归与 panic。
- **竞态检测隔离**：`-race` 跑在独立 job，不在主 test 中启用，避免 session manager / 热重载等并发路径拖慢整体。
- **工具版本必须 pin**：所有外部工具（golangci-lint、govulncheck、actionlint、protoc-gen-defaults）均在 `Taskfile.yml` vars 与 CI 安装命令两处同步固定版本。
- **Windows 适配**：Taskfile 用 `platforms: [linux, darwin]` / `[windows]` 条件命令；Windows CI 仅覆盖三个敏感 module，其余走 Linux 主循环。
- **示例模块隔离**：`_example/bald` 与 `cobramcp/examples/*` 是独立 module，不被根模块 `go build ./...` 覆盖，需经 Taskfile 显式任务验证。
- **Protobuf 产物可复现**：`bconf/gen/go` 可通过 `buf generate --template buf.gen.local.yaml` 本地重新生成，并由 `TestGeneratedMatchesProto` 校验与 BSR 产物一致。