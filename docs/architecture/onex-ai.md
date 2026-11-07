# onexai 工具输出存档（逐字源材料）

> 来源：《OneX Harness AI Coding 实战课》第 01 章 PDF 中被文本解析截断、经人工补抄的原文。
> 内容：① 46 个 convention 工具的全量描述（§规范工具化）；② `onexai convention overview` 的完整输出
>（MCP 命名规则、三级规范定义、按类别索引）；③ 20 个 create 脚手架命令清单；④ PRD 工作流输入样例
>（prompt.md 全文 + translate 生成的 prd.md 全文，§Prompt）；⑤ 第 13 章《如何使用 OneX AI 的工程规范
>（convention）》关键原文节选（规则 ID、check 契约、自定义规范目录，§第 13 章 convention 实战）。
> 用途：[onexai-gap-review.md](onexai-gap-review.md) 的对照证据；此处保持逐字原样，不做编辑。
> 口径：课程中的 miniblog/onex 一致性评分对比是作者让 AI 对代码仓库做的评测，不是 onexai 工具输出，故不在此存档。

## 规范工具化

  overview                Get the complete index of Go development conventions grouped by category
  architecture            Get Clean Architecture conventions: layered design, dependency rules, testability, and the miniblog reference implementation
  benchmark               Get benchmarking conventions: writing benchmarks with b.Loop, running with benchstat, profiling from benchmarks, documenting results in commits, CI regression detection
  ci                      Get CI/CD conventions: GitHub Actions pipelines, testing with -race, lint, SAST (govulncheck/CodeQL), coverage, Dependabot/Renovate, GoReleaser, AI code review
  cli                     Get CLI development conventions: command structure, flag handling, configuration layering, version embedding, exit codes, I/O patterns, signal handling, shell completion
  cobra                   Get cobra conventions: command tree, RunE hook chain, Args validators, persistent vs local flags, command groups, completions, doc generation, testing with SetArgs/SetOut
  coding                  Get the full Go coding standard: formatting, naming, imports, functions, packages, testing, and best practices
  commit                  Get commit conventions: Conventional Commits format, branching strategy, and merge request guidelines
  concurrency             Get Go concurrency conventions: goroutine ownership, channel patterns, and race avoidance
  context                 Get Go context conventions: cancellation, deadlines, and value propagation
  data-structures         Get data structures conventions: slice/map preallocation, container/heap/list/ring, strings.Builder vs bytes.Buffer, generic collections, pointer types, copy semantics
  database                Get database access conventions: transactions, connection management, and migration strategies
  dependency-injection    Get dependency injection conventions: manual constructor injection vs wire/dig/fx, constructor injection, composition root, DI library decision table
  dependency-management   Get dependency management conventions: go.mod, version selection, and vulnerability scanning
  design-patterns         Get design patterns conventions: functional options, constructors, error flow, resource management, graceful shutdown, resilience, data handling, streaming
  dev-system              Get the standardised development system design: the three-step methodology, convention catalog, and implementation strategy
  directory               Get directory structure conventions: cmd/, internal/, pkg/ layout, naming, and file organisation rules
  documentation           Get documentation conventions: godoc comments, README structure, CONTRIBUTING, CHANGELOG (Keep a Changelog), Example tests, API docs, llms.txt
  error-return            Get error return conventions: HTTP response patterns, semantic error codes, and error message security rules
  errorsx                 Get the ErrorX error package design: struct definition, API, error wrapping, and integration patterns
  grpc                    Get gRPC conventions: proto organization, server/client implementation, interceptors, status code errors, TLS/mTLS, bufconn testing, streaming RPCs
  id-generation           Get ID generation conventions: resource ID format, reference ID patterns, and generation rules
  klog                    Get klog logging conventions for Kubernetes-ecosystem components: klog.InfoS/ErrorS, log levels, structured KObj/KRef usage
  lint                    Get Go linting conventions: linter setup, static analysis, and CI integration
  logging                 Get mandatory logging rules: English-only, structured KV format, log levels, past tense, no sensitive data
  misc                    Get miscellaneous coding conventions: pointer handling with ptr package, avoiding unnecessary nil/empty checks
  modernize               Get Go modernization conventions: deprecated package migrations, language feature adoption, standard library upgrades, migration priority guide, modernize linter
  observability           Get observability conventions: metrics, distributed tracing, and the three-pillar model
  onexstack               Get the complete onexstack package catalog: all reusable packages with APIs, signatures, and usage patterns
  onexstack-reuse         MANDATORY RULE: Always check and reuse onexstack/onexstack packages BEFORE writing any new code
  performance             Get performance optimization conventions: profile before optimizing, allocation reduction, decision tree (if X bottleneck apply Y), GC tuning, hot-path optimization
  prd                     Get the PRD methodology: format, workflow, and best practices for AI-driven development
  refactoring             Get refactoring conventions: coverage-adaptive safety net, tool-driven behavior-preserving transforms (gopls Rename/Inline/Extract), atomic commits, breaking import cycles, type alias migration
  rest-api                Get REST API design conventions: URI naming, HTTP methods, versioning, error responses, and pagination
  safety                  Get Go safety conventions: nil traps, panic prevention, and defensive coding
  scaffold                Mandatory post-creation build steps after onexai create commands
  security                Get Go security conventions: input validation, SQL/command injection prevention, and secure defaults
  semver                  Get the pre-release version auto-generation rules used by onexai semver tag
  slog                    Get log/slog conventions: context-aware logging, flat key-value pairs, snake_case keys, sensitive data protection
  structs-interfaces      Get struct & interface design conventions: small interfaces, consumer-side definition, and abstraction
  testify                 Get testify conventions: assert vs require, mock expectations and matchers, suite lifecycle, advanced assertions like Eventually and JSONEq
  testing                 Get Go testing conventions: table-driven tests, coverage, and test naming
  troubleshooting         Get troubleshooting conventions: root cause methodology, debugging golden rules, common Go bugs, race detection, pprof setup, GODEBUG tracing, production debugging
  version                 Get semantic versioning conventions: MAJOR.MINOR.PATCH format, version bump rules, and onexai semver integration
  viper                   Get viper conventions: layered precedence (flag > env > file > KV > default), BindPFlag, env binding with key replacer, Unmarshal with mapstructure tags, hot reload, test isolation
  web-frontend            Get the web frontend standards: React 19 + Rsbuild + Wujie micro-frontend, monorepo layout, naming, components, stores, routing, i18n, styling, and commit rules

