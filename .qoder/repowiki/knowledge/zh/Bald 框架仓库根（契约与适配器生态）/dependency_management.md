## 1. 系统/方法

仓库是一个 Go 多模块（multi-module）仓库，使用 `go.mod` / `go.sum` 作为唯一的依赖声明与锁定机制，没有 vendor 目录、没有 `package.json` 或 npm 体系。根模块 `github.com/kalandramo/bald` 通过大量 `replace` 指令把内部子模块（如 `bconf`、`bootstrap`、`transport`、`log/*`、`registry` 等）指向本地相对路径；对外部发布的同组织模块（`github.com/kalandramo/bald/bconf v0.15.3`、`.../bootstrap v0.13.4` 等）则同时出现在 `require` 中，形成「本地开发走 replace、发布后走版本」的双轨模式。

示例应用 `_example/bald` 是独立的 module（`module github.com/kalandramo/bald/example/bald`），以语义版本（如 `github.com/kalandramo/bald v0.34.1`）引用已发布的 bald 包，与根模块的 replace 隔离开，从而验证发布产物可被外部消费者消费。

## 2. 关键文件

- `go.mod`、`go.sum`：根模块的依赖声明与锁文件。
- `_example/bald/go.mod`：示例应用的独立依赖声明，用作“外部使用者”视角的校验。
- `scripts/list-modules.sh`：仓库内所有 module 的唯一派生来源——`find . -name go.mod` 并排除 `.git` 与 `_example*`，按字典序输出。CI 与 Taskfile 都调用它，避免各自维护 find 谓词。
- `scripts/multi-major.sh`：只读工具，对每个 module 执行 `go mod edit -json`，检测同一 base path 下出现多个 major 的版本组合（用于观察传递依赖的多 major 并存，不阻断）。
- `.github/workflows/ci.yml`、`.github/workflows/windows.yml`、`.github/workflows/workflow-lint.yml`：统一设置 `GOPROXY: "https://goproxy.io|https://proxy.golang.org,direct"`，主源 goproxy.io + 备源 proxy.golang.org + direct 回退。
- `buf.gen.yaml` / `buf.lock`（位于 `bconf/proto`、`_example/bald/api`）：Protobuf 代码生成依赖由 buf 管理，`buf.lock` 锁定第三方 proto 模块版本。
- `_example/bald/taskfile.yml`：示例应用的构建入口，仅做 `go build/vet/test`，不引入额外包管理器。

## 3. 架构与约定

- **单模块一 go.mod**：每个功能域（`bconf`、`bootstrap`、`transport/grpc`、`log/sentry`、`contrib/authz-casbin` 等）都是独立 module，拥有自己的 `go.mod` 与 `go.sum`。这使各适配器可按自身 SDK 生命周期升级。
- **内部依赖双轨**：根模块用 `replace` 绑定本地实现，同时保留对外发布的 `require` 版本，保证 `go mod tidy` 在 CI 上检查的是“替换前”的语义版本约束。
- **依赖发现集中化**：`scripts/list-modules.sh` 是模块清单的唯一事实源，注释明确声明“CI 与本地 Taskfile 都消费这一条，不再各自内联 find”，并断言“清单为空 = 闸门静默失效，必须响而不是返回空”。脚本在找不到任何 module 时直接 `exit 1`。
- **多 major 观测器**：`scripts/multi-major.sh` 扫描每个 module 的 require，对去掉 `/vN` 后的 base path 统计不同 major 数量，只输出报告、退出码恒为 0，注释写明“不是闸门”。
- **CI 依赖闸门**：`ci.yml` 在每个 module 循环内执行 `go mod tidy -diff`，go.mod/go.sum 与 import 漂移即失败；同时运行 `govulncheck` 扫描依赖漏洞，以及 `go test -bench . -benchtime=1x` 冒烟基准编译。
- **Proto 依赖隔离**：buf 生成的 proto 依赖通过 `buf.lock` 锁定，与 Go 依赖解耦。

## 4. 约定与约束

- **模块列表唯一派生**：`scripts/list-modules.sh` 是模块清单的唯一来源；CI 与 Taskfile 不得自行维护 find 谓词（见脚本注释）。违反会导致 CI 与本地覆盖范围漂移。
- **GOPROXY 顺序固定**：所有 GitHub Actions job 统一设置 `GOPROXY=https://goproxy.io|https://proxy.golang.org,direct`，原因注释说明 goproxy.io 为主源（解决 Azure US runner 上 proxy.golang.org 的 HTTP/2 中流错误），sumdb 校验不受影响。
- **go.mod/tidy 一致性是硬性门禁**：CI 在每个 module 上跑 `go mod tidy -diff`，漂移即失败（见 `ci.yml` 注释“依赖面闸门：…go.mod/go.sum 与代码 import 漂移即红”）。
- **漏洞扫描是硬性门禁**：`govulncheck` 按模块扫描“代码可达”依赖漏洞（见 `ci.yml` 注释）。
- **示例 module 独立于 replace**：`_example/bald` 在 `list-modules.sh` 中被 `-not -path "./_example*"` 排除，由 Taskfile 的 `example-*` 任务单独处理，并用已发布版本（非 replace）消费 bald 包，作为发布可用性校验。
- **无 GOPRIVATE/GONOSUMCHECK 配置**：仓库未定义私有 registry 或 GOPRIVATE 规则，所有依赖来自公共 Go Proxy。
- **无 vendor 策略**：仓库不使用 `go mod vendor`，依赖树完全由 `go.sum` 锁定。
