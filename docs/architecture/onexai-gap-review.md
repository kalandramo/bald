# OneX Harness AI Coding 依赖清单 × bald 差距评审

> 评审日期：2026-11-07。信息源：孔令飞《OneX Harness AI Coding 实战课》第 01–06 章 PDF
>（01 工具介绍 / 02 安装配置 / 03 Web 服务 / 04 多服务项目 / 05 异步任务服务 / 06 进程管理与消息队列）。
> 对照仓库：`kalandramo/bald`（`main` @ `9eab57b` 附近），兄弟仓 `bald-admin` / `bald-crud` / `bald-utils` / `desktop-kit` / `zke` 仅浅查。
>
> 本文回答两个问题：**onexai 的效果依赖哪些东西**（§1–§2），以及 **bald 在这些维度上有什么、缺什么**（§3–§4）。
> 注意口径：onexai 是「Harness AI Coding 工具套件」（CLI + 规范 + MCP + 脚手架的完整闭环），
> bald 是「声明式装配框架 + 代码生成 CLI」——两者定位不同，本文只做依赖维度的客观对照，不主张 bald 必须复刻 onexai 全部形态。

---

## 1. onexai 的方法论依赖（为什么需要下面那些组件）

第 01 章的论证链条，决定了整套工具的组件构成：

1. **问题判定**：AI 编码面临的代码质量问题（破坏架构、忽略边缘条件、缺乏全局视野、风格不统一）
   与传统软件工程中初中级工程师面临的问题高度一致 → 解法不在 AI 侧，在工程体系侧。
2. **非 AI 时代的五个手段**：开发硬能力、规范约束（文档化）、开发脚手架（固化架构）、
   代码审查（自动化质量网关 + 人工 Review）、透彻理解产品需求（PRD）。
3. **AI 时代的转译**：这些手段本质上是「数据」。核心思想是
   **与其让 AI 通过 Loop 反复纠错，不如直接提供一套高质量数据，并建立 AI 感知数据的通道**。
4. **高质量数据四性**：数据全面、数据质量高、数据易理解（结构化 / 规范化 / 一致性）、扩展能力（插拔简单清晰）。
5. **递进关系**：Prompt Engineering → Context Engineering（知识库）→ Harness Engineering（SKILL / MCP Tool）→
   Loop Engineering（数据质量越高，Loop 次数越少）。

这条链条推出六个依赖层：**规范数据层、基础包层、脚手架层、AI IDE 集成层、工程运维配套层、流程方法论层**。以下逐层展开。

---

## 2. onexai 依赖的东西（六层清单）

### 2.1 规范数据层：46 个 convention 工具

课程体系中的完整开发规范经梳理加工后封装为 46 个工具，全部自动转成 MCP Tool（`convention_<subcommand>`），
并有一个 `convention overview` 入口工具向 AI 通报全部可用规范及其级别，而非全量喂入。

- **规范级别**：`Mandatory 强制`（所有项目必须遵守）/ `Recommended 建议`（有正当理由才可偏离）/ `Reference 参考`（按项目情境灵活采纳）。级别标注在规范文档内的具体规则上，不是命令级分类。
- **按类别覆盖**（46 项全列）：
  - Core Principles：`onexstack`（包目录）、`onexstack-reuse`（复用优先，MANDATORY：写代码前必先检查能否复用 onexstack/onexstack 包）
  - Coding Standards：`benchmark` `cli` `cobra` `coding` `concurrency` `context` `data-structures` `database` `dependency-management` `design-patterns` `documentation` `id-generation` `klog` `lint` `logging` `misc` `modernize` `observability` `performance` `refactoring` `safety` `security` `slog` `structs-interfaces` `testify` `testing` `troubleshooting` `viper`
  - API & Error Handling：`error-return` `errorsx` `grpc` `rest-api`
  - Architecture & Design：`architecture`（Clean Architecture）`dependency-injection` `directory`
  - Operations：`ci` `commit`（Conventional Commits）`semver` `version`
  - Platform & Development / AI & Methodology / Scaffold & Tooling / Web Frontend：`dev-system` `misc` `prd`（PRD 方法论）`scaffold`（脚手架后置构建步骤）`web-frontend`（React 19 + Rsbuild + Wujie 微前端）
- **逐字原文存档**：46 条工具描述、overview 完整输出与 20 命令清单见 [onex-ai.md](onex-ai.md)（人工补抄的 PDF 截断原文）。从中可见几个此前截断的关键细节：
  - **MCP 命名规则**：工具名 = `convention_<subcommand>`，下划线拼接、**子命令内连字符原样保留**（`error-return` → `convention_error-return`）；
  - **级别定义完整版**：Mandatory「只影响产品功能；例外需正式评审批准；**存量项目分期改进、新项目自设计期强制**」，Recommended「默认遵守，例外需评审；存量项目渐进采纳」，Reference「仅指引，按情境采纳」；
  - **`ci` 规范全量覆盖**：GitHub Actions、`-race`、lint、SAST（govulncheck/CodeQL）、coverage、Dependabot/Renovate、**GoReleaser、AI code review**——后两项是 bald 尚无的工程面（govulncheck bald 已有，属领先项）；
  - **`documentation` 规范显式包含 `llms.txt`**——onexai 把「给 LLM 的站点级索引文件」当作文档规范的正式组成部分；
  - `troubleshooting` 含 pprof setup 与 GODEBUG tracing（印证 §4.3 的 pprof 缺口）；`viper` 的分层优先级（flag > env > file > KV > default）与 bald 配置系统设计的层模型是同构物；`onexstack-reuse` 全文即 "Always check and reuse onexstack/onexstack packages **BEFORE writing any new code**"。
- **消费策略**（Context 成本控制）：新项目按规范开发；阶段性用 `/review` 命令做一次性再规范化（`--review-timing=per-story|at-end`，at-end 省 token）；日常由 AI 自行判断调用哪个规范，`onexai rag` 提供语义查找兜底。
- **Review 工具**：`onexai review` 可强制按规范等级 Review 代码并自动修复。

### 2.2 基础包层：onexstack 可复用包目录

规范是「说法」，基础包是「做法」——脚手架生成的代码大量直接引用这些包，保证实现与规范一致：

