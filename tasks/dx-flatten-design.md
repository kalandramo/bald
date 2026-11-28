# dx-flatten:单模块扁平化与接缝瘦身 — 设计文档

> 需求原文(2026-11-28):"bald 的 tag 太多了,因为是我个人使用的,对于日常使用来说很复杂。因为框架的契约必须显式 import,导致很多接缝代码,很丑陋,多了很多逻辑。"
> 状态:设计+切片,未开工实现。基线 `main` @ `058f4756`。

## 术语表(Glossary)

| 英文 | 中文 | 本文含义 |
|---|---|---|
| module | 模块(Go module) | 独立版本线;本仓现有 70 个 `go.mod`(含 `_example`) |
| release unit | 发布单元 | 有独立 tag 系列的 module;现 69 个,`scripts/list-modules.sh` 派生 |
| flatten | 扁平化 | 把适配器/契约合并为单一框架 module,统一版本线 |
| wiring | 接线代码 | import 契约包并向 `MustServe` 传 Provider 的代码 |
| seam | 接缝 | 框架文档用语:适配器与装配层之间的 contract 包 |
| glue | 胶水代码 | 消费者手写的装配/桥接代码 |
| four-piece | 四件套 | `Segments/Apply/Plan/Build` 全手写一个 appkit Provider 的样板 |
| build tag | 构建标签 | `//go:build nacos` 之类的编译期变体开关 |
| codegen | 代码生成 | `bald gen app/domain` 的 owned 树产出 |
| lockstep release | 同步发版 | 全部模块同抬一版的发布模式 |
| dogfooding | 自用自测 | cli 按已发布 tag 消费框架,保持外部视角 |

---

## 一、产品需求

### 1.1 痛点量化(全部可复跑)

| 痛点 | 数字 | 复跑命令 |
|---|---|---|
| 版本线爆炸 | `go.mod` 70 个(含 `_example`);发布单元 69;git tag **703** 个 | `find . -name go.mod \| wc -l`;`bash scripts/list-modules.sh \| wc -l`;`git tag \| wc -l` |
| 拓扑维护税 | 相对 `replace` **357** 条;release/multi-major 工具链整条为拓扑服务 | `rg -n replace */go.mod */*/go.mod \| wc -l` |
| 版本漂移债 | §7.40「本地绿+远端红」整类缺陷的成因(13 个 module 用本地 pkg + 已发布 bootstrap 编译) | risk-review §7.40 |
| 分层无闸门 | check-arch 只看得到根模块 **2/131** 个包;depguard 未启用;全仓分层当前无任何机器守卫 | `check-arch.py --verify` 实跑(输出留档 `.dev/`) |
| 接缝样板 | 框架内手写 `Plan` 实现的 Provider **22 个**(各配 `Build`,加 admin 侧更多);`_example` 曾手抄 70 行 logger Provider(N-2 已修) | `rg -l 'func \([^)]*\) Plan\(' --type go -g '!*_test.go' \| wc -l` |
| 消费者胶水 | bald-admin `internal/bootstrap` 手写 **4255 行**;大量是为补官方 Provider 缺口而写的四件套(cacheRedisProvider 降级语义、audit_wiring、appBindingProvider) | `wc -l ../bald-admin/backend/internal/bootstrap/*.go` |
| 构建标签变体 | nacos/grpcgw 等变体靠 `//go:build` 手写文件对(`register_nacos.go` vs `registrar_inmemory.go`) | `rg -l '//go:build' _example/bald/internal/bootstrap` |

### 1.2 用户画像与日常动作

单一用户 = 框架作者本人;消费者只有 bald-admin(同级目录)与 `_example/bald`。「日常」= 起服务、加后端、加业务域、升框架、发版。任何只在"多消费者分治"下才回本的机制,对这个人都是纯税。

### 1.3 验收标准(每条绑可执行判据)