## 通过 onexai convention overview 告知 AI 开发规范



```markdown
 Every convention below is available as an MCP tool. MCP tool name is `convention_<subcommand>`: the underscore joins `convention` to the subcommand, and hyphens inside the subcommand are preserved (never rewritten as `_`). For example Error Return → `convention_error-return`, Dependency Injection → `convention_dependency-injection`.
> Invoke the exact MCP tool name from the table (or the equivalent CLI form `onexai convention <subcommand>`).

## Convention Levels (规范级别)

Each convention document marks its rules with one or more levels. Levels are
NOT a command-level classification — a single convention may contain 强制,
建议, and 参考 rules together.

| Level | Label | Description |
|---|---|---|
| **Mandatory** | 强制 | Must be followed in all projects. Only affects product function. Exceptions require formal review approval. Existing projects: phased improvement. New projects: enforced from design stage. |
| **Recommended** | 建议 | Follow unless there is a justified reason not to. Existing projects: adopt incrementally. New projects: default follow, exceptions require review. |
| **Reference** | 参考 | Guidance only, not hard requirements. Adopt flexibly per project context. |

## Conventions by Category (按类别)

### Core Principles

| MCP tool | Convention | CLI command |
|---|---|---|
| `convention_onexstack` | OneXStack Package Catalog | `onexai convention onexstack` |
| `convention_onexstack-reuse` | OneXStack Reuse-First Principle | `onexai convention onexstack-reuse` |

### Coding Standards

| MCP tool | Convention | CLI command |
|---|---|---|
| `convention_benchmark` | Benchmarking Conventions | `onexai convention benchmark` |
| `convention_cli` | CLI Development Conventions | `onexai convention cli` |
| `convention_cobra` | Cobra Conventions | `onexai convention cobra` |
| `convention_coding` | Go Coding Standard | `onexai convention coding` |
| `convention_concurrency` | Concurrency Conventions | `onexai convention concurrency` |
| `convention_context` | Context Usage Conventions | `onexai convention context` |
| `convention_data-structures` | Data Structures Conventions | `onexai convention data-structures` |
| `convention_database` | Database Access Conventions | `onexai convention database` |
| `convention_dependency-management` | Dependency Management Conventions | `onexai convention dependency-management` |
| `convention_design-patterns` | Design Patterns Conventions | `onexai convention design-patterns` |
| `convention_documentation` | Documentation Conventions | `onexai convention documentation` |
| `convention_id-generation` | ID Generation Conventions | `onexai convention id-generation` |
| `convention_klog` | KLog Conventions | `onexai convention klog` |
| `convention_lint` | Linting Conventions | `onexai convention lint` |
| `convention_logging` | Logging Conventions | `onexai convention logging` |
| `convention_modernize` | Modernize Conventions | `onexai convention modernize` |
| `convention_observability` | Observability: Metrics & Tracing | `onexai convention observability` |
| `convention_performance` | Performance Optimization Conventions | `onexai convention performance` |
| `convention_refactoring` | Refactoring Conventions | `onexai convention refactoring` |
| `convention_safety` | Safety & Defensive Coding | `onexai convention safety` |
| `convention_security` | Security Conventions | `onexai convention security` |
| `convention_slog` | Slog Conventions | `onexai convention slog` |
| `convention_structs-interfaces` | Structs & Interfaces Design | `onexai convention structs-interfaces` |
| `convention_testify` | Testify Conventions | `onexai convention testify` |
| `convention_testing` | Testing Conventions | `onexai convention testing` |
| `convention_troubleshooting` | Troubleshooting Conventions | `onexai convention troubleshooting` |
| `convention_viper` | Viper Conventions | `onexai convention viper` |

### API & Error Handling

| MCP tool | Convention | CLI command |
|---|---|---|
| `convention_error-return` | Error Return Conventions | `onexai convention error-return` |
| `convention_errorsx` | ErrorX Package Design | `onexai convention errorsx` |
| `convention_grpc` | gRPC Conventions | `onexai convention grpc` |
| `convention_rest-api` | REST API Conventions | `onexai convention rest-api` |

### Architecture & Design

| MCP tool | Convention | CLI command |
|---|---|---|
| `convention_architecture` | Architecture Conventions | `onexai convention architecture` |
| `convention_dependency-injection` | Dependency Injection Conventions | `onexai convention dependency-injection` |
| `convention_directory` | Directory Structure Conventions | `onexai convention directory` |

### Operations

| MCP tool | Convention | CLI command |
|---|---|---|
| `convention_ci` | CI/CD Conventions | `onexai convention ci` |
| `convention_commit` | Commit Conventions | `onexai convention commit` |
| `convention_semver` | Semver Pre-release Rules | `onexai convention semver` |
| `convention_version` | Versioning Conventions | `onexai convention version` |

### Platform & Development

| MCP tool | Convention | CLI command |
|---|---|---|
| `convention_dev-system` | Development System Design | `onexai convention dev-system` |
| `convention_misc` | Miscellaneous Conventions | `onexai convention misc` |

### AI & Methodology

| MCP tool | Convention | CLI command |
|---|---|---|
| `convention_prd` | PRD Methodology | `onexai convention prd` |

### Scaffold & Tooling

| MCP tool | Convention | CLI command |
|---|---|---|
| `convention_scaffold` | Scaffold Post-Creation Steps | `onexai convention scaffold` |

### Web Frontend

| MCP tool | Convention | CLI command |
|---|---|---|
| `convention_web-frontend` | Web Frontend Development Standards | `onexai convention web-frontend` |

```