- `pkg/app`：应用入口框架（配置加载、信号处理、优雅关停），少代码量拉起一个应用。
- `errorsx`：统一错误包设计（struct 定义、API、wrap 链路）。
- `restry`：声明式第三方 REST 客户端（`create webserver --clients=[]` 的底层，生成调用框架、业务补路径与参数）。
- Kafka 客户端封装：消费者组、消息循环、优雅关停（JobServer/MQServer 的 `internal/pkg/kafka`）。
- GORM 通用 Store + `where` 查询构建 + `AfterCreate` 钩子自动生成资源 ID。
- `contextx`：带 Trace ID / Logger 的上下文注入；`errno` 统一错误码；`workerpool`（按 `per-watch-concurrency` 提交任务）；`ptr`；限流器。
- ID 生成规范：资源 ID 形如 `post-zds48n`（前缀 + 随机段）。
- **复用优先强制规则**（`onexstack-reuse`）：这是基础包层能被 AI「用起来」的关键约束。
- **参考实现锚点**：`miniblog`、`onex` 两个开源仓库是规范体系内的参考实现（`convention architecture` 的工具描述里就点名 miniblog reference implementation）。口径注意：课程里「一致性得分 96/100」「miniblog vs miniblog2 评分表」等是作者让 AI 对代码仓库做的评测，**不是 onexai 工具的能力**——工具侧没有内建评分/一致性评测。

### 2.3 脚手架层：20 个 create 命令 + 项目模型

- **20 个子命令**：`quickstart` `project` `webserver` `api` `jobserver` `job` `mqserver` `mq` `mcpserver` `mcp` `clitool` `cmd` `console` `micro` `declserver` `crd` `controller-manager` `controller` `operator` `operator-api`。
  覆盖企业五类高频服务：Web（REST/RPC）、异步任务（状态驱动）、消息队列（事件驱动）、CLI、声明式 API 服务器 + 控制器（kube-apiserver / kube-controller-manager / Operator 风格），外加 Web 前端（console / 微前端 micro）。
- **模式**：`create X` 建框架（二进制入口、引擎、配置、存储、部署文件），`create y` 往已有框架追加资源（api→webserver、job→jobserver、mq→mqserver、crd/controller→declserver/controller-manager、operator-api→operator）。
- **PROJECT 账本文件**：项目根的「账本」，记录模块路径、元信息、各服务配置与已生成资源；所有追加类命令读取并写回。丢失即报 `open project config "PROJECT": no such file or directory`。
- **onexstack.yaml**：多服务项目定义（`metadata` + `webServers/jobServers/mqServers/mcpServers/cliTools/declServers/controllerManagers/operators` 数组），一次生成多服务并共享同一套工程基础设施。
- **单服务命令参数体系**（webserver 为例）：`--web-framework`（gin/echo/fiber/go-zero/grpc/grpc-gateway/hertz/iris/kitex/kratos）、`--app-framework`（onex/cobra）、`--clients`、`--deployment-method`（docker/kubernetes/systemd）、`--distroless-mode`（always/never/auto）、`--makefile-mode`（none/unstructured/structured）、`--sdk-type`（gRPC/REST SDK）、`--service-registry`（consul/eureka/nacos/none/polaris）、`--storage-type`（etcd/mariadb/memory/mongo/mysql/postgresql/redis/sqlite，默认 memory 开箱即用）、`--with-otel`、`--with-preloader`。
- **生成物清单**：`cmd/<binary>` 入口、`configs/<binary>.yaml`、`internal/` 分层（handler/biz/store 或 watcher/store/model）、`third_party/protobuf`、`scripts/`、Makefile、`.golangci.yaml`、`.protolint.yaml`、`manifests/`（K8s）、`build/docker/<binary>/Dockerfile`（distroless 分档）、校验（validation）与类型转换（conversion）代码、统一错误码/日志/链路追踪基础代码。
- **前端例外**：console / micro 不进 onexstack.yaml，走独立的 pnpm + Turborepo 工程体系。

### 2.4 AI IDE 集成层

- **MCP Server**：`onexai mcp` 把全部子命令暴露为工具，供 Claude 等 AI 助手驱动；IDE 内 `/mcp` 确认连接。
- **`onexai ide init --ide=claude|opencode|crush|cursor`**：生成 `.claude/settings.local.json`（默认开启 onexai mcp server）、`commands/onexai.md`（`/onexai <prompt>` 编程命令）、`commands/onex-commit.md`、`commands/onex-review.md`、`skills/onexai-conventions.md`（OneX 规范 SKILL，由模型自行判定调用时机）。
- **AKSK 认证**：`$HOME/.onexai/onexai.yaml` 存 ak/sk；OneX 平台（课程云）通过 AKSK + Remote MCP Server 一键分发全栈能力。
- **PRD 工作流**：`onexai prd translate -i prompt.md -o prd.md`（自然语言 → PRD/Story 列表，US-xxx 带 Status/Priority/Acceptance Criteria，拆分 story 是为了减少每轮实现时的 Context）→ `onexai prd run --all-levels --tui --review-timing=at-end prd.md`（TUI 进度展示，全部 story 完成后全量规范 Review）。

### 2.5 工程 / 运维配套层

- `onexai compose`：管理本地 OneX 后端服务（依赖中间件的本地拉起）。
- 排障命令：`sysload`（性能排查时看系统负载）、`cleanup-zombies`（清理僵尸进程）。
- `onexai standard`：企业 IT 命名与分级标准查询与强制。
- **Makefile target 体系**：`build`（BINS/IMAGES/VERSION/V 选项）`image` `format`（gofmt）`add-copyright` `tidy` `clean` `protoc` `test`（`-race -cover -shuffle -short`）`cover`（覆盖率阈值门禁）`lint`（golangci）`deps`（装构建与代码生成工具）`generate`（go:generate）`help`。
- CI：GitHub Actions、`-race` 全量。
- 可观测三件套：`/healthz` 健康检查（如 jobserver `healthz-port: 8881`）、Prometheus `/metrics`（`metrics-addr: 5555`）、OTel（`--with-otel` 默认开）；MQServer 另开 pprof（`enable-pprof: true`）。
- 版本发布：semver 预发布自动生成规则（`onexai semv`）+ Conventional Commits。
- **异步任务/MQ 的工程纪律**（规范与生成代码共同承载）：状态流转（只查 `Pending` → 成功置 `Succeeded`）、幂等第一原则（业务唯一索引 / eventID 去重表 / 设置目标状态而非增量修改）、失败留痕（`retry_count` / `last_error` + 告警）、消费错误可观测（日志/指标/死信队列，不静默丢弃）、Kafka 主题命名规范（如 `mb.mqserver.post.events.v1`）与事件信封（protobuf oneof payload + protojson，一个主题承载多事件类型）。

### 2.6 流程方法论层（PRD → Story → 实现 → Review）

即 §2.4 的 PRD 工作流作为「透彻理解产品需求」的 AI 化：Prompt 转结构化 Story、逐 Story 实现、
按规范等级 Review、TUI 展示进度——这是把「代码审查 + 需求对齐」两个非 AI 时代手段搬进 harness 的落点。

输入/输出样例（逐字原文见 [onex-ai.md](onex-ai.md) §Prompt）：