| id | 标准 | 验证方式 |
|---|---|---|
| A1 | 发布单元 69 → 4(框架 / bconf / cobramcp / cli) | `bash scripts/list-modules.sh \| wc -l` == 4 |
| A2 | 一次升级 = 一行 require;admin go.mod 的 bald require ≤ 4 行 | `grep -c 'github.com/kalandramo/bald' ../bald-admin/backend/go.mod` ≤ 4 |
| A3 | 声明的后端零手写接线:**已声明面**（server http/grpc/gateway/cron/asynq/mcp + broker 族）增删只改 appspec.json + `bald gen app` | `_example` 改已声明面开关,git diff 仅含 owned 生成物;**新面类型**（sse/websocket/kafka/rabbitmq）需 `ServerSpec` 加字段+模板分支,归 bconf 变更窗口（评审 N-7）,不在 S1 验收内 |
| A4 | 四件套压缩:试点 contract 包迁 `segment.New`,**机械样板(Segments/Apply/Plan/类型)每包归一(实测 -93%)**;测试仅私有类型构造行等价改写、断言与语义一字不改仍绿 | `go test ./transport/cron/contract/ ./cache/redis/contract/ ./registry/etcd/contract/` + 引擎级 `_example internal/bootstrap` |
| A5 | 分层闸门生效:`.arch.json` 全仓可判 | `check-arch.py --verify` rc=0;`--blind-spots --governed @config` 覆盖 ≥95% 非 main 包 |
| A6 | 功能不变 | 每批:逐模块 `go test -race ./...` + `task acceptance` + `bald gen verify` + admin `go test ./...`;docscheck |
| A7 | admin 胶水收敛 | `internal/bootstrap` 手写(非 `.gen.go`)行数 ≤ 1000 |

---

## 二、设计取舍

### 2.1 核心判断

显式 import 与强类型契约是框架资产,保留;痛的不是哲学,是**拓扑**(每后端一 module)和**书写者**(接缝由人写)。所以方案 = 动拓扑与书写者,不动哲学。

### 2.2 目标拓扑(4 个发布单元)

```
github.com/kalandramo/bald          ← 框架单 module:bootstrap、pkg/*、transport/*、broker/*、
                                       cache/*、registry/*、log/*、metrics/*、encoding/*、
                                       ratelimit/*、circuitbreaker/*、selector/*、retry、health、
                                       berrors、bconfig、oss/*、workflow/*、contrib/*、tools、internal
github.com/kalandramo/bald/bconf    ← 保留独立:proto 契约是 bconf-r02 版本纪律的宿主,
                                       且是 cli 的第一依赖
github.com/kalandramo/bald/cobramcp ← 保留独立:CLI 依赖面瘦身(否则 cli 传递吞整个框架);
                                       自身是独立产品(MCP 桥)
github.com/kalandramo/bald/cli      ← 保留独立:AGENTS.md 的零 replace dogfooding 约束原样有效
```

关键事实:各子 module 现名 = 根 module 路径 + 目录,所以**合并后所有包 import 路径逐字不变**(如 `github.com/kalandramo/bald/pkg/appkit`)——扁平化对消费者只是 go.mod 变短,源码零改动。

### 2.3 已否决/未采纳的方案

| 方案 | 否决理由 |
|---|---|
| init() 全局注册 / database-sql 式插件化 | 毁掉「未 import 后端零编译成本」与依赖图可见性(J5);与严格契约门(J7/J9)冲突;主线已消灭 tag 之痛,不值得再赔上显式性 |
| 保留多模块、只做 lockstep release(全模块同抬一版) | 半吊子:tag 少了,但 372 replace、§7.40 漂移人口、闸门失明、N-1/ADR-6「改不动」的根因全在——收益只有账面,拓扑税照付 |
| go.work 日常联合开发 | 仓内已明确否决(CI/Taskfile `GOWORK=off`,verify-modules 自检),放行它等于把发布形态判据静默退化成本机形态 |
| 把 bconf/cobramcp 也并进框架 | cli 的 `go install @latest` 依赖闭包与 go.sum 会吞下全框架第三方 SDK;且 bconf-r02 契约纪律需要独立版本位 |

### 2.4 要付的代价(诚实清单)

- 失去按后端独立 semver:对「唯一消费者是我」= 零损失;旧 tag 线冻结在合并前版本,已在用的外部消费者(若有)钉旧版仍可编译。
- 单 module 的 go.sum 变大、`go get -u` 影响面变大:换来 vuln/audit 一次扫全仓。
- CI 并行度下降(63 job → 4):单测总时长不变,wall-clock 变快(无逐模块循环开销)。
- docscheck `TestModuleListMatchesDerivation` 的模块清单、《多模块发布与变更日志》卡、release 工具链、README/AGENTS.md 要同步改写——都是机械跟改,列入切片验收。

### 2.5 接缝的再分配(谁写胶水)