## 通过脚手架生成框架代码
可用命令：
  quickstart           创建仅供学习的演示/示例项目（切勿用于实际生产应用）
  project              从 YAML 配置文件创建新项目（需要 onexstack.yaml）
  webserver            创建 REST API / Web 服务项目（Web 应用和 API 的主要命令）
  api                  创建 API 资源
  jobserver            创建异步任务 / Cron 定时任务消费服务项目
  job                  为现有 JobServer 添加特定资源类型的异步任务处理程序
  mqserver             创建消息队列（Kafka）消费者项目
  mq                   创建 MQ 消费者
  mcpserver            创建 MCP (Model Context Protocol) 工具服务项目
  mcp                  创建新的 MCP 工具
  clitool              创建命令行工具（CLI）项目
  cmd                  创建新的 CLI 命令
  console              创建现代化 Web 前端项目
  micro                为现有 Web 控制台项目创建微前端子应用
  declserver           创建声明式（kube-apiserver 风格）API 服务项目
  crd                  向 declserver 添加 REST 资源（CRD）
  controller-manager   创建控制器聚合（kube-controller-manager 风格）项目
  controller           向 controller-manager 添加监听 CRD 资源的控制器
  operator             创建独立的 controller-runtime operator 项目
  operator-api         向 Operator 添加 CRD 及其配套控制器