- **输入是一个一句话 Prompt**（「请搭建一个工单管理系统：包含用户认证、工单和评论的 CRUD等操作、
  超时工单自动关闭的后台任务、管理CLI。**先查规范。**」）——`prd translate` 将其展开成 5 个 US 的结构化 PRD；
- prompt 里的「**先查规范**」是显式指令，对应课程 TUI 记录里模型动手前先调 convention 系工具
  （onexstack-reuse / rest-api / security / error-return / logging / directory / scaffold / errorsx）——
  印证 §2.1「渐进式喂规范」在真实执行序列中的形态：**通道就绪后，一句「先查规范」就能触发 AI 走数据而非凭记忆写码**；
- prd.md 的结构形态：`US-xxx` 编号 + `Status/Priority` 头部 + "As a … I want … so that …" 的
  Description + checkbox 版 Acceptance Criteria——将来 `bald prd` 的输出格式可直接以此为蓝本。

---

## 3. bald 现状对照

### 3.1 总表

| 依赖层 | onexai 依赖物 | bald 现状 | 缺口 |
|---|---|---|---|
| 规范数据 | 46 个 convention 工具 + 级别标注 + overview 入口 + MCP 化 | `docs/devel/zh-CN/` 约 32 篇设计判据/契约文档、`docs/guide/zh-CN/` 17 篇使用手册；`.golangci.yml` 仅 5 个正确性 linter；无 `.protolint.yaml`；无级别标注、无查询 CLI、未 MCP 化 | **大** |
| AI 会话引导 | `ide init`（.claude commands/skills）、AKSK + Remote MCP | 无 `.claude/`、无根目录 `AGENTS.md`/`CLAUDE.md`（父目录与 zke 有） | **大** |
| 复用优先规则 | `onexstack-reuse` MANDATORY | 无对应物——AI 极易绕过 appkit/berrors 自造轮子 | **大** |
| 基础包 | onexstack 全目录 | `pkg/`（appkit/store/authn/authz/audit/middleware/contextx/id/metricshttp 等）+ `contrib/` 七个桥接 + bconf/bconfig/berrors/log/broker/registry/cache/ratelimit/circuitbreaker/retry/metrics/health/oss/encoding/transport(17 子包)——**多数维度已超出 onexstack** | 小（个别缺件，见 §3.3） |
| 脚手架 | 20 个 create + PROJECT 账本 + onexstack.yaml | `bald gen proto/store/app/domain`（AppSpec 声明驱动，`--check` 零差异门禁）；`appspec.json` 单应用多 role（api/worker）；无多服务项目定义、无 K8s 控制器族 | 中 |
| 服务类型示例 | 五类服务全有端到端参考 | `_example/bald` 仅 api + worker 两 role；库内有 `transport/cron`/asynq/broker/`transport/mcp`/cobramcp 但无对应示例 | 中 |
| AI 集成底座 | MCP Server 能力 | **已具备**：`cobramcp/`（任意 Cobra CLI → MCP server，含 `mcp start/stream/tools` 与 vscode/cursor enable）+ `transport/mcp`（应用侧 MCP 传输）+ `ai/` 三个 LLM 框架适配（openai/langchaingo/eino）；但 bald CLI 自身未挂 cobramcp，文档未工具化 | 底座有、产品层缺 |
| PRD/Review 工作流 | prd translate/run、review 命令 | 无 | **大** |
| RAG/语义检索 | rag 命令 | 无（langchaingo 封装只是库，未做产品） | 中 |
| 本地环境/排障 | compose / sysload / cleanup-zombies | 无；最接近的是生成应用内置 `doctor`（装配诊断，非环境管理） | 中 |
| Makefile/CI 体系 | build/image/cover/lint/deps/protoc 全套 + CI | Taskfile 42+ target（build/test/race/lint/verify/vuln/proto-contract/example-e2e/acceptance/release-* 等）+ CI 七组作业（含 kind 集成）+ docscheck 机器闸门——**质量门禁强于 onexai 课上演示**；缺 image/cover/deploy 类 target | 小 |
| 部署物模板 | Dockerfile（distroless 分档）/ K8s manifests / systemd | 仓内无 Dockerfile/manifests/compose 模板；兄弟仓 zke 有 `deploy/chart + deploy/docker` 可参考 | 中 |
| 可观测 | healthz + /metrics + OTel + pprof | `health/`（readiness+checkers）、`metrics/`（prometheus/otel/datadog）+ `contrib/observability-otlp` + tracing 中间件；缺 pprof/expvar 端点集成 | 小 |
| 版本/分发 | semver 规则 + Conventional Commits + 一键分发 | `cli/internal/release`（plan/sync/changelog/tag，从 git 历史推 semver）+ CHANGELOG.md + publish workflow；缺 goreleaser / install 脚本 / 二进制分发 | 小 |

### 3.2 onexai 命令 ↔ bald 映射

| onexai | bald 对应 | 状态 |
|---|---|---|
| `create webserver` / `create api` | `gen app`（api role，http+grpc+gateway）/ `gen domain` | ✅ 有（AppSpec 声明驱动而非类型预设） |
| `create jobserver` / `create job` | `transport/cron`、asynq 能力 | ⚠️ 库有、示例与生成模板缺 |
| `create mqserver` / `create mq` | `broker/`（kafka/rabbitmq/redis/rocketmq 四实现） | ⚠️ 库有、示例缺 |
| `create mcpserver` / `create mcp` | `cobramcp/` + `transport/mcp` | ⚠️ 库有、示例缺 |
| `create clitool` / `create cmd` | cobra（cobramcp 注解体系） | ⚠️ 库有、模板缺 |
| `create console` / `create micro` | —（兄弟仓 desktop-kit / bald-admin 另行承担前端面） | ❌ bald 侧无 |
| `create declserver / crd / controller-manager / controller / operator / operator-api` | — | ❌ 完全缺 |
| `convention ×46` + overview | `docs/devel` 32 篇 + `docs/guide` 17 篇（未工具化、无级别） | ⚠️ 有内容、无通道 |
| `review`（分级 Review + 自动修复） | — | ❌ |
| `rag`（语义规范查找） | — | ❌ |
| `prd translate / prd run` | — | ❌ |
| `mcp`（CLI → MCP） | cobramcp（能力就绪，bald CLI 未接入自身） | ⚠️ 半有 |
| `ide init` | — | ❌ |
| `compose` / `sysload` / `cleanup-zombies` | `doctor`（仅装配诊断） | ⚠️ 部分 |
| `standard` | — | ❌ |
| onexstack 基础包 | `pkg/` + `contrib/` + 各能力模块 | ✅ 大幅超出 |
| PROJECT 账本 | `appspec.json`（单应用多 role） | ⚠️ 部分对应，无多服务模型 |

### 3.3 bald 已领先 / 已超出 onexstack 的部分