1. **声明能表达的 → codegen 写**:`assemble.gen.go` 已生成 happy-path 接线(本轮实读确认);缺口是变体接线仍靠手写 build-tag 文件对、以及 admin 根本没吃生成树。补齐「变体 → 生成」这条腿,并把 appspec 能表达的段全部纳入 Options() 派生。S1b 实读补记:codegen 面派生是 ServerSpec 开关驱动(10 面白名单 vs 6 面接线),「声明即接线」成立的范围=ServerSpec 已有开关的面;新面类型的生成化前置是 bconf proto 变更,登记为评审 N-7、排在 bconf 窗口。
2. **声明表达不了的(业务语义,如 cache-redis 降级告警)→ builder 写薄**:新增 `segment.New` 数据式构造,引擎 `Provider` 接口不动(它是词汇);四件套里 `Segments/Apply/Plan` 的机械部分按缺省派生,人只写 `build` 函数与例外元数据。
3. **框架自有段(bootstrap→transport 倒挂,N-1)→ 归位 contract 模式**:扁平化后「契约包引 appkit 会成环」的前提消失(同 module 内包环由编译器直接拒,分层由 .arch.json 判),N-1 冻结规格随 S3 执行。

---

## 三、实现契约

### 3.1 架构分层(全局契约,写入 `.arch.json`)

层表(内→外):`contract`(bconf/bconfig/berrors/SPI 接口层) → `kernel`(bootstrap) → `kit`(pkg/*) → `adapter`(各后端) + `seam`(*/contract 接入子包,唯一允许同时 import kit 与 adapter 的层) → `tooling`(cli/tools/internal)。forbid 三条:contract 不得 import 以上各层;kernel 不得 import 具体 transport 与 pkg(收编 N-1);kit 不得 import contrib。
现状:工具在多模块形态下只见根模块 2 个包,`--verify` 判 FAIL——登记为盲区处置:**根因即本需求对象,S2 合并后翻绿**;期间以「不改既有 import 方向」为软约束。

### 3.2 `segment.New` builder API 草案

```go
// pkg/appkit/segment(新子包,引擎旁挂;不改 Provider 接口)
func New(segment string, build BuildFunc, opts ...Opt) Provider
type BuildFunc func(ctx context.Context, s appkit.ApplyScope) (any, appkit.Cleanup, error)
// Opt:At(phase) / Key(k) / DependsOn(keys...) / Reload(mode) /
//     Effect(name) / Opaque() / Priority(n) —— 全部缺省从段名派生
```

段族表是对 18 个 contract 包现行节点声明逐字扫描的产物(S1 实现时实测):同族后端 Phase/Priority/Reload/EffectName 完全一致,`instrumentation.%s` 族 Effect 名带段后缀模板。注意与 `segment_coverage.providerHints` 是**两条知识轴**——hints 管「报错指路:段 → 契约包」,族表管「节点形状缺省」,实现时不合并(设计初稿此处表述不准,已随实现修正)。迁移判据 = A4(派生节点与手写字面量逐字段一致,单测 `TestFamilyDefaultsMatchPilots` 钉死)。

### 3.3 生成物契约

- `assemble.gen.go` 新增覆盖:build-tag 变体的接线改由 AppSpec `variants[].segments/imports` 派生(现 `variant_*.gen.go` 已是先例,扩量);手写 `register_nacos.go`/`registrar_inmemory.go` 文件对退役为生成物 + 一个填充点。
- Gate1/Gate2 语义不变:未实现面仍在生成期拒绝;`bald gen verify --check` 零差异仍是 owned 树判据。
- cli 模板改动走 codegen 金样测试(现 `TestGeneratedMatchesProto` 同族)。

### 3.4 迁移批次(详细判据与回滚见 `.dev/tasks/dx-flatten-slices.md`)

S1 胶水先行(不动拓扑)→ S2 单模块合并 → S3 N-1 归位 + `.arch.json` 翻绿 → S4 admin 吃生成树 + 工具链退役。每批独立可回滚;S2 之前的任何时刻回退 = 单纯 revert 分支;S2 后旧 tag 线冻结、新线自 `v0.57.0` 起。

### 3.5 门禁映射

| 批次 | 门禁 |
|---|---|
| S1 | 逐模块 `go test`(含 -race)+ 契约包金样 diff + lint |
| S2 | `task acceptance` + `bald gen verify` + 4 模块全量 `-race` + docscheck + `check-arch --verify/--blind-spots` 翻绿 |
| S3 | 同 S2 + 停机序断言回归(AR-1/25 那族) |
| S4 | A2/A7 数字判据 + admin e2e(环境依赖项按现状口径跳过并登记)+ release 首跑(v0.57.0 单系列) |