## Prompt

请搭建一个工单管理系统：包含用户认证、工单和评论的 CRUD等操作、超时工单自动关闭的后台任务、管理CLI。先查规范。

prd.md

```
# PRD: 工单管理系统

## Introduction
一个工单（Ticket）管理系统，用于帮助团队跟踪、处理和闭环客服/运维工单。系统提供用户认证、工单与评论的完整 CRUD 操作、超时工单自动关闭的后台任务，以及用于日常运维与管理的命令行工具（CLI）。

### US-001: 用户认证
**Status:** todo
**Priority:** 1
**Description:** As a 系统用户, I want 通过账号完成注册、登录/登出并获取鉴权凭证 so that 只有经过认证的用户才能访问受保护的资源。

**Acceptance Criteria:**
- [ ] 支持用户注册、登录与登出
- [ ] 登录成功后返回鉴权凭据（如 JWT）
- [ ] 受保护接口校验凭据，未认证或凭据无效时返回 401
- [ ] 密码以哈希形式存储，不以明文保存

### US-002: 工单 CRUD
**Status:** todo
**Priority:** 1
**Description:** As a 已认证用户, I want 创建、查看、更新和删除工单 so that 能够完整地跟踪与维护工单生命周期。

**Acceptance Criteria:**
- [ ] 支持创建工单（含标题、描述、状态、优先级等字段）
- [ ] 支持查看单个工单详情与分页列表
- [ ] 仅工单创建者或管理员可更新/删除工单
- [ ] 工单状态流转（如 open → in_progress → closed）合法

### US-003: 评论 CRUD
**Status:** todo
**Priority:** 2
**Description:** As a 已认证用户, I want 在工单下创建、查看、更新和删除评论 so that 能够围绕工单进行协作与沟通。

**Acceptance Criteria:**
- [ ] 支持在指定工单下创建评论
- [ ] 支持查看某个工单下的全部评论
- [ ] 仅评论作者或管理员可更新/删除评论
- [ ] 评论必须关联到一个已存在的工单

### US-004: 超时工单自动关闭后台任务
**Status:** todo
**Priority:** 3
**Description:** As a 系统运维人员, I want 一个后台定时任务自动关闭超时未更新的工单 so that 过期工单无需人工处理即可被自动闭环。

**Acceptance Criteria:**
- [ ] 后台任务按固定时间间隔运行
- [ ] 对超过设定超时阈值且未关闭的工单自动标记为已关闭
- [ ] 自动关闭操作留有审计记录或日志
- [ ] 超时阈值与运行周期可配置

### US-005: 管理 CLI
**Status:** todo
**Priority:** 4
**Description:** As a 系统管理员, I want 一个命令行工具用于管理工单系统 so that 无需登录 Web/API 即可完成运维与管理操作。

**Acceptance Criteria:**
- [ ] CLI 支持工单的增删改查命令
- [ ] CLI 支持用户管理命令（如创建/禁用用户）
- [ ] CLI 支持查看系统状态或健康检查
- [ ] CLI 输出格式清晰（如表格/JSON），并提供帮助信息
```

## 第 13 章 convention 实战（关键原文节选）

> 来源：《13 _ 如何使用 OneX AI 的工程规范（convention）？.pdf》（11 页，2026-11-08 取得并补抄关键段落）。

### 定义与三入口

一句话：**OneX 工程规范的可查询、可检查版本**。三部分组成：①规范文档（几十份，覆盖架构、目录、编码、
错误、日志、测试、API、提交、CI）；②规则级别（强制 / 建议 / 参考）；③工具入口（CLI 给人看、MCP 给 AI 用、
check 确定性检查）。

| 入口 | 用法 | 适合场景 |
|---|---|---|
| CLI | `onexai convention <主题>` | 人查阅规范、写方案、做评审 |
| MCP | `convention_<主题>` 工具 | AI 在写代码前自动查规范 |
| Check | `onexai convention check` | 生成后自检、CI 拦截 |

注意，convention 不是一个代码生成命令。它解决的是：**当代码由人、AI 或两者共同编写时，如何让所有人遵守同一套工程约定**。

### 规则 ID

```text
$ onexai convention error-return
# Error Return Conventions (`error-return`)

## Mandatory

- [error-return-r01] HTTP status code: Use actual status codes (200, 400, 404, …
- [error-return-r02] Error type: All errors must be ErrorX from onexstack/onexst…
```