评审要双向看，以下维度 bald 不需要向 onexai 对齐，反而是资产：

- **装配工程化**：`pkg/appkit` 的段驱动 Provider 契约、装配计划（plan）、`doctor` 装配诊断、`Drift` + `Reconcile`（L4）——onexstack `pkg/app` 只是「入口框架」，无此层。
- **配置契约**：`bconf`（protobuf 单一真相源）+ `bconfig`（多源 provider）+ bootstrap 层模型装载器（apollo/consul/etcd/env/file/kubernetes/nacos/vault）。
- **质量门禁**：golangci + govulncheck + 全量 `-race`（CI 派生模块清单）+ kind 真集群集成测试 + 生成器 `--check` 零差异三道门 + `internal/docscheck` 文档/架构机器闸门——高于课程演示形态。
- **能力包广度**：broker 四实现、registry 五后端、transport 17 子包（含 mcp/graphql/sse/webrtc/http3）、encoding 12 种、circuitbreaker 四实现、ratelimit 三实现。
- **AI 适配底座**：`ai/`（openai/langchaingo/eino 三适配）+ `cobramcp`（onexai 的 `mcp` 子命令只桥接自己，cobramcp 可桥接任意 Cobra CLI，泛化性更强）。

个别缺件：无 `workerpool` 包（jobserver 模式需要）、ID 生成仅 `pkg/id/uuid.go`（无前缀化资源 ID / snowflake / ulid）、无 restry 式声明式 REST 客户端封装（`transport/client/` 是薄封装）。

---

## 4. 缺口清单与建议

> **状态总览（2026-11-10 回填）**——逐项落地进度：
>
> | 项 | 状态 | 落地物 |
> |---|---|---|
> | P0-1 规范工具化通道 | ✅ | 22 张蒸馏卡 + `bald convention` + cobramcp 挂载（`cli/v0.2.0`，§4.4′） |
> | P0-2 AI 会话引导物 | ✅ | 根 AGENTS.md + `bald ide init` 五工具（`cli/v0.2.0`/`v0.3.0`） |
> | P0-3 复用优先强制规则 | ✅ | reuse 卡 + AGENTS.md 最小强制集 |
> | P1-4 示例矩阵 | ✅ | cron/asynq worker 示例（`cli/v0.6.0`）+ mq（broker.redis 段 + mqdemo 域）+ mcp（api 角色 mcp 面）（2026-11-10，`bconf/v0.26.0`+`cli/v0.7.x`） |
> | P1-5 预设脚手架模板 | ✅ 大部分 | webserver/worker/fullstack/jobserver/mcpserver 五预设入库（`TestPresetsPassGates` 自动覆盖）；**mqserver 待生成器按 broker 段发 contract provider 接线**（否则预设应用 L3 段覆盖启动期报错——codegen 发射 vs std 收编需一轮裁决）、**clitool 待验证无 server 面角色的启动行为** |
> | P1-6 多服务项目模型 | ⏳ | 未做 |
> | P1-7 PRD/Story/review 工作流 | ⏳ | 未做（Phase 3，依赖 P0 数据层——已就绪） |
> | P1-8 RAG/语义检索 | ⏳ | 未做（远期兜底） |
> | P2-9…15 工程配套 | ⏳ | 未做（部署模板/compose/pprof/workerpool/分发/protolint/CI AI review） |
>
> §4.1–§4.3 原文保留作问题定义；§4.4′/§4.4″ 是 Phase 1/2 的验收记录。

### 4.1 P0：Harness 效果的直接前提（AI「看不见」bald 的约束）

1. **规范工具化通道**。bald 不缺规范内容（docs/devel 的判据、契约、装配等级文档质量很高），缺的是 AI 感知通道：
   - 给判据/契约类文档补「强制 / 建议 / 参考」级别标注（对齐 §2.1 的三级模型）；
   - 写一个 `bald convention` 查询 CLI（list / get / check），文档内容数据化；
   - 用现成的 **cobramcp** 把它挂成 MCP server——底座已就绪，缺的只是这一层薄封装。
2. **AI 会话引导物**：根目录补 `AGENTS.md`/`CLAUDE.md`（装配契约速览、gen 命令纪律、门禁清单）；
   提供 `bald ide init` 类命令生成 `.claude/commands/bald.md`、`skills/bald-conventions.md`。
3. **复用优先强制规则**：写一条 MANDATORY 规范——业务/适配代码落笔前必先检查 `pkg/`、`contrib/`、各能力模块是否已提供对应能力（对应 `onexstack-reuse`），进 AGENTS.md 与 convention 工具。

> 用户决策（2026-11-07）：缺口收敛为「**规范指引文档** + **CLI 完善**」两条主线——onexai 的模式正是
> **CLI 把规范数据暴露给 MCP，供 AI 查询**。§5、§6 把上述三项展开为可落地的设计：
> §5 = 数据层（规范蒸馏卡目录与级别模型），§6 = 通道层（`bald convention` CLI + cobramcp 挂载 + AI 会话引导物）。

### 4.2 P1：放大器（有库无「样子」，AI 学不到）

4. **示例矩阵**：`_example/` 补 job/cron、mq consumer、mcp server 三类最小端到端示例（库能力全在，缺的是参考实现与 e2e）。
5. **预设脚手架模板**：`gen app` 之上提供预置 AppSpec 模板集（webserver/jobserver/mqserver/mcpserver/clitool 对应的 spec 模板 + 交互式生成），复刻 create 命令的产品体验而无需新代码生成器。
6. **多服务项目模型**：`appspec` 升级支持多服务声明（对齐 onexstack.yaml + PROJECT 账本的双文件模型）。
7. **PRD/Story/review 工作流**：`bald prd translate/run` 与分级 review 命令（可后置，依赖 P0 的规范工具化）。
8. **RAG/语义检索**：规范文档多起来之后的前缀检索兜底（langchaingo 封装可复用）。

### 4.3 P2：工程配套

9. 部署物模板：生成树输出 `build/docker/<binary>/Dockerfile`（distroless 分档）与 `manifests/`（可参考兄弟仓 zke 的 deploy 结构）。
10. `compose` 式本地环境管理命令（依赖中间件一键拉起，CI 已有真中间件经验可复用）。
11. 生成应用 pprof 端点集成。
12. `workerpool` 包、前缀化资源 ID 生成（`post-` 风格）进 `pkg/`。
13. 分发：goreleaser / install 脚本（当前 `go install github.com/kalandramo/bald/cli/cmd/bald@latest` 已可用，但缺版本钉板与二进制渠道）——onexai 的 `ci` 规范也把 GoReleaser 列为标准件（见 §2.1）。
14. `.protolint.yaml` 进生成物与门禁。
15. CI 内接 AI code review——onexai `ci` 规范的显式组成项（见 §2.1）；bald CI 已有七个作业但无 AI 审查面，且 §6 的 convention 工具就绪后，`review` 编排命令可直接被 CI 调用。

### 4.4 最小闭环（如果要动手）

```
Phase 1（通道，设计见 §5 + §6）：蒸馏卡 20 张 + bald convention（overview/list/get/check）
                                  + cobramcp 挂载 + AGENTS.md + ide init        ✅ 已落地（§4.4′/§4.4″）
Phase 2（样子）：_example 补 job/mq/mcp 三示例 + AppSpec 预设模板集             ✅ 已落地（2026-11-10 收口）
Phase 3（流程）：bald review 分级编排（依赖 Phase 1 的数据层）                  ⏳ 未做
Phase 4（配套）：Docker/K8s 模板 + compose + 分发                               ⏳ 未做
```

Phase 1 成本最低而杠杆最大：bald 的文档质量本就是资产，只差「AI 感知数据的通道」——这恰好是课程第 01 章的核心命题。

---

## 5. 落地设计 A：规范指引文档体系（数据层）

### 5.1 从 onexai 提炼的机理：为什么「CLI → MCP」有效

onexai 把「规范喂给 AI」拆成三个解耦部件，缺一不可：

1. **数据**：46 份规范文档，规则级级别标注（强制 / 建议 / 参考）；
2. **通道**：`onexai convention <sub>` CLI——机器可查询、可进 CI、可被 shell 组合；
3. **协议**：MCP server 把 CLI 自动转工具，AI 侧零配置调用。

三个关键手法值得原样复刻：

- **overview 入口模式**：一个索引工具告诉 AI「有哪些规范、什么级别、怎么调」（课程原文：*Invoke the exact MCP tool name from the table*），而非全量喂入——Context 成本从 O(全量规范) 降到 O(索引 + 按需取用)。
- **级别语义**：AI 知道违反「强制」的代价 ≠ 违反「参考」，取舍有依据；
- **check 确定性检查器**：能机器判定的规则不靠 AI 自觉（`convention check` 跑 checker）。

onex 的元方法论是「**规范化 → 标准化 → 工具化**」三步（源文档《标准化开发体系构建》：规范化统一团队风格；
标准化形成**可编码**的开发规范/规则；工具化把一切标准化事务自动化——工具自动生成代码、文档、错误码）。
bald AI 化恰好逐层对应：`docs/devel` 判据文档＝已完成的规范化；本章蒸馏卡＝标准化；§6 convention CLI + MCP＝工具化。
所以 Phase 1 不是发明新东西，是把 bald 已有的第一层走完后两级。

### 5.2 bald 的起点不同：单一真相源问题与蒸馏卡方案

onexai 的规范是课程体系再加工出来的独立文档；bald 已有 32 篇 devel 设计文档 + `设计判据.md`（J1–J9）+
`框架契约总览.md`（64KB 的 API surface 目录——这就是 bald 的 `convention_onexstack`）。
**若再写一套平行规范文档，必然双源漂移。**

方案：**convention 条目 = 蒸馏卡（distilled card）**

- 每张卡 ≤ 60 行：front-matter（`id / title / level / category / sources / checks`）+ 规则清单
  （每条前缀 `[强制]` / `[建议]` / `[参考]`）+ 指向 devel 文档的引用；
- devel 文档仍是论证与细节的真相源，卡是机器消费的界面——喂 AI 一张 3KB 的卡，而不是一篇 64KB 的总览；
- **物理位置约束**：CLI 是独立 module（`github.com/kalandramo/bald/cli`，零 replace），不能 import 根仓包，
  `embed.FS` 只能嵌本 module 内的文件 → 卡放 `cli/internal/convention/cards/*.md`；
- **防漂移闸门**（扩 `internal/docscheck`）：每张卡的 `sources:` 链接必须解析到真实文档与锚点；
  `checks:` 声明的 checker 必须存在于 checker 清单（lint 规则 / docscheck 测试 / gen `--check`）——卡与母文档解耦漂移时 CI 红。

**卡 body 模板**（2026-11-08 自 onex 源规范 PDF 提炼——《Go标准化开发体系课》的规范原文即 convention 工具的内容母体，
两种原型齐备）：

- **rules 型**（对应 judgments / gates / codegen / logging 等卡，样例：《日志规范》9 页）：
  ①「遇到这些问题说明需要规范」的问题/症状清单（动机开篇）→ ②【强制】规则清单（每条尽量带代码示例）→
  ③提示栏：反模式清单（「无用日志常见情况」「日志过少的情况」这类 negative examples）与团队惯例差异说明 →
  ④【建议】规则（含格式模板，如 `Failed to <动词> + <一些事>`）→ ⑤决策表/决策树（如日志级别选择图）→
  ⑥环境/场景设置规范 → ⑦外部参考链接（K8s Logging 等）；
- **package 型**（对应 catalog / bconf / errors / store / broker 等卡，样例：《errorsx 错误包》9 页）：
  ①包定位 + 使用案例指针（miniblog）+ 关联规范交叉引用 → ②命名 rationale（如 `<冲突包原始名>x` 约定）→
  ③核心类型定义（godoc 注释齐全）→ ④方法逐一讲解 + 设计模式 rationale（链式调用、WithXXX 命名）→
  ⑤提示（边界语义，如 `Is` 不比较动态 Message）→ ⑥预定义常量/错误码（项目耦合 vs 通用的归属说明）→
  ⑦runnable 使用示例。

两个量化观察：

- **onex 规范源文档本身就短**（日志规范 9 页、errorsx 9 页、目录/ID 生成各 3 页）——规范正文按「可整体喂 AI」
  的长度写。这印证卡方案的尺寸假设；同时说明 bald 与 onex 的结构差异：onex 是单层（规范文档即工具内容），
  bald 的 devel 文档动辄 30–70KB，必须「蒸馏卡 + 母文档引用」双层——这是 bald AI 化设计里不可省略的一环；
- 日志规范的正文分级只有【强制】/【建议】两级加「提示」栏，与 overview 的三级模型并存——【参考】级在实际
  规范里用得少。bald 首批卡按实际密度标注即可，不必为凑三级硬造参考级条目。

**第 13 章（《如何使用 OneX AI 的工程规范（convention）》，2026-11-08 取得）敲定的三个设计决定**
（关键原文节选见 [onex-ai.md](onex-ai.md)）：

1. **规则 ID 进卡**：onexai 每条规则带 `<主题>-rNN` ID（如 `error-return-r01`、`logging-r06`），用途贯穿三处——
   评审意见引用 ID（比「错误处理不规范」具体得多）、CI 用 `--rules=` 只查指定规则、check 的 JSON finding 带
   `rule_id`。bald 卡的每条规则行升级为 `- [bconf-r01] [强制] …` 形态；docscheck 防漂移闸门相应加一条：
   卡内规则 ID 唯一且格式合法。