规则 ID 不是装饰。后面做代码评审、写提交信息、在 CI 里只检查某几条规则时，都可以直接引用它。
例如评审意见可以写「这里违反了 error-return-r01，错误场景不能返回 200」，比「错误处理不规范」具体得多。

### check 行为契约

规范文档解决「怎么做」，check 解决「有没有做到」。它只检查「能够被程序稳定判断」的规则
（是否用标准库 log、是否在库代码里 panic、错误包装是否规范、是否 gofmt）。
**check 通过不等于代码一定符合规范，它只是最后一道机械化的底线。**

参数：`--format=json`（接入其他工具）；`--min-level=mandatory|recommended|reference`（默认只检强制）；
`--rules=...`（只检查指定规则，逗号分隔，存量项目迁移有用）；`--fail-on-findings`（发现问题时非零退出）。

易踩的坑：**默认情况下，即使发现问题，check 的退出码也是 0**——为了让 MCP 客户端能拿到完整结果；
CI 里拦截必须显式加 `--fail-on-findings`。

```json
[
  {
    "rule_id": "logging-r06",
    "level": "mandatory",
    "file": "main.go",
    "line": 6,
    "message": "stdlib log is used",
    "suggestion": "use the project logger (slog/klog)"
  }
]
```

### 自定义团队规范

`onexai convention` 支持通过 `--convention-dir` 指定目录，默认 `.onexai/conventions`，目录里可以放
YAML 或 Markdown 文件。建议把 `.onexai/conventions` 和代码一起提交到仓库——新人拉下代码就能看到
团队约定，AI 也能在同一个位置查到它们。

自定义规范要尽量「小而具体」。「代码要优雅」「命名要合理」这种无法判断的规则，写进去也没有用。
**好的规则应该能回答三个问题：在什么场景下、做什么、做到什么程度。**
团队约定示例：handler 里不允许直接使用 `time.Now()`，必须注入时钟；所有对外接口必须带幂等键；
新表必须有 `created_at`、`updated_at` 和软删除字段。

### 开发流程节奏（6 步）

1. 生成前：按服务形态选 create 命令搭骨架；2. 生成后：按 scaffold 规范完成必做步骤，不要急着写业务代码；
3. 写业务前：让 AI 先查对应规范；4. 提交前：`convention check` + 单元测试 + lint；
5. CI 中：`--fail-on-findings` 拦截强制规则，同时 `go test -race`；6. 提交时：按 commit 规范写提交信息。

Makefile 统一入口：

```makefile
.PHONY: convention
convention:
	onexai convention check --min-level=recommended --fail-on-findings ./...
```

### 高频踩坑点（最小强制集 + 正反例的表达形态）

- **分层**：依赖只能从上到下 handler → biz → store；最常见问题是 handler 绕过 biz 直接访问 store。
- **错误**：错误码在源头确定，上层用 `fmt.Errorf("...: %w", err)` 补上下文、不重新定义错误码；库代码禁 panic；
  HTTP 用真实状态码、错误信息不含敏感数据；响应体用扁平对象（`{"code": "NotFound.UserNotFound", ...}`）。
- **日志**：只使用扁平 key-value（不用 `slog.String()` 这类结构化 helper）；错误 key 固定 `"error"`；
  所有 key 用 snake_case；敏感信息先 `secretutil.TruncateSecret()` 截断；业务代码禁标准库
  `log.Print/log.Fatal`，K8s 生态组件用 klog。
- **复用**（onexstack-reuse 核心）：错误用 errorsx、日志适配 slog/klog、中间件/数据库连接/Token 用
  onexstack 对应包；**不要把 onexstack 的代码复制或 vendor 到项目里，始终以 Go module 依赖引入**。
- **提交**：`<type>(<scope>): <subject>`，冒号后必须有空格；scope 是名词、不切太细；subject 小写动词开头、
  现在时祈使语气、结尾不加句号；正文说明「为什么改」和「改前后差异」；不相关的改动不混提交。

### 小结（四步用法）

1. 查总览（`overview`，先知道有哪些规范）；2. 查主题（写代码前用 CLI 或 MCP 查对应规范）；
3. 做检查（写完后 `convention check`）；4. 进流程（CI 加 `--fail-on-findings`，让规范成为提交门槛）。

两点特别注意：①规范分三级，不要用一种力度执行所有规则；②check 只覆盖可确定性判断的规则，
它通过不代表代码一定符合规范，**代码评审仍然不可替代**。