2. **项目级自定义卡目录**：onexai 支持 `--convention-dir`（默认 `.onexai/conventions`，YAML/Markdown，
   随仓库提交，让「团队约定」与「通用基线」同位可查）。bald 对应设计：内置基线卡住
   `cli/internal/convention/cards/`，同时支持项目级 `.bald/conventions/` 目录（默认读取、`--convention-dir`
   覆盖）——业务方不必 fork bald 就能接入自己的卡。
3. **好规则三问 = 写卡质量标准**：「在什么场景下、做什么、做到什么程度」；「代码要优雅」「命名要合理」
   这类无法判定的规则不进卡。此标准写进 §6.3 的 AGENTS.md，作为贡献者与 AI 写卡/提规范的共同约束。

### 5.3 首批蒸馏卡目录（22 张，全部映射既有资产）

| 类别 | 卡 id | 级别 | 内容来源（母文档） | 需要新写的部分 |
|---|---|---|---|---|
| 核心原则 | `judgments` | 强制 | 设计判据.md（J1–J9） | 仅重排版 |
| 核心原则 | `reuse` | 强制 | **无母文档** | 全新：落笔前查 `pkg/` + `contrib/` 能力地图，引用框架契约总览 |
| 核心原则 | `catalog` | 建议 | 框架契约总览.md | 蒸馏：模块 → 能力包速查表 |
| 装配与架构 | `assembly` | 强制 | Bald 声明式装配设计.md | 蒸馏：段驱动 Provider、biz 段、L1–L5 |
| 装配与架构 | `appspec` | 强制 | 应用框架设计 + 代码生成工具设计 | 蒸馏：AppSpec 声明纪律（哪些段必须显式） |
| 装配与架构 | `directory` | 强制 | 代码生成工具设计 | 蒸馏：生成树布局、业务域目录、填充点位置 |
| 装配与架构 | `bootstrap` | 建议 | Bald Bootstrap 设计 + 配置系统设计 | 蒸馏：层模型装载、J7/J9 的落点 |
| 契约与编码 | `bconf` | 强制 | Bald 配置契约设计.md | 蒸馏：proto 单一真相源、J8 契约不超承诺 |
| 契约与编码 | `errors` | 强制 | Bald 错误模型设计.md | 蒸馏：berrors 传输中立、grpcerr/httperr 边界 |
| 契约与编码 | `logging` | 建议 | Bald 日志设计.md | 蒸馏：契约层/bslog 两层、KV 结构化 |
| 契约与编码 | `context` | 建议 | 上下文契约设计.md（仅 3KB） | 几乎零蒸馏 |
| 契约与编码 | `store` | 建议 | Bald 存储设计.md | 蒸馏：泛型 store、where/paging 用法 |
| 契约与编码 | `broker` | 建议 | Bald 消息代理设计.md | 蒸馏 + 补幂等纪律（课程 §05/§06 的工程三件套：状态流转 / 去重 / 失败留痕） |
| 契约与编码 | `transport` | 建议 | Bald 传输层设计.md | 蒸馏：Server 契约、web 流水线强绑定 gin |
| 契约与编码 | `validation` | 参考 | 校验设计.md | 蒸馏 |
| 生成与门禁 | `codegen` | 强制 | 代码生成工具设计.md | 蒸馏：`--check` 零差异、**不手改生成树**、填充点规则 |
| 生成与门禁 | `gates` | 强制 | README 质量门禁节 + Taskfile.yml | 蒸馏：lint/verify/race/vuln/docscheck 的 task 入口与何时跑 |
| 生成与门禁 | `testing` | 建议 | 测试工具设计.md + CI 集成测试纪律 | 蒸馏：`-short` 语义、e2e/acceptance、真中间件用例 |
| 运维与发布 | `release` | 强制 | 多模块发布与变更日志.md | 蒸馏：plan→sync→changelog→tag 顺序 |
| 运维与发布 | `commit` | 强制 | **无母文档** | 全新：Conventional Commits（仓库已在实践，只差成文） |
| 运维与发布 | `observability` | 建议 | 健康检查设计 + 指标设计 | 蒸馏：healthz/metrics/OTel 埋点时机 |
| 运维与发布 | `doctor` | 建议 | Bald 装配计划与 doctor.md（guide 层） | 蒸馏：何时跑 doctor、判据输出怎么读 |

级别分布：强制 8 / 建议 12 / 参考 2。工作量估算：有母文档的 20 张是「压缩改写」，每张 30–60 分钟；
`reuse`、`commit` 两张需新写（各半天）。首版不必 22 张齐——`judgments` + `reuse` + `catalog` + `codegen` +
`gates` + `commit` 六张即可支撑最小闭环。

写 `reuse` 卡时补一条第 13 章来的硬规则：**不复制、不 vendor bald 库代码进业务项目，始终以 Go module
依赖引入**（`go get github.com/kalandramo/bald/pkg@version`）——复制省一时事，让升级与安全修复变得非常困难。

### 5.4 级别标注模型（对齐课程三级，bald 语义化）

| 级别 | bald 语义 | 典型条目 | checker |
|---|---|---|---|
| `[强制]` | 违反 = 门禁红或评审必打回；**豁免需正式评审批准**；**存量代码分期改进、新代码自设计期强制**（对齐 onexai overview 的 Mandatory 完整定义，原文见 [onex-ai.md](onex-ai.md)） | J2 重复税、J9 配置单通道、不手改生成树 | 有（golangci / docscheck / gen `--check`）；暂无 checker 的在卡里登记为「待补」 |
| `[建议]` | 默认遵守；偏离需在评审中给出正当理由；存量渐进采纳 | log KV 格式、store 泛型优先 | 无，靠 review |
| `[参考]` | 按项目情境采纳 | 性能基准 | 无 |

保留课程的一个细节：**级别是规则级而非文档级**——一张卡可同时含三种级别的条目，front-matter 的
`level` 记的是卡的「主体级别」，仅用于索引过滤。

---

## 6. 落地设计 B：bald CLI 通道层

### 6.1 `bald convention` 子命令设计

```
bald convention overview                     # 默认入口：全条目索引（id / 标题 / 级别 / 类别 / get 调用方式）
bald convention list [--level L] [--category C]
bald convention get <id> [--format md|json]  # 蒸馏卡全文
bald convention check [--id <id>]            # 跑卡声明的确定性 checker（薄封装 lint/docscheck/gen --check）
```

`check` 的行为契约照抄第 13 章踩好的坑：只跑「能被程序稳定判断」的规则；`--min-level` 默认 mandatory；
`--rules=bconf-r01,...` 精确到规则 ID；`--format json` 输出
`[{rule_id, level, file, line, message, suggestion}]` 并配 `outputJSONHint`；**默认发现问题时退出码仍为 0**
（让 MCP 客户端拿完整结果），CI/Taskfile 拦截必须显式 `--fail-on-findings`。check 通过 ≠ 符合规范——
它只是机械化的底线，代码评审不可替代（这句要进 AGENTS.md）。
```

- 注册进 `cli/cmd/bald/main.go`：`root.AddCommand(convention.NewCommand())`，与 `codegen` / `release` 并列；
- **卡 id 命名约定**：全部不含连字符（§5.3 的 22 个 id 已如此）——onexai 的 overview 需要专门写明
  「子命令内连字符原样保留、永不改写为下划线」这条规则（见 [onex-ai.md](onex-ai.md)），卡 id 无连字符
  即可从源头规避这类命名歧义；
- 数据：`embed.FS` 内嵌 `cli/internal/convention/cards/`；front-matter 手写解析（CLI module 零 replace，
  不引重型依赖，约 60 行）；
- **面向 AI 的输出原则**（对齐课程的「AI 友好」）：GFM 短表/短文，禁长篇 prose；`--format json` 配合
  cobramcp 的 `outputJSONHint` 注解 → 结果进 `structuredContent.data`，LLM 直接拿结构化数据组装后续调用，
  不用二次解析文本；
- `overview` 输出里显式写调用方式一行（`调 bald_convention_get {"id":"..."}`），对齐 onexai overview 的做法。

### 6.2 cobramcp 挂载：底座现成，一天工作量

```go
// cli/cmd/bald/main.go
root.AddCommand(cobramcp.Command(&cobramcp.Config{
    Selectors: []cobramcp.Selector{{
        CmdSelector: cobramcp.AllowCmdsContaining("convention", "gen"),
    }},
}))
```

要点与边界：

- 工具自动命名为 `bald_convention_overview / _get / _list` 等；查询命令统一标
  `cobramcp.AnnotationReadOnly: "true"` + `AnnotationTitle`；
- **`gen` 是否暴露要谨慎**：`gen app --spec` 写文件。首期建议只暴露 `convention`（AI 先能读规范），
  `gen` 的暴露放 Phase 2 并标 `AnnotationDestructiveHint`——对齐课程「渐进式喂给」；
- **`release` 面绝不暴露**（plan/tag 是打版本的外向动作）；
- Claude Desktop 不被 cobramcp 支持（README 明确移除了 `mcp claude` 子命令）→ Claude Code 走手动
  stdio 配置（`.mcp.json` 或 `.claude/settings.local.json` 写 `{"command":"bald","args":["mcp","start"]}`，
  这一步由 §6.3 的 `ide init` 代劳）；VSCode/Cursor 用现成的 `bald mcp vscode|cursor enable`；
- `bald mcp tools` 导出 JSON 进 CI（挂在 docscheck 旁）：MCP 工具面变更可校验，防漂移。

### 6.3 AI 会话引导物：AGENTS.md + SKILL + `bald ide init`

三层分工（对齐课程 SKILL vs MCP Tool 的职责切分）：

| 载体 | 放什么 | 何时被消费 |
|---|---|---|
| 根 `AGENTS.md` | 30 秒版：J1–J9 一行表、gen 纪律两句、门禁 task 清单、reuse-first 一句 MANDATORY、「详细规范调 `bald_convention_get`」 | 每轮会话自动进 context |
| `.claude/skills/bald-conventions/SKILL.md` | 判定说明：什么任务查哪张卡（写业务域 → codegen+appspec+directory；接中间件 → catalog+对应能力卡；改配置 → bconf+bootstrap） | 模型自行判定加载 |
| MCP tool | 规范数据本体 | 按需调用 |

`bald ide init --ide=claude|vscode|cursor`：幂等生成上述文件 + MCP 配置段。
首版只做 claude（用户主用）+ vscode/cursor（cobramcp 现成 enable），不做 opencode/crush。

另补一件小物：**根目录 `llms.txt`**——onexai 的 `documentation` 规范已把它列为文档标准件
（原文见 [onex-ai.md](onex-ai.md)），兄弟仓 desktop-kit 也已有 `llms.txt` 先例；内容即 bald 的
「站点级索引」：仓库是什么、三层文档入口、convention 卡清单、CLI 命令面。一并进 `ide init` 的幂等生成范围。

开发流程挂接（对齐第 13 章的 6 步节奏，bald 版）：`gen app` 搭骨架 → 生成树构建（`go mod tidy && go run`）→
写业务前查规范（AGENTS.md 的「先查规范」+ SKILL 判定）→ 提交前 `bald convention check` + `task verify` →
CI 用 `--fail-on-findings` 拦强制规则（挂进现有七组作业）→ commit 按 `commit` 卡写。
Taskfile 统一入口照抄 onexai 的 `convention:` target 形态：`bald convention check --min-level=recommended
--fail-on-findings ./...`。

### 6.4 review / rag：后置，记录依赖关系

- `bald review --level=mandatory`：本质 = `convention check`（确定性部分）+ 把对应级别卡片喂给模型做
  人工式审查的编排命令。**依赖 §5 数据层与 §6.1 就绪**，放 Phase 3；
- **rag 首版不做**：22 张卡 × ≤60 行，overview + get 已经够用；卡超过 ~50 张再引入语义检索
  （`ai/langchaingo` 封装可复用）。对齐课程的原则：数据量没到之前，先解决通道。

### 6.5 验收判据（Phase 1 的 done 定义）

1. `bald mcp tools` 的 JSON 输出里出现 3 个 `readOnlyHint` 的 convention 工具；
2. Claude Code 新会话问「在 bald 里新增一个业务域要遵守什么」，模型先调 `bald_convention_overview`
   再 `get codegen/appspec`，答案含级别语义（哪些是强制）；
3. 人为手改生成树一处 → `bald convention check` 红；
4. docscheck 新闸门通过：全部卡的 `sources:` 链接可解析、`checks:` 声明与真实 checker 清单一致；
5. `bald ide init` 在干净目录幂等重跑零差异；
6. 规则 ID 可引用：故意违反一条卡规则后，`bald convention check --format json` 的 finding 带该规则 ID，
   且在 AGENTS.md 约束下模型写评审意见时引用同一 ID（如「违反 bconf-r01」），而非「配置不规范」。

### 4.4′ Phase 1 验收记录（2026-11-08 实施，已发 `cli/v0.2.0`）

六条判据的实测结果（本机，`cli` @ 本轮实现）：

| # | 判据 | 结果 |
|---|---|---|
| 1 | MCP 工具面 | ✅ `bald mcp tools` 导出 4 个工具（overview/list/get/check），全部 `readOnlyHint: true`；selector 挡住 gen/release |
| 2 | AI 会话行为 | ✅ 引导物就位（AGENTS.md「先查规范」+ `/bald` 命令 + SKILL 判定表 + `.mcp.json` stdio 配置）；实际模型行为待真实会话复核 |
| 3 | 手改生成树 → check 红 | ✅ 改 `_example/bald` 的 owned 文件 → `codegen-r03` finding → `--fail-on-findings` exit 1；还原后零差异 |
| 4 | docscheck 新闸门 | ✅ `TestConventionCards`（front-matter/sources 可解析/规则 ID 唯一合法/checker 声明⊆注册表且绑定规则存在）；`TestModulePackageSets` 登记新包 |
| 5 | ide init 幂等 | ✅ 干净目录首跑 generated/updated，二跑 kept/ok 零差异；`.mcp.json` 合并保留既有键 |
| 6 | 规则 ID 双向引用 | ✅ check 的 JSON finding 带 `rule_id`；text 输出 `file:line: [codegen-r03] …` + suggestion |

落地范围：`cli/internal/convention`（包 + 六张卡 45 条规则 + 四子命令 + gen-app-drift checker）、
`cli/internal/ide`（init + 三模板）、`cli/cmd/bald`（挂载 + cobramcp selector）、`cli/internal/codegen.CheckAppSpec`
（零差异门的程序化入口，语义单份）、`internal/docscheck`（卡闸门 + 包集登记）、`Taskfile`（convention target）、
根 `AGENTS.md` + `llms.txt`。门禁：gofmt/golangci（cli 0 issues）/race（新包 + docscheck）/docscheck 全量/
`task convention` 全绿。

### 4.4″ Phase 2 增量（2026-11-08 实施，同日发 `cli/v0.3.0`）

- **AI 工具面换成用户实际在用的四件套**：`ide init --ide` 扩到 claude|zcode|trae|qoder|workbuddy。
  zcode 走权威事实（`.zcode/config.json` 的 `mcp.servers`，严格 schema，条目只含 type/command/args；
  项目 SKILL 进 `.zcode/skills/`）；trae/qoder 各落规则文件；workbuddy 私有格式未核实、只落公共面不猜路径。
- **项目级自定义卡**：`.bald/conventions/`（LoadAll 合并，卡 id 冲突即报错——补充不是覆盖）。
- **卡库扩到 16 张 / 97 条规则**：新增 assembly/appspec/directory/bconf/errors/logging/context/testing/release/doctor
  （contract 类别入列），每条带母文档锚点；规则提炼自《Go标准化开发体系课》同款方法论（问题清单→【强制】→反模式）。
- **CI 接闸**：test 作业加 `convention check --fail-on-findings` 步（`task ci-workflow-lint` 过，actionlint
  v1.7.12 经 goproxy.cn 装到 pin 版）。

---

## 附：证据索引

- bald 侧结论来自 2026-11-07 对仓库的实盘清点（cli/internal/codegen、pkg/appkit、cobramcp、ai/、Taskfile.yml、.github/workflows、docs 三层、_example/bald），关键路径均在上文出现；架构与质量门禁的细节见 [risk-review.md](risk-review.md) 与 [current-architecture.md](current-architecture.md)。
- §5–§6 设计所依赖的具体事实，均经代码回读核实：
  - `cobramcp/annotations.go` 提供 `AnnotationReadOnly / AnnotationDestructive / AnnotationIdempotent / AnnotationOutputJSON`（后者把 JSON stdout 直出 `structuredContent.data`）；`cobramcp/README.md` 明确移除了 `mcp claude` 子命令（Claude Desktop 不支持），提供 `mcp start/stream/tools` 与 `vscode|cursor enable|disable|list`、`Config.Selectors`（`AllowCmdsContaining` / flag 过滤 / middleware）、`Config.CommandName` 防撞名；
  - `cli/cmd/bald/main.go`：根命令只挂 `codegen.NewCommand()` 与 `release.NewCommand()`，加 `convention` 即第三节；CLI 是独立 module（零 replace，README §仓库结构 与 risk-review §3.4），决定蒸馏卡只能 embed 在 `cli/` 内；
  - `docs/devel/zh-CN/设计判据.md`（J1–J9 判据 + 判据总表）与 `docs/devel/zh-CN/框架契约总览.md`（64KB，按模块分节的 API surface）即 §5.3 中 `judgments` 与 `catalog` 两张卡的母文档；`上下文契约设计.md` 仅 3KB，近乎零蒸馏；
  - 仓库提交历史已实践 Conventional Commits（`feat(bconf,pkg/appkit): …` 等），`commit` 卡只是成文。
- onexai 侧结论来自课程 PDF 第 01–06 章原文（含 46 规范清单、20 create 命令清单、onexstack.yaml 示例、JobServer/MQServer 生成物目录树、PRD 工作流）；其中被 PDF 文本解析截断、经人工补抄的逐字原文存档于 [onex-ai.md](onex-ai.md)（46 规范描述、overview 完整输出、20 命令清单、prompt.md/prd.md 输入样例全文），§2.1 / §2.6 / §5.4 / §6.2 / §6.3 的细节均以其为准。口径说明：课程中的 miniblog/onex 一致性评分（96/100 等）是作者让 AI 对代码仓库做的评测，非 onexai 工具输出，本文 §2.2 仅作参考实现引用、不作为工具能力。
- 局限：兄弟仓仅浅查（desktop-kit 有 `llms.txt` + `docs/ai-usage.md`，zke 有 deploy 目录可参考）；onexai 内部实现（如 review 的具体修复策略、rag 的检索实现）课程未展开，按公开行为描述；§5.3 的级别分布与工作量是估算，落地时以首版六张卡实测校正。
- §5.2 的卡 body 模板提炼自《Go标准化开发体系课》的规范源文档 PDF（2026-11-08 用户提供于 `~/Downloads/`，共 22 篇——日志规范、errorsx 错误包、标准化开发体系构建、软件架构规范、Go 编码规范、代码提交规范、目录规范、ID 生成规范、REST API 规范、Code Review 规范等，即 convention 工具的内容母体，文件名与 convention 一一对应；不进仓库）。已精读：标准化开发体系构建（4 页，三步方法论 + 课程体系全貌）、日志规范（9 页，rules 型原型）、errorsx 错误包（9 页，package 型原型）；其余按写卡需要单篇取用。源文档短小（3–9 页）的事实本身已作为设计证据写入 §5.2。实战课第 13 章《如何使用 OneX AI 的工程规范（convention）》（11 页）同日取得并精读——规则 ID、check 行为契约、自定义规范目录、6 步流程、高频踩坑点已吸收进 §5.2 / §6.1 / §6.3 / §6.5，关键原文节选存档于 [onex-ai.md](onex-ai.md)。
