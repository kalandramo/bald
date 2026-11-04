# bald 架构风险 / 质量评审

> 产出方式：`risk-quality-reviewer`，交叉引用 `system-modeler`（`current-architecture.md`）与 `architecture-health`（本文对制品自身的校验）。
> 证据日期：2026-11-01，`main` @ `ca981b1`，工作树干净。
> 核对方式：文中每条结论要么由我本人读源码/执行 grep 得到，要么由脚本从制品中抽出 `路径:行号` 后逐条打开源文件比对。凡未做到这一点的，明确标 ⚠ 并写清依据。
> **本评审登记时不改动任何代码**。状态列标 **已修** 的条目由后续修复轮次落地，每条带「修法 / 验活 / 未修」三段；行号会随这些改动漂移，引用以符号名为准复核。

## 严重度概览

| # | 风险 | 严重度 | 置信度 | 状态 |
|---|---|---|---|---|
| 6.5 | gRPC 的 `tls` 契约字段**未接线**，生成物硬编码明文；gateway 出站也硬编码明文 | **高** | ✅ 已核对源码 | **已修** |
| 3.1 | 根 module ↔ 适配器 **模块环**，靠互相 `replace` 才编译 | **高** | ✅ 已核对 go.mod/import | **已修（两轮）**：删重复机制后实测图为 DAG；`pkg/` 独立成 module 后 require 根的适配器 22→0，见 §3.1「收口」 |
| 6.1 | 鉴权 **fail-open**：未注入即整层消失，无 default-deny、无告警 | **高** | ✅ 已核对源码 | **已修** |
| 2 | **发布面 ≫ 使用面**：retry / circuitbreaker 等无消费方 | 中 | ✅ 全仓 grep | **部分收口**（§2「选项 1」的三条分支里 Discovery 接线半、circuitbreaker 熔断半已落地；`retry` 仍 reference-only 且刻意不进服务入口链；移出发布批次未做。circuitbreaker 接线同时修掉 `hystrix`/`sres` 的滑窗清桶错格，见 §2「熔断半」） |
| 7.1 | **CI 全量只跑 `-short`**，集成测试在 CI 中从不执行 | 中 | ✅ 已核对 `.github/workflows/ci.yml` | **已修（registry×3 / store / 队列，五条套件 13 条判据）**：新增 `integration` 作业注入 etcd/consul/redis/mongo，另开 `integration-kubernetes` 作业起真 kind 集群跑第三个 registry 后端；两侧同构——探活 + 逐条断言判据真的 PASS；作业文件自身另开 `workflow-lint` 作业（actionlint）把关，见 §7.1 收口及其 consul / kubernetes / 示例去仓库外依赖 / 示例 module 进 CI 四条补条；~~示例侧（etcd/nacos 服务发现）仍只在本地跑~~ **2026-11-04 起示例有自己的 `example` 作业**（按 pin 装 buf 生成 gitignore 的 `api/gen` + etcd/nacos 服务容器 + 三态构建 24 条判据逐条断真 `--- PASS`），见下面「7.1 补：示例 module 进 CI」 |
| 7.2 | **无覆盖率/测试存在性闸门**：零测试的 module 不会自己暴露（§1.3 那两个适配器是人数字文件数出来的） | 中 | ✅ 闸门两次变异实测：移走 `broker/kafka` 的测试文件 → 红；把纯声明豁免判据改恒假 → `metrics`/`transport/subscribe` 一起红 | **已修**：`TestModulesWithLogicHaveTests`——「有可执行逻辑的 module 必须有测试」，豁免由 `go/ast` 数带体函数得出而不是名单；比例阈值刻意未做——2026-11-04 已把 94 module 的基线实测出来（逐包 154 个有数字、0% 档 0 个；合并口径最低三档是 0.0% 而它们是零语句模块根），实测反而坐实了「阈值会假红」，见 §7.2 收口与「7.2 补」 |
| 7.4 | **统计量当不变量断言**：`circuitbreaker/sres` 并发用例钉「放行 ≥90/100」，而该值取决于 Allow 与 Mark 的交错 | 中 | ✅ 本机 `-count=400` 实测 + CI 红过一次（run 41388597082） | **已修**：先灌满窗口使单次拒绝率有上界 1/1001，「≥90」因此成为可证的界；另补 accept=0 状态下的并发错型用例，见 §7.4 |
| 7.5 | **Windows 只在周界作业里跑，一跑就红**：产物扩展名、清单差异的路径身份、检出 CRLF 三处平台适配缺陷 | 中 | ✅ `ci-windows` schedule 实测红（run 41428497056）+ 本机 CRLF 克隆复现；修后 dispatch run 41445557462 七 step 全绿 | **已修**：`.bin` 产物名、`checkTree` 差异按清单 slash 身份输出、`moduleLine`/`readText` 对行尾不敏感，并用 `.gitattributes`（`eol=lf`）收口检出形态，见 §7.5 |
| 7.6 | **配置热更把「写到一半」当变更派发**：`bconfig/file` 的就地重写会漏出 0 字节快照，消费侧解成「这一层没有 key」 | 中 | ✅ `race` 作业实测红（run 41485136191）+ 本机 linux/arm64 容器量化；修后 Linux `-count=10` 全绿、两种退化各自转红 | **已修**：派发前等内容稳定（`readSettled`），空载荷另加递进确认；`ValueWatcher` 契约补上「连续变更允许合并」，见 §7.6 |
| 1.1 | race 作业是两处手工清单，webrtc/kafka 均缺失 | 中 | ✅ 已核对 `.github/workflows/ci.yml` / `Taskfile.yml` | **已修**（改成 find 派生 + 单份排除清单；扩范围实测揪出并修掉 graphql 自身的竞争） |
| 1.3 | kafka / rabbitmq 适配器 **零测试文件** | 中 | ✅ 实测计数 | **已修**（两个适配器各有一份测试，15 / 7 个测试；补测试当场揪出 4 个生产缺陷；订阅者摘除口径已定并由测试钉住） |
| 1.2 | 本地 `task verify` 手写清单，静默跳过约 40 模块 | 低-中 | ✅ 已核对 `Taskfile.yml` | **已修**（CI 与本地共用 `scripts/list-modules.sh` 单条派生；全量 92 模块实测 exit=0） |
| 附 | `tokenbucket.Close()` 不唤醒 `Wait`，与 gin 侧注释承诺相反 | 中 | ✅ 读源码 + 变异验证 | **已修**（`Close` 内 `close(notify)` + 幂等；判据 `TestClose_WakesBlockedWait`） |
| 5 | `berrors` 分类法未在 transport/broker/appkit 落地 | 中 | ✅ 实测计数 | **口径已定**（选项 b：定位为边界投影模型；不改码，兜底不对称与硬规则见 §5） |
| 6.2 | gRPC 链无限流、无超时（与 gin 链不对称） | 中 | ✅ 已核对源码；装配半另经生成树 e2e 实测（429 / `RESOURCE_EXHAUSTED` / 提前 503 / `DEADLINE_EXCEEDED`） | **已修**（unary + stream + 契约面 + 两面契约驱动装配，见 §6.2） |
| 7.7 | env 覆盖表达不出契约的多词键：严格门禁下启动即报，非严格下优先级反转 | 中 | ✅ 本轮写 e2e 时实测撞上（四条用例全红在 `unknown contract keys`），根因读到 `environMap`/`lookupField`/`findUnknown` 三处；修后同一姿势的示例 e2e 转绿、去掉折叠三条消费侧用例必红 | **已修**：env 层在合树前按契约折回规范键（`bconf.FoldEnvKeys` ← `bootstrap/config` 的 `envLayer`），两个后果一起消失；业务自持键仍避开下划线，见 §7.7 |
| 7.8 | `TestWatchValue_NoTornEmptySnapshot` 断得比 `ValueWatcher` 契约还强，共享 runner 上偶发红 | 低-中 | ✅ 远端实测：run 41597365828（head `b149fbf`）`test` 作业红在 `bconfig/file`，仅此一条断言失败（`重写过的 "v21" 从未被派发`），同一用例在后续 run 全绿 | **已修**：逐值断言换成三条时序无关的不变量（0 次空载荷 / 派发序单调不减 / 末值必为真实终值），三条变异各自只打中一条；Linux 容器 `-race -count=10` 全绿，见 §7.8 |
| 7.9 | **契约产物的字节判据绑工具链**：本机工具与产物不同源就红，而差异只是注释排版 | 中 | ✅ 第一/二/三轮记的是「buf 1.50.0 零差异、1.57.2 补空格」「源码构建 buf 与官方发行二进制不等价」——**这两条已被第四轮的单变量实测推翻**（当时两行之间真正的另变量是 PATH 里那把插件）。第四轮的矩阵：buf 1.50.0 与 1.57.2 配同一把插件都零差异；同一版插件（自报 v1.36.12、module h1 相同）由 go1.26.5 编译时补空格、由 go1.27.1/1.27.3 编译时零差异；把 Go 钉住换插件版本（v1.36.10）则差在全部 18 个文件而注释不变 | **第四轮收口**：字节比对前机器判**三把 pin**——`bconf/buf.version`、产物头部的 protoc-gen-go 版本行、新加的 `bconf/protoc-gen-go.goversion`=1.27（读 `go version -m` 的构建工具链）；机制落在 protobuf `compiler/protogen` 的 `Content()` 用 stdlib `go/printer` 重排。产出与校验同走本机模板；不为绿重生成产物、不弱化字节判据。**第五轮**补上远端证据：CI 新增专职作业 `proto-contract`，按 `bconf/buf.version` 下载官方 buf 并按 `bconf/buf.sha256` 校摘要、插件版本读自入库产物头部现编、判据步要求真 `--- PASS`（Skip 即红）；`acceptance` 表里那条仍标 `no` 只是不重复装工具链，见 §7.9 第五轮。**第六轮**把后两轴从 PATH 搬进仓内：判据在 `bconf` module 里 `go build` 现编 protoc-gen-go，插件版本轴改读 `bconf/go.mod`（protobuf 由 v1.36.11 升到 v1.36.12 对齐产物头部），口味轴改读那把自编译二进制的 `go version -m`；CI 作业里「按产物头部 `go install` 插件」那一步随之删除。这补掉的是一个**改造前根本测不到**的洞：仓内 go.mod 与产物头部不一致时，旧腿只要 PATH 上恰好装着对的那把就会静默绿 |
| 6.3 | 限流位于认证之后，认证层自身不受保护 | 中 | ✅ 已核对注释与实现 | **口径已定**（保持现位置 + 落文档；不改默认的理由与自保姿势见 §6.3） |
| 3.2 | 21 个 module 同时 require 同一库的两个 major（`// indirect`） | 低 | ✅ 脚本实测 | **已修（只读面）**：`scripts/multi-major.sh` + `task deps-multi-major`；`go list -deps` 实测确认真正同链的是 2 条，见 §3.2 |
| 3.3 | 根 `v0.35.0` **发得出去、装不出来**：仓库里的生成物文件名让代理建不出模块 zip | **高** | ✅ `goproxy.cn` 实测 404 `create zip: malformed file path … invalid char '（'`，230 条路径逐条核对 | **已修**：`.qoder/` 导出移出版本库（磁盘副本保留），`bald release tag` 新增路径闸门（实测在坏树上拦下 230 条、落 0 个 tag）；`v0.35.0` 按「已发布不可重发」保留不用，由 `v0.36.0` 取代，见 §3.3 |
| 3.4 | 官方 CLI **文档里的安装方式从未装成功过**：`go install …/cmd/bald@tag` 被 Go 直接拒绝 | 中 | ✅ `v0.31.0`/`v0.36.0`/`v0.37.0` 三个 tag 实测同一条错误；判据取自本机 Go 1.27.1 源码 `load/pkg.go` | **已修（走 B）**：CLI 拆成独立 module `github.com/kalandramo/bald/cli`，`cli/go.mod` 零 `replace`，另加两条机器闸门（`TestCliModuleIsReplaceFree`/`TestGoWorkIsNotTracked`）；`cli/v0.1.0` 已于 2026-11-03 发出，零 `replace` 夹具实测 `go install …/cli/cmd/bald@v0.1.0` 装得上、生成的骨架跑得起来，五处文档改回默认按 tag，见 §3.4 |
| 4.1 | 停机期 `_ = deregister(...)` 吞错 | 低-中 | ✅ 已核对源码 | **已修** |
| 4.2 | 端点上界 5s、hook 超时 5s 为常量（Stop 超时本身可配） | 低 | ✅ 已核对源码 | **已修** |
| 4.4 | 启动窗口内「调用方取消」与「server 启动崩溃」共用一条 `wait for endpoints canceled` 文案：前者被当装配错误退出，后者真实失败被整条抹掉 | 中 | ✅ 本机四条确定性用例先红后绿（三条判据各自只红相应用例）；bald-admin 装配 e2e 在共享 runner 上红过一次（run `41675043918`），报的就是这句 | **已修**：`startupOutcome` 一条判据挂到五处启动分支（含 errgroup 之前的 beforeStart/组件启动），见 §4.4 |
| 7.10 | `-tags nacos` 的**注册中心接管是死的**：示例一边钉住内存 Registrar，一边在契约里选 nacos，Provider 按设计被计划期跳过 | 中 | ✅ 三条独立证据（doctor 的 `programmatic-takeover` 告警 / 真 nacos `service/list` 返回 `count=0` / `custom.go` 里无条件的 `appkit.Registrar(inmemory.New())`）；同形状的 `registry/nacos` 命名侧 `namespace` 语义**已实测**：`public` 与 `""` 同一租户，无需折叠（与配置 API 相反） | **已修**：钉住按构建形态拆成 `registrar_inmemory.go`（`!nacos`）/`register_nacos.go`（`nacos` 留空）；装配侧新增 `TestNacosAssemblyRegistersIntoBackend`（真 nacos 断两面上册 + 停机摘除，查询走独立 HTTP 口）；计划门的 opaque 清单与 `programmatic-takeover` 告警改成按变体激活双向判；变异一次三红。装配那条当时只有本机证据（`-tags nacos` 不是 CI 的构建形态，示例 module 又进不了 CI）；两条**机制**判据同日下沉到 `registry/nacos/contract` 并由 `integration` 作业注入真 nacos 判，见 §7.10 第三轮。**同日更晚：`example` 作业落地**，装配那条按单条 `-run` 进了远端取证（并查出它自己的终态守卫有断言竞态，已修），见「7.1 补：示例 module 进 CI」与 §7.13 |
| 7.11 | **§7.4 同一形状的第二次红**：`sres` 冷窗口用例钉「放行 ≥180/200」，而冷窗口的拒绝自我强化 ⇒ 精确 DP 给 P(红)=8.4e-5 | 中 | ✅ 远端实测：`race` 作业 run 41806067800 红（`allowed 179/200`）；本机 `-race -count=3001` 最低 178、均值 194.21（DP 均值 194.16） | **已修**：比例 90% 一字未动，样本 200→500 ⇒ 阈值 450，DP P(红)=2.0e-13、均值 493.22（本机 `-race -count=3000` min=478、mean=493.16）；两道变异各红（`MarkSuccess` 记 error → 1/500、分母 `+1`→`*2+1` → 250/500），见 §7.11 |
| 7.12 | `cli` 里那条「真 `buf build` 编译生成的六件套」自 `691929c` 起**结构性判红**：夹具写 v1 `buf.yaml`，复制来的 `buf.lock` 已是 v2 | 中 | ✅ 未改动的树上实测红（`got a v2 buf.lock file for a v1 buf.yaml`），buf 1.50.0 与 1.57.2 都拒——与工具链版本无关；远端一直没红是因为这条腿在 buf 缺席时 `t.Skip`，CI runner 不装 buf（§7.9 同一口径） | **已修**：夹具的 `buf.yaml` 改 v2（`modules: path: .`），耦合点写进注释；改后两个 buf 版本各 `--- PASS`、`cli` module 全量绿。判据本身一字未动，见 §7.12 |
| 7.13 | **同进程内密集的配置查询**撞 nacos-sdk-go **自己的进程内令牌桶**（5/s、桶 5，按 `dataId@@group@@tenant` 分键、包级共享）⇒ 配置层 `Load` 判错、应用起不来；判限时**一个包都不发**，而文案在快照缺失时只报「本地缓存文件不存在」，在快照存在时干脆 `err == nil` 返回陈旧配置 | 中 | ✅ 固定二进制的密度矩阵（每格新进程、`-count=1`）：单条装配 12/12 绿、并集 3 次红 2、整包 3 次红 3。**归因错过两次**：连接复用✗（uid 每 client 随机 UUID）、服务端限流✗（无网络只调 `IsLimited` 就放行 0..4；「新建 client 再查」60/60 拒且单次耗时 `0s`）⇒ 真因在 `config_client/limiter.go:42-56` | **已修（2026-11-04）**：`bconfig/nacos.Load` 带上界重试（4 × 250ms；250 是最坏观测回填 220ms 的余量）+ 错误文案同时带上界与候选真因；四条假 `IConfigClient` 判据各配变异。牙齿是同键并发 10 次的 A/B：`HEAD` 冷轮 10 个里 5 个判错，当前树三轮 0 失败。**修完后整包不再复现红**，所以「套件绿」不是证据。第二面（快照把限流静默成「配置没变」）未修——唯一开关 `DisableUseSnapShot` 是删离线快照能力，owner-gated。作业形状照旧（重试摊开突发而非消掉），见 §7.13 |
| 8 | `docscheck` 只校验链接完整性，测不出描述过期 | 低 | ✅ 已核对测试 | **已修（清单半 + acceptance 进 CI）**：`module-list` 围栏 + 双向断言 + 与脚本逐行比对，三条变异实测；`acceptance` 判据由 `scripts/acceptance-gate.sh` 交出并接进 `ci.yml` 的 `acceptance` 作业（清单取自 `go test -list`、逐条断言顶层 PASS），两条本地腿的理由成文；「描述已删除 API」半**已按一次性 AST 测量收口**（2026-11-04）：改掉 12 族假引用，宽松口径对本仓真漂移归零，并量化了「不造永久闸门」的三条理由——见 §8 |

---

## 1. 模块面 vs 构建/验证面

**1.1 race 作业是手写清单，且两处重复** ✅ 已修

原状：`.github/workflows/ci.yml` 的 race 作业手工枚举 23 个模块，`Taskfile.yml`
的 `race` 镜像同一份清单（两处必须同步改）。`transport/webrtc`（15 个非测试文件
+ 8 个测试文件，含 `signalSessions` 共享会话表与 PeerConnection 生命周期，正是
race 该覆盖的形态）、`broker/kafka`、`contrib/*` 全部缺席，而 `metrics` 是零测试
文件的死条目。

- 修法：两处都改成**从仓库派生**——复用 test/lint/vuln 作业那条
  `find . -name go.mod -not -path "./.git/*" -not -path "./_example*" | xargs -n1 dirname | sort`，
  再减去 `.github/race-skip.txt`（单份排除清单，每条必须自带理由）。默认值从
  「不跑」变成「跑」：新增 module 两处都不用改。Taskfile 侧用 `vars.MODS` 的 `sh:`
  + `for:` 派生，逐模块仍走内部 `race-module` 的 task 级 `dir:`（go-task 3.52 会
  静默忽略「逐命令 dir」和「for 循环内的 dir」），并加 `platforms: [linux, darwin]`
  ——派生要 POSIX shell，Windows 下整个任务跳过，反正 `-race` 本身要 cgo、
  `.github/workflows/windows.yml` 也从不跑 race。
- 验活：按 CI 谓词共 **92 个 module**（89 个库模块 + `cobramcp/examples/` 下 3 个
  示例模块），本机逐模块 `go test -short -race` 全量一遍 = 90 通过 / 2 失败，
  串行合计 351s（race 作业原先 23 个模块在 CI 上 4.5 分钟，扩到全量的代价可接受）。
- 扩范围立刻抓到的第一个真 bug 在 `transport/graphql`（此前从未被 race 覆盖）：
  `Start()` 在调用方 goroutine 里写 `s.listener` / `s.server`（`transport/graphql/server.go:116-119`），
  `Stop()`（`:141-148`）与 `Endpoint()`（`:152-163`）却无同步地读。接进装配面就命中：
  `Start` 跑在 `pkg/appkit/appkit.go` 的 `Run` 里那个 `g.Go`，端点等待（`waitForEndpoints` 轮询 `s.Endpoint()`）
  在同一个 goroutine 上并发读；停机阶段每个 server 的 `Stop` 也各自起 goroutine
  （`stopAll` 的 server 停止阶段）。改法：`mu sync.RWMutex`
  只保护这两个运行期字段（`:63`），`Serve`/`Shutdown` 改用局部 `hs`；测试里三处
  「固定 sleep + 直接读 `srv.listener`」换成轮询 `Endpoint()` 判就绪，中间件用例的
  共享变量 `headerSeen` 换成 channel 传递。
- 刻意没做：`log/aliyun` 的竞争在上游 `aliyun-log-go-sdk v0.1.131`（已核实是最新版，
  无升级可解）内部——`Start()` 起 `monitor.reportThread` goroutine，`Close()` 里
  `monitor.Stop()` 写同一字段，两者无同步。bald 的 `Close` 只是转调，要规避必须先
  关掉运行期指标上报（删能力），因此进排除清单并写明理由，而不是改代码掩盖。上游
  哪天修好，这条就该删。

**1.2 CI 的模块发现是健壮的，本地 `task verify` 不是** ✅ 已修

原状：CI 用 `find . -name go.mod ... | xargs -n1 dirname | sort` 零维护地发现全部模块（**循环排除了 `./_example*`**），逐模块 `go mod download` + `go mod tidy -diff` 闸门 + build + vet + test + bench 编译。
但 `Taskfile.yml` 的 `test` 只跑根 module，`verify` 手写依赖清单只覆盖 bconf/cobramcp/retry/ratelimit/broker/circuitbreaker/encoding/example —— `contrib/*`、`registry/*`、`transport/http|grpc|web`、`oss/*`、`ai/*`、log/metrics 后端**不在任何本地任务里**。只跑 `task verify` 会静默跳过约 40 个模块。严重度低（CI 兜底），但作者的问题要等到 CI 才暴露。

- 修法：把那条 `find` 提为唯一派生点 `scripts/list-modules.sh`（空清单 = 报错退出，不静默返回空），CI 的 test/lint/vuln/race 四处内联循环全部改成 `sh scripts/list-modules.sh`；本地新增 `verify-module`（单模块 tidy -diff + build + vet + test -short）、`verify-modules`（按派生清单逐模块跑，与 CI test 同范围）、`verify-scope PREFIX=<dir>`（前缀子集）。原先手写清单的 `retry-verify`/`encoding-verify`/`ratelimit-verify`/`broker-verify`/`circuitbreaker-verify`/`cobramcp-example-verify` 改成委托 `verify-scope`，约 30 个手抄模块名和 5 个只为它们服务的内部任务一并删除。`task verify` 的 deps 换成 `verify-modules`。
- 踩过的两个坑（都属会静默变绿的类型，记下来防止再犯）：① `go build -o <dir> ./...` 只对 main 包合法，纯库模块会报 `no main packages to build`——改成先用 `go list -json ./...` 判有没有 `"Name": "main"` 再决定形态（不用 `-f '{{.Name}}'` 是因为花括号会被 go-task 当模板渲染）；② `platforms:` 写在任务上会让它在其它 OS **静默跳过**，所以派生守卫写成「逐命令 platforms + Windows 分支显式 `exit 1`」，宁可让 Windows 报错也不假绿。
- 验活：空清单与不存在的 `PREFIX` 都硬失败（`verify: 清单派生为空…` / `verify-scope: … 下未派生出任何 module`，非零退出）；`sh scripts/list-modules.sh` 的输出与替换前的内联管道逐字节相同（92 个模块）。
- 实测：`task verify-modules` 全量跑完 **exit=0**，覆盖 92 个 module（每模块一次 `go mod tidy -diff` 闸门，共 92 次），158 个测试包 `ok`，0 FAIL。这轮改动之前，同一条命令只能覆盖到手抄清单里的那十来个模块。
- 未修：`task test` 仍只跑根 module（刻意保留——它是快速回路，全量入口是 `verify`/`verify-modules`）；`verify` 里 `go mod download` 这一步没有独立任务（CI 有；本地由 `tidy -diff` 间接完成）。

**1.3 零测试的网络适配器** ✅ 已修（kafka / rabbitmq）

| 模块 | 非测试 .go | 测试 .go（修前 → 修后） |
|---|---|---|
| `broker/kafka` | 9 | **0 → 2**（15 个测试 + 32 个子用例） |
| `broker/rabbitmq` | 11 | **0 → 1**（7 个测试 + 6 个子用例） |
| `transport/subscribe` | 1 | **0**（单文件 13 行选项，未补） |
| `retry` | 1 | 1 |

修前：kafka 适配器核心文件 639 行，含 writer 重建重试（`broker/kafka/kafka.go:415-437`）与订阅退避逻辑（`broker/kafka/subscriber.go:316-339`）与致命错误判定（`:341-362`）——这些分支从未被执行过。CI 对它只做到「能编译、vet 干净、空测试通过」。

- 修法：不起真实 broker，用「本地端口 listen 后立刻 close」造确定 ECONNREFUSED，
  失败分支全部在 `-short` 下可跑；把不可测的形态先拆成可测的——判据抽成
  `shouldRetryFreshWriter`（`broker/kafka/kafka.go:356-359`，两条发布路径共用），
  退避时长抽成纯函数 `backoffDuration`（`broker/kafka/subscriber.go:316-331`，注入随机源
  才能钉住边界）。发布侧覆盖缓存 writer 摘除与重建、`retriesCount=0`、热更重连后再发布、
  单 writer 分支（`broker/kafka/kafka.go:447`）、编码失败短路；订阅侧覆盖退避边界、`isFatalError` 真值表、
  致命退出与取消退出、close 幂等、`Unsubscribe(true)` 摘除注册表（`broker/kafka/subscriber.go:91-101`）。
  rabbitmq 只补不联网也能钉住的那层：`Init` 的地址归一化、三个未连接入口报错不 panic、
  Connect 失败后 `Disconnect` 仍能收尾、`rabbitHeaderToMap` 对非字符串 header 丢成空串。
- 补测试当场揪出 4 个生产缺陷（`fix(broker/kafka)` 7545f8c），每个都按「把缺陷改回去、测试必须红」验活：
  1. `Writer.Close` 把容器置成 nil map（`broker/kafka/writer.go:96-107`），热更重连后第一次发布
     往 nil map 写元素直接 panic（`broker/kafka/kafka.go:399`、`:431`）；panic 落在 `b.Lock()`
     窗口内，写锁永久泄漏，随后的 `Disconnect` 卡在 `RLock`（`broker/kafka/kafka.go:271`）——
     现场表现是测试既不失败也不通过，而是挂到超时。
  2. 重建路径 `if err = writer.Close(); err != nil { break }` 用 Close 的返回值顶掉写失败本身；
     `retriesCount == 0` 时重试循环一次不跑，`Publish` 对一条从未写出的消息返回 nil。
  3. `closeLocked` 关掉 `done` 后又把字段置 nil（`broker/kafka/subscriber.go:109-129`），
     而两条 process 循环的 `select` 不加锁读它（`:166`、`:234`）——`-race` 直接命中
     （`Unsubscribe` 与订阅 goroutine 并发）。`closed` 标志本就挡得住二次 close，改写是多余的。
  4. 致命错误分支 `return` 前没有 close（`broker/kafka/subscriber.go:185-188`、`:242-245`），
     reader 与 `done` 泄漏，订阅者还留在 `b.subscribers` 里。
  另附一条只有 race 闸门扩到全量后才会暴露的：退避原先用包级
  `rand.New(rand.NewSource(..))` 自建源（非并发安全，每个订阅各自一个 goroutine），已换成
  `math/rand` 全局函数。
- 刻意没做：`transport/subscribe` 只有一个 13 行的选项文件，不值得单独立一份测试；
  `retriesCount` 没有对外 Option（`retriesCountKey` 只被读、从没被写），测试按包内白盒直接改字段，
  没有为它新增公开 API；rabbitmq 的连接/通道/重订阅面要真实 AMQP server 才谈得上覆盖，本次未做。
- **「致命退出后订阅者还留在 `b.subscribers`」的口径已定（不改码）**：摘除责任在**调用方**，
  订阅 goroutine 自己只释放资源、不动注册表——`Unsubscribe(true)` 与 `Disconnect` 是仅有的两条摘除路径。
  三条理由：① 注册表按 topic 键且 `Add` 是覆盖式，退出中的订阅者无从判断「表里这条还是不是我」，
  自摘会在调用方已为重订阅换上新订阅者时把新的误摘掉；② kafka / rabbitmq / redis pubsub / redis stream /
  rocketmq 五个 broker 形态一致，只改 kafka 等于让同一问题在不同后端有不同答案；③ 残留条目对清理无害，
  `Disconnect` → `Clear` → `Unsubscribe(false)`，而 close 已幂等。**代价如实写明**：
  `broker.SubscriberSyncMap` 的 `Topics()/Len()/Has()` 是公开 API，会把已死的订阅算进「当前订阅」上报；
  仓内无人读它，判存活要看返回的订阅者本身（kafka 的 `IsClosed()`，不在 `broker.Subscriber` 接口上）。
  这条口径由 `TestSubscriberExitKeepsRegistryEntry` 钉住（fatal 退出后条目仍在、同 topic 重订阅覆盖而非累积、
  带死条目 `Disconnect` 仍清空且不报错）；变异验证：在两处致命分支加自摘 → 恰好红在
  「致命退出不得自行摘除注册表条目」这一条，复原后 `go test -race ./broker/kafka/` ok。
  真要改成「自摘」是行为变更（公开 API 可见性变了），得把上面三条理由都翻过来才做。

---

## 2. 发布面 ≫ 使用面（已核实的未接线能力）✅

| 能力 | 模块数 | 消费方 | 证据 |
|---|---|---|---|
| `retry/` | 1 | **无** | 全仓 `grep kalandramo/bald/retry` 唯一命中是注释 `ratelimit/bbr/bbr.go:68` |
| `circuitbreaker/` | 1 + 4 后端 | **部分已接**：接口 + `hystrix` 由入口链消费 | 非测试 importer 为 `pkg/middleware/{gin,grpc}/circuitbreaker.go`（判据：`find . -name '*.go' ! -name '*_test.go' -not -path './circuitbreaker/*' \| xargs grep -l 'kalandramo/bald/circuitbreaker'`）；`vegas`/`sres`/`sentinel` 三个后端仍只被自己的测试驱动 |
| `registry.Discovery` | 接口 + 4 实现 | **已接**（gateway 出站） | 评审初值「无外部调用者」成立；现非测试的 `GetService` 调用点新增在 `transport/gateway/discovery.go` 的 `discoveryBuilder.Build`（watch-based resolver），消费方是 `transport/gateway` 段驱动装配；etcd 与 nacos 两个后端已真机跑通注册→推送→拨通 |
| `transport/graphql`、`workflow/argo`、`ai/{openai,eino,langchaingo}`、`oss/{s3,minio}` | 7 | 仓内 0 | 全仓 grep 仅 `transport/mcp/options.go:14` 的注释提到 graphql；其余无命中。有 `*/contract` Provider 但无消费者或示例 |

**为什么这是风险而不是「可扩展性好」**：这些模块进发布 tag、进 vuln/lint CI、进 `CHANGELOG.md`，但只有构造函数级测试（或没有测试）在触碰它们，**会静默腐烂**。Discovery 尤其值得单列——它让「服务发现」在 README 与契约里存在，而评审当时实际服务间调用走配置地址直连（`transport/gateway/gateway_server.go` 用 `s.backend.GetAddr()` 拨号，地址来自契约段），使用者容易误判。**这一条的误判面已由下面的「接线半」消解**：同一地址现在可写 `discovery:///<服务名>` 并真的被解析。

消解路径（按成本排序，任选其一，不要默认「留着以后用」）：
1. 接进 `pkg/appkit/std` 的标准角色（retry/circuitbreaker 作为可选 bundle 层，Discovery 作为 gRPC resolver）；
2. 显式标注为 reference-only，移出主线发布批次，在 README 的 SPI 表里标「未接线」；
3. 归档删除。

**收口（选 (2) 的标注半，未动发布批次）**：owner 既有口径是「能力不删除——未接线 ≠ 没用，删之前要问」，所以本轮只做「不再假装」：

- 修法：`README.md` 仓库结构段下新增「未接线能力（reference-only）」一条，点名 retry / circuitbreaker 后端 / `registry.Discovery` / graphql / argo / ai/* / oss 并说明「照常发布、不删除、不假装已接线」；`docs/architecture/current-architecture.md` §6 标题改为「未接线面（reference-only）」，补上 `transport/graphql`+`workflow/argo`+`ai/*`+`oss/*` 这一组（评审初值只列了前三项）与**可复现判据**（`find` 非测试文件 + 排除自身目录的 grep），并把两处随 §6.2 漂移的行号引用换成符号名。
- 验活：README 的链接与 §6 锚点由 `internal/docscheck` 校验通过；表内每条能力重新 grep 一遍，消费方仍为 0（`retry` 唯一命中还是 `ratelimit/bbr/bbr.go:68` 的注释）。
- 刻意没做：① **移出主线发布批次**——「哪些模块进 tag」由 `cli/internal/release/repo.go`（`isExampleDir` 是唯一的范围规则，且只排除 `_example`）决定，改它等于改变下游能拉到什么，属对外行为变更，要 owner 单独定；② 未接线 ≠ 下架，标注不改变任何包的公开性；③ 接线（选项 1）里 **circuitbreaker 的 bundle 层已落地**（见下「熔断半」，失败语义选 opt-in），**retry 的没做**——而且刻意不接进服务入口链：服务端自动重试会重复执行有副作用的 handler，这不是配个段就能定的事。它的诚实接线处是替换仓内三处手写退避（`broker/kafka` 的 `backoffSleep`、`registry/etcd` 的 `retreat`、`bconfig/apollo` 的 `loadWithSyncWait`），属重构面。
- 还不能证明：仓外消费方（bald-admin 及其它下游）是否已在用这些包。本轮只核了 bald-admin 的 berrors 采用面，未核它对 retry/circuitbreaker/oss 的 import，所以「零消费方」的严格含义是**仓内零消费方**。

**接线半（选项 1 的 Discovery 分支）已落地**：owner 后续口径是「接线吧，用 bald-admin 作为消费者」，所以这里不是标注，是真接。

- 修法（六层；1–4 是 source-compatible 的新增，5–6 是 resolver 内部**行为变更**、非 API 变更）：
  1. **读取口**：`App.Registrar()`（`pkg/appkit/appkit.go:342`）。此前段 Provider 产物经 `SetRegistrar` 只落在 `App` 私有字段上，装配好的注册中心对外**没有读取口**——消费方要 Discovery 只能按配置重连一次。
  2. **地址判据**：`usesDiscoveryTarget`（`transport/gateway/discovery.go:33`）把「是否走发现」交给地址字符串自己表达，契约字段 `backend_grpc_addr` 仍是 `string`，不改类型、不加新段；判定与 `DiscoveryScheme` 同文件，避免规则两处漂移。契约注释同步写下两种形态（`bconf/proto/bootstrap/v1/server.proto` 的 `Gateway.backend_grpc_addr`，随 `task proto-contract` 重生成 `gen/go`，`task bconf-verify` 含生成物一致性检查，绿）。
  3. **优先级在拨号点裁决**：`GatewayServer.resolveDiscovery`（`transport/gateway/gateway_server.go:129`）＝**显式实例 > 惰性来源 > fail-fast**。新选项 `WithDiscoverySource`（`:60`）是「需要时才取」的闭包——只有 addr 真用了 `discovery://` 且没显式 `WithDiscovery` 才被调用。这层延迟不是装饰：`scope.App` 是活指针，注册中心可以在 server 段 `Build` 之后才被 `SetRegistrar` 换上（NewApp 路径把注册中心交给应用自持即如此），`Build` 时派生会把「那一刻没有」固化成错误；而「谁被真正用上」只能按地址判，不能按选项顺序猜。addr 用 `discovery://` 而来源缺失时 **Start 直接失败并报明确错误**，不再让 gRPC 回落到默认 dns resolver 去解析一个不存在的主机名。
  4. **段驱动装配**：`gatewaycontract.AppProvider` 改为保存构造参数（`register` + `opts`）而不是预绑工厂，`Build` 时按当次 `ApplyScope` 前置注入 `discoveryScopeOption`（`transport/gateway/contract/provider.go:84`、`contract/discovery.go:35,46`）；来源从 `App.Registrar()`（次选 `Decl.Registrar`）断言 `registry.Discovery`。**消费方因此只改一行配置。**
  5. **端点归一化（接线的实测缺陷，不是一处装饰）**：`grpcAddressOf`（`transport/gateway/discovery.go:133`）——resolver 原样把注册端点交给 gRPC 拨号，而 **bald 自己注册的端点是带 scheme 的**（`transport/grpc/grpc_server.go:143` 交 `grpc://host:port`，HTTP 面交 `http(s)://host:port`，同实例混排）。gRPC 只吃 `host:port`，带 scheme 直接 `too many colons in address`——**即按 §6 判据「已接线」写完前，真实注册形态一个都拨不通**（既有单测都用裸 `host:port` 造实例，恰好绕过了这条）。现在 `grpc://` 剥前缀、其它 scheme 丢弃、裸地址照用；只剩非 gRPC 面的实例整体出局（走「无实例」路径并报订阅错误，而不是给出必然拨不通的地址）。
  6. **首次快照失败不再交回 gRPC（真机跑出来的第二个缺陷）**：`Build`（`transport/gateway/discovery.go:57`）原来把 `GetService` 的错误当成构建失败返回。「后端还没注册」到底是空集还是错误，是各后端的自由——内存与 etcd 返空集，**nacos 直接报 `instance list is empty!`**。而 gRPC 对构建失败的 resolver **不会在近期内重新解析**（实测：客户端只解析一次，45s 窗口内一次都没有重建），所以返回错误等于放弃恢复：实例后来注册上了也接不住。nacos 那轮端到端因此稳定 503 到超时。现改为「`cc.ReportError` 上报原因 + 保留订阅」，与「解析成功但实例为 0」走同一条路径，由 `Watch` 的推送把实例带回来；仍然返回错误的只剩两种确定不可能的情况——服务名为空（配置写错）与 `Watch` 建不起来（没有推送通道，重建 resolver 是唯一恢复路径）。**这条是「网关先于后端启动」这一常见时序的前提**，而 nacos 正是 bald-admin 用的注册中心。
- 验活：`transport/gateway`（含 `contract`）与 `pkg/appkit` 全量 `-race` 绿；判据共 **23 条新测试**（+1 条改写）——网关侧 10 条（`discovery://` 无来源 fail-fast、来源返回 `(nil,nil)` fail-fast、来源报错使 Start 失败并 wrap 原错、惰性来源拨通、直连 addr 不调来源、显式实例胜过来源、带 scheme 端点归一化、全是非 gRPC 面时报「无实例」、按真实注册形态拨通进 READY、**首次快照报错后仍能被推送救回**），装配侧 7 条（真 `grpc.Server` + `inmemory.Registrar` 端到端进 READY、`Decl.Registrar` 路径、无注册中心 fail-fast、注册中心不实现 Discovery 时 fail-fast、直连不需注册中心、显式 `WithDiscovery` 不被要求装配注册中心、`HTTPProvider` 确实不派生），appkit 侧 2 条（`App.Registrar()` 读取口 + 「registry 段阶段 B 产物在 server 段 `Build` 时可见」的时序不变量），**端到端 4 条**：
  - 消费者闭环 `_example/bald/internal/bootstrap/discovery_e2e_test.go`（默认构建，进 `task verify` 清单）起真应用——`appkit.register` 把 `grpc://` 端点注册进 inmemory，配置里 `backend_grpc_addr: discovery:///bald-demo`，REST 打到转码面拿到 `hello, discovery`。
  - 生产注册中心 `_example/bald/internal/bootstrap/discovery_registry_e2e_test.go`（**opt-in**：`-short` 跳过、地址不可达即 `t.Skip`，所以 CI 的 `-short` 作业不会因此变红；文件头给了可复制的 etcd/nacos 起法与 `BALD_E2E_ETCD_ADDR`、`BALD_E2E_NACOS_ADDR` 覆盖）——etcd 一轮、nacos 一轮，加一条把「nacos 的服务名带 `.grpc` 后缀」这个命名事实单独钉住的判据（裸名解析到 0 个、`<名>.grpc` 解析到 1 个且端点仍是 `grpc://`）。两轮的注册都刻意把**无人监听的 `http://` 面排在 `grpc://` 前面**，因此「拨通了」同时证明过滤在生效。
- **真机侧的状态机**（etcd、nacos 各跑一遍，四条断言按序）：注册前 503 → 注册后 200 且拿到 `hello, registry`（greeting 带回本用例自带的后端，排除了打到注册中心里残留的同名实例；Watch 推送 + 端点过滤同时成立）→ 注销后**仍 200**（钉住「空快照不下发」这条既定语义，不是遗漏）→ 后端进程下线后 503。
- **21 处改坏点逐条变异验证**（fail-fast 三个分支、scheme 判据、惰性/显式优先级、resolver 未安装、`App`/`Decl` 两条取源、两个错误分支、scope 选项被丢、`HTTPProvider` 误接线、`App.Registrar()` 返回 nil、把阶段 B 循环移到 late 钩子之后、scheme 不剥、非 gRPC 面不丢、示例 e2e 依赖解析、**删空快照守卫**、**首次快照失败改回返回错误**）——每条都是「基线 pass / 变异后恰好对应判据转红」，时序那条精确红在 `seen == nil` 的断言上；真机三条红法分别是：不剥 scheme → etcd 轮 `dial tcp: address grpc://127.0.0.1:… : too many colons in address`；删空快照下发守卫 → etcd 轮阶段③ `bad resolver state`；快照报错返回给 gRPC → 单测红 `Build(discovery:///svc): gateway: discovery svc: instance list is empty!`（这一条原先只能靠 nacos 那轮 45s 超时暴露）。`golangci-lint` 三模块 0 命中、`gofmt` 干净、`go mod tidy` 无差异、`task verify`（92 模块 + example）exit=0。
- 刻意没做：① `gatewaycontract.HTTPProvider`（转码面接管 `server.http`）**不派生来源**——它的上游是本进程 gRPC 面自己的绑定地址，填 `discovery:///` 是配置错误，测试 `TestHTTPProviderDoesNotAutoDeriveDiscovery` 把这条钉住；② 裸工厂 `contract.Provider` 不自动派生（它没有 scope，看不到装配作用域），用它的人显式传 `gateway.WithDiscovery`；③ 不改 resolver 的负载均衡语义——多实例交给 gRPC 默认 `pick_first`，是故障转移不是分摊流量，要 round_robin 得在拨号侧另配；④ **来源只在 Start 取一次**，不做「注册中心被换掉后重取」：resolver 的生命周期跟 `ClientConn` 而不是跟装配，Start 之后再 `SetRegistrar` 换实例，已建连接仍指向旧实例。现契约下这条路是闭合的——registry 段既没有 `ReloadReconcile` 也没有 reconciler（reconciler 只由 domain 节点与组件注册，`pkg/appkit/bootstrap.go:508,534`），热更新真重建的只有 logger 段，所以换注册中心本来就要重启进程；⑤ 不动 `transport/http` 与业务自拨 gRPC 客户端（那是另一个消费面，需要独立的 client 抽象）；⑥ 不手工写 CHANGELOG 条目——该文件由 `bald release changelog` 从提交生成。
- 还不能证明：① **`Watch` 真机验证只覆盖四个生产后端里的两个**——etcd、nacos 各跑通上面那台状态机（全量快照推送、断线后自愈的 `reWatch` 只读到代码，未被本轮触发）；**consul、kubernetes 仍是代码阅读**。且两轮验的都是「客户端主动注册/注销」这条正向：nacos 服务端健康检查踢掉实例、etcd 租约过期后是否推空集，都没有造出那个故障场景，也未验多实例并发变更下的顺序。② **bald-admin 的真实消费未跑通**——它 pin `bald v0.34.0` 且零 `replace`，本次新增的 API 要模块 tag 才可用，而 tag 是对外共享状态、需 owner 决策；③ 本轮改动的 CI 结果（提交后以流水线为准）；④ nacos 那轮是在 `--platform linux/amd64` 模拟下跑的（官方镜像无 arm64 manifest），功能语义不受影响，但不是原生 arm64 的时序。

**熔断半（选项 1 的 circuitbreaker 分支）已落地**：`retry` 仍按上面的口径留在 reference-only，circuitbreaker 的接线走的是「契约段 → 两面中间件 → bundle → appkit 钩子」这条已为限流/超时铺好的通路，因此不需要新机制。

- 修法（五层）：
  1. **契约段**：`server.http.middleware.circuit_breaker`（`error_threshold`/`min_requests`/`open_duration_ms`/`window_ms`/`status_code`，字段号 8）、`server.grpc.middleware.circuit_breaker`（前四个，**不带 `status_code`**）。`Validate` 不碰它——校验放在翻译点，两面共用同一个构造函数，避免两处口径漂移。
  2. **桥**：`bundle.CircuitBreakerFromMiddleware`/`GRPCCircuitBreakerFromMiddleware` → `*ginmw.CircuitBreaker`，缺段返回 `(nil, nil)`，非法值启动期 fail-fast；`close` 进聚合 closer，停机路径调。
  3. **中间件**：`ginmw.CircuitBreakerMiddleware`（拒绝 → 可配状态码，默认 503；失败口径「响应码 ≥500」，4xx 不记）、`ginmw.NewCircuitBreaker`（建底层 hystrix，0 值透传取底层默认）、`grpcmw.CircuitBreakerInterceptor`（拒绝 → `UNAVAILABLE` + `Reason=CIRCUIT_OPEN`；`IsFailureStatus` 认 berrors Code 再看 `status.FromError`，`Internal`/`Unknown`/`Unavailable`/`DeadlineExceeded`/`DataLoss` 记失败，`ResourceExhausted`/`Canceled` 不记）。
  4. **bundle**：`CircuitBreaker(cb)` 驱动两面、`GRPCCircuitBreaker(cb)` 只驱动 gRPC unary——覆盖关系与限流同款（只给 HTTP 那段则两面共用同一个熔断器；两个都给就是两个各自计数的熔断器，不存在后写静默盖前写）。链序固化在 `Authz → RateLimit → CircuitBreaker → Timeout`。
  5. **装配**：既有契约钩子 `appkit.WithHTTPContract`/`WithGRPCOptionsContract` 原样吃新段，生成脚手架不需要改形状——这是限流那轮把「谁读段」收口后的直接收益。
- 四条口径（各对应一类会静默失效的形状）：
  - **opt-in**：缺段即不挂。默认开会让「一次 5xx」在没人要求的场合变成整站拒绝。判据：`TestE2E_HTTPCircuitBreakerAbsentDoesNotTrip`。
  - **链序不能更外也不能更内**：更外（认证之前）匿名流量就能把失败率打满、替所有人开窗；更内（限流之前）两层的拒绝互相喂养；更内（超时之内）Timeout 写出的 503 落在它下面，这一层只看见「handler 都成功返回」——而「handler 总是超时」正是熔断该抓的形状。判据：`TestCircuitBreaker_GinChainPosition`/`_GRPCChainPosition`，端到端用「timeout 改 504、熔断留 503」把两面的拒绝码分开，第三发的 503 同时证明接线与顺序。
  - **stream 面刻意不挂**：一条流的「失败」没有一致答案——按建流算则开窗与真实退化无关，按流终态算则一条长流长期占住半开探针、把其它流全拒。判据：`TestGRPCCircuitBreaker_NotOnStreamChain`。
  - **上报义务**：`Allow` 放行即欠一次 `Mark*`，半开的「在途探针」标记只由 `Mark*` 复位。所以 panic 路径必须在 defer 里上报后再原样抛出——少了那次上报，熔断**永久拒绝**。这是本层唯一一处「写错不会立刻报错」的地方，判据是摘掉 defer 上报的变异必须转红（gin 侧红在开窗断言、grpc 侧红在记账断言），gin 那条原来是 `t.Skip`，已换成硬 `t.Fatalf`。
- **接线暴露出的实现缺陷（滑窗按绝对格点清桶）**：`hystrix`/`sres` 的桶下标是 `UnixNano()/桶长 % 桶数`，即绝对格点；旋转时清的范围必须是「自锚点以来新跨过的格」。原实现按「距上一次旋转的时长 / 桶长」推步数，锚点落在格中间时就与绝对格点错开一格：多清会抹掉上一发样本（该开窗时不开、`min_requests` 永远凑不满），少清会让一整圈前的陈旧样本滞留（服务已恢复仍被限）。两处都修了，锚点选择按各自调用面定制——`hystrix` 的评估只在 `Mark*` 里跑，用上一发样本的格号（`lastSample`/`lastIdx`）；`sres` 的 `Allow()` 也会推进窗（读之前必须让窗老化），所以用绝对格号 `lastAbs`。`sres` 的 `State()` 另补一次旋转，消除「读出来还在限流、发出去却放行」的双口径。
- 验活：
  - 判据三层共 **108 条**——算法层 76（`circuitbreaker` 3、`hystrix` 18、`sres` 24、`vegas` 20、`sentinel` 11），中间件层 32（`gin/circuitbreaker_test.go` 11、`grpc/circuitbreaker_test.go` 7、`bundle/circuitbreaker_test.go` 14），端到端 4 条在 `_example`（两面各一条开窗、一条缺席不触发、一条 env 覆盖），且四条已进 CI 的 acceptance 清单（`-run` 命中 0 个测试不再返回 0）。
  - 滑窗判据是**双向**的：「跨过一整圈但不足一窗」那条空档由两条断言夹住——少清一格红第一条（陈旧样本仍算在窗内），过清红第二条（`min_requests` 攒不满）；`sres` 侧另有一条同格两发不被抹掉、一条窗内聚合恰好等于 `min(i+1, bucketCount)`。时序判据一律先对齐绝对格点再构造对象，相位自检不成立时 `t.Fatalf` 报「构造失败」而不是 Skip。
  - 变异：清桶步数改成 `steps := 0`（B'）、少清一格（C'）、`sres` 的三处（M1/M2/M3）、摘掉 defer 上报、拒绝码写死、链序外移——每条基线绿、变异后恰好对应判据转红，`cp` 备份 + 精确复原 + `diff -q` 核对。
  - 诚实记录：`sres` 的 `TestSlidingWindow_LapOldSampleIsReplaced` 在**未修实现**下也是绿的（它钉的是另一个错开方向），本轮用 `git show HEAD:circuitbreaker/sres/sres.go` 复现的红灯只有 `SameBucketSamplesAreNotLost` 与 `State_IdleWindowAgesOut` 两条。
- 刻意没做：① 不给 `vegas`/`sres`/`sentinel` 各配一个契约段——sres 无请求量下限（单次失败就开始概率性拒流，低流量服务不可用）、vegas 按出站 RTT 膨胀判定（入口面没有「下游」可测）、sentinel 要引第三方规则中心；换算法经 `bundle.CircuitBreaker(...)` 代码级注入。契约段要不要提供「实现选择」是开放问题，记在《Bald 熔断设计》。② `Close()` 之后 hystrix 对所有请求回开态，所以 closer 只在停止接收流量之后才有意义——不提供「运行中热关熔断层」。
- 已发版（2026-11-05 批次）：这批改动随 20 个 tag 落地——`bconf/v0.18.0`（契约段）、`pkg/v0.3.0`（两面桥与中间件）、`circuitbreaker/hystrix/v0.1.1` 与 `circuitbreaker/sres/v0.1.4`（滑窗清桶修复）、`transport/{grpc,http}/v0.2.0`，外加根 `v0.38.0` 与 13 个按版本消费它们的适配器/中间层。外部装载校验：`goproxy.cn` 与 `goproxy.io` 上 20 个 tag 的 `list/.info/.mod/.zip` 四端点全 200，零 `replace`、全新 `GOMODCACHE` 的 scratch module 里 `go get` 到计划版本 20/20 一致并编译通过 21 个包（含 `pkg/middleware/{gin,bundle}` 与 `circuitbreaker/{hystrix,sres}`），根 zip 95 条里 `.qoder/`、`pkg/`、`_example/` 各 0 条。所以**仓外经代理消费的示例侧钉到的已是修好的 `hystrix v0.1.1`**，不再是 `v0.1.0`。

---

## 3. 依赖治理

**3.1 根 module ↔ 适配器的模块环** ✅ —— 本次评审的最高风险之一。（环已断，见本节末「修复」；剩下的是适配器单向 require 根的扇入耦合）

修复前的双向拓扑（评审当时的实测，现已变化）：

```
根 module ──require──▶ 适配器（评审当时在根 go.mod 的 require 块里，行号随两轮修改已无意义）
  bootstrap v0.13.3
  transport v0.4.0 / transport/gateway v0.4.1 / transport/grpc v0.1.1
  bootstrap/server.go:15-17   import transport/{gateway,grpc,http}
  bootstrap/health.go:13      import transport/grpc
  pkg/middleware/gin/authn.go:15  import transport/web

适配器 ──require──▶ 根 module（以下四行是评审当时 `v0.34.0` 的快照，行号已随两轮修改失效，现值见「3.1 收口」）
  transport/gateway/go.mod   require github.com/kalandramo/bald v0.34.0
  transport/gateway/contract/provider.go  import github.com/kalandramo/bald/pkg/appkit
  registry/{etcd,nacos,consul,kubernetes}/contract/provider.go  import pkg/appkit
  cache/redis、oss/{minio,s3}、contrib/*、broker/redis、transport/{sse,asynq,cron,tcp,webrtc}  同样
```

实测：**22 个非示例模块直接 require 根 module**（另有 `_example/bald`，合计 23），根 module 自身有 19 条本地 `replace`（17 条聚在 require 块前，另两条是 ratelimit 相关），每个 require 根的适配器也都写了 `replace github.com/kalandramo/bald => ../..`。Go 允许 module 图成环（按包级加载），所以修复前它**能编译**——但代价是：

- 回边被本地 `replace` 遮蔽：改 `pkg/appkit` 的导出 API，根的 `go mod tidy` 与根自己的测试都看不出下游会断；断裂只在**给下游模块 bump require 到不存在的 tag 之前**都不显现。
- 发版顺序变成人工约束。近期提交正是这条链的症状：`ca981b1`「tag 分批推送，避免触发流水线漏跑」、`ec4223f`「归位 bconf 的消费者 require」、`89a6a94`「未发版模块也跟随归位内部依赖」。
- `*/contract` 子包是回边的主要载体（30 个目录），**这也是唯一可下刀的地方**。

- 消解（方向，非承诺）：把 v2 Provider 所需的最小面（`Provider`/`Node`/`PlanScope`/`ApplyScope`/`Cleanup`/`Option`）抽到一个叶子 module（例如 `pkg/appkit` 独立成 `github.com/kalandramo/bald/appkit`，或新建中立的契约 module），让适配器只依赖叶子而非聚合根；收益是版本追赶下降、下游对根 API 变更解耦。**注意：gateway 那条断掉后图里已无环，这一步是解耦而非断环。**（落地时按最宽切法做了整个 `pkg/`，见下节；当年这句里并列的「`replace` 数量下降」实测不成立，已在收口节撤回。）
- 消解条件：任何适配器不再 `require github.com/kalandramo/bald`（仅剩 `pkg/` 内不参与 Provider 的包可以例外）。

**3.1 修复（gateway 这一条环）✅ 已修**

原评审把这条环写成「22 个模块互相环」是夸大了。按 `go list` 解析每个模块的真实
import（模板字符串里的 import 字面量不算）后实测：环只有一个 3-模块 SCC
`{. , bootstrap, transport/gateway}`，由两条边撑起——

| 边 | 载体 | 现状 |
|---|---|---|
| A `transport/gateway → 根` | `transport/gateway/contract/provider.go` import `pkg/appkit`（Provider 接入面，方向正确） | 保留 |
| B `bootstrap → transport/gateway` | `bootstrap/server.go` 的 `HttpServerProvider` 内建 gateway 分支 + `WithGatewayRegister` + `DriverGrpcGateway` | **已删** |
| C `根 → transport/gateway` | `pkg/appkit` 的 `Decl.GatewayRegister` / `WithGatewayRegister` / `StandardServerProvider` 的网关分支，及其包内测试 import gateway | **已删** |

B、C 是同一套机制的两份实现（命令式能力声明 vs 段驱动 Provider），删掉的是**重复**
而不是能力：转码面改由 `transport/gateway/contract` 的两个段 Provider 表达——
`HTTPProvider(register)` 接管 `server.http`（同端口单面），`AppProvider(register)`
消费 `server.gateway`（独立端口并存）。为让「接管 HTTP」不误伤 gRPC，
`std` 的 server Provider 拆成 `StandardHTTPProvider` + `StandardGRPCProvider`
（段路径末者胜是按 Provider 整体驱逐的，合并成一个节点就会两面一起被顶掉）。

- 配套：`driver` 的准入判据从 bootstrap 移到转码面自己（构造期 fail-fast 不变）；
  `WithHTTP` 与转码 Provider 同时声明且 `driver` 留空仍构造期报错（显式 > 隐式）；
  探针包装随实现迁到 gateway；`--plan` 的 `serverCapability` 不再投影 gateway
  （它现在是真实节点）；codegen 模板与 `_example` 的 `grpcgw` 变体改出新写法。
- 计划节点键随之变化（breaking）：`server` → `server.http` / `server.grpc`。
- 验活：`go list` 图脚本在修复前的同一提交上抓到 SCC（168 条内部边），修复后
  非测试与含测试两遍均为 `NONE (DAG)`（168 / 177 条边）；`_example` 的 `grpcgw`
  变体真实起进程，`GET /v1/greet/bald` 经转码面返回 200，gin 专属路由 `/v1/ping`
  在该面上 404，`/healthz` 200 且挂 Down 检查器后 `/readyz` 503。
- 刻意没做：删掉重复机制就够断环了，没有为其余适配器动刀。`registry/*`、`contrib/*`、
  `oss/*` 等 22 个模块仍然 require 根 module（单向回边，实测图已是 DAG，不再成环），
  它们的版本追赶/编辑期不可见要靠抽 `appkit` 叶子 module 才能消——独立 breaking
  动作，owner 2026-11-02 选定 C-A，落地见下面「3.1 收口」（这一条是第一轮当时的状态，现已不成立）。根与 `bootstrap` 的 `go.mod` 里指向 `transport/gateway` 的
  本地 `replace` 一并保留（已无对应 require，留着是为将来复用不必重新接线）。
- 还不能证明：断环带来的实际收益只有「版本追赶不再成对」这一条，已经过 Linux CI（`5f771e0`：test/race/lint/vuln 全绿）与 `_example` 起进程实测。扇入本身没消：根 module 一次改动仍要让 22 个适配器重编，那要抽 `appkit` 叶子 module 才做得到——已落地，见下节。

**3.1 收口（`pkg/` 独立成 module）✅ 已落地，发版待 owner**

按上面的「消解条件」动刀，取最宽的切法：整个 `pkg/`（22 个包）成为 module
`github.com/kalandramo/bald/pkg`。**import 路径一字未改**——`github.com/kalandramo/bald/pkg/appkit`
本来就是嵌套路径，Go 按最长 module 前缀解析，所以变的只有各模块 `go.mod` 的 require/replace 指向，
Go 源码零改动（除下面第 ① 条）。根 module 里 `pkg/` 之外唯一被适配器 import 的包
`observability/metricshttp` 随迁为 `pkg/metricshttp`（`git mv` 两个文件，内容不动）——不迁它就仍要
单独 require 根，消解条件破功。

实测（同一份 Tarjan 脚本读 `go.mod` 的 require 边；改前取 `git archive main` 快照，改后取工作树）：

| 量 | main | 本分支 |
|---|---|---|
| `go.mod` 总数 / CI 派生 module 数 | 93 / 92 | 94 / 93（新增 `pkg`）——现值 95 / 94，见 §3.4 |
| 根 module 内的包（`go list ./...`） | 27 | 4（`cmd/bald` + `internal/{codegen,docscheck,release}`）——现值 1，见 §3.4 |
| require 根 module 的非示例模块 | 22 | **0** |
| require `github.com/kalandramo/bald/pkg` 的模块 | — | 23（22 适配器 + `_example/bald`） |
| 根自己 require 的同仓 module | 13 | 1（`bconf`）——现值 0，`bconf` 的 require 随 CLI 迁进 `cli`，见 §3.4 |
| module require 边总数 | 311 | 312 |
| 大小 >1 的 SCC | 无（已是 DAG） | 无（仍是 DAG） |
| 全仓本地 `replace` 条数（除 `_example`） | 410 | **423** |

- 改了什么：① `observability/metricshttp` → `pkg/metricshttp`，并改掉 `contrib/observability-otlp/contract`
  的 import 与 5 处旧路径文字（`pkg/appkit/observability{,_test}.go`、`contrib/observability-otlp/metrics`、
  `Bald 指标设计.md`、`flows.md`）；② `pkg/go.mod` 新建（13 条本地 `replace`，seed 根的兄弟 require）；
  ③ 22 个适配器 + `_example/bald` 的 `go mod edit -droprequire/-dropreplace github.com/kalandramo/bald` 换成
  `bald/pkg` + `replace => ../../pkg`，逐个 `go mod tidy`（23/23 无失败）；④ 根的 require 收敛到 `bconf`，
  原先为它加的 `bald/pkg` require/replace 实测是死的（根不再 import `pkg/*`），已删；
  ⑤ `docs/architecture/module-deps.dot` 的 22 条红边目标改为 `pkg` 并重渲染。
- 验活：`task verify`（`scripts/list-modules.sh` 派生的 93 个 module + `_example`，每模块一次
  `go mod tidy -diff`）**exit=0，0 FAIL，0 diff**；`go test ./internal/docscheck/` 过（`module-list` 围栏
  已加 `pkg` 行，92→93 双向断言不响即证明围栏真的在数）；`pkg` 与 `_example/bald` 各自 `go build ./...` + `go vet ./...` 干净。
- 验活（其余三道闸门，本机）：`task race` 覆盖 93 个派生 module 减去 `.github/race-skip.txt` 的 1 个
  （`log/aliyun`，上游 SDK 自带竞争）= 92 次 `-race`，**0 FAIL、0 `DATA RACE`、exit=0**，新 `pkg` 在清单内。
  `task lint` **首跑是红的**，红在我这次新增的代码上：`internal/docscheck` 的 `importBlockRe` 用双引号串写正则，
  staticcheck `S1007` 要求 raw string；改成反引号后重跑 93/93 模块 `0 issues`、exit=0。改完不放过这条改正
  可能带来的漏判——重新变异一次：往 `pkg/appkit/observability.go` 的 `import (...)` 块塞
  `github.com/kalandramo/bald/internal/codegen` → `TestPkgModuleIsLeaf` 立刻红，复原 → 绿。
  （该包现已随 §3.4 走 B 迁到 `cli/internal/codegen`；这条变异是当时那轮的实测记录，
  今日要复跑请改用仍归根 module 的 `github.com/kalandramo/bald/internal/docscheck`。）
- 验活（`task vuln`）：本机 exit=201，红在 `ai/eino`——4 条**标准库**漏洞（GO-2026-6603/6611/6613/6617，
  `net/http` 与 `net/http/internal/http2`，官方在 go1.27.2 修），而本机是 go1.27.1、CI 的 vuln 作业是
  `.github/workflows/ci.yml` 里的 1.27.3。与本次拆分无关有两条证据：`ai/eino` 本轮 0 个文件改动；取
  `git archive main` 快照在同一个 `ai/eino` 跑 `govulncheck` 得到**同样这 4 条**。用 CI 的 1.27.3 工具链
  （`GOTOOLCHAIN=go1.27.3`）重跑：`ai/eino` → `No vulnerabilities found`，新 `pkg` module → 0 条可达；
  按 1.27.3 跑**全量** `task vuln` → 93 次扫描、exit=0。也就是说本机那条红是工具链版本差，不是这批改动带来的。
- 最后按 CI 的口径（`GOTOOLCHAIN=go1.27.3`，与 `.github/workflows/ci.yml` 的 `go-version` 一致）重跑了一遍**全量 `task verify`**：
  96 次 `go mod tidy -diff` + 各模块 build/vet/`test -short`，**exit=0、0 FAIL、0 diff**。
  踩到的环境坑（接手要用 1.27.3 时同样会碰到）：`GOTOOLCHAIN=go1.27.3` 与 `GOSUMDB=off` 互斥——
  工具链本身是 `golang.org/toolchain` 这个 module，关掉校验和库会让它拒绝解包
  （`verifying module: checksum database disabled by GOSUMDB=off`，`task verify` 因此在第一个测试步骤就 exit=201）。
  走 goproxy.cn 时把 `GOSUMDB` 设回 `sum.golang.org` 即可，代理会一并转发校验数据。
- 新增三条闸门（`internal/docscheck`），逐条变异验证后才复原：
  - `TestNoModuleRequiresAggregateRoot`——任何非示例模块的 `go.mod` 再出现 `require github.com/kalandramo/bald`
    （含 `// indirect`）即红。变异：给 `cache/redis` 加回 `require github.com/kalandramo/bald v0.34.1` →
    恰好红在它身上；复原后 `go mod tidy` 连那两行 go.sum 一起清空。这条是必需的：本地 `replace` 让违反它的代码
    照样编译，CI 的 build/vet/test 全绿，扇入复辟只在发版时显现。
  - `TestPkgModuleIsLeaf`——① 根 module 的包集合必须恰为 `cmd/bald` + `internal/{codegen,docscheck,release}`，
    ② `pkg/` 下任何 `.go` 的**真实 import**（不含注释与模板字符串）不得归到根 module，另带「扫到 <50 个文件即
    `t.Fatal`」的防空转。变异三次：往 `pkg/id/uuid.go` 塞 `_ "…/internal/release"` → 红在②；临时在根下建一个空的 `scratchleaf` 包
    → 红在①「预期外的包」；把 `importBlockRe` 从双引号串改成 raw string（`task lint` 的 `S1007` 要求）之后，
    再往 `pkg/appkit/observability.go` 的 `import (...)` 块塞 `github.com/kalandramo/bald/internal/codegen` →
    仍红在②，证明改正没把括号块那条解析路径改坏。
    §3.4 走 B 之后 ① 这条独立成 `TestModulePackageSets`（按属主 module 逐集合比对，根只剩
    `internal/docscheck`，`cli` 是 `cli/cmd/bald` + `cli/internal/{codegen,release}`），② 留在 `TestPkgModuleIsLeaf`。
  - `TestModuleOfImport`——锚定归属判定的最长前缀语义，故意含一组**乱序**输入。这一条当场抓出我第一版实现的
    真缺陷（拿候选 module 路径长度与「目录串」长度比，`transport/grpc` 会被后面的 `transport` 顶掉）；改成
    `bestLen` 后，把判据换成「后到者胜」立刻红在乱序组上，证明比较真的在起作用。
- **收益比原评审写的窄，且有一条反向代价**：appkit/middleware/audit 等现在在 `pkg`，改它们照样要 22 个
  适配器追 require，只是追的对象从 `bald` 换成 `bald/pkg`——**全仓 `replace` 不降反升（410→423）**，
  当年那句「收益是 `replace` 数量与版本追赶下降」的前半实测不成立，撤回。真正消掉的是：
  `cmd/bald`、`internal/*` 这些只有本仓工具在用的包，改动不再产出适配器必须追的根 tag；
  根的 `go mod tidy` 也不再需要 13 条兄弟 require 撑着。这是解耦，不是断环（图本来就是 DAG）。
- 新出现的下游后果（已实测复现，不是推测）：根的最后一个含 `pkg/*` 的 tag 是 `v0.34.1`，那份 zip 里包还在。
  同一个 build 里同时 require `github.com/kalandramo/bald v0.34.1` 与 `github.com/kalandramo/bald/pkg` 会直接
  `ambiguous import: found package github.com/kalandramo/bald/pkg/appkit in multiple modules`。
  所以下游迁移是**替换**而非新增：去掉对根的 require，换成 `pkg`；`bald-admin` 这类本地联调还需补一条
  `replace github.com/kalandramo/bald/pkg => ../../bald/pkg`（该仓本轮未动）。
  冲突边界已在 `pkg/v0.1.0` 发出后实测收窄（2026-11-03，干净 scratch module、零 replace）：**只有消费者自己
  直接 require 根**才报 ambiguous；根经兄弟模块传递进闭包时不报错——同 build 里
  `bald/pkg v0.1.0` + 拆分前的 `transport/gateway v0.4.2`（其 `contract` 正是 import `pkg/appkit`）编译通过，
  `go list -f '{{.Module}}' github.com/kalandramo/bald/pkg/appkit` 归到 `bald/pkg v0.1.0`（长路径模块取得该前缀
  下的解析权），tidy 后根连 require 都没留下。判据因此是「自己 go.mod 里不许留根」，不是「闭包里不能出现旧根」。
- 下游那条已经实测（2026-11-03）：`bald-admin/backend` 在补 replace 之前 `go build ./...` 全仓失败，报错是
  `github.com/kalandramo/bald/pkg@v0.0.0-00010101000000-000000000000: ... i/o timeout`——它把 `../../bald` 的
  各模块 `replace` 到了本仓检出，而那些模块现在 require `bald/pkg`，而 `bald-admin` 里没有对应的 replace，
  于是 Go 去代理找一个根本不存在的伪版本。补上 `replace github.com/kalandramo/bald/pkg => ../../bald/pkg`
  后 `go mod tidy` + `go build ./...` + `go vet ./...` 全过，且 tidy 自动把 `github.com/kalandramo/bald v0.34.1`
  这条 require 删掉了（`bald-admin` 只用得到 `pkg/*` 路径）——即上面那句「替换而非新增」在真实下游仓库上成立。
  **这条 replace 只进本地联调叠加层**：bald-admin 仓库 backend 模块提交进去的依赖形态是「零 replace、按 tag 消费」，
  它要改成 require `github.com/kalandramo/bald/pkg v0.1.0`——该 tag 已于 2026-11-03 发出（见下），前提成立。
- 已发（2026-11-03，owner 授权**只发 `pkg` 一个模块**）：`pkg/v0.1.0` 指向 `8314887`，CHANGELOG 条目由
  `bald release changelog` 生成（198 条提交——首发的模块没有前置区间，工具按目录归属把计入 `pkg/` 的历史全量列出）。
  发版工具走的是同一套守卫：过滤后的计划文件（`.bald/` 已 gitignore）+ `release tag --apply --push`，
  没有手工 `git tag` 绕过「tag 不存在 / 工作区干净 / HEAD 相对计划只多发布改动」三道检查。
  **发版前的装载校验**（临时 worktree，删掉 `pkg/go.mod` 的 13 条同仓 `replace`，逼 Go 从 goproxy.cn 解析真实 tag）：
  `go build ./...` 退出码 0；13 条同仓 require 全部命中已存在 tag（脚本逐条比对 `git tag --list`，missing = none）；
  `go mod tidy` 只补一条 `transport/gateway v0.4.2 // indirect`，版本一个没被抬高——即 `pkg` 在 HEAD 的源码
  不依赖任何未发版的兄弟 API，这是「能不能单发」的真正判据，不是「它自己能不能编译」。
  **其余 30 个模块已发（2026-11-03，owner 授权整批发版）**：`9cf17db` 一次发布根 `v0.35.0` 加 29 个
  适配器/中间层，23 个消费者的零伪版本由 `release sync --write` 归位到真实 tag，归位后全仓同仓 require
  逐条命中「已发布 tag ∪ 本批 next」，零伪版本占位 0 条；tag 前 HEAD 那轮 `ci` 五作业 completed/success。
  **但根 `v0.35.0` 事后被证明装不出来**（`.qoder/` 导出文件名让代理建不出 zip），已由 `v0.36.0` 取代，
  根因、判据与新增的 tag 闸门见 §3.3——这条是本节「还不能证明 ①（published 形态整条链路）」被实测打脸的地方。
  `_example/bald:17` 也**没有**改成按 tag 消费：它在 CI 里测的是工作树代码，钉到 tag 等于把端到端降级成测已发布版本；
  它需要补齐对被顶版兄弟的 `replace`，否则整批发版在 tag 前就绿不了，见《多模块发布与变更日志》的叠加层前置条件。
- 刻意没做：① 没有只抽 appkit 的最小面（选项曾列 `Provider`/`Node`/`PlanScope`/`ApplyScope`/`Cleanup`/`Option`
  六个符号）——那要把 `pkg/appkit` 从 `pkg/middleware` 等既有互相依赖里摘出来，改动面比整切更大；
  ② 没动 `bald-admin`；③ 没重跑 `.dsl` 的 C4 机器校验（本机 JDK/Maven 已失踪，`.dsl` 未改）。
- 已证（发版后复跑，2026-11-03）：「published 形态」这条外部链路装得上——干净 scratch module、零 `replace`、
  `GOPROXY=https://goproxy.cn,direct`，`go get github.com/kalandramo/bald/pkg@v0.1.0` 从代理下载成功，
  import `pkg/appkit`、`pkg/audit`、`pkg/metricshttp`（含随迁那个）`go build ./...` 退出码 0。
  原来的未验证面「代理上 `bald/pkg` 至今 404，外部消费者按 tag 装不上」由此收口。
- 还不能证明：① Windows 周界（`ci-windows` 只跑 3 个 module、按周/手动 dispatch，`pkg` 不在其中，这批 go.mod 没在
  Windows 上执行过）；② goproxy.cn 与 direct 之外的镜像同步时延；③ 发布提交 `8314887` 推上 main 后的远端作业以
  流水线为准，不在本文档里宣称。
- 顺手发现（不在任何闸门范围内，本轮未修）：`_example/bald` 被清单谓词 `-not -path "./_example*"` 排除在
  lint/race/vuln 之外，按 1.27.3 单跑 `golangci-lint` 得 2 条既存问题——`user/cmd.go` 的 `p.Close` 未查返回值
  （errcheck）、`internal/bootstrap/e2e_helpers_test.go` 的 `newClient` 无人用（unused）。登记在此，免得下次
  当成拆分引入的新问题；它们与本批改动无关（`_example/bald` 只动了 `go.mod` 的 require/replace）。

**3.2 依赖卫生：多 major 并存（低）** ✅ 脚本实测

此前写成「未见问题」是错的。逐模块解析 `go.mod` 的 `require` 块后实测：**21 个 module 同时 require 同一库的两个 major**，全部带 `// indirect`，主要是三组：

| 库 | 并存的路径 | 出现在 |
|---|---|---|
| `go.yaml.in/yaml` | `/v2 v2.4.4` + `/v3 v3.0.5` | 21 个 module（含 `_example/bald`） |
| `github.com/alibabacloud-go/tea-utils` | `v1.4.4` + `/v2 v2.0.7` | 20 个 module |
| `github.com/pelletier/go-toml` | `v1.9.x` + `/v2 v2.2.4` | 根 module、`oss/minio`、`_example/bald` |
| `github.com/cenkalti/backoff` | `/v4 v4.3.0` + `/v5 v5.0.3` | 仅 `contrib/observability-otlp` |

- 判断：Go 按 module path 区分 major，两个 major 在同一个 module 图里合法，且这里绝大多数来自上游 SDK 的传递依赖。它不构成正确性问题，代价是同一进程可能加载两套实现（二进制体积、行为差异、类型互不兼容）。
- 未测：本结论只覆盖各 `go.mod` 的 `require` 行，未展开 `go mod graph` 的全量图，也未测「同一服务的最终二进制里是否真的同时链接两个 major」。
- 消解：不值得为此加 `exclude`/`replace`（会与上游 SDK 冲突）。可加一条只读报告（如 `task deps-report`）让新增共存可见；真正要跟的是「同一调用链上同时出现两个 major」。

**3.2 收口（只读报告 + 把「未测」补成实测）**：

- 修法：`scripts/multi-major.sh`（逐 module `go mod edit -json`，按去掉尾部 `/vN` 的基础路径分组，列出同组内 ≥2 个 major 的组合）+ Taskfile `deps-multi-major`。明确**不是闸门**：不改 go.mod、不加 exclude/replace、退出码恒 0，`platforms: [linux, darwin]`（要 POSIX awk）。
- 实测（本轮）：**43 条 = 21 个 module**，按库分布 `go.yaml.in/yaml` 20、`alibabacloud-go/tea-utils` 20、`pelletier/go-toml` 2、`cenkalti/backoff` 1。与评审初值差一条：初值里那条 `nacos-sdk-go v1+v2` 只存在于 `_example/bald`，而清单谓词排除 `_example*`，报告按 CI 同一条派生所以不含它。**这条现在整库消失**：示例的 nacos 配置源改走仓内 `bconfig/nacos`（只用 SDK v2），v1.1.6 的 require 随之删掉——所以「差一条」的差值也一并归零，`task deps-multi-major` 重跑仍是 43 条（谓词本来就没算示例）。
- **把「二进制里到底链了几个」这一条从推测变成证据**（原「未测」项）：`go list -deps ./...` 在根 module 与 `_example/bald` 各自展开后，`go.yaml.in/yaml/v2` + `/v3` **与** `pelletier/go-toml` + `/v2` 都真的同时进入依赖集——即同一二进制确实链接两套实现；`cenkalti/backoff/v5` 则没有（被 MVS 剪掉），`tea-utils` 两个 major 都不在根的依赖图里（只在 `require` 清单里挂着）。
- 因此优先级口径落定：值得跟的不是「require 里并存」（43 条里多数是上游 SDK 传递），而是**「`go list -deps` 同时命中两个 major」这 2 条**——它们是能观测到行为差异/类型互不兼容的那一类。后续新增按此判据筛。
- 刻意没做：不加 `exclude`/`replace`（与上游 SDK 冲突，且会掩盖真实传递链）；不把报告接进 CI 阻断（它今天 43 条全属已知，接成闸门只会让 CI 长期红）。

**3.3 发布出去的版本装不出来：模块 zip 的文件名判据不在任何闸门里**（高，已修）

- 现象：`bald release` 打出 30 个 tag 后做外部装载校验，`goproxy.cn` 对根模块回
  `404 not found: create zip: .qoder/repowiki/knowledge/zh/Bald 框架仓库根（契约与适配器生态）/…: malformed file path …: invalid char '（'`。
  同一时刻本地 `task verify` 全量绿、远端 `ci` 五作业全绿——**这条路径没有任何闸门覆盖**：Go 的
  build/vet/test 都不校验文件名能否进模块 zip，只有代理在建 zip 时校验。
- 判据（权威出处，不是镜像私有规则）：`golang.org/x/mod/module` 的 `fileNameOK`——ASCII 只放行字母数字
  与 `!#$%&()+,-.=@[]^_{}~ `，非 ASCII **必须是 `unicode.IsLetter`**。中文汉字过关（是字母类），
  全角括号 `（）`、顿号 `、` 不过关（标点类）。官方代理与所有镜像同一条判据，所以这不是国内镜像特例。
- 范围实测：`git ls-tree -r v0.35.0` 共 1662 条路径，其中 **230 条不合规，全部落在 `.qoder/` 下**（知识中心
  导出的目录名带全角括号），非 `.qoder` 的不合规路径 0 条。所以只污染**根模块**：其余 29 个子模块的子树
  里没有这些路径，逐个问代理已实测 25 个 `HTTP 200` 可装载，剩下的是回源延迟（`temporarily unavailable`）
  而非 zip 问题。`v0.34.1` 早于导出提交，所以 `v0.35.0` 是首个被污染的根 tag。
- 修法（两层）：① `.qoder/` 加进 `.gitignore` 并 `git rm -r --cached`——只脱离索引，磁盘副本保留；
  生成物本就不该进发布模块的文件树。② `bald release tag` 加路径闸门（`cli/internal/release/tag.go`
  的 `checkZipPaths`）：按 `plan.Changes` 的模块归属，把 `HEAD` 每个待发模块的文件路径逐段过判据，
  不合规即拒绝且**一个 tag 都不落**。判据用 stdlib 复刻而非 require `x/mod`——根模块的兄弟 require
  刚收到 0（§3.1），不值得为一个发布期检查再引入依赖，代价是上游若放宽需人工跟。
- 验活（不是只跑单测）：把新 `tag.go` 放到 `9cf17db` 那份坏的文件树上跑 `release plan && release tag`，
  输出 `230 条文件路径进不了模块 zip`、`git tag --points-at HEAD` 为 0，与独立脚本的 230 条计数一致。
- `v0.35.0` 怎么处理：按「tag 一经推送不可重发」保留，不做删除也不移动（代理缓存的失败记录删不干净，
  移动会让已缓存它的下游拿到错版）；由 `v0.36.0` 取代（`feat(release)` 按 Conventional Commits 升 minor，不是 patch），本条即是它的出处说明。
- 发版后的外部装载校验（2026-11-03 复跑，收口上面那句「剩下的是回源延迟」）：干净 scratch module、零 `replace`、
  `GOPROXY=https://goproxy.cn`、`GOSUMDB=off`、新 `GOMODCACHE`，`go get` 本轮 31 个 tag（根 `v0.36.0` + `pkg/v0.1.0`
  + 29 个适配器/中间层，不含 `v0.35.0`）退出码 0，逐条比对 `go.mod` 落到的版本与计划版本 31/31 一致，
  `go mod verify` 通过，import `pkg/stringcase` + `ratelimit/tokenbucket` `go build ./...` 与 `go run` 均退出码 0。
  根 `v0.36.0` 的 zip 直接取回解包：139 条，`.qoder/**` 0 条、`pkg/**` 0 条——zip 判据缺陷与拆分前的 ambiguous
  import 两条都不在这份发布物里。`v0.35.0` 仍确定性 404 `create zip: malformed`，与「保留不用」一致。

**3.4 官方 CLI 按 tag 装不出来：`go install` 拒绝含 `replace` 的 module**（中，✅ 已修——走 B：CLI 拆成独立 module `cli/`，`cli/v0.1.0` 已发出并在零 `replace` 夹具里实测装得上、跑得通）

- 现象：五处文档——`README.md`、`docs/guide/zh-CN/quickstart/README.md`、《用 bald CLI 起步新服务》、《代码生成工具设计》摘要、`cmd/bald/main.go` 的 doc comment——都写着 `go install github.com/kalandramo/bald/cmd/bald@latest`（或 `@vX.Y.Z`）。这条命令**从未成功过**。本机对三个已发布 tag 各跑一次（`v0.31.0` 在 `pkg/` 拆分前、`v0.36.0`/`v0.37.0` 在拆分后），报错逐字相同：

  ```
  go: github.com/kalandramo/bald/cmd/bald@v0.37.0 (in github.com/kalandramo/bald@v0.37.0):
      The go.mod file for the module providing named packages contains one or
      more replace directives. It must not contain directives that would cause
      it to be interpreted differently than if it were the main module.
  ```

- 判据（取自本机 GOROOT 源码，不是国内镜像私有规则）：`cmd/go/internal/load/pkg.go` 在安装命名包时抓取目标 module 的**已发布 go.mod**，`if len(f.Replace) > 0` 即报错（紧接着对 `Exclude` 同一判据）。它看的是**存在性**，与该 replace 是否真被 build 用到无关——根 `go.mod` 现有 19 条 `replace`，其中只有 `bconf` 一条有对应 require（`go list -deps ./cmd/bald` 实测同仓依赖只剩 `bconf v0.17.0`），另外 18 条是 §3.1 注脚里「已无对应 require，留着为将来复用」的，它们对编译是惰性的，照样让这条命令失败。
- 范围：只坏在 **CLI 分发**这一条路径，库侧不受影响——`go get github.com/kalandramo/bald@v0.37.0` 与 `bald/pkg`、各适配器按 tag 解析都正常（§3.3 的零 `replace` scratch module 已实测 31 个 tag 可装载并编译）。所以这是「官方工具入口的可复制性」缺陷，不是发布物缺陷。
- 为什么长期没暴露：安装路径不在任何闸门里。`task verify` 与 CI 都在带 `replace` 的检出里跑 `go build ./...`，走的永远是本地替换；`bald release tag` 的三道守卫查 tag 是否已存在、zip 路径是否合规、工作树是否干净，不查「文档给的命令装不装得上」。这正是 §8「docscheck 测不出描述过期」的一个实例——只是这次过期的是**命令**而不是描述。
- 上一轮处置（拆分前先止血）：五处安装说明改成源码检出姿势（`git clone` → `cd bald/cli` → `go install ./cmd/bald`），库侧明写按各自模块的 tag 引用。验活：`go install ./cmd/bald` 在本地检出跑通，产物落在 `$(go env GOPATH)/bin/bald` 且 `bald --help` 正常输出 `gen` 四个子命令与 `release`。拆分与发版之后，源码检出退成「改生成器本身」的备选姿势，默认回到按 tag 安装（见下面「兑现」）。
- 防回归（已落地）：`internal/docscheck` 新增闸门 `TestDocsInstallsAreReplaceFree`——Markdown **代码围栏**里出现的 `go install <path>@<ver>`，若 `<path>` 按最长前缀归到本仓某个 module，则该 module 的 `go.mod` 不得含任何 `replace`（与 Go 的判据同一条）。围栏内才算「承诺复制即可跑」，内联 code 与围栏外的裸行不参与——文档要引用那条失败命令做说明（《用 bald CLI 起步新服务》§2 正是这么写的）。四次变异逐条验活、全部复原：① 合成夹具里删掉那条 `replace` → 违规消失，证明判据真在读 go.mod 而非认模块名；② 给夹具的 `bconf/go.mod` 加一条 `replace` → 违规 1 条变 2 条，证明读的是**每个 module 自己的** go.mod；③ 去掉 `inFence` 守卫 → 围栏外那行裸命令也被抓，证明围栏这条边界是承重的；④ 往真实 `README.md` 塞回 `go install github.com/kalandramo/bald/cmd/bald@latest` 的代码块 → 立刻红并报出「19 条 replace」，证明全仓扫描不是空转。
- 修法（owner 选定 **B**，联合开发姿势取 **B-iii**）：
  - **B（选定）**：把 `cmd/bald` + `internal/{codegen,release}` 抽成独立 module `github.com/kalandramo/bald/cli`，安装路径变成 `go install github.com/kalandramo/bald/cli/cmd/bald@vX.Y.Z`。`internal/docscheck` 留在根——它是 `go test ./...` 里的文档闸门，跟着根走才守得住仓库级判据。
  - **A（放弃）**：剥掉根 `go.mod` 的全部 `replace`，同仓未发版的消费改走 `go.work`。代价实测过重：根自己就 require `bconf`，剥掉替换后「bconf 未发版就编不过」，且全仓 423 条 `replace` 里有 19 条在根、其余在 90+ 个适配器里，A 只解得开根这一处，等于把一个仓库级问题按一个模块的规模去修。
  - **B 的联合开发姿势三选一**：i 用 `cli/go.mod` 的目录 `replace`（**直接违背拆分目的**，Go 看的是发布物里 replace 的存在性，一条就够）；ii 每次联调先给 `bconf` 打 tag（不可接受，tag 不可重发）；iii **`go.work` 只留本机、绝不入库，且所有代表 CI 口径的判据显式 `GOWORK=off`**（选定）。iii 的代价是「本机形态」与「发布形态」两套跑法必须被机器区分开，所以 `Taskfile.yml` 与两个 workflow 文件都在顶层写死 `env.GOWORK=off`，`verify-modules` 开头再加一条 `[ "$(go env GOWORK)" = "off" ]` 自检——删掉那条 env 注入而本机还留着 `go.work` 时，任务当场失败而不是绿到发版（实测：有 `go.work` 且未设 `GOWORK` → 判据为假；`GOWORK=off` → 判据为真）。
- 落地形态（本 PR，纯搬迁，无行为改动）：`cli/go.mod` 模块路径 `github.com/kalandramo/bald/cli`，`go 1.27.1`，require `bconf v0.17.0` + cobra + protobuf + yaml，**零 `replace`**；根的 4 条同仓 require 随 CLI 迁走，根现在 `go list ./...` 只剩 `internal/docscheck` 一个包、0 条 require。派生清单 93 → 94 个 module（新增 `cli`），`go.mod` 总数 94 → 95。
- 配套改写（同一 PR，缺一不可）：① `Taskfile.yml`——`test`/`build`/`acceptance` 各加一条 `dir: cli` 的腿，四个 `release-*` 任务改在 `cli/` 下执行并显式传绝对计划路径（`--out/--plan {{.ROOT_DIR}}/.bald/release-plan.json`，否则中间产物落到 `cli/.bald/`，与文档承诺的仓库根 `.bald/` 漂移）；`bald release` 的仓库根定位走 `git rev-parse --show-toplevel`，与 cwd 无关，实测在 `cli/` 下仍指向仓库根。② CI：`ci.yml` 的 `TestTree_*` step 与 `windows.yml` 的两条 codegen step 加 `working-directory: cli`，后者另补一条 `cli module go test -short`——原来这半张覆盖面是随根模块跑的，不补就静默消失。③ `internal/docscheck`：`TestPkgModuleIsLeaf` 的「根包集合」那半条独立成 `TestModulePackageSets`（按属主 module 逐集合比对，含「归集到 <50 个 module 即 `t.Fatal`」的防空转），归属表加 `cli`，另新增本节两条闸门。④ `.gitignore` 加 `go.work`/`go.work.sum` 与 `/cli/bald`——在 `cli/` 里 `go build ./cmd/bald` 会把可执行文件吐在 module 根（实测产物 12MB，`git check-ignore` 命中该条），原来这份落在仓库根、由既有的 `bald.exe`/`bin/` 挡不住同名的无扩展名产物。
- 验活（本机，`GOWORK=off`）：`go test ./internal/docscheck/` → ok（含两条新闸门）；`go build ./...` + `go vet ./...` 在 `cli/` 与根各自干净；`go list ./...` 根 = 1 个包、`cli` = 3 个包；`(cd cli && go run ./cmd/bald gen {app,domain} --spec ../_example/bald/appspec.json --out ../_example/bald --check)` → `clean: 21 files` / `clean: 5 files`。四道全量闸门按 CI 口径复跑：`task verify` → exit=0（94 个派生 module 逐模块 `go mod tidy -diff` + build + vet + `test -short`，含 `cli`，另加 `_example`），`task lint` → 94 次 `0 issues`、exit=0，`task race` → 93 次 `-race`（`.github/race-skip.txt` 排除 1 个）、exit=0 且 0 `DATA RACE`——`cli/internal/codegen` 那条是 51.3s 的未缓存实跑，`task vuln` → 94 次扫描 `No vulnerabilities found`、exit=0（`GOTOOLCHAIN=go1.27.3` + `GOSUMDB=sum.golang.org`，与 §3.1 记的那条互斥坑同姿势）。CI 里 `TestTree_EndToEndTimed`/`TestTree_AuthzClosedLoop` 那条独立 step 在 `cli/` 下实跑 → ok 13.6s；改过的两个作业文件另过 `task ci-workflow-lint`。
- 两条新闸门各自变异验活后才复原：给 `cli/go.mod` 追加一条 `replace github.com/kalandramo/bald/bconf => ../bconf` → `TestCliModuleIsReplaceFree` 红并报「含 1 条 replace」；挪走 `cli/cmd/bald/main.go` → 同一条红在「闸门失去判定对象」；`git add -f go.work` → `TestGoWorkIsNotTracked` 红并报出该文件名，随后 `git rm --cached` 复原、`git ls-files` 里 `go.work` 计数 0。`/cli/bald` 那条忽略也实测过：`cd cli && go build ./cmd/bald` 真的把 12MB 可执行文件吐在 module 根（`git check-ignore` 命中该条）。
- 本 PR 顺带带上 `_example/bald/domains.manifest.json` 的一行修正（`spec` 摘要补到当前声明），是这次跑 `gen domain` 时暴露出来的漂移，成因与修法记在 §8 末条；域生成物五个文件的字节一字未动。
- 代价（如实记下，不要在文档里抹平）：**发布工具发自己**——`bald release` 现在住在 `cli`，用它发 `cli` 的这一版必然滞后一代（`cli/v0.1.0` 只能由上一个版本的 tag 逻辑推出，或人工按同一规则打）；`cli` 是本仓第一个**没有目录 `replace` 兜底**的同仓消费者，改 `bconf` 的生成物后，`cli` 在 `cli/vX.Y.Z` 打出来之前编不过新代码，联合开发靠本机 `go.work`（见上）。CLI 版本与根 tag 不同步：`cli` 从 `v0.1.0` 起独立计数。
- 兑现（2026-11-03 发版批次，`d4396185`）：`v0.37.1`、`cli/v0.1.0`、`pkg/v0.2.1` 三个 tag 一次打出（恰好落在 `tagPushBatch = 3` 的单批内，按 tag 过滤的流水线不会漏 push 事件），五处安装文档同步升格为**默认按 tag**、源码检出降为备选。
- 兑现的证据（全部实测，判据来自本机跑过的命令）：
  - 装载：零 `replace` 的 scratch module 里 `GOPROXY=https://goproxy.cn,direct go install github.com/kalandramo/bald/cli/cmd/bald@v0.1.0` → 退出码 0，`$(go env GOPATH)/bin/bald` 12,498,514 字节，`bald --help` 正常；同一代理上 `cli/@v/v0.1.0.mod` 的 `grep -c replace` = 0（判据的那半边——发布物本身干净——单独看过原文）。
  - 最小路径端到端：用**这个从代理装来的 CLI**在 `/tmp` 空目录跑 README 的那份 appspec → `bald gen app --spec appspec.json` → 生成的 `go.mod` 0 条 `replace` → `go mod tidy` 解析到 `pkg v0.2.1` 等 11 个本仓模块 → `go build ./...` 通过 → `go run ./cmd/demo-svc` 起 2 个 server → `curl :8080/healthz` 返 200。「文档给的命令装得上」与「装上之后生成的东西跑得起来」是两件事，这次两件都验了。
  - 远端：`ci` run 41659194548（head `d4396185`）五作业 `lint`/`test`/`race`/`vuln`/`integration` 全 success；本轮改过的 `windows.yml` 另手动 dispatch 一轮 run 41663096754 → success（它只有 cron 与手动两个入口，不补这一轮，`working-directory: cli` 与新增的 cli 测试步骤要等到下周一才第一次真跑）。
- 发版当轮新记录的代理陷阱（写进了《用 bald CLI 起步新服务》§2）：同一时刻 `cli/v0.1.0` 在 goproxy.cn 是 200，`pkg/v0.2.1` 与根 `v0.37.1` 却先返 `temporarily unavailable`（goproxy.io 三者都 200）。滞后期内 `go get github.com/kalandramo/bald/pkg@v0.2.1` 报的不是「找不到版本」，而是 `module github.com/kalandramo/bald@v0.2.1 found, but does not contain package github.com/kalandramo/bald/pkg`——根模块历史里有 v0.1.0…v0.2.9 这批同名 tag，子模块没被索引时路径解析就落到父模块。这是遮蔽，不是 tag 丢了；换代理或等索引即可，别照字面去「修 pkg」。
- 外部消费者侧的兑现（B-iii PR3，2026-11-04，bald-admin `6d117a3`）：下游仓库改成**零 `replace`、全按 tag 消费**，三个新事实只在真实下游才暴露：① 旧路径 `go run github.com/kalandramo/bald/cmd/bald` 在消费者侧报 `no required module provides package github.com/kalandramo/bald/cmd/bald; to add it: go get …`——拆分对下游是**破面**，凡是指向旧路径的 `go run`/`go install` 都要改成 `…/cli/cmd/bald`（bald-admin 三处：`Taskfile.yml` 的 `gen:domain`/`gen:check` 与 `backend.yml` 的 Domain manifest in sync）。② 迁移是**替换**：`go mod tidy` 会把根模块 `github.com/kalandramo/bald` 的 require 判成不需要而删掉（下游 import 的全是子模块路径，`appkit` 在 `bald/pkg`）。③ **CLI 是纯工具依赖时 tidy 看不见它**：`bald/cli` 不是任何源码的 import，`go mod why` 报「main module does not need package」，tidy 因此删掉它的 require、`go run` 当场断——bald-admin 用 `backend/tools/tools.go`（build tag `tools` 的 blank import）钉住版本。这是「发布工具发自己滞后一代」之外**另一条没预见到**的代价，任何同仓 CLI 的消费者都会撞上。
- 仍未复跑（如实记下）：本机到 github 直连不通，`git clone` 那半段端到端仍没跑过。它现在只影响备选姿势那一半——默认路径（按 tag 装）已在本机走通，且 Go 的判据与 clone 无关。
- 已证（远端）：`e4a61bb`（含 `TestDocsInstallsAreReplaceFree` 的 `d1765b5` 与五处文档改动）的 GitHub run 41632332664 五作业 `test`/`integration`/`lint`/`vuln`/`race` 全部 success。它前一个 head `34ac420` 那轮（41631889618）按 `concurrency` + `cancel-in-progress` 被取消，当时无 tag 待打，作废绿证没有代价。
- 一处刻意不动的过期引用：`bconf/proto/bald/appspec/v1/appspec.proto:109` 的注释仍写 `internal/codegen`。改它要重跑 `buf generate` 并把 `bconf` 拖进本轮发版，而 `bconf/v*` 的 tag 会触发 BSR 契约推送——为一条注释动用共享状态不值。`bconf/gen/**` 是生成物，手改必与 `TestGeneratedMatchesProto`（本机因 `buf` 不在 PATH 而 Skip）对不上，所以只能整体重生成，不能就地补。

---

## 4. 并发与生命周期

**先说好的部分**：`appkit.Run` 是全仓成熟度最高的代码——CAS 重入保护、`Resolve` fail-fast、errgroup 启动失败级联取消、`:0` 端口轮询带上界、`stopAll` 五阶段带逐阶段独立超时、`runHook` recover panic（`pkg/appkit/appkit.go` 的 `Run`/`stopAll`/`runHook`）。停机用未取消的新 ctx（`Run` 主等待之后那个 `stopAll` 调用点的注释明确解释了为什么不能用被取消的信号 ctx）。

**4.1 停机期吞掉反注册错误** ✅ 已修 原状：`pkg/appkit/appkit.go` 两处 `_ = a.deregister(context.Background())`（主停机路径与 afterStart 失败路径），失败时注册中心的残留条目不可见，后果是流量继续打到已停实例——恰是它想避免的事。
- 消解：`logDeregister`（`pkg/appkit/appkit.go`）在两个调用点记录 `appkit deregister failed` + 底层错误，返回值语义不变（停机期没有可返回错误的调用方）。回归由 `pkg/appkit/deregister_log_test.go` 固化：经真实装配路径（`StandardLoggerProvider` + buffer handler）断言两条路径各出一条含底层错误的日志，且 `Run` 仍返回 `nil` / afterStart 原错误。

**4.2 端点上界与 hook 超时不可配** ✅ 已修 原状：`waitForEndpoints` 的 5s 是函数内常量；`defaultHookTimeout = 5 * time.Second` 封顶 before/afterStop 钩子，而 `appkit.StopTimeout(d)`（→ `Decl.StopTimeout` → `stopTimeout`，契约默认 30s）本身是可配的——于是唯一的可调入口管不到这些阶段。
- 影响：慢监听（如需要初始化大量连接的 broker transport）会中止启动而无调整入口；慢钩子同理。
- 消解（已落地，未新增 Option）：删掉 `defaultHookTimeout`，阶段预算统一由 `phaseBudgetFor(stopTimeout) = stopTimeout/2` 派生（`pkg/appkit/types.go:13-22`）——`stopAll` 的钩子阶段、单条 undo、组件 Start/Dispose（`newFromDecl` 缺省即派生，显式声明优先）、端点等待四处同缩放；端点等待另优先取调用方 ctx 的截止期（`waitForEndpoints` 的 `ctx.Deadline()` 分支），框架不缩短调用方给的预算。回归见 `pkg/appkit/phase_budget_test.go`（含「stopTimeout 200ms → 100ms 上界报错」这条对旧实现的反证）。

**4.3 registry 心跳** ✅ 未见泄漏：`registry/etcd/registry.go:245-324` 的 `heartBeat` 有 `maxRetry` 上界（`:258`）、重试循环与各等待点都检查 `ctx.Done`（`:259,277,294`）、`stopCtx` 也参与 select（`:320`），cancel handle 在 mutex 下跟踪并在 `Deregister` 关闭（`:163-181`）。残余风险仅在 clientv3 `KeepAlive` 阻塞超过 cancel，属推测。

**4.4 启动窗口内的取消与真实启动失败同文案** ✅ 已修 原状：`Run` 在 servers 并发启动后要等端点定型（`waitForEndpoints`），而这个窗口对任何带 gRPC 面的进程都真实存在——`transport/grpc` 的 `Endpoint()` 在 `net.Listen` 完成前返回空串，慢机器上可到几百毫秒。窗口内 `gctx` 被取消有两条来源，旧实现只有一条文案 `appkit: wait for endpoints canceled: context canceled`：
- 调用方取消（SIGTERM 打在仍在启动的进程）——主等待一侧早已把 `context.Canceled` 当正常停机，启动一侧却当装配错误返回，同一个信号因落地时刻差几毫秒而结局不同；
- errgroup 因某个 server 的 `Start` 失败而取消 gctx——真实失败（如端口 bind 冲突）被这句「取消」整条抹掉，日志与返回值都不含它。
- 发现路径：bald-admin 的装配 e2e（`cmd/bald-admin/app/assembly_e2e_test.go`）在 GitHub 共享 runner 上红过一次（run `41675043918`），本地 `-count=30` 复现不了；`Serve` 直接返回错误、不经 `MustServe` 的 stderr 兜底，故整轮 CI 除了那句文案没有任何线索。
- 消解：判据只有一条——`startupOutcome(callerCtx, stepErr)`（`pkg/appkit/appkit.go`）：**调用方已取消且这一步的错误就是这条取消本身**才算正常停机（返回 `nil`），其余一律记账后原样返回。它挂在 Run 的全部五处启动分支上（`beforeStart` 钩子、组件顺序启动、端点等待、服务注册、afterStart 钩子）；errgroup 建立之后的三处先经 `abortStartup` 取 servers 的非取消启动错误（必须在 `stopAll` 之后 `g.Wait()`：gRPC 的 `Start` 只在 Server 被 Stop 后才返回），再走同一条判据。判据读的是**调用方**的 ctx：各分支自己会 `cancel()`，被遮蔽的派生 ctx 此后恒为已取消，读不出区别。两个条件缺一个都不对——只看取消会把「同窗口内真的装配失败」放过（fail-fast 是既定口径），只看错误就把 SIGTERM 打成装配错误。
- 回归：`pkg/appkit/boot_cancel_test.go` 四条确定性用例（不等机器快慢：端点永不定型的 server、阻塞到 ctx 结束的 beforeStart 钩子与组件 Start，各自配调用方 50ms 取消；另有一条 server 直接 `Start` 返回 boom，和一条「取消 + 真实失败」必须仍返回真实失败）。三条判据各自只被相应用例打中——去掉 `g.Wait` 那条只红崩溃用例，去掉「这一步错误 = 取消」只红真实失败用例，去掉整条判据红三条取消用例，实测各红一次后恢复。
- 已证（远端）：本仓 `0561cc9` 的 `ci` run 41682211384 五作业 `test`/`race`/`lint`/`vuln`/`integration` 全 success。下游 `bald-admin` 侧同时把装配 e2e 的就绪判据从「只 Dial HTTP 端口」收紧为「逐个已声明 server 端口各连一次」（`4a40298`，backend run 41682275441 success，上一 head `6d117a3` 的红消失）——注意它消费的仍是 `pkg v0.2.1`，也就是**残余②③那条框架分级还没进任何已发布 tag**，下游要拿到分级得等下一个 `pkg` 版本。
- 残余（如实记下，不是已解决）：① 这三处的 `log.Error` 仍在 `stopAll` 之后，而 logger 效应在停机时把全局后端还原为 nop，故该日志**必然被吞**（生产 main 走 `MustServe` → `reportServeFailure` 有 stderr 兜底，直接调 `Serve` 的测试路径没有）；错误本身没丢——它就在那个返回值里。② 分级只覆盖「会观察到 ctx」的分支：`loadConfig`/`Resolve` 两处失败不来自 ctx，取消不可能被误判成它们，故不在范围内。③ 「已进入运行态」的外部信号是 `AfterStart` 钩子（触发时端点已定型、注册已完成，只早于首次协调一步），经 `Serve` 就能用；`App.startedCh` 未导出、`Serve` 也不交回 App 句柄，但补一条只能靠句柄读的通道与既有钩子重复，没做——这是口径选择，不是缺口。

---

## 5. 错误分类法未统一 ✅ 口径已定（选项 b），未改码

`berrors` 存在且设计良好，但采用度极低：
- 全仓非测试文件 import `berrors`：**13 个**（`berrors/` 自身除外）。
- `transport/http`：**0**　`broker/kafka`：**0**　`pkg/appkit`：**0**
- 同期 `transport` + `broker` + `pkg` 三个目录下非测试文件里 `fmt.Errorf` **278** 处、`errors.New` **102** 处。
- appkit 直接返回裸包装错误，如 `waitForEndpoints` 里的 `fmt.Errorf("appkit: servers not ready within %s …")`。

例外（说明能力是真的）：`transport/web` 统一经 `berrors.FromError` + `httperr.CodeToHTTP` 映射并兜底 INTERNAL（`transport/web/response.go:56-93`）；gRPC 有集中式 `grpcmw.ErrorInterceptor`（链首，`pkg/middleware/bundle/bundle.go:298`）。

- 影响：**错误语义只在「泛型绑定面」成立**，在 server 构造面（transport/http、broker）不成立。跨语言/跨协议消费者拿到的是无码错误。
- 消解：二选一并停止摇摆——(a) 在 transport/broker 边界强制 `berrors`；(b) 承认 `berrors` 只服务 `transport/web` + gRPC 拦截器，从 README 的能力叙述里降级。

**收口（选 (b)）**：`berrors` 的定位写定为**边界投影模型**——只在协议投影收口点（HTTP 响应体、gRPC 拦截器）要求错误可用，内部构造面不强求；这不是「待补的采用率」，是设计承诺。理由与实测一并落在《[Bald 错误模型设计](../devel/zh-CN/Bald%20错误模型设计.md)》的「采用边界」节，此处只记结论：

- 复测（本轮，判据：`find` 全仓非测试文件 + 排除 `berrors/`、`_example/`）：berrors 族 importer **12 个**（transport/web 4、pkg/middleware/grpc 6、pkg/middleware/gin 2）；桥接子包 grpcerr 1、httperr 3；`transport`+`broker`+`pkg` 非测试文件 `fmt.Errorf` **279**、`errors.New` **103**。与评审初值同数量级，说明采用面没有自发增长。
- 下游 bald-admin：import 根包 **38 个**文件、import 桥接子包 **0 处**——投影被框架收口后业务仓库与桥接天然解耦，这正是 (b) 的兑现面，也是当初「零依赖」承诺的实际形态。
- **不修的代价写明白**：gRPC 侧非 berrors 错误经 `status.Convert` 保留原状态，裸 error 落 `codes.Unknown`；HTTP 侧非 berrors 错误落 `berrors.Internal`。两侧兜底不一致是**已决定的行为**（Unknown 表示「框架不认识这个错误」，Internal 表示「服务端出问题了」），不是待修的遗漏。
- 给接手者的两条硬规则：① 新增协议投影收口点必须走 berrors + 对应桥接子包；② 内部构造面继续用 `fmt.Errorf`，不要为了「统一」做全仓改写——那会把 279 处错误文案换成 279 处需要维护的 Reason 键，收益只在边界，成本摊在中间。

---

## 6. 安全

**6.1 鉴权 fail-open（高）** ✅ 已修
原状：`pkg/middleware/bundle/bundle.go` 的注释把设计说得很清楚——「未注入的依赖对应层直接省略（零开销），如未设 Authn 则链中无认证层」，实现即条件 `append`。后果：**一个没配 authenticator 的 bundle 就是一个无认证服务器，且没有任何启动告警**。零开销是优点，默认开放是风险，两者被同一个 `if` 绑在一起。
- 消解（已落地，零开销设计不变）：认证姿态必须被决定——`bundle.Authn(...)` 或 `bundle.Public()` 二者之一。`Bundle.Verify()`（`pkg/middleware/bundle/bundle.go:185-212`）在两者都没给时报 `AUTHN_UNDECLARED`，`Gin()`/`GRPCInterceptors()` 出链时对未决定的姿态告警一次；业务想要硬失败就在装配期调 `Verify()`。生成脚手架写出 `bundle.Public()` + TODO，使「本服务无认证层」成为一行显式声明而不是疏漏。
- 为什么不是构造期 panic：链是零开销省略的载体，`New()` 直接失败会让「先起链、再决定」的既有形态无处安放，也会让新 `gen app` 出的最小示例跑不起来（最小路径必须可复制即跑）。
- 回归：`pkg/middleware/bundle/authn_posture_test.go`（Authn/Public/两者都不给/只给 Authz 四种判定；两个出链点对未决定出声、对已决定不出声）。
- 未修：契约侧仍没有 authn 段（`server.http.middleware` 只有 recovery/cors/logging/request_id/tracing/rate_limit/timeout；`server.grpc.middleware` 另有 validate 与 §6.2 这次补的 rate_limit/timeout），认证器实例仍只能代码注入。补一句改口径：`Server.Http.Middleware` 不再是「无框架消费者的参考段」——§6.2③ 之后两面各有一条契约驱动的装配路径（`appkit.WithHTTPContract`/`WithGRPCOptionsContract` 在契约装载后把段交给业务钩子，生成脚手架默认经 `bundle.FromMiddleware`/`FromGRPCMiddleware` 翻译）。段的「业务自取」性质只剩一处：**认证姿态**仍是业务代码里的一行显式声明（`bundle.Public()` 或 `bundle.Authn(...)`），框架不代替业务宣布「本服务公开」，要把它搬进配置得先加 `middleware.auth` 段，属独立一轮。

**6.2 gRPC 链缺限流与超时（中）** ✅ 已修（unary + stream + 契约面 + 两面装配面）

原状：`pkg/middleware/bundle/bundle.go` 的 `GRPCInterceptors()` 到 Authz 即结束，而 `Gin()` 另有 RateLimit + Timeout 两层。gRPC 面上的暴力尝试与慢 handler 没有任何框架级保护——同一对 `bundle.RateLimit`/`bundle.Timeout` Option 在 gRPC 侧被静默吃掉。

- 已修（unary 面）：新增 `pkg/middleware/grpc/ratelimit.go`（`RateLimitInterceptor(limiter, wait)`）与 `timeout.go`（`TimeoutInterceptor(d)`），`GRPCInterceptors()` 在与 gin 相同的链位（Authz 之后、最内层之前）按同一对 Option 挂载。**零新增 API、零配置零开销**（`TestEmpty_NoOverhead` 的基线长度 4 未变），复用 gin 侧 `RateLimiter` 已暴露的 `Limiter()`/`Wait()`，因此**默认**两条链共用同一个桶，不会出现「HTTP 与 gRPC 各扣一份配额」；要给 gRPC 单独的桶得显式配 gRPC 契约段（见下「已修（契约面）」）。
- 语义差别全部写在注释里而不是抹平：拒绝 = gin `429` vs gRPC `RESOURCE_EXHAUSTED`+`Reason=RATE_LIMITED`；超时 = gin 硬性提前 `503` vs gRPC 只给 handler 一个会到期的 ctx，**且绝不缩短调用方已带的 deadline**（`d<=0` 时 gRPC 省略该层，gin 照旧挂立即 503 的层——这是 gin 的既有行为，动它需要独立决策）。
- 为什么 gRPC 超时不把 handler 丢进 goroutine 提前返回：那样 panic 会绕过外层 `RecoveryInterceptor`（只覆盖自己调用栈），进程直接崩。代价（响应不保证按时结束）如实写进注释。
- 回归：`pkg/middleware/grpc/timeout_test.go`(6) + `ratelimit_test.go`(6) 逐条钉住上述语义；`pkg/middleware/bundle/grpc_contract_test.go`(6) 钉住链上真实生效——含「配额耗尽时授权拒绝仍以 `PERMISSION_DENIED` 返回」（限流确在授权之后）与「被限流的调用不穿过超时层」（Timeout 确在最内层）。三处判据均按变异验证会失败：删 deadline 保留分支 / 删非正值守卫 / 删 `defer cancel()` / 删 `err != nil` 拒绝 / 把 bundle 两处 `append` 短路。
- 已修（stream 面）：`GRPCChain()` 此前只出 unary 链，流式调用**整条链是空的**——限流、超时没有，连认证也没有。
  - 新增 `grpcmw.RateLimitStreamInterceptor(limiter, wait)`、`grpcmw.TimeoutStreamInterceptor(d)`（+ `ServerStreamWithContext`，替换流 ctx 必须包一层）、`grpcmw.ErrorStreamInterceptor()`；bundle 侧出 `(*Bundle).GRPCStreamInterceptors()`，链为 `Error → RateLimit`。
  - **零配置零开销的口径延续**：`GRPCChain()` 只在 gRPC 面**有**限流器（`RateLimit` 或 `GRPCRateLimit`）时追加 `ChainStreamInterceptor` 这一个 ServerOption；都没有时选项数与之前完全一致。
  - 计费口径：**每条流建立消耗一个令牌**，流内后续消息不扣。挡建流洪水，不挡一条流里狂发。与 unary 取的是同一个 `grpcLimiter()`，所以 gRPC 的两条链不会各扣一份配额。
  - **stream 超时刻意不进 bundle 默认链**：unary 的 `default_timeout_ms` 套在流上会静默变成「流的最大存活时长」，长订阅/watch 到点被砍且没有可区分的错误。要这个语义得业务自己挂 `TimeoutStreamInterceptor`。
  - 保留的既有边界（不是这次引入）：Authn/Authz/Audit 只有 unary 形态，stream 链上仍没有认证层——此前 stream 面整条为空，同样没有认证。
  - 回归：`pkg/middleware/grpc/{ratelimit,timeout,error}_test.go` 的 stream 用例（每流一令牌、与 unary 共享桶、wait 收到的是流 ctx 且取消即刻拒、`d<=0` 不挂、不缩短调用方 deadline、handler 返回即 cancel、错误原样透传）；`bundle/grpc_stream_contract_test.go`（链位、未配置时选项数、"unary 超时不漏进流"）；`bundle/grpc_stream_server_test.go`（真 `grpc.NewServer(b.GRPCChain()...)` + 真 client 的端到端拒绝）。八处变异逐个落地复核均为红：删 stream Error 层 / 删限流层 / 无条件挂 `ChainStreamInterceptor` / 超时不包装流 ctx / 去 nil 守卫 / 去拒绝分支 / 不发 stream option / e2e 删 Error 层（客户端实测收到 `code = Unknown desc = code: 8, reason: RATE_LIMITED`，正是这条收口必须存在的理由）。
- 已修（契约面）：`Server.Grpc.Middleware` 补 `rate_limit`/`timeout` 两段（`bconf/proto/bootstrap/v1/server.proto`，字段号 5/6，纯新增不动既有号），生成物经 `task proto-contract` 重落、`task proto-contract-check` 绿。
  - 「写 gRPC 配置就能限流/超时」现在成立，路径是新的 `bundle.FromGRPCMiddleware`：`rate_limit` → `bundle.GRPCRateLimit`，`timeout` → `bundle.GRPCTimeout`，非法值启动期 fail-fast。
  - **覆盖口径（这次真正的设计决策）**：`RateLimit`/`Timeout` 仍是「一面给全部」（只配 HTTP 段时 gRPC 照旧共用那个桶/预算，存量行为与零开销不变）；gRPC 段产出的是**只作用于 gRPC** 的 Option，给了就替换 gRPC 面上的那一份。两个段都给 → 两个互不相干的桶，**不存在后写盖前写**。理由：一条长流与一次短请求的容量本就该各设，硬塞一个桶才是「配了不生效」。
  - **gRPC `timeout` 只管 unary**，段注释与 `GRPCTimeout` 注释都写明理由（流上会变成「流的最大存活时长」）；`status_code` 字段不镜像——HTTP 专属，gRPC 用 status code，镜像过来就是一个「写了不生效」的字段。
  - 诚实边界：`recovery`/`logging`/`tracing`/`validate` 四段本桥**不翻译**（空 message，没有可映射字段；bundle 的 gRPC 链本就无条件挂 Recovery/RequestID）。写这四段目前不改变任何行为——`FromGRPCMiddleware` 的注释与本节都这么记，别当成配置面。
  - 回归：`pkg/middleware/bundle/grpc_middleware_bridge_test.go`(8) 从配置文本出发断桥——段只加在 gRPC 面（gin 链长度=基线、gin 请求全放行）、stream 面同受段管、gRPC 段盖过 HTTP 桶且 HTTP 桶不被吃、只给 HTTP 段时三面仍共桶（存量回落）、超时不外溢到流也不外溢到 gin、非法值 fail-fast、空段零开销，外加一条真 `grpc.NewServer` 的流式端到端。判据按变异验证会红：`grpcLimiter`/`grpcBudget` 的优先级分支各删一次、stream 改用 `b.rateLimiter`、桥的 fail-fast 守卫删掉、桥把 gRPC 段错译成 HTTP 的 `Timeout`（泄漏到 gin 链）。
  - **发版前置**：`pkg` 的这段新代码引用 `bconf` 生成物里的新类型，`bconf` 必须先出 tag，`pkg` 才能出 tag（`bald release sync` 会把这条依赖排进批次）。`bconf/v*` tag 另会触发 BSR 推送，属 owner 授权动作。**实测的形状是「同批即可」而非「分两轮」**：一次批次里所有 tag 都指向同一个提交，`bconf/v0.18.0` 与 `pkg/v0.3.0` 一起推送后代理两侧都能解析，装载判据是「推送完成后逐 tag 问代理拿得到」，不是 tag 之间的先后。真正必须分轮的是 `cli`——它零 `replace`，只能在下轮归位（见《多模块发布与变更日志》）。
- 已修（装配面）：缺陷的真实形态不是「gRPC 链没自动装配」这一条，而是**两个 middleware 段都没有框架消费者**——HTTP 面拿 `WithHTTP(handler)` 收一个业务自己建好的 `*gin.Engine`，gRPC 面拿 `WithGRPCOptions(fn)` 收一条业务自己建好的链，两座桥（`FromMiddleware`/`FromGRPCMiddleware`）翻译得对，但没有任何组件在启动时读那两个段。「配置文件里写了限流」与「限流生效」之间缺一次调用，两面同病，所以按 owner 定的**对称做**收口。
  - 新 API：`appkit.WithHTTPContract(fn)` / `appkit.WithGRPCOptionsContract(fn)`，钩子入参就是对应的 middleware 段，返回（handler 或选项链，`close`，error）。求值点在装配期——契约已装载、变体与域接线之前（`pkg/appkit/contract_faces.go`）。HTTP 必须在此定型：域接线（`dom.Routes` 挂到 handler）在 `BeforeStart` 链末尾、迟后阶段（server 构造）之前，那个窗口要求 handler 已存在；gRPC 与它同点求值，为了「两面的链由同一份契约段驱动」只有一个实现点。
  - 面存在性判据统一走 `hasHTTPFace`/`hasGRPCChain`（两条口任一即算），钩子声明字段求值后**不清空**：`--plan` 不执行用户代码，契约钩子形态在计划期只有声明、还没有 handler——直接读 `Decl.HTTPHandler` 会让 `--plan` 漏掉路由节点而真实启动装上它，计划与执行必须同源（I2）。
  - 三条 fail-fast：`WithHTTP` 与 `WithHTTPContract` 双写报错；钩子返回 nil handler 报错（「想要无链的 handler 请用 WithHTTP」）；钩子错误包成 `appkit: WithHTTPContract: …` 让启动失败。钩子给出的 `close` 进停机 Effect，启动失败回滚同样释放——限流器这类资源不在成功路径上才归还。
  - 变体不能偷偷改链：钩子求值后若 build variant 再打 `server.*.middleware` 段，深拷贝快照核验报错「链已由补丁前的段构建」。这不是「变体没生效」，是「变体改了配置却不改变链」那种静默行为，必须显式拦下；未声明钩子时快照与核验全部跳过（零开销）。
  - 一条已知成本（不是缺陷）：`WithHTTP(nil)` 让位转码面的形态（`_example/bald` 的 `grpcgw` 变体）下，HTTP 钩子仍会在变体之前求值一次——gin 链与限流桶建出来后被 `WithHTTP(nil)` 弃用（该 Option 同时清掉 `HTTPContract` 声明，钩子不会把面装回来），`close` 仍由停机 Effect 兑现所以不泄漏，代价是白建一次。要省掉它得把「变体让位」提前到求值之前可知，属 Decl 相位顺序的事，本轮不动。
  - 生成脚手架对称改形（**breaking**，存量工程见下方迁移条）：seeded 的 `internal/apiserver/apiserver.go` 里 `NewRouter()` 从 `func() *gin.Engine` 改成 `func NewRouter(mw *bootstrapv1.Server_Http_Middleware) (http.Handler, func() error, error)`，owned 的 `assemble.gen.go` 相应从 `appkit.WithHTTP(apiserver.NewRouter())` 改成 `appkit.WithHTTPContract(apiserver.NewRouter)`；gRPC 面在 seeded 的 `internal/bootstrap/custom.go` 里新增 `newGRPCChain(mw)` 并经 `appkit.WithGRPCOptionsContract(newGRPCChain)` 登记。接管 `server.http` 的网关角色不挂 gin 链，因此**不**登记 `WithHTTPContract`（仍按转码口径走），middleware 段也不继承给该角色。
  - 角色的 `configs/<app>-<role>.yaml` 现在从 `AppSpec.bootstrap.server` 继承两段 middleware（codegen `inheritMiddleware`）。此前多角色契约只继承地址段、middleware 段恒空——那是同一个缺陷在配置侧的形状：在角色文件里写限流没有任何路径能到链上。
  - 认证姿态**刻意不进契约**：脚手架两面各出一行 `bundle.Public()` 加注释。§6.1 的结论在此延续——框架不代替业务宣布「本服务公开」；要做成配置得先有 `middleware.auth` 段（以及认证器如何按名实例化），属独立一轮。
  - 回归：`pkg/appkit/contract_faces_test.go`(14) 钉住两面钩子真收到装载后的段、双写/nil/错误三条 fail-fast、`close` 在停机与启动失败两条路径上都被调用、装配出错时回滚释放、变体补丁守卫（有补丁必红、无补丁必静默）、`--plan` 只声明不执行、求值出的 handler 真能出响应。变异判据：把传给钩子的段实参改成 nil（两面用例同红）/ 删双写守卫 / 删 nil handler 守卫 / 删补丁守卫 / 让计划路径执行钩子。
  - 存量工程迁移（一行签名，故意不做兼容）：`func NewRouter() *gin.Engine` 的工程重跑 `bald gen app` 后，`assemble.gen.go` 会按 `WithHTTPContract` 消费 `NewRouter`，旧签名直接编译失败——这是有意的，让「配置写了不生效」必须经过一次显式改写，而不是静默降级回不读段的形态。改法：签名收 `mw *bootstrapv1.Server_Http_Middleware`，链改由 `bundle.FromMiddleware(mw)` 出，返回 `(http.Handler, func() error, error)`；gRPC 侧把原有的 `WithGRPCOptions(自建链)` 搬进 `newGRPCChain(mw)`，用 `bundle.FromGRPCMiddleware(mw)` 译段。
  - 端到端实证：`_example/bald/internal/bootstrap/middleware_e2e_test.go`(4) 起真进程（`appkit.Serve` + 真 gin + 真 `grpc.NewServer`），从契约 YAML 出发断言四条——
    | 用例 | 契约段（临时文件覆盖） | 实测 |
    | --- | --- | --- |
    | HTTP 限流 | `server.http.middleware.rate_limit{rate:1,burst:1}` | 首个 POST `200`，次个 `429` |
    | gRPC 限流 | `server.grpc.middleware.rate_limit{rate:1,burst:1}` | 首个 Greet `OK`，次个 `RESOURCE_EXHAUSTED`，且经 `grpcerr.FromStatus` 还原出 `Reason=RATE_LIMITED` |
    | HTTP 超时 | `timeout{default_timeout_ms:50}` + `biz.greet.delay: 600ms` | `503`，**响应头**在 500ms 内到达（gin 层提前返回并 Flush）|
    | gRPC 超时 | 同参数在 gRPC 面 | `DEADLINE_EXCEEDED`，往返 ≤500ms（handler 收到会到期的 ctx 后自行退出）|
    六个删接线变异逐个实测（本轮重跑，非沿用）：① 生成物退回 `WithHTTP(NewRouter(nil))` → 两条 HTTP 用例红成 `200`/`200`（响应头 603ms），启动一切健康——正是「配置写下去没人读」的复现；② `custom.go` 摘掉 `WithGRPCOptionsContract` → 两条 gRPC 用例红，HTTP 绿；③ `apiserver.go` 的桥改成 `FromMiddleware(nil)` → 两条 HTTP 用例红；④ `grpc.go` 的桥改成 `FromGRPCMiddleware(nil)` → 两条 gRPC 用例红；⑤ handler 不看 ctx（`select` 换 `time.Sleep`）→ **只有** `TestE2E_GRPCContractTimeout` 红，HTTP 的 `503` 照旧绿（gin 提前返回不依赖 handler 配合，gRPC 依赖——这条不对称由变异本身钉住）；⑥ 删 `grpcerr.ToStatus` 的 `DeadlineExceeded` 分支 → e2e 收到 `code = Unknown`（52ms 就返回），`berrors/grpcerr` 单测两处同红。
- 附带修掉一处真缺陷（超时接线的直接后果）：`berrors/grpcerr.ToStatus` 对 `context.DeadlineExceeded`/`context.Canceled` 走到兜底 `status.Convert`，两者都不是 gRPC status → 落 `codes.Unknown`。grpc-go 自己不经这座桥时给的是 `DEADLINE_EXCEEDED`/`CANCELLED`，所以「装了收口拦截器」比「不装」更差：超时看起来不像超时，客户端按码重试/告警的策略全部失效。现在两条 ctx 语义显式映射，`status` 原样透传的分支也补在前面（`status.FromError` 命中即返回，不再兜 Unknown），裸 error 仍落 Unknown。回归 `TestToStatusContextErrors` + 上面变异⑥。

- 附带修掉一处真缺陷：`tokenbucket.Close()` 过去只置 `closed`，从不写 `notify`，而 `pkg/middleware/gin/ratelimit.go` 承诺「`Close` 会唤醒阻塞在 `Wait` 的调用者」——停机时 `wait=true` 的请求会挂到自己那轮 `time.After(wait)` 到期（rate 低时是小时级）。现在 `Close` 在锁内 `close(notify)` 并以 `closed` 保证幂等。判据 `TestClose_WakesBlockedWait`（冻结时钟 + `rate=0.0001`，wait 算出约一万秒，`Close` 后必须 2 秒内返回）；原有 `TestClose_WaitReturnsErrAfterClose` 通过是因为 `New(1,1)` 的 wait 恰好 1 秒，**不是因为唤醒生效**。

**6.3 限流在认证之后（中，作者已知）** ✅ 口径已定，未改码

`pkg/middleware/bundle/bundle.go` 明确记录了这个取舍：为了认证后的 subject 可见（为按主体配额留位），代价是认证层自身不受这一层保护，匿名洪水先穿过认证/审计/授权才撞限流。

- 结论：**保持现位置，不改码**。把默认挪到认证之前会让现有部署静默变形——今天按主体计的配额一旦搬到认证前只能看到全局流量，等于悄悄把「按主体限流」换成「全局限流」，且没有任何报错。要保护认证层的部署显式把 `ginmw.RateLimit`/`grpcmw.RateLimitInterceptor` 挂到自己链首，两者都不要求认证上下文。
- 本轮补的是**口径落文档**：《Bald 限流设计》新增「链序与两侧口径」一节，给出实测链序图、双向代价、不改默认的理由、以及两面拒绝语义的差异；《Bald 中间件设计》的链序段同步。此前设计文档里写的是「限流在 `Recovery` 之后、`Authn` 之前」，与实现相反——文档在这一点上是错的，已按实现改写。
- 熔断层接进来时**沿用同一条口径**，所以它也排在认证/授权之后（`Authz → RateLimit → CircuitBreaker → Timeout`）。这意味着匿名洪水同样先穿过认证/审计/授权/限流才撞到熔断——但这里的取舍比限流更清楚：熔断判的是「下游现在值不值得真做」，让匿名流量替所有人开窗更糟（谁都能把失败率打满），所以它**必须**更内于认证。而它必须在限流之内、超时之外：配额先拒（便宜），内层 Timeout 写出的 503 要被记成失败。三条判据各有一条位置测试钉住，见 §2「熔断半」。

**6.4 CORS / Secure 默认关闭（低-中）** ✅ `b.corsCfg != nil` 才挂 CORS、`b.secure` 默认 false（`pkg/middleware/bundle/bundle.go:184-189`；`New()` 只默认 `logging`/`normalized`，`:159`）。

**6.5 gRPC TLS 契约字段未接线（高）** ✅ 已修

原状：契约声明了 TLS，但没有任何代码消费它——

| 环节 | 事实（修复前） |
|---|---|
| 契约字段 | `Server.Grpc` 声明 `TLS tls = 3;`（`bconf/proto/bootstrap/v1/server.proto:138`） |
| 装配层 | `GrpcServerProvider` 只读 `GetReflection()`，从不读 `GetTls()` |
| 服务端构造 | `grpc.NewServer(unary...)` + 裸 `net.Listen`，无 `grpc.Creds` |
| 生成物默认 | 硬编码 `grpc.Creds(insecure.NewCredentials())`，且在 **owned** 的 `assemble.gen.go` 里，注释却写「业务可扩展」 |
| gateway 出站 | 硬编码 `insecure.NewCredentials()` |

- 影响：在契约里写 `server.grpc.tls` **不会生效**，gRPC 仍是明文；与 HTTP 行为不一致，按 HTTP 心智配置的人会误判「我已开 TLS」。

修复后的消费面（同一 `tlsconf` 语义，四处生效）：

| 环节 | 现在的消费者 | 证据 |
|---|---|---|
| TLS 材料→`*tls.Config` | `tlsconf.Server`（cert+key 必需；给 `ca` 即 `ClientCAs`+`RequireAndVerifyClientCert`；MinVersion TLS1.2；忽略 `insecure_skip_verify`） | `bconf/tlsconf/tlsconf.go:26-59` |
| gRPC 服务端 | `Start` 读 `s.cfg.GetTls()` 构造 `tls.Config`，监听器套 `tls.NewListener`，并补 `NextProtos=["h2"]`（grpc-go ≥1.67 强制 ALPN，否则客户端报 missing selected ALPN property） | `transport/grpc/grpc_server.go:88-101` |
| HTTP 服务端 | 同一入口，`net.Listen` 前先构 TLS 配置（失败 fail-fast，端点保持空） | `transport/http/http_server.go:66-79` |
| gateway 出站 | `dialOpts(backend.GetTls())`：无段 → insecure，有段 → `credentials.NewTLS(tlsconf.Client(..))`，错误上抛 | `transport/gateway/gateway_server.go:68-86` |
| 生成物 | `newGRPCServerOptions` 的 insecure stub 与 `insecure` import 一并删除，改为 `appkit.WithGRPC(registerGRPCService)`；TLS 不再是业务可选项 | `cli/internal/codegen/appspec_tmpl_app.go:144,194`、`_example/bald/internal/bootstrap/assemble.gen.go` 已按 `--adopt` 重生成 |

- 三处语义变化，接手者需要知道：① 服务端 `tls.ca` 从「写了没用」变成「写了就强制校验客户端证书」；② `tls` 段存在但材料不全 → 启动期 fail-fast（旧行为是静默明文）；③ gRPC TLS 依赖 h2 ALPN，业务自持 `*tls.Config` 时必须自己带上（框架只在字段为空时补）。
- 未修的范围（刻意保留）：契约 `tls` 字段在 Http3/Graphql/Sse/Websocket/Tcp/Thrift/Webtransport/Signalr/Socketio/Webrtc 仍无消费者（`bconf/gen` 都生成了 `GetTls()`）。SSE contract 明确写了「不加载证书，需业务注入 `*tls.Config`」（`transport/sse/contract/contract.go:71`）；HTTP3 的 `generateTLSConfig` 无条件自签（`transport/http3/server.go:169`）。这些适配器没有 `contract` 之外的框架装配路径，属 §2「发布面 ≫ 使用面」的同一议题。
- 回归：`bconf/tlsconf/tlsconf_test.go`（文件/内联 PEM 等价、材料不全逐条报错、真实 mTLS 握手、服务端拒绝无客户端证书、客户端拒绝不可信服务端）+ `transport/http/http_tls_test.go`、`transport/grpc/grpc_tls_test.go`、`transport/gateway/gateway_tls_test.go`、`bootstrap/grpc_tls_test.go`（装配层端到端）。证书由 `bconf/tlsconf/tlstest` 现发（一次性 CA + 叶子证书，产出即契约 `TLS` 段），因此每条断言走的都是真实契约路径而非 mock。

**6.6 机密** ✅ 未发现机密被写日志的证据；脱敏有文档化缺口：不覆盖 `slog.Attr` 形态（如 `slog.String("password", v)`），见 `bconf/gen/go/bootstrap/v1/log.pb.go:36` 的说明。**govulncheck 是 fail-closed 且覆盖全模块**（`vuln` 作业的 `install govulncheck` step 固定 v1.9.0，逐模块循环消费 `scripts/list-modules.sh`）——这一项没有问题。

---

## 7. 测试与验证策略

**7.1 CI 全量只跑 `-short`（中）** ✅ 评审初值：`test` 与 `race` 作业的每模块命令都是 `go test -short`（`.github/workflows/ci.yml` 的「build + vet + test all modules」/「go test -race (all modules)」两步）。按 CI 自己的注释，`-short` 的语义是**跳过集成测试**。因此 kafka/redis/nacos 之类的真实后端交互在 CI 里从不执行；唯一非 `-short` 的是「TestTree_EndToEndTimed / TestTree_AuthzClosedLoop」这一步的 `cli/internal/codegen` 两条端到端用例。
叠加既有事实：asynq 集成测试需要 `BALD_TEST_REDIS_URI`，Redis 不可达时自动跳过——**注入与否决定这批用例是否真的跑过**。
- 消解：明确哪些接缝必须有集成覆盖（至少 store/registry/broker 各一条），为它们开一个按需注入依赖的 CI job，而不是继续在 `-short` 里假定绿。

**7.1 收口（已落地：`integration` 作业）**

- 修法：`.github/workflows/ci.yml` 新增 `integration` 作业，`services:` 注入 **etcd（quay.io/coreos/etcd:v3.5.14）/ consul（hashicorp/consul:1.19.2）/ redis:7-alpine / mongo:7**，四个后端地址由 `env:` 显式给出（`BALD_E2E_ETCD_ADDR`、`BALD_E2E_CONSUL_ADDR`、`BALD_TEST_REDIS_URI`、`BALD_MONGO_URI`），跑四条接缝的非 `-short` 套件：`registry/etcd`、`registry/consul`（各自的 `contract` 包随父模块 `./...` 一起跑）、`contrib/store-mongo`、`transport/asynq`。三步结构：探活 → 逐套件 `-v` 留档 → 按 EXPECTED 逐条断言 `--- PASS`（本作业现 10 条判据；registry 的第三条后端腿在 `integration-kubernetes` 作业里另判三条，见下面的 kubernetes 补条——全仓集成判据合计 13 条）。
- **etcd 的启动参数只能用 `env`，不能用 `args`**（本轮真实踩过）：首版写 `services.etcd.args: >-` 传 `--listen-client-urls`，GitHub 的解析器对 `services` 只认 `credentials/env/image/options/ports/volumes`，未登记的键让**整个文件在编译期被拒**——run 41411058652 的表现是 0 个 job、瞬间 failure、run 名回落成文件路径，REST 里连 `error_message` 都是 null；而 Ruby Psych 与 PyYAML 加载同一份文件毫无异议。等价的合法写法是同名 `ETCD_*` 环境变量（`ETCD_LISTEN_CLIENT_URLS`、`ETCD_ADVERTISE_CLIENT_URLS`），本机实测：容器日志确认两个 URL 生效、`/health` 返回 `true`、`registry/etcd` 全套件 PASS。
- **这一类失败的闸门不能写在 `ci.yml` 里**（文件本身坏了，里面的检查永不执行）。新开 `.github/workflows/workflow-lint.yml`：`actionlint` 版本 pin v1.7.12（与 Taskfile 的 `ACTIONLINT_VERSION` 同一个真相源），push/PR 仅在 `.github/workflows/**` 变化时触发，对全部作业文件做 schema 校验 + runner 预装的 shellcheck。本地同一条命令是 `task ci-workflow-lint`（`tools-actionlint` 负责按需 pin 安装）；它不进 `task verify`，因为那会让纯 Go 的本地闸门多一条网络依赖。actionlint 只在检测得到 `shellcheck` 时才跑它，本机没有，故另在 `koalaman/shellcheck-alpine` 容器里用同一版本（Linux 侧编译）复跑了一次，含 shellcheck 结论全仓 3 个作业文件干净。
- **为什么必须有第三步**（这是新增闸门最容易自欺的地方）：这些用例写的是「不可达即 `t.Skip`」，所以服务挂了作业照样绿。本地实测反证：把三个地址都指向死端口，**跑套件这一步 exit=0、23 条 SKIP**，而断言步骤 exit=1 并逐条点名五条判据。consul 那一轮（2026-11-04）单独复验过同一件事：`BALD_E2E_CONSUL_ADDR` 指向死端口后 `registry/consul` 全套件 **exit=0、6 条 SKIP**，断言步骤照样把五条 consul 判据逐个点名成 `::error::`——**跳过与没跑在这条作业里不可能混淆**。探活步同样实测：加一条指向 27018 的检查 → `::error::deadservice 未就绪`，作业在跑测试之前就失败，而不是放行成一片 Skip。EXPECTED 只列「非真服务不可证」的判据（etcd 三条、consul 五条、`TestMongoCRUD`、`TestTaskSubscribe`），普通单测不重复列；kubernetes 的三条判据在**另一个作业**里，同一条形状各自复验过，见下面的 kubernetes 补条。
- 主循环为什么仍留 `-short`：`transport/{redis,kafka,rabbitmq,rocketmq,tcp,webrtc}` 的 `TestServer`/`TestClient` 是等信号的手动用例（`t.Skip("manual test: blocks on signal…")`），去掉 `-short` 会把作业挂到 timeout。集成作业因此**点名套件**而不是全仓扫。
- 验活：三个步骤从 YAML 里导出后用 bash 原样执行过（GitHub 默认 shell 同为 bash），正向四件套全绿（`READY etcd/consul/redis/mongo` → 四条套件 ok → 十条判据逐条 PASS）；反向两条如上。三处描述「CI 无 etcd / 无 mongod」的过期注释（`registry/etcd/registry_test.go`、`registry/etcd/contract/contract_test.go`、`contrib/store-mongo/testutil.go`）改成指向 `integration` 作业。**原话里「YAML 由解析器加载通过」这条判据已被证明无效**（Psych 接受而 GitHub 拒绝，见上），取而代之的是 actionlint 的 schema 校验：`task ci-workflow-lint` 与容器内 `actionlint + shellcheck` 双双 exit 0。
- 验活（新闸门自身的变异）：① 把 `args:` 加回 `services.etcd` → `unexpected key "args" for "services" section. expected one of "credentials", "env", "image", "options", "ports", "volumes"`，exit 1；② 新写的 `workflow-lint.yml` 自身两处错误被同一类闸门当场拦下——`run: "$(go env GOPATH)/bin/actionlint" -no-color` 是 YAML 解析错（引号标量后还有内容），改成整体单引号后又报 SC2046（命令替换未加引号），最终形态 `"$(go env GOPATH)/bin/actionlint" -no-color` 才干净；③ 探活循环里未使用的 `i` 触发 SC2034 → 改为 `local ... tries=60` 的 while 计数；④ `find` 派生为空时任务打出「未发现作业文件，闸门未覆盖任何东西」并 exit 1（与 lint/vuln/race 同一防静默失效口径）。
- 刻意没做：① ~~**`_example/bald` 不进 CI**——因为 go.mod 把两个 kratos contrib 模块 `replace` 到仓库外~~ **这条「没做」同日整条撤销**：仓库外 `replace` 先撤（见「7.1 补：示例的去仓库外依赖」），剩下的两条阻塞（gitignore 的 `api/gen` 要 buf + BSR 网络、服务发现用例要真 etcd/真 nacos）由 `example` 作业逐一接住——按 `bconf/buf.version` + `bconf/buf.sha256` 装 pin 的 buf 生成产物、`services:` 注入 etcd + nacos v2.3.2（含 9848）、三态构建 24 条判据逐条断真 `--- PASS`，见「7.1 补：示例 module 进 CI」。② 没有 `task integration` 本地入口——命令以 `integration` 作业与 README 为准，再加一份清单就是再造一条会漂移的真相。③ ~~不覆盖 consul / kubernetes / nacos 后端~~ **consul 与 kubernetes 于 2026-11-04 落地**，见下面两条补条；nacos 现在**配置源侧有真机证据**（同一天的示例迁移腿，见「7.1 补：示例的去仓库外依赖」），**注册中心侧的 CI 服务也在同一天落地**（`integration` 作业注入 nacos v2.3.2，判两条机制：注销按对服务名、契约 cleanup 真关 client——见 §7.10 第三轮）——但它原来那条「命名 API 的 `namespace` 语义未证、示例里注册被程序化接管挡住」的双重未知已在同一天闭合：命名侧实测 `public` 与 `""` 是同一租户（与配置侧相反，`registry/nacos` 不该折叠），装配侧的接管也已生效并有真机用例（`TestNacosAssemblyRegistersIntoBackend`），~~装配那条仍缺远端证据，缺的不再是「CI 里没有 nacos」而是「示例 module 进不了 CI」（proto 产物 + buf 工具链，见 §7.9）~~ **同日 `example` 作业补上了这条远端取证**（单条 `-run` 的形状；整包/并集跑的限制另登记 §7.13），见 §7.10 与「7.1 补：示例 module 进 CI」。顺带记一条**新出现的可能性**：kubernetes 腿给出了一个真集群，配置源侧的 `bconfig/kubernetes`（那两个用 `BALD_KUBE_INTEGRATION` 显式开关的测试）从此有了可用的跑法——那是另一件事，本条未做。④ Kafka/RabbitMQ 的 broker 接缝没有可在 CI 里跑的集成用例（仓内那批是等信号手动用例，`broker/kafka` 的测试不需要真 Kafka）——§7.1 原话里「broker 一条」由 `transport/asynq`（Redis 队列）代。⑤ ~~未加覆盖率闸门（§7.2 另议）~~ **2026-11-04 已落地**：`TestModulesWithLogicHaveTests` 走的是「先立不变量、比例阈值另议」那条路，见 §7.2 收口与其下的「7.2 补」（基线实测出来后阈值仍缓做，理由换成「分母是口径的函数」）。

**7.1 补：consul 后端真机作业（2026-11-04）**

- 加了什么：`services:` 多一个 `hashicorp/consul:1.19.2`（**不需要 `args`/`options`**——本地 `docker image inspect` 证实镜像默认 CMD 已是 `agent -dev -client 0.0.0.0`，dev agent 自带单节点集群，8500 直接可查；这也正好绕开了上面 etcd 那条「多出键即整个文件编译期被拒」的坑），`env:` 多一条 `BALD_E2E_CONSUL_ADDR`，探活步多一条 `wait_tcp 127.0.0.1 8500 consul`，套件步多一条 `registry/consul`，EXPECTED 多五条判据。
- **consul 的测试不能照抄 etcd 的形状**，三处平台语义不同，每一处都决定了判据怎么搭：
  1. **健康检查在 agent 自己的网络命名空间里执行**（etcd 的 TTL 是 lease，由 lease 存活决定，与「端口能否连通」无关）。所以从 runner 侧注册一个端口，agent 侧探不到 → 检查 critical → `passingOnly=true` 的发现层把它过滤掉。对策：多数腿用 `WithHealthCheck(false)` 关掉 TCP 检查，只在专门那条过滤判据里开检查；而那条判据的「健康」地址用的是 **agent 自己的 HTTP 端口**（从同一个 `BALD_E2E_CONSUL_ADDR` 推出），这是 agent 一定探测得到的地址，配 `127.0.0.1:1` 作对照——一个必 passing、一个必 critical，过滤语义才是被证出来的而不是被假定出来的。
  2. **新实例在首次检查跑之前一直是 critical**（实测 interval=1~2s 时约 1 个 interval 空窗，10s 时最长等 10s），所以发现类断言一律走有界重试 `waitFor`，而不是注册后立刻断言。
  3. **服务名是 DNS label**，etcd 那套带点的 `150405.000000` 命名在这里非法，改用 `UnixNano`。
- 五条判据各钉一件事（`registry/consul/registry_test.go`、`registry/consul/contract/contract_test.go`）：
  - `TestRegisterDiscoverDeregister`：ID/Name/Version/Metadata/Endpoints 五字段经 consul tags + TaggedAddresses 往返不丢，注销后用 `Agent().Services()` 返回的 **map 里查不到**来核对（`Agent().Service(id)` 对不存在的服务是 404 error，不能当「查不到即通过」用）。
  - `TestWatch_ReceivesChanges`：Watch → Register → `Next` 在 15s 内到达。
  - `TestHeartbeat_DeregisterKeepsOtherInstances`：**这条是给 `c293c67` 补的回归**——该修复（`Deregister` 只停该实例心跳，不取消共享 ctx）落地时**没有任何测试**（`git grep -l "takeHeartbeat\|setHeartbeat\|heartbeats" c293c67 -- '*_test.go'` 无输出，即修复所在那个提交里一个测试文件都没提到这三个符号）。形状：`WithHealthCheckInterval(2)` → TTL 4s，注册 A+B 后注销 A，睡过 6s，断言 B **既可发现又仍在 agent 注册表里**——共享 ctx 的老 bug 会让 B 的心跳一起停掉、TTL 过期后被 agent 摘除，两个断言各自对应一种症状。
  - `TestGetService_HealthCheckFiltersNonPassing`：passing 侧过滤（见上），外加反真空——用 `Health().Service(name,"",false,nil)` 证明故障实例**确实注册着**且带 ≥1 条 `ServiceID` 归属它的非 passing 检查。这一版第一次跑红在「故障实例的 serfHealth 居然是 passing」：serfHealth 是**节点级**检查、恒 passing，按 `c.ServiceID == id` 过滤掉才是服务级检查——这个归属过滤就是红出来的产物，不是设计时想到的。
  - `TestProvider_BuildsUsableRegistrar`：契约映射产物除 `registry.Registrar` 外也满足 `registry.Discovery`，注册→轮询→注销走通。
- `TestGetService_UnknownNameReportsError` 另记一条后端差异：consul 把「服务名不存在」报成 error，etcd 报成空切片——抽象层不该抹平这个差别，所以测试里直接写清各自形状。
- 验活（变异五条，逐个改生产代码 → 断言对应判据转红 → 精确复原并核对文件回到原内容）：A 删掉 `Register` 里的 `Validate` 前置闸门 → `TestRegister_RejectsInvalidInstance` 红；B `Deregister` 的心跳取消改回共用 `c.cancel()`（即 `c293c67` 修掉的那个缺陷）→ `TestHeartbeat_DeregisterKeepsOtherInstances` 红；C 发现层 `cli.Service(..., passing=true)` 改成 `false` → `TestGetService_HealthCheckFiltersNonPassing` 红；D resolve 轮询去掉 `broadcast` → `TestWatch_ReceivesChanges` 红；E 注册时不写 `version=` tag → `TestRegisterDiscoverDeregister` 红（Version 往返丢）。
- 反证（Skip 不算通过那条闸门在 consul 上同样成立）：`BALD_E2E_CONSUL_ADDR` 指向死端口 → 套件 exit=0、6 条 SKIP；EXPECTED 步骤 exit=1 点名五条。
- **拓扑复现**：Linux 容器里起了一个与作业同构的环境（`hashicorp/consul:1.19` 容器 + `golang:1.27` 容器 + bridge 网络），把三个步骤从 YAML 导出后原样执行，全绿。这一步产出了一个**真实缺陷**：首版把解释性注释写在 `EXPECTED="…"` 字符串内部，循环按空白拆词，注释被当成一堆假判据（`grep: consul: No such file or directory`），断言步骤 exit=1 ——**如果只在 macOS 上单跑测试，这个 bug 会带着提交进 CI**。修法：注释移到字符串上方，并加一条 `case "$row" in *"|"*)` 兜底，任何非 `<日志>|<测试名>` 形态直接点名报错；重跑正向 exit 0、五条 PASS 逐条打出，反向（把注释塞回去）exit 1 并精确指出是哪个 token。
- 顺带修掉一处会静默失效的旧测试：`register_validate_test.go` 的 `TestRegister_RejectsInvalidInstance` 原本用 `newRegistry(nil)`（nil client）构造，而 `Register` 的第一条语句就是 `Validate`——用 nil client 时「校验前置」和「客户端坏了」两种红法分不开。改成 `New(WithAddress("127.0.0.1:1"))` 后，判据变成三条可区分的：缺 Name/缺 ID 报校验错、**合法实例必须走到网络路径**（错误里不得含 `is required`）、耗时 `>1s` 证明校验先于网络。
- 已知限制（**记录而非修复**）：`registry/consul` 的 resolve 轮询用 1s ticker + 索引比对来广播变更，而 consul 的 blocking index 是**集群级 raft index**——任何一次写操作都会推进它。若在索引变化但服务列表为空时也广播，会把「从未见过的服务」一起置成空快照、污染下游缓存。因此**注销/移除方向不广播**（现有 etcd 侧同构行为的差异写在 `TestWatch_ReceivesChanges` 注释里）。这不是可以「一改就绿」的 bug：真要覆盖移除通知，得把比对改成按服务名维护独立索引，属另一件事。
- 已证（GitHub 实测）：推送 `4c07bac` 后 `ci` run 41782592576 **六个作业全部 success**（`test`、`race`、`lint`、`vuln`、`integration`、`acceptance`），`workflow-lint` run 41782592595 同样 success。`integration` 作业里 `服务探活` → `集成套件` → `判据必须真的跑过` 三步逐一步 `success`；**第三步通过本身就是 consul 五条判据真实 `--- PASS` 的机器证明**——它在任何一条被判据名为 SKIP 时 exit 1，不会放行。这条作业耗时 2m56s，与 etcd/redis/mongo 三件套（run 41413712267）合起来覆盖四条接缝十个判据。
  - 取证明细限制（如实记）：`gh run view --log` 对这两个 run 返回空（非仓库管理员的日志读取权限，403），所以远端证据形态是**作业/步骤级 conclusion**，不是日志原文。这不影响判据强度：断言步骤的语义本身就是「判据没 PASS 就红」。
- 已证（GitHub 实测，不再是「以流水线为准」的悬置项）：推送 `cab08e3` 后 `ci` run 41413712267 **五个作业全部 completed/success**（`test`、`integration`、`lint`、`vuln`、`race`）；`workflow-lint` 是另一条流水线，同样 success。`integration` 作业第一次在 GitHub 上真实执行：三个 `services:` 镜像可拉起、bridge 网络下 etcd 端口映射生效、runner 的 Go 1.27.3 与本地 1.27.1 无差异、`workflow-lint` 的 `paths:` 过滤按预期触发。
- 未验证（部分收口）：Windows 周界作业（`windows.yml`）不覆盖这几个后端服务，也没有 `integration` 对偶；它用的 `go-version: 1.27.1` 与主 ci.yml 的 `1.27.3` 不一致，属既有状态，本轮未动。**这条「未验证」在 2026-11-02 的周界 schedule 里变成了真红**，见 §7.5。
  - 2026-11-04 收掉工具链那一半：`windows.yml` 的 pin 升到 `1.27.3`，与主 ci/`workflow-lint` 一致——周界作业要在**发布用的那个 Go** 上取证，否则「Windows 绿了」证明的是另一个工具链。这条一致性不再靠人看：`internal/docscheck` 新增 `TestWorkflowGoVersionsAgree`，比对 `.github/workflows/*.yml` 里所有 `go-version` pin，出现两个不同值即红（另有「pin 少于 2 处即 `t.Fatal`」的防空转断言）。两次变异实测：把 `windows.yml` 改回 `1.27.1` → 红并逐处点名列出 8 个 pin；把三个文件的 `go-version:` 键名整体改写 → 命中空转断言「只找到 0 处 pin」。复原后闸门 `ok`。**改后的远端取证**：`ci-windows` 手动 dispatch run 41756456016（head `559ac3d`）七个 step 全 `success`，含 `TestTree_EndToEndTimed` 的 CTRL_BREAK 停机全链路——所以这条 pin 不是纸面改数，它在 Windows 上按 `1.27.3` 真跑过一遍。
  - **`integration` 的 Windows 对偶不做，是平台硬约束不是偷懒**：GitHub 的自托管运行器参考明写「要跑用 Docker 容器动作或**服务容器**的作业，必须用 Linux 机器且装好 Docker」，托管 Windows runner 因此给不出 `services:`；etcd/consul/redis/mongo 那一侧要么外部提供、要么换 self-hosted Linux——两条都不是一改文件就能成立的事。Windows 的证据仍由 `windows.yml` 的窄口径承担（信号/控制台/路径语义），后端交互统一归 ubuntu 的 `integration` 作业，见 §7.5 的口径。

**7.1 补：kubernetes 后端真机作业（2026-11-04）**

- **结论：做，不是缓做**。原话里「需要真集群，所以挂起」的隐含前提是「CI 里给不出真集群」，这条前提实测不成立：`sigs.k8s.io/kind@v0.27.0` 装得上、`kindest/node:v1.31.0` 拉得动、集群能到 Ready，而且 GitHub 托管 runner 自带 Docker——kind 官方文档里就有 GitHub Actions 的用法。挂起项一旦变成可取证项，就不该继续挂。
- **落地时立刻查出一条真的生产缺陷**（这是这条腿最大的收获，不是测试覆盖率）：`registry/kubernetes` 的 `Deregister` 从**自校验闸门落地那次提交起就必然失败**——它的实现是「借道 `Register`，传一个只有空 `Metadata` 的实例回去」，而 `registry.ServiceInstance.Validate` 的闸门（`3647d71`，2026-10-08）第一句就拒空 Name。借道那个形状本身是 `30f2bc5`（2026-09-17，后端下放独立 module）带进来的，而闸门落地那次提交里这个模块**一个测试文件都没有**（`git ls-tree -r --name-only 3647d71 registry/kubernetes` 只有 `contract/`、`go.mod/go.sum`、`options.go`、`registry.go`），所以没有任何东西能响——也就是说**注销从来没生效过**。危害不只是「标签没摘」：按 §4.1「反注册失败不再被吞掉」的口径，这条错误现在**每次优雅停机都会报出来**，而流量仍会打到已停的 Pod。修法是自己发 strategic merge patch，把三个 `bald-service-*` 标签与两份注解一起置 null（值 null 即删键；只清标签会留下一份无人认领的 annotation）。
- **为什么独立成 `integration-kubernetes` 而不是并进 `integration`**：kind 那条腿的失败面是「Docker Hub 拉不到 `kindest/node`」「runner 上集群起不来」这类平台问题，与四条服务腿无关。放进同一个作业，第一次平台抖动就让**整个作业的结论作废**——etcd/consul/redis/mongo 四条腿明明没坏，也得跟着重跑一遍才能重新取证；而这种「不是我们的代码坏了」的红最难维护——下一个接手人加的是 `continue-on-error`，不是修集群，闸门就是这么废掉的。拆开后两边的判据都还是硬门，谁红查谁。这条口径值得单独记住：**给闸门加证据时，耦合失败面就是在预支将来的取巧动机**。
- **为什么 fake clientset 不够**（作业头注释与 `registry_integration_test.go` 头注释都写了）：这个后端的三条独有语义里，fake 一条都判不了——① `Deregister` 靠 strategic merge 的「值为 null 即删键」，fake 的 fixture tracker 不实现该合并语义，键删不掉但测试照样绿；② 发现侧只列 `Phase == Running` 的 Pod，而 phase 是 kubelet 写回的状态，fake 里由测试自己 set，判不出「真跑起来的才可发现」；③ `Watch` 依赖真 watch 的事件序列（informer 不与已存在的对象重放处理器）。
- 三条判据各钉一件事（`registry/kubernetes/registry_integration_test.go`）：
  - `TestRegister_WritesPodIdentityAndDiscovery`：`Watch` 先建（后加的处理器不会重放已有对象）→ `Register` → 直接从裸 Pod 断三个标签（`bald-service-id/-app/-version`）与两份注解（metadata、`{"8000":"http","8001":"grpc"}` 的端口→协议映射），再断 `GetService` 往返出的 `Endpoints` 逐字等于 `[http://IP:8000, grpc://IP:8001]`，最后断 `Watch.Next` 送达 1 条实例。**端口的 name 故意取 `port-8000`/`port-8001`**：端口名回退分支会拼出 `port://…`，所以「出现 http/grpc」只能来自注解里的映射表，这条形状是被设计成不可冒充的。
  - `TestDeregister_RemovesPodFromDiscovery`：注册 → 发现到 → 注销 → 断三个标签**和两份注解都从 Pod 上消失**，且 30s 内 `GetService` 列不到。发现侧的移除方向用 `GetService` 而不是 `Watch` 判，因为 `Watch` 的处理器按 `bald-service-app` 标签过滤，标签被摘掉等于把对象从订阅里摘出去，**移除事件根本不会广播**——这是后端的既有语义，不是本轮引入的缺陷，写在注释里。
  - `TestGetService_ExcludesNonRunningPod`：一个 Running + 一个 Pending（`NodeName` 指向不存在的节点，必然停在 Pending）都打上标签，断发现只返回 Running 那个——Pending 实例的 IP 是空的，端点会烂在解析侧。
- 验活（变异五条，逐个改生产代码 → 对应判据转红 → 精确复原并核对文件回到原内容）：A `Deregister` 改回借道 `Register`（即缺陷原样）→ 红在 `registry_integration_test.go:304`，报的正是那句 `registry: service instance Name is required (empty name registers under a malformed key)`；B 注销只清标签不清注解 → `:316` 点名两份注解仍是原值；C 删掉发现侧的 `Phase != Running` 过滤 → `:376` 报「Pending 的 Pod 也被当成实例返回了（[pend-1 run-1]）」；D 端口→协议映射按 scheme 建键（`ret[u.Port()] = u.Scheme` → `ret[u.Scheme] = u.Scheme`）→ `:234` 注解变成 `{"grpc":"grpc","http":"http"}`、`:254` 端点变成 `port://IP:8000 port://IP:8001`；E 注册不写 version 标签 → `:228` 标签为空、`:247` 往返出的 Version 为空。
  - 中途废掉一条假变异：首版 D 是「不写 `AnnotationsKeyProtocolMap`」，删掉赋值让 `protocolMapVal` 变成未使用变量 → **编译失败**的红，判据本身没被判，证明不了任何事。换成 D 那种「能编译、但语义错」的形态才算验活。
- 反证（Skip 不算通过那条闸门在这条腿上同样成立）：`KUBECONFIG` 指向 `https://127.0.0.1:1`（没有东西在听）→ 套件 **exit=0、3 条 SKIP**，判据步骤 **exit=1** 并逐条点名三条判据。探活步实测正向打出 `READY kubernetes (https://127.0.0.1:57230)`。
- **拓扑复现**：本机 kind 起一个与作业同名的集群（`bald-e2e`，kind `v0.27.0` + `kindest/node:v1.31.0`），把作业的四个步骤从 YAML 用 awk 导出成脚本后**在 bash 里原样执行**（只有两行不得不改：kind 二进制换 darwin/arm64、节点镜像换同一镜像的国内 mirror 标签——本机直连 Docker Hub 不通；这两行都不属于判据逻辑）。结果：`kind create --wait 180s` 35s（镜像已缓存），套件 `ok …/kubernetes 5.721s` + `ok …/kubernetes/contract 0.640s`，判据步逐条打出三条 PASS 后 exit 0；反向如上。闸门侧：`task ci-workflow-lint`（actionlint 1.7.12）与 `rhysd/actionlint:1.7.12` 容器（含 shellcheck）双 exit 0；`task verify` exit 0；`registry/kubernetes` 的 gofmt / vet / `go mod tidy -diff` / golangci-lint 全清。
- 这条腿**没覆盖**的（写清，别当成「k8s 后端全验过了」）：① `New()` 走 `rest.InClusterConfig()`，只有 Pod 内才成立，本腿一律用 `NewWithClient` 注入（就是包注释给的「集群外」正道）；② `GetNamespace()` 读 `/var/run/secrets/.../namespace` 的包级变量在 Pod 外为空，测试是直接把它指向自建 namespace，所以那条读文件的代码没被执行；③ 真集群的 RBAC——测试里 informer 缓存同步失败会 `t.Fatal`（「RBAC 不允许 list pods？…会在空缓存上假绿」），这是防「权限不足时列不到东西反而让断言通过」，但它本身要求 runner 侧是 cluster-admin（kind 是），生产要另配 `pods` 的 get/list/watch/patch。（原先还列了第④条「runner 能否从 Docker Hub 拉到 `kindest/node`」——远端 run 41787082957 已把它答成能，见下条取证。）
- 消费侧口径不变：`registry/kubernetes` 在仓内仍**无消费方**（codegen 模板、`_example`、gateway resolver 都没引它；`grep -rn "bald/registry/kubernetes"` 的非自身命中为空）。本轮补的是后端自身的真机证据，不是把它接线；按 §2 的口径它照常随仓库发布、按 reference-only 保留。
- 已证（远端，head `ea7ffe3`，run 41787082957）：`integration-kubernetes` 作业 **success**，七个 step 逐个绿——起 kind 集群 → API 探活 → 集成套件（非 `-short`，逐条留档）→ 「判据必须真的跑过」。**判据步绿本身就是三条 `--- PASS` 的机器证明**（它按名字逐条 grep，Skip/改名/没跑都 exit 1）。同一 run 的 `test`/`race`/`lint`/`vuln`/`integration`/`acceptance` 也全 success，所以这个提交是一条完整绿证，不是「只有新腿绿」。runner 从 Docker Hub 拉 `kindest/node:v1.31.0` 由此成立——本机当初改用 mirror 只是因为直连不通，不是镜像本身够不着。
  - **这里有个先前想错过的地方，记下来免得再犯**：`concurrency: ci-${{ github.ref }}` + `cancel-in-progress: true` 只作废**还在跑**的那次 run，已经 completed 的绿证不会被后续推送抹掉——所以「为了保住 run 41782592576 而把这条腿压到下一个提交再推」是不成立的取舍，正确做法是直接推、然后盯这条新作业的第一次结果。若它因 runner 拉不到 `kindest/node` 而红，红的只是这条腿自己的作业，etcd/consul/redis/mongo 四条腿的证据不受牵连——这正是把它拆成独立作业买到的东西。

**7.1 补：示例的去仓库外依赖（2026-11-04）**

- 加了什么（其实是**删**）：`_example/bald/go.mod` 里两条指向仓库外的 `replace`（`../../../kratos/contrib/{config,registry}/nacos`）连同 nacos-sdk-go **v1** 的 require 一起去掉；`-tags nacos` 的配置源改走仓内 `bconfig/nacos`，经 `appkit.ConfigLayers` 显式叠一层（`internal/bootstrap/register_nacos.go`）。实测：仓库外相对路径在 `go.mod` 里 **0 命中**、`go list -m all` 里 kratos contrib 条目 **0 条**。
- **为什么值得单开一节**：它拆掉的是本节「刻意没做 ①」的前提。在**新开的 worktree**（干净检出后把本轮改动文件复制进去）里 `go mod download` exit 0——示例 module 现在装得起模块图了。剩下的两条阻塞与 `replace` 无关：`api/gen` 是 gitignore 的 proto 产物（编译要 buf + BSR 网络，即 §7.9 那套工具链），以及那几条服务发现用例要真 etcd / 真 nacos。所以「示例进 CI」从今天起是**可规划的**，但仍不是加一步就行，而且按失败面隔离得自己一个作业。
- **真机证据**（本机 `nacos-server:v2.3.2` standalone + `-tags nacos` 起真实进程），五件事各自独立成立：
  - 配置真从 nacos 进来：`curl :8080/v1/greet/alice` → `{"greet":"from-nacos-v1, alice"}`，而 `from-nacos-v1` 只写在 nacos 那份 `bald-demo.yaml` 里，仓内 yaml 没有这个值。
  - 热更不重启：把同一 dataID 改成 `from-nacos-v2-hot` → 约 6s 后同一 URL 返回新值，进程没动。
  - 默认构建不受影响：不带 tag 起进程、nacos 还开着也不接它 → `{"greet":"hello, alice"}`，最小路径照旧。
  - 层序可证：`BALD_DEMO_BIZ_GREET_GREETING=pinned-by-env` → `{"greet":"pinned-by-env, alice"}`，env 压在契约源层之上，与 §7.7 及配置系统文档的口径一致。
  - 优雅停机：SIGTERM → `received signal, shutting down` → `deregistering` → 端口释放。
- **当场查出一条生产缺陷**，而且它不在示例里：`bconfig/nacos` 把用户/契约写的 `namespace` **原样**塞进 SDK 的 `NamespaceId`，而 nacos 的默认命名空间在 HTTP 参数（`tenant`）里必须是**空串**——`NamespaceId="public"` 时 `GetConfig` 返回**空内容且不报错**。危害形状是最难查的那类：源接上了、启动不报错、值一个也没进来。修法：`PublicNamespace` 常量 + `namespaceID` 折叠（其余取值原样透传），并把它从 `init` 拆成可测的一层 `clientConfig`，配 `TestNamespaceID_DefaultNameFoldsToEmpty` / `TestClientConfig_NamespaceIsFolded`（删掉折叠分支两条一起红）。
  - 因果链要说清：示例旧代码自己硬编码 `NamespaceId: "public"`（v1 client 那条路径），所以缺陷的表现**早就存在**，只是被示例的绿色掩盖了——两个端到端断言在 nacos 里真写着别的值时仍然绿，因为它们断的 `hello` 恰恰是「远端层贡献零个键」的产物。这是本节主条「Skip 不算通过」的另一面：**跑过也不等于跑对**。修法落在仓内而不是示例里，因为下一位接 `bconfig/nacos` 的人撞的是同一条。
- 折叠落地后转红三条，逐条按根因处理而不是放宽：
  1. `internal/bootstrap/plan_acceptance_test.go` 的变体激活断言硬编码 `v.Name == "grpcgw" && grpcgwOn`——任何第二个变体都被断成「应为 false」。改成通用形状：按 `VariantDecl.When` 调 `appkit.EvaluateBuildConstraint`（`When` 为空则回退到 `BuildTags` 的 `&&` 连接），即拿二进制**真实的** tag 集判，两个变体各自成立。
  2. & 3. `TestGreet_REST_GeneratedBinding`、`TestGatewayBackendViaDiscovery`：把业务值钉在 **env 层**（`t.Setenv("BALD_DEMO_BIZ_GREET_GREETING", "hello")`），断言文案一字未改仍写「hello, world」。理由：这两条断的是绑定/路由/发现，不该依赖「本机那个 nacos 里正好写了什么」；env 在声明式源之上，所以这个钉法对四种构建形态都成立，且无 tag 时它等于域内默认值。
- 验活（变异四条，改完即精确复原；用 `cp` 备份，**不用 `git checkout --`**）：① 把计划测试的 `want` 改成恒 `false` → `-tags nacos` 红在变体 `nacos`、`-tags grpcgw` 红在变体 `grpcgw`、无 tag 全绿——这条断言从今天起真的在按构建形态判；② 删掉 `e2e_helpers_test.go` 的钉值 → `TestGreet_REST_GeneratedBinding` 在 `-tags nacos` 红，证明钉值不是装饰；③ 把钉值改成 `pinned-other` → **无 tag** 构建也红并打出 `pinned-other, world`，证明 env 这条注入真的通到了 `Settings()`；④ 删掉 `discovery_e2e_test.go` 的钉值 → 泄漏出 `from-nacos-v2-hot, discovery`，正是原缺陷的症状文本。复原后 `none / nacos / grpcgw / "nacos,grpcgw"` 四种 tag 组合的 `go test ./...` 全 `ok`，`bconfig/nacos` 亦 `ok`。
- 顺带查出一条只在干净检出上咬人的工具链漂移：入库的 `_example/bald/api/buf.lock` 是 `version: v1`，而 `api/buf.yaml` 是 `v2`，于是 `buf generate` **直接拒绝**（`got a v1 buf.lock file for a v2 buf.yaml`）。`task proto-example` 用 `buf dep update || true` 把它盖住了——本地永远看不见，只在**没网**的机器上以一句读不懂的失败现身。改法是提交 buf 自己产出的 v2 锁（protovalidate 依赖 commit 未变；googleapis 由 `c17df5b2…` 前进到 `8ac61b9e…`，那是 `buf dep update` 的正常产物），实测此后 `buf generate` 不再需要 `buf dep update`。与 §7.9 同一类：**工具版本/格式差异冒充语义变更**。
- 一条**证据链**教训（当场纠正，未落进代码）：第一次「新二进制仍返回 `hello, alice`」的结论是错的。`lsof -nP -iTCP:8080` 显示在听的是 15:04 由 `go run` 缓存产物起的老进程，新二进制其实因 `listen tcp :8080: bind: address already in use` 根本没起来。判「跑的是哪份代码」之前先核对监听 PID 与它的构建时间。
- 未覆盖（如实记）：① ~~示例仍不进 CI，阻塞见上~~ **同日已闭合**：`example` 作业按 pin 装 buf 生成 `api/gen`、注入 etcd/nacos 服务容器、三态 24 条判据逐条断真 `--- PASS`，见下面「7.1 补：示例 module 进 CI」；② ~~只验了 nacos **配置源**侧——同 tag 下的**注册中心**接管其实是死的，新登记 §7.10~~ **同日已修**：钉住按构建形态拆开，装配侧另落一条真机用例（`TestNacosAssemblyRegistersIntoBackend`，本地跑——`-tags nacos` 不是 CI 的构建形态），同一天另两条**机制**判据已下沉到 `registry/nacos/contract` 并进 CI，见 §7.10 第三轮；③ ~~`public` 折叠在配置 API 上是实测，nacos **命名 API** 的同形状未测（HTTP 探针走非 ephemeral 注册返回「service not found」，判不出，见 §7.10）~~ **同日用 SDK ephemeral 注册实测判出，结论相反**：命名 API 里 `public` 与 `""` 是同一个租户，`registry/nacos` 原样透传是对的、不该折叠（配置 API 必须折叠才拿得到默认租户）。这条探针最初确实判不出——判得出的是把注册换成 ephemeral，见 §7.10。
- 远端绿证（GitHub 实测，2026-11-04）：这条迁移的两个提交（head `b8906b7`）的 `ci` run 41795844844 **七个作业全部 completed/success**（`test`、`race`、`acceptance`、`integration-kubernetes`、`vuln`、`lint`、`integration`）。
- 文档同步：《Bald 配置系统》§4.2（远程桥改成 reference-only；`ConfigLayers` 一条换成示例真实的 `nacos.New(...)` 写法，并给出可复制即跑的验证块）、《Bald 配置系统设计》§1.6（折叠规则 + 实测形状 + 注册侧未测）、`ci.yml` 的 `integration` 作业头注释、`internal/docscheck` 三条 `shortSkipExemptions` 的理由（原文写「`go.mod` 有仓库外的 kratos contrib replace」，这条理由已经不成立，换成 `api/gen` 那条真实阻塞）。

**7.1 补：示例 module 进 CI（`example` 作业，2026-11-04）**

- 加了什么：`ci.yml` 里 `integration-kubernetes` 与 `acceptance` 之间新增 `example` 作业，把 `_example/bald` 的 e2e 面第一次搬到远端取证。形状：`services:` 注入 etcd `v3.5.14` + `nacos-server:v2.3.2`（`8848` 与 `9848` 都做端口映射）→ 按 pin 装 buf → `buf generate` 出 gitignore 的 `api/gen` → 三路 TCP 探活 → 三态构建逐套件 `-v` 留档 → 末尾按 `EXPECTED` 清单断言每条有真 `--- PASS`。
- 为什么是**单独一个作业**而不是往主循环加一步：① `scripts/list-modules.sh` 的谓词本来就排除 `./_example*`，四条主循环天然碰不到它，此前这个 module 的远端证据是**零**；② 它的装载方式不同（17 条 in-repo `replace`、`GOFLAGS=-mod=mod`）；③ 失败面不同——proto 工具链 + 两个真后端 + 端到端时序，混进 `test` 作业会让一次 flake 洗掉全部模块的判定。
- 三条阻塞各自怎么倒的：
  - 仓库外 `replace`：同一天早先那条已撤（见上节），这里直接复用其结论。
  - `api/gen` 是 gitignore 的：作业内先 `sh scripts/install-buf.sh "$RUNNER_TEMP/bin"`（版本读 `bconf/buf.version`、摘要按 `bconf/buf.sha256` 校验，与 `proto-contract` 作业**同一个脚本**，不再各装一份），再 `(cd _example/bald/api && buf generate)`。`buf dep update` **故意不跑**——`buf generate` 读入库的 `api/buf.lock`（BSR 依赖已 pin），而 `dep update` 会重写这份受版本管理的文件。这条不是口头约定：生成后紧跟 `git diff --quiet -- _example/bald/api`，一旦产物/锁文件被改动就打 `::error::` 并 `exit 1`。
  - 要真后端：`services:` + 探活。9848 单独探一次是这里唯一能看出「**半死**的 nacos」的线索——v2 SDK 的 naming 走 `8848+1000`，8848 通而 9848 不通时注册**静默失败**，用例只会以「查不到实例」现身。
- 判据数：默认态 13 + `-tags grpcgw` 10 + `-tags nacos` 装配 1 = **24** 条 `EXPECTED` 行，逐条 `--- PASS` 才算过。`-tags nacos` 那态只跑装配单条（形状取自用例文件头注释的自带命令），原因与新登记的 §7.13 是同一条（该条同日已修，但重试摊开突发而非消掉它，所以**形状不变**）；这不是把判据调窄——现状 `task example-e2e` **根本不含** nacos 态，这一条是净增。
- docscheck 耦合（改这里必须同改那里）：`TestAcceptanceTableCoversShortSkippedTests` 放行一条 `-short` 跳过的用例有三条分支——进验收表 / 在 `.github/workflows/*.yml` 里被点名 / 在 `shortSkipExemptions` 里。把示例那几条的名字写进 `example` 作业的 `EXPECTED` 之后，它们全部落到第 2 条分支，原来的 4 条示例豁免就成了「过期条目」（该检查按 `seen` 判，留着死条目等于把判据关掉），所以 `shortSkipExemptions` 清成了空 map，并在注释里写明**空本身是判据**。实测计数器：`判据腿=9 覆盖包=4 候选=6 进表=0 CI点名=6 豁免=0`。另有一条 `TestWorkflowGoVersionsAgree`：新作业的 `go-version` 必须与其余 pin 同值（现 11 处 pin，全 `1.27.3`）。
- 本机正向证据：三态各 exit 0、`EXPECTED` 24/24 打出 `PASS`（重放用 **bash**——zsh 不按空白拆词，`for row in $EXPECTED` 在 zsh 里只跑一轮会变成假绿）。工具链按 CI 口径对齐过一次：本机 PATH 上的 buf 是 1.57.2 而 pin 是 1.50.0，所以用 1.50.0 重生成 `api/gen` 后重跑三态，证据不落在另一套工具链上（§7.9 同类坑）。
- 验活（三道变异，各自精确复原）：① 把 `EXPECTED` 里一个测试名改坏 → 该条红（`::error::判据 … 没有 PASS 记录`）；② 删掉一份日志文件 → 命中同一条红；③ 把注释写进 `EXPECTED` 字符串内部 → 红且文案点名「EXPECTED 里有非 `<日志>`|`<测试名>` 的项」。另过 `task ci-workflow-lint`（actionlint 1.7.12）exit 0。
- **顺带查出一条测试侧的断言竞态并已修**（与 §7.13 是两件事，各自独立成立）：装配用例的终态守卫把 `<app>.grpc` 轮询到零，却对 `<app>.http` **只查一次**——nacos 按 `<服务名>.<scheme>` 存成两个独立服务，两条 ephemeral 摘除是各自独立的事件，所以「HTTP 面此刻还剩一个」在单条跑里就会发生（实测 8 次红 1，红在 `.http` 那条 `t.Fatalf` 上）。改成 `waitNacosHostsGone` 两条各 15s 轮询后，单条 **12/12 全绿**；牙齿用 `stop()` → `_ = stop` 变异重证：红在 15.52s，文案照旧指到「client 没关，心跳还在续租」。
- 未覆盖（如实记）：① ~~远端绿证以本次推送后的 `example` 作业为准（尚未取得）~~ **已取得**（2026-11-04，分支 `ci/example-leg` head `b57dd1e`，run 41832136892 **九个作业全部 completed/success**，作业 `example` = job 126581704475）。日志逐条交出取证：`install-buf: 装好 buf v1.50.0（Linux-x86_64，摘要已按 bconf/buf.sha256 校验）` + `buf --version -> 1.50.0`、`READY etcd (127.0.0.1:2379)` / `READY nacos (127.0.0.1:8848)` / `READY nacos-grpc (127.0.0.1:9848)`、生成步 `git diff --quiet` 守卫未触发（说明 pin 的 buf 在干净 runner 上复现了入库的 `buf.lock`），判据步 **24/24** 打出 `PASS  <名>  (<日志>)`，含 `--- PASS: TestNacosAssemblyRegistersIntoBackend (0.97s)`。三态的包级结果：`internal/bootstrap ok 2.450s`（默认）/ `ok 0.189s`（grpcgw）/ `ok 0.999s`（nacos）。
  - 同一形状在 **main** 上复跑一遍也是同一结论（head `6d7eda7`，run 41834825763 **九个作业全部 success**，作业 `example` = job 126590928265：日志同样 24 行 `PASS  <名>`、`buf --version -> 1.50.0`、`READY nacos-grpc (127.0.0.1:9848)`、`PASS  TestNacosAssemblyRegistersIntoBackend (/tmp/ex/nacos.log)`）——所以这条不是「分支上侥幸过一次」。
  - 那两次跑里唯一的 SKIP 是 `internal/apiserver` 的 `TestBizLayerImportsAreBusinessOnly`（`no biz layer yet (gen domain seeds it)`——这个示例工程没有 `biz/` 目录，它是按「目录存在才判」设计的分层守卫，不是 `-short` 跳过，也不在 24 条判据里）。它不在判据清单里是**已知且刻意**的：本作业断的是装配/绑定/发现/限流这些端到端面。
  - ② 装配那条**机制**判据（只注销 / 只 cleanup）仍在 `registry/nacos/contract`，由 `integration` 作业跑——`example` 作业里的这条是终态守卫，判不到「谁摘的」；③ 整包 `-tags nacos` 的并集红已修（§7.13：`bconfig/nacos.Load` 对上 SDK 进程内令牌桶的有限重试），但**作业形状没跟着改**——重试摊开突发而不是消掉它，单条仍是当前口径。
- 文档同步：`ci.yml` 文件头（示例「不进四条主循环，但有自己的 `example` 作业」）、`integration` 作业头（原来写「刻意不含 `_example/bald`」的 12 行改成指向新作业）、`Taskfile.yml` 的 `example-e2e`（补第三条 nacos 态命令 + 单条形状的原因）、`scripts/acceptance-gate.sh` 的 `scope=ci` 块（示例两态改由 `example` 作业断，buf 不在那里重复装）、`_example/bald/internal/bootstrap/nacos_assembly_e2e_test.go` 头注释里「本示例 module 进不了 CI」那句。

**7.2 无覆盖率闸门** ✅ 评审初值：所有 workflow grep `coverage|codecov` → 无匹配。没有任何比例约束，零测试模块不会因此暴露——§1.3 那两个适配器是评审时数测试文件点数出来的，不是闸门响出来的。（原话里的「根 module 有 365 个测试文件」这个数字已随 §3.1/§3.4 两次拆分失效：现在根 module 只剩 `internal/docscheck` 一个包、0 个非测试文件，94 个 module 的测试分布在 92 个 module 里。）

**7.2 收口（已落地：`TestModulesWithLogicHaveTests`）**

- 口径选择：**先立不变量，再谈百分比**。闸门判的是「有可执行逻辑的 module 必须有测试文件」，不是覆盖率数字。理由写在测试的文档注释里：94 个 module 的比例阈值要逐个实测才有出处，而适配器那一半的分母由「后端不可达即 `t.Skip`」主导——先给它一个假的分母，闸门就成了装饰。覆盖率工具（`go test -coverprofile` + 逐包合并）在跨 94 个 module 的形态下是另一件事，未做。（这段是**落地当时**的理由；同一天把基线实测出来之后，理由已换成「分母是口径的函数」，见下面「7.2 补」。）
- 落地位置：`internal/docscheck/docscheck_test.go`，随 `go test ./...` 与 CI 的 `test` 作业跑——不新增作业、不新增工具依赖，成本一条断言。
- 判据形状（**豁免是判据，不是名单**）：`topLevelFuncBodies` 用 `go/ast` 只数**带函数体**的顶层 `FuncDecl`，所以「只有 interface/type/const 的契约包」天然是 0 而不索要测试；`moduleGoFiles` 逐 module 走目录树，嵌套 module 各自统计（否则根 module 会把全仓吞进来），并与 CI 的清单同源地排除 `_example*`、`testdata`、`vendor`、`.git`。
- 实测分布：总 94，有逻辑 89，含测试 92，纯声明豁免 2（`metrics` 两个接口、`transport/subscribe` 两个类型）；根 module 也落在豁免侧（唯一的包 `internal/docscheck` 只有测试文件），它本来就只由闸门自己构成。
- 验活（正向）：三条防空转断言——派生 module <50、有逻辑 <50、含测试 <50 各自 `t.Fatal`，谓词被改坏时是响而不是「零违规」。
- 验活（变异，两次实测后复原）：① **把 `broker/kafka` 的两个测试文件移出树** → 红在 `module broker/kafka 有 94 个顶层函数却一个测试文件都没有`，即这条闸门真的能自己喊出 §1.3 那一类缺口（而不是等下一次人肉数文件）；② **把 `bodies == 0` 的豁免判据改成恒假** → `metrics` 与 `transport/subscribe` 同时转红并报「有 0 个顶层函数」，证明豁免面由判据撑着：删掉判据就多索要两份「断言接口存在」的假覆盖。复原后 `git status` 对 `broker/` 干净、闸门 `ok`。
- 刻意没做：① 比例/覆盖率阈值（落地当时的理由是「要先有 94 个 module 的实测基线」；基线已在同一天实测出来，阈值仍不做，新理由见上面「7.2 补」）；② 不给纯声明 module 补「测试存在性」用例；③ 不改 `test` 作业的 `-short` 口径——这条闸门是静态结构判据，`-short` 里也照样执行。

**7.2 补：那条「先有基线再谈阈值」的债，2026-11-04 把基线实测出来了——结论仍是缓做，但理由换成实测**

- 怎么测的：两条口径各跑一遍全部 94 个 module，都在本机、`-short`、`-count=1`。
  - **逐包口径** `go test -short -cover ./...`：166 行包里 **154 个有数字**、6 个 `[no test files]`（`metrics` 与四个 `transport/{kafka,rabbitmq,redis,rocketmq}/testapi`）、3 个 `coverage: [no statements]`（根 module 的 `internal/docscheck`、`cache`、`ratelimit`——模块根只剩接口/常量）、3 行 `FAIL` 汇总且全部出自同一个包 `bconf`（默认 PATH 下那条腿按 §7.9 设计判红，不是覆盖为 0；所以下面的分档里没有 `bconf` 本包）。分档：**0% = 0 个**、0–20% = 13、20–40% = 11、40–60% = 13、≥60% = 117；未加权均值 73.8，最低 1.2（`contrib/store-mongo`），最高 100。
  - **逐 module 合并口径** `go test -short -coverpkg=./... -coverprofile=…` 再 `go tool cover -func | tail -1`：94 个 module 里 92 个有数字、2 个整模块无测试文件（`metrics`、`transport/subscribe`）。分档：<20% = 10、20–40% = 10、40–60% = 11、60–80% = 15、≥80% = 46；未加权均值 67.4。**最低三档是 0.0%，而它们是 `.`、`cache`、`ratelimit`——module 根里根本没有可执行语句**。
- 为什么这条基线**加强而不是削弱**「先立不变量」的选择：那三个 0.0% 正是假红的形状。任何「每 module ≥ X%」的闸门会当场判红三个 module，而那三个的错误是「没有东西可覆盖」，与 §7.2 闸门管的「有可执行逻辑却没有测试」恰好相反——要躲开它就得再引入一条「语句数为 0 就豁免」的判据，也就是把这条阈值退化回结构不变量。
- `-short` 让同一个 module 有两个数（这是分母不可固定的直接测量，不是推测）：`registry/nacos` 合并口径 `-short` = **29.3%**、去掉 `-short`（真 nacos 在场）= **45.7%**，差 16.4 个百分点；它的逐包数（9.7 / 60.5）与合并数（29.3）又互不相等，因为本仓的适配器普遍把真机套件放在同 module 的 `contract` 兄弟包里——`go test -cover` 默认只算被测包自己的语句。同一件事在 `contrib/store-mongo` 更极端：`-short` 逐包 1.2%，而它的 `TestMongoCRUD` 只在 `integration` 作业跑。
- 由此确定的姿势（如果将来仍要做，按这条做，别做逐 module 阈值）：**全仓一个数 + 棘轮**——分母固定为「`-coverpkg=./...` 合并、去掉 `coverage: [no statements]` 那三个、把 `gen/go` 这类生成码显式列进排除」，阈值取当次实测再只许上调；`-short` 与真机两种口径各留一条数，不许互相替代。代价是要在 CI 里多存一份 profile 合并产物，且判据的语义是「不许比昨天差」而不是「够不够好」。本轮没做，因为它不新增任何一条真实判据（结构缺口已被 `TestModulesWithLogicHaveTests` 挡住），而它必然带上「生成码算不算分母」这类新的口径裁决——那属于 owner 决策，不是一次收尾。
- 基线的可复跑性：两条命令就是上面那两条，任何机器上都能原样跑出同量级的数（`-short` 下真机相关的包本就被跳过，所以那 13 个 <20% 的可复现性最高）。本节数字是**本机一次实测**，未进 CI，也没做成闸门。

**7.3 已具备的成熟度**（避免只报负面）：`-timeout 5m/10m`、codegen 树的端到端用例拆成独立步骤单独给 15m 预算（`test` 作业的 `TestTree_*` step）、依赖下载 retry×3、golangci-lint/govulncheck 版本 pin（`install *` 两步）、基准冒烟编译每模块跑（均在 `.github/workflows/ci.yml`，行号随作业增删漂移，故按 step 名指）。核心包（`pkg/appkit`、`bconf`、`bconfig`、`transport/http`、`pkg/middleware`）非测试代码里 **TODO/FIXME/HACK = 0 命中**——干净到更像是「债务存在但没标记」，而非没有债务。

**7.4 统计量被当不变量断言（中）** ✅ 发现路径不在评审时，而在推送后：`test` 作业红过一次（run 41388597082：`circuitbreaker/sres` 的 `TestConcurrentExecute` 报 `allowed = 89/100, want >= 90`）。这类问题只有跑够次数才暴露，静态评审看不出来。

- 根因不是「阈值定得太严」：`Allow` 里 `accept = requests/(requests+1)`，`requests` 是**已 MarkSuccess 的条数**。100 个 goroutine 并发时，第 k 次 `Allow` 看到的 `requests` 取决于 Allow 与 Mark 的交错，落在 `requests=1` 那一档时单次拒绝率是 1/2——放行数本来就是调度次序的函数，`≥90` 只是大概率成立。本机实测 `go test -count=400` 红 15 次（**3.75%**），最低放行 87；与 CI 观察（约 8 次运行红 1 次）同量级。
- 修法（**阈值 `≥90` 一字未动**，改的是前置状态）：并发 burst 之前先 `MarkSuccess` 1000 条灌满窗口。此后单次拒绝率上界恰为 `1/1001`，而且两个方向只会更松——`MarkSuccess` 让 `requests` 只增（accept 上移），窗口轮转把计数清零后走 `requests==0 → accept=1` 的早退分支。于是「100 次里拒绝 ≥11 次」的概率按 `p=1/1001` 的二项尾算得 `1.3e-19`，`≥90` 第一次成为**可证的界**。
- 顺带补上一条真空：拒绝分支的**错型**在并发面根本没被跑到——prime 后 100 个 goroutine 大概率一次都不被拒（变异「拒绝改返回别的错误」实测溜过了整条用例）。新增 `TestConcurrentExecuteRejectedErrorKind`：把窗口灌成 `accept<=0` 的全失败状态，100 个并发 `Execute` 必须全部以 `ErrCircuitOpen` 失败，这条是确定性的。
- 验活（正向）：三条用例 `-count=1000` 全跑 **0 失败**；并发放行实测分布 98..100（阈值 90，最低 98）。新用例 `TestHealthyWindowAcceptanceNearOne` 的阈值出处写在注释里：拒绝数 ≈ Poisson(λ=2000/1001≈2.0)，`minPass=1985`（容忍 ≤15 次拒绝）对应 `P(R≥16)≈4.7e-10`；同批 1000 次实测 R 落在 0..7、均值 2.06，最低放行 1993（样本外余量 8），分布形状与 λ=2 吻合——这条界是算出来再被实测确认的，不是拍脑袋放宽的。`-race -count=100` 三条全绿。
- 验活（变异，四条）：① 拒绝分支恒拒绝 → 并发用例红 `放行 = 0/100`、窗口用例红 `0/2000`；② 拒绝率升约两个数量级（公式分母 `+1` 改 `+100`：prime 后单次拒绝率 `1/1001 → 1/11`）→ 并发用例红 `87/100`、窗口用例红 `1816/2000`；③ 拒绝改返回别的错误 → 新增错型用例红 `want ErrCircuitOpen`；④ 忽略 `closed` 标志 → 既有 `TestClose_RejectsAfterClose` 红（对照组，说明既有覆盖没被这次改动削弱）。
- 刻意没做：① 不给 breaker 加可注入随机源——那能让比例断言完全确定，但属生产 API 变更，与「修一条假红」不成比例；② 不放宽阈值来消红；③ 同类写法已 `grep` 核对：非测试代码里用随机源的只有 `circuitbreaker/sres`、`retry`、`registry/consul`、`broker/kafka`（后者退避抖动已由 `fixedRand` 注入变成确定断言），`retry` 与 `registry/consul` 的测试里没有比例阈值断言（实测 grep 为空）。
  - **这条收口当时留了一个同文件的缺口**：③ 那句核对的是「别的包有没有比例阈值断言」，没回看 `sres_test.go` 自己。同文件里 `TestExecute_AllSuccessNotThrottled`（冷窗口、不 prime）仍是同形状的统计判据，2026-11-04 在 `race` 作业红出——见 §7.11。
- GitHub 实测：修复合入后 `ci` run 41416339090（head `80edb7b`）五个作业 `test`/`integration`/`lint`/`vuln`/`race` 全部 success——这条用例红的是 `test` 作业，现在同一作业在带 prime 的版本上通过，且 `race` 作业（覆盖 `circuitbreaker/sres`）也过。剩下唯一没被证过的仍是次数：CI 每次 push 只跑一遍，误红率是算出来的（`1.3e-19` / `4.7e-10`），不是跑出来的。

**7.5 Windows 周界作业的真红（中）** ✅ 已修：本周一的 `ci-windows` schedule（run 41428497056，head `5585a21`）把上面那条「Windows 未验证」的悬置项变成了一次真实执行——它跑 `root` + `bootstrap` + `contrib/authz-casbin` 三个窄口径 module，root module 的 `go test -short` 直接红，四类失败全是跨平台适配，不是产品语义：

| 失败点 | 根因 | 修法 |
|---|---|---|
| `cli/internal/codegen` `TestTree_VariantsProjectedAtRuntime`（tagged/untagged） | 测试用 `go build -o <无扩展名路径>` 再 `exec` 该路径；Windows 上产物是 `<name>.exe`，于是 `executable file not found in %PATH%` | 改用带扩展名的 `demoapp.bin`，与同包另三处构建口径一致 |
| `cli/internal/codegen` `TestGen_CheckClean` | `--check` 的差异文案打的是**本机分隔符**路径（`out-of-date: cmd\demoapp\app\command.go`），而清单与调用方按 **slash 身份**匹配（清单写入时用 `filepath.ToSlash`） | `checkTree` 的 `missing:`/`out-of-date:` 统一按清单身份（slash）输出；Linux 无变化，Windows 才显形 |
| `internal/docscheck` 三条用例 | 仓库根发现用精确串 `"module github.com/kalandramo/bald\n"`；Windows runner 的 git 默认 `core.autocrlf=true`，go.mod 落盘成 CRLF → 「repo root not found」 | 按行解析 module 声明（`moduleLine`），对行尾形态不敏感 |
| 同一 CRLF 条件的另一半 | 文档正文也是 CRLF，`module-list` 围栏正则与链接判定失真 | 读文本统一走 `readText`（CRLF→LF 归一） |

- **系统性收口是 `.gitattributes`（`* text=auto eol=lf`）**：只固定检出形态，不改变任何已提交内容（实测全仓文本 blob 里 CR 命中为 0）。没有它，任何按字节比对检出文件的测试都会在 Windows 上重演同一件事。
- 复现与验活（本机做，不等 Windows）：`git -c core.autocrlf=true clone` 出一份 CRLF 工作树，`go.mod` 首行实测 `d \r \n`——**旧代码在同一棵树上红 4 条，报的正是 CI 那句 `repo root (go.mod with module github.com/kalandramo/bald) not found`**（先复现根因再改）。改后同一棵树只剩 `TestModuleDerivationMatchesScript` 红：CRLF 的 `scripts/list-modules.sh` 被 `sh` 拒掉（exit 2）——这条在 Windows 上本就 `t.Skip`，而 `.gitattributes` 让检出根本不产生 CRLF；带该文件的克隆实测 `go.mod` 首行回到 `d \n`。
- 刻意没做：① 不在产品里为 Windows 造第二套路径策略——清单身份就是 slash，其余人类可读输出（`generated:` 等）保持本机形态；② ~~不动 `windows.yml` 的 `go-version: 1.27.1`（与主 ci 的 `1.27.3` 不一致是既有状态）~~ **这一条在 2026-11-04 撤销**：pin 已升到 `1.27.3` 并加了 `TestWorkflowGoVersionsAgree` 防再次漂移，理由与两次变异实测见 §7.1「未验证」条；③ 不把周界作业扩成多 module（窄口径是既定设计，全量归 ubuntu）。
- 已证（dispatch run 41445557462，head `474a939`，7m5s completed/success）：上面「从未执行」的三步第一次真跑，全绿——`TestTree_EndToEndTimed`（CTRL_BREAK 停机链路）、`bootstrap` build+vet+test、`contrib/authz-casbin` build+vet+test。同一 head 的主 `ci`（run 41445554812）五个作业亦全绿。本轮修法覆盖到 Windows 周界作业的全部 step，不再有余下未执行段。
- 仍不能证明：`ci-windows` 的窄口径（root + bootstrap + casbin 三个 module）不等于全仓 Windows 兼容——其余 89 个 module 在 Windows 上从未跑过，ubuntu 上的全量清单不含它们。周界作业的人工设计（「全量归 ubuntu」）本身就承认这一层，不是本轮能收口的证据缺口。

**7.6 热更把「写到一半」当变更派发（中）** ✅ 已修：发现路径不在评审时，而在又一次推送后——`d515fa0`（**纯文档提交**）的 `ci` run 41485136191 里 `race` 作业红在 `bconfig`：`--- FAIL: TestWatchValue`（`github.com/kalandramo/bald/bconfig/file`，`FAILED AT MODULE: ./bconfig`）。同一个 run 的 `lint` 也红，但红在依赖安装步骤（`goproxy.io/sumdb/sum.golang.org/... read: connection reset by peer`），是 runner 侧网络，与仓内代码无关——两条分开记，不要把一条的绿拿来解释另一条。

- 根因：`os.WriteFile` 是 O_TRUNC 之后再写，就地重写期间存在一个「文件 0 字节」的窗口，而 inotify 在 truncate 那一刻就已经发事件；`WatchValue` 收到事件直接 `os.ReadFile`，于是把空载荷当成一次变更派发出去。消费侧 `bootstrap/config` 的 `apply` → `decodeDocument` 对 `len(data)==0` 返回 `(nil, nil)`（不是错误）→ 这一层缓存被清空并触发一次 `OnChange`。如果这是最后一次写（CI 那次正是），配置会一直缺项到下一次真实变更为止。
- 量化（把 `readSettled` 退化回「直接派发第一次读」即可复现，`-race`）：**Linux 容器** 50 轮 50ms 间隔重写有 **14/18/18** 轮次派发过空内容（即约 30% 的写），200 轮 2ms 间隔有 **15~31/约 340** 次派发是空的；**darwin** 用 CI 那种「单次重写」形态 5 次探针都复现不出来（都只有 `["v2"]`），但 2ms 风暴形态照样漏（29/200）。所以「本地跑不出来」不等于没问题——CI 那条红是只在 Linux 稳定暴露的产品缺陷。
- 修法：派发前等内容稳定（`readSettled`：连续两次读到一致才认，间隔 5ms、最多 4 次）；用完仍不一致就**放弃这次事件**——最后一次写的事件后面没人再抢，必然落定，最终值不会丢。空载荷再单独递进确认（`confirmEmpty`：50ms×4）：只靠 5ms 一致判定在高频重写里仍然漏（darwin 实测 2/11、2/8 次空派发），因为 `-race` 能把 O_TRUNC 与 Write 之间的间隔本身拉到 5ms 以上。
- 契约口径：`bconfig.ValueWatcher` 的注释补了一句「连续变更允许合并：通道收到的一定是某一时刻的完整值，但不保证每个中间值都送达」。这条字面承诺（「每当发生变更……收到新的原始字节值」）对文件源本来就兑现不了——是文档过度承诺，不是这次把行为改坏了。合并对唯一消费方 `bootstrap/config.setupWatch` 无害：它每收到一个载荷都重算整层，少收几次只是少几次 `OnChange`。
- **确认窗放宽到 400ms 无效，而且不该那样断言**（这一条是本轮最值得记的方法论）：先把 `emptyConfirmWait` 从 50ms×4 提到 100ms×4 想让风暴场景归零——Linux `-count=30` 实测泄漏次数没变（50ms×4 是 8 次/30 轮，100ms×4 是 3 次/30 轮，每轮 200 次写，同量级）。加了「确认窗读满后再复读一次」的诊断才看清机制：**8 次泄漏里 3 次立刻读到了 4 字节内容**，即那是写入方被调度停摆 >200ms 造成的**真实空窗**，读侧无论把窗开多大都会把它误判成「文件被清空」（代价还要摊给合法清空场景）。于是判据改成断**伤害**而不是断次数：`TestWatchValue_StormSettlesFinal` 只要求「末次派发必须是最后一个真实值」——空载荷落在中间会被下一轮纠正，**落在最后**才是配置永久缺项（CI 首次红就是这一种）。诊断用的临时 `println` 与放宽的常量都已回退，未进任何提交。
- 验活（正向）：`TestWatchValue_NoTornEmptySnapshot`（50 轮 × 50ms：0 次空 + **50 个值全部都要派发** + 末值正确）、`TestWatchValue_StormSettlesFinal`（200 轮 × 2ms：允许合并/丢弃，末次派发必须是 `v199`）、`TestWatchValue_ClearedFileStillDispatches`（被真正清空的文件仍要派发空载荷，最坏多等 200ms）。Linux 容器 `-race -count=10` 与 `-count=3` 全绿，darwin `-race -count=3` 全绿，`bconfig` 全模块 `-race` 全绿；消费侧 `bootstrap/config` 在 darwin `-race` 与 Linux `-short -race` 都过。
- 验活（变异，两条独立的退化）：**M1「直接派发第一次读」**（= 修法前的行为）→ `TestWatchValue_NoTornEmptySnapshot` Linux `-count=3` **三轮全红**（20/19/16 次空派发），风暴与清空两条绿——空快照这条判据确实由新闸门承担；**M2「所有事件都按未落定丢弃」**→ 前两条**同时红**（50 个值「从未被派发」+ 风暴「一次都没有派发」），证明判据不是只盯空值、把派发路径删掉不能蒙混过关。既有的 `TestWatchValue`（CI 首红那条）在 M1 三轮里也是绿的：它是概率性用例，确定性覆盖来自新增两条，这点要如实记下而不是拿来充数。
- 刻意没做：① 不在消费侧把空载荷当「忽略」，也没在 `apply` 里加「空载荷先回读一次再认」——那会把「这一层被清空」和「写到一半」混成一件事，属删能力；风暴场景的瞬态缺项**已按口径收口**（2026-11-04，见本节「口径收口」条）：读侧无法区分，改由写入方原子发布消除，不动消费语义；② 不在 `file` 源里造第二套写入协议（临时文件 + rename 由写入方负责，写入方不在仓内；本源 watch 的就是父目录，那种写法不丢事件）；③ 不动 `Load`（启动期一次读，没有中间态问题）。
- 固有上限（不是未修，是读侧方案的边界）：写入方在 O_TRUNC 与 Write 之间停摆超过整个确认窗（200ms）时，读侧看到的就是一个真空文件，无法与「运维真的清空了配置」区分——这种派发仍然会发生（实测 Linux 风暴场景 8 次/30 轮），但一定跟在真实内容之前，末次派发是最终值。CI 每次 push 只跑一遍，新闸门的漏检率没有像 §7.4 那样给出千次量级的实测分布；风暴场景的残余概率我只量到「每轮 200 次写里 0~2 次瞬态」这个粒度。
- 口径收口（2026-11-04，这条不再作为「未修」挂着）：**读侧已到边界，剩下的属写入方姿势**。任何「先 truncate 再写」的写法都会留下空窗，读侧无论把确认窗开多大都无法与「运维真清空」区分——所以风暴场景的瞬态缺项不在仓内消，改由**写入方原子发布**（同目录临时文件 + `rename`）消除；rename 对本源无害，因为 `file` watch 的就是父目录，改名即事件、不丢，且根本没有 truncate 中间态。成文位置：《Bald 配置源层设计》的「`file` 源的更新姿势」。**不改消费侧语义**（那会把「这一层被清空」和「写到一半」混成一件事），也不在仓内强制（写入方不在仓内）。
- 已证（GitHub 实测）：推送 `95db197` 后 `ci` run 41495509820 **五个作业 `test`/`integration`/`lint`/`vuln`/`race` 全部 success**——`race` 正是首红的靶子。上一轮 `d515fa0` 红在 `lint` 的依赖安装步骤（sumdb 连接重置），同一个步骤在这次 run 上正常通过，反过来印证那次确实是 runner 侧网络而不是仓内问题。推送前本地同批闸门：`task verify` 全量派生清单 exit=0、`task lint` 93 module `0 issues` exit=0、`task race` exit=0（0 条 DATA RACE）；`bconfig` 全模块 `-race` 与 Linux 容器 `-race -count=10` 的监听用例全绿，消费侧 `bootstrap/config` 在 darwin `-race`、Linux `-short -race` 都过。

**7.7 env 覆盖表达不出契约的多词键（中）** ✅ 已修（发现路径：§6.2③ 写端到端实证时，四条用例想按最省事的姿势从 `BALD_SERVER_HTTP_MIDDLEWARE_RATE_LIMIT_*` 注入参数，四条全部在启动期被严格门禁拦死）

- 现象：`bootstrap/config/merge.go` 的 `environMap` 用 `NewReplacer("_", ".", "-", ".")` 把变量名里的下划线**和**连字符一律当路径分隔符，于是 `BALD_SERVER_HTTP_MIDDLEWARE_RATE_LIMIT_RATE` 落树成 `.server.http.middleware.rate.limit.rate`。装载侧 `bconf/unmarshal.go` 的 `lookupField` 有 `foldPath` 折叠兜底，能把 `rate_limit` 这种契约字段名重新按段下钻找回来——所以「env 能覆盖多词键」在**关掉严格门禁时**基本成立。撞死的是另一侧：同一文件的 `FindUnknownKeys`/`findUnknown` **没有折叠兜底**，而严格门禁（`appkit.StrictContract`，生成物默认开启）在 unmarshal 之前跑（调用点在 `bootstrap/config/config.go` 的装载流程里），逐层比对字段描述符，`.server.http.middleware.rate` 这一层就判定「未知契约键」并终止启动。实测报错原文：`config: unknown contract keys (typo? or declare in AllowBusinessSections): .server.http.middleware.rate`。
- 两个后果，性质不同：① **开了严格门禁 → env 只能覆盖单词契约键**（`server.http.addr` 这类），多词键全数启动期失败。这是「安全闸门反过来阉割了配置层」——门禁把一种合法的注入姿势判成了笔误。② **没开严格门禁 → 优先级反转**：`lookupField` 的口径是「平级匹配优先于折叠」（注释明写，为的是真嵌套键不被折叠抢走），所以文件层已写 `rate_limit` 时，env 的折叠链永远排在其后被读到，env > file 的分层承诺静默失效——不报错，只是 env 说了不算。
- 修法（键名规范化下移到**合树之前**）：`bconf` 新增导出 `FoldEnvKeys(m, msg)`，按契约描述符把 env 切出的嵌套链就地折回规范字段名；`bootstrap/config` 的 `envLayer` 在 `environMap` 落树后立刻调用它，`Load` 与 `MountEnv`（两遍合并的第二遍）两条挂载路径同走此函数。合并因此发生在**同一个规范键**上，两个后果一起消失：env 覆盖得到 file，门禁看到的是契约名。读侧的 `lookupField`/`foldPath` 兜底**保留**——它仍服务于不经 `config.Store` 的 `UnmarshalMap` 调用方，以及没开门禁时文件/远程层里手写的嵌套契约键；但 env 路径不再依赖它。
- 折叠规则（与 `findUnknown` 的遍历口径逐条对齐，五条都是判据而非风格）：① 平级规范名优先，键本身就是本层字段名时按原样下钻；② 只在**唯一链**上折——被消费的每个节点必须恰有一个键，否则切分点不唯一，保持原样交给门禁；③ 逐段拼接必须**恰好**等于某字段名才算命中，命中即停（最短命中）；④ 规范键已被另一种形状（proto 名或 JSON 名）占据时不折，同层两种写法并存是用户自己的歧义；⑤ `repeated message` 逐元素下钻，`map` 字段与业务自持段整棵跳过。折叠增删键，故先取按键名排序的快照再遍历，结果与 map 迭代序无关。
- 判据与验活（每条变异只打中它对应的规则，跑完即回退）：`bconf/foldkeys_test.go` 六条用例——多词链（含折后 `UnmarshalMap` 端到端 + 门禁必须静默）、跨层再下钻（`request_id.header_name`、`cors.allowed_origins`、`app.stop_timeout`）、汇合点兄弟键不误伤（同一 env 前缀写 `rate` 与 `burst` 两个值）、三类反证（分支链不动、规范键被 proto 名/JSON 名占据时让位、业务段不动）、幂等与 nil 入参、list 下钻 vs map 跳过。六条独立退化 M1 不下钻 message / M2 去掉唯一链判据 / M3 去掉占用检查（proto 名、JSON 名各一条）/ M4 去掉 repeated 下钻 / M5 把 map 当 message 下钻，各自都有用例转红；M2 需 `-count=10` 才稳定命中（分支链在去掉判据后按 map 随机序折或不折，这正是「不猜」这条规则的存在理由）。消费侧四条：`TestEnv_MultiWordBeatsFile`（env 压过文件的规范键，去掉折叠必红在 `=3`）、`TestEnv_MultiWordPassesStrictGate`（去掉折叠必红在 `unknown contract keys`）、`TestEnv_MountEnvFoldsToo`（`DeferEnv` 第二遍同口径，去掉折叠必红）、`TestEnv_MultiWordTypoStillFailsGate`（`rat_limit` 这类折不出来的键仍被门禁指名——反向证明折叠没把门禁变成摆设；关掉门禁该用例必红）。示例侧两条端到端：`TestE2E_EnvOverridesContractRateLimit` / `TestE2E_EnvOverridesContractGRPCRateLimit` 用 `BALD_DEMO_SERVER_{HTTP,GRPC}_MIDDLEWARE_RATE_LIMIT_{RATE,BURST}=1` 把角色文件里的 `{1000,200}` 压成 `{1,1}`，断第二个请求 `429` / `RESOURCE_EXHAUSTED`+`RATE_LIMITED`；去掉折叠后两条都红在启动期的 `unknown contract keys`（示例默认开着严格门禁），即这两条同时是「env 姿势可用」的产品级实证。
- 残余限制（不是未修，是这套折法的边界）：**业务自持键仍然表达不出下划线**——`biz.greet.delay_ms` 这类键没有契约描述符可折，示例继续避开下划线（`greetv1.readOwnConfig`）；分支链与两种形状并存的键保持原样，由严格门禁指名（可见优于猜中）。原先的绕行（改临时契约文件而不是 env）仍是有效写法，且 `roleConfigOver` 一类用例还在用它——文件面能一次改整段，比多个 env 更贴近配置的本来书写面，只是不再是被迫的。

**7.8 `TestWatchValue_NoTornEmptySnapshot` 断得比契约还强（低-中）** ✅ 已修（发现路径：§6.2③ 期间 `b149fbf` 的 `ci` run 41597365828 里 `test` 作业红在 `bconfig/file`——`file_test.go` 的「重写过的 `"v21"` 从未被派发」，同用例的另两条判据（0 次空载荷、末值 = `v49`）都过，同 run 其余四个作业亦全绿；后续两个 head 同一用例转绿）

- 不是产品缺陷：`bconfig.ValueWatcher` 的契约注释明写「连续变更允许合并：通道收到的一定是某一时刻的完整值，但**不保证每个中间值都送达**」。丢一个中间值 `v21` 是契约内的行为，红的是断言强度。
- 根因（读代码可得，不必等复现）：事件循环每收到一条 fsnotify 事件才去 `readSettled`（`bconfig/file` 的 `WatchValue`），而落定本身要花时间——非空最快是连续两次一致（`settleInterval` 5ms 一轮），空载荷还要再递进确认 `emptyConfirmWait×emptyConfirmAttempt` = 200ms；输出通道只有 1 格缓冲，消费慢时事件还会在 fsnotify 队列里排队。**一条被延迟的事件读到的必然是当时的最新内容**，于是它替前面那几个还没处理的写各发了一次同样的最新值，中间值根本没被读到过。50ms 的轮间隔只在「处理一条事件快于 50ms」时才等价于逐值送达——`-race` 与共享 runner 的调度抖动不保证这一点。所以这条红是时序性的，同一段代码本地 `-count=10` 全绿而 CI 偶发。
- 修法（选项 ①，删掉逐值断言）：把三条判据换成**与事件时序无关**的不变量——① 0 次空载荷；② 派发序按重写轮次**单调不减**（同一事件循环串行读，读到更旧的值只能是把缓存当现读，这是唯一能补上「删除逐值断言后不判时序也能抓到回退」的那条）；③ 末次派发是最后一个真实值，且至少派发过一次。逐值「每个中间值都要出现」不再断言，因为它兑现不了。
- 验活（三条变异各自只打中一条判据，改完即回退）：M1 派发语句整体停摆 → 只红「50 轮重写一次都没有派发」；M2 在第 10 个事件补发一次历史快照（末次派发仍是 `v49`）→ **只**红「派发序回退：第 2 轮出现在第 8 轮之后」，另两条判据全过，即回退这条有独立牙齿而不是被末值判据顺带兜住；M3 把 `readSettled` 退回「直接派发第一次读」→ 空载荷判据红（Linux 容器 `-race` 实测 11 次）。同一条 M3 在 darwin `-race` 上不复现——APFS 观察不到 `O_TRUNC` 与 `Write` 之间的 0 字节瞬间，所以这条判据的代表性环境仍是 Linux（CI 的 amd64 runner 同侧）。
- 修后实测：Linux 容器（`golang:1.27`，amd64 镜像在 arm64 宿主上以模拟运行，ISA 与 CI runner 一致但时序受模拟影响）`-race -run TestWatch -count=10` 全绿（64.7s）；darwin 本地 `-run TestWatch -count=3` 全绿；`bconfig` 全模块 `-short` 加 `gofmt`/`go vet` 干净。
- 已知残余（与 §7.6 同一条，未变）：写入方在 `O_TRUNC` 与 `Write` 之间停摆超过整个确认窗时，读侧无法与「文件真空」区分，仍会派发一次空载荷——判据① 只在轮间隔远大于落定窗口的这一条用例里成立，风暴形态由 `TestWatchValue_StormSettlesFinal` 只断末值。

**7.9 契约产物的字节判据绑 buf 版本（中）** ✅ 第一轮按「留本地 + 写明判红姿势」收口（不改判据强度）；✅ 第二轮把版本 pin **成文并机器判**，同时查出「本机模板与 BSR 远端模板不等价」这条更深的坑（发现路径：§7.2 之外另一条——把 `acceptance` 接进 CI 时逐条验活，`bconf` 那条腿在**未改动的仓库**上第一次跑就红）；✅ 第四轮撤掉「buf 版本驱动字节」的归因；✅ 第五轮把这条腿接进 CI；✅ 第六轮把插件的两轴从 PATH 搬进仓内（未闭合②），其远端绿证已取得

- 现象：`bconf` 的 `TestGeneratedMatchesProto`（= `task proto-contract-check`）判的是「本机 `buf generate` 重出的产物与入库 `bconf/gen/go` **逐字节**一致」。在**未改动**的仓库上：buf **1.57.2** 报「契约层生成物与 proto 不一致（1 项）… 内容不一致: bootstrap/v1/log.pb.go」，`FAIL … 0.274s`；差异只有一类——**中文注释里 `//` 之后被补了一个空格**。
- **A/B 实证（同一份工作树、同一份 proto，只换 PATH 里的 buf）**：同一条命令换成 buf **1.50.0**（本机在 `/Users/moweilong/go/bin/buf`，GOBIN 里那个是 1.57.2）→ `ok 0.244s`。也就是说这条腿红的时候，**没有任何契约事实**，只有工具版本事实。
- 根因不在 protoc 插件侧：`protoc-gen-go` v1.36.12 与产物文件头声明一致、protobuf require 是 v1.36.11，重跑的字节差异全来自 buf 的注释排版。**仓内对 buf 没有任何 pin**——`Taskfile.yml` 的 `proto-*` 任务直接调 PATH 里的 `buf`，`publish-bconf.yml` 用 `buf-setup-action@v1`（不锁小版本），但那条路径只做 `lint`/`build`/`push`，不产 Go 产物，所以两边都不构成真相源。（以上是**发现时**的事实；第二轮把 pin 补上了，见本节末。这一句当时的判断也错了一半：插件侧确实无关，但**产出路径**另有问题。）
- 复现与判红姿势（接手者按这条走）：`task proto-contract-check`，等价于 `cd bconf && go test . -run TestGeneratedMatchesProto`；红了**先比 `buf --version`**，再决定是改 proto 还是换工具链。工具链缺失时该测试自己 `t.Skip`（CI 就落在这一档），所以「这条腿到底跑没跑」由 `scripts/acceptance-gate.sh` 的清单兜，不靠它自证。
- 同轮把版本诊断打进失败文案（`bconf/gen_repro_test.go`，`test(bconf)` 型改动，不触发发版）：1.57.2 下现在报的是「本机 buf=1.57.2；与入库产物一致的版本实测是 1.50.0——版本不同先换回 1.50.0 复跑，仍然不一致才是契约漂移」，而不是让人对着一条 `内容不一致` 猜契约是不是动了。**判据强度一点没降**：仍然逐字节比对、仍然红，改的只是文案。
- 对本地闸门的影响（如实记）：`task verify` 含 bconf 这条腿，所以**本机 buf 版本会决定 `task verify` 的红绿**。本轮推送前的预推验证在「1.50.0 前置 PATH」（与入库产物同源的那套工具链）下跑；CI 侧 runner 没有 buf，这条腿在 `test` 作业里自己跳过——两侧口径不同是既有事实，不是这次引入的。
- 这条腿上叠着**第二类静默**：Go 的测试缓存键只看包内输入，不含 PATH 里外部工具的版本，而 `task verify` 的 `verify-module` 写的是 `go test -short ./...`（无 `-count=1`）。实测证据——本轮 `task verify` 日志里这条腿是 `ok github.com/kalandramo/bald/bconf (cached)`，也就是它复述的是**上一次**跑时的工具版本结论，1.57.2 的失败不会重放。所以「`task verify` 绿」在这条腿上证明不了契约，只能证明缓存里有过一次绿。带 `-count=1` 的两条入口才拿到真结论：`task proto-contract-check`（Taskfile 里就带着）与 `scripts/acceptance-gate.sh`（每条 `go test` 都带），本轮 `-count=1` 复跑实测 `--- PASS: TestGeneratedMatchesProto (0.42s)`。判据未改，改的是「哪条命令的绿可以引用」：**引用这条腿必须来自带 `-count=1` 的那两条**。
- 危害形状不是「测试坏了」，而是**一个会被误当成契约变更的假红**：如果按 1.57 重生成并提交，排版差异会混进 `bconf/gen/go`，在下游 diff 里看起来像 proto 契约动了（而这确实是 breaking 面最大的那个 module）。同类的「工具版本差异冒充语义变更」在 `_example/bald/api/buf.lock` 的 v1→v2 上已经出现过一次（§8 末尾那条，2026-11-04 由 §7.1「补：示例的去仓库外依赖」闭合）。
- 决策（本轮）：① 这条腿**留本地**——`scripts/acceptance-gate.sh` 的 scope 表把它标成 `no`，CI 的 `acceptance` 作业因此不含它（第五轮给它在 CI 里单开了专职作业 `proto-contract`，表里那条仍是 `no`，理由从「进不了 CI」换成「不在两条作业里各装一遍工具链」）；② **不重生成产物**，不为了绿而把 1.57 的排版差异写进入库文件；③ 把版本敏感性写在三处会撞见它的位置：`Taskfile.yml` 的 `proto-contract-check` 上方注释（「看到这条红先比 `buf --version`，别急着重生成提交」）、脚本文件头的 scope 说明、以及测试自己的失败文案。判红姿势由此成文：**先比工具版本，再决定是改 proto 还是改工具链**。
- 刻意没做：① ~~不给 CI 装 buf 并 pin 版本~~ ——第二轮按这条自己的口径做了 pin，但 pin 在**产出路径**而不是给 CI 装工具（见本节末）；② 不把字节判据换成 AST/语义判据——现在的口径「入库产物形态必须与 proto 源一致」里排版也是产物的一部分，弱化它等于让产物漂移变得不可见；③ 不在闸门里自动重生成（闸门只判，不改）。
- 固有上限（诚实记）：只要本机 buf 与产物来源版本不同，`task acceptance`（scope=all）与 `task proto-contract-check` 就会红，这一步没有自动兜底。已证的只有 1.50.0（零差异）与 1.57.2（一处注释空格）两个版本，其余版本未测。

**第二轮收口（2026-11-04，pin 成文 + 机器判 + 产出路径改形）**

- **`bconf/buf.version` = `1.50.0`**：仓内第一份 buf 真相。`TestGeneratedMatchesProto` 在字节比对**之前**先比它，不等就判红并点名两个数字与升级姿势；文件为空/读不到也是红（`t.Fatal`），这条腿不接受「pin 缺失＝放过」。
- **第二把 pin 不新建文件**：`protoc-gen-go` 的版本行本来就在入库产物头部（`// 	protoc-gen-go v1.36.12`），测试从排序后第一个 `.pb.go` 读出来与本机 `protoc-gen-go --version` 比——单一真相，无第二处需要维护。取排序首个文件而不是遍历：多文件版本行不一致（部分重生成）会由后面的字节比对抓到，两条判据不重叠也不漏。
- **三次验活（各自打中一条，改完即复原）**：① 本机默认 PATH 的 buf 就是 1.57.2 → 直接命中 pin 红（`gen_repro_test.go:40`，`FAIL 0.181s`），前置 1.50.0 后 `ok 0.186s`；② `: > bconf/buf.version` → 命中「pin 文件不能留空」；③ 把 `bald/appspec/v1/appspec.pb.go` 头部版本行改成 `v1.26.0` → 命中插件 pin 红（`:57`，报出「本机 1.36.12 vs 产物声明 1.26.0」）。反证：**只改 `bootstrap/v1/log.pb.go`**（字节序里不是第一个文件）的版本行不会走 pin 分支，而是落到字节红——确认了 pin 判据的取材范围，也确认字节比对是兜底那一层。两处产物改动都从 `/tmp` 备份复原，`git status` 对 `bconf/gen` 干净。
- **查出更深的坑：两份模板并不等价**，而文档曾写「已核实该路径与远端插件产物逐字节一致」——这句是假的，实测推翻：BSR 远端插件（`buf.gen.yaml`）用 **v1.36.12**（钉版本）或不钉（当天 latest 也是 1.36.12，两者产物 `-rq` 全同）重出，与入库 `gen/go` 差**一处**——`bootstrap/v1/log.pb.go` 中文注释 `//（…）` 变成 `// （…）`。同版本号插件在 BSR 容器里与本机二进制的注释排版不同，所以字节差异不止 buf 驱动器一个来源。入库产物出自**本机**模板。
  - 危害形状：`task proto-contract`（旧形态走 `buf.gen.yaml` 远端插件）跑一次就会把排版差异写进 `gen/go`，下一次 `proto-contract-check` 判红——看起来像「有人改了 proto」，实际是模板选错了。
  - 消解：`proto-contract` 改用 `buf.gen.local.yaml`（生成与校验同一条路径），并删掉因此变成纯别名的 `proto-contract-local`（三处文档引用同步改成 `task proto-contract`；行为没丢，只是不再有两个名字指同一件事）。`buf.gen.yaml` 保留作 BSR 对照，远端插件钉到 `:v1.36.12`（产物头部声明的那版），并在两份模板的头注释里写清各自角色。
  - 验活（正向）：buf 1.50.0 前置 PATH 跑 `task proto-contract`，随后 `git status --porcelain bconf/gen` **无输出**——产出路径幂等于入库产物，这是这条改形的判据本身。
- **第三轮（2026-11-04，同一轮 §7.10 取证时撞出来的）**：`bconf/buf.version` 那个 pin 只钉得住**版本号**，钉不住**来源**。同一份未改动的树、同一个 `protoc-gen-go v1.36.12`，只换 buf 的来源：
  - 官方发行二进制 `buf 1.50.0`（`/Users/moweilong/go/bin/buf`）→ 与入库产物**零差异**，`--- PASS (0.22s)`。
  - `go install github.com/bufbuild/buf/cmd/buf@v1.50.0` 的源码构建，`--version` 同样报 `1.50.0` → **版本门通过、字节门红**（`bootstrap/v1/log.pb.go` 差那一处注释空格）。
  - 顺带把空格归因测到底：`protoc-gen-go v1.36.10` + 源码构建 buf 1.50.0 → **不补空格**；`v1.36.12` + 同一 buf → 补。所以这一处排版同时受插件版本与 buf 来源两个变量影响，版本号只是其中一半。
  - 后果不是红本身，而是**红的归因是假的**：旧文案在这条红里写「因此这是契约漂移而不是工具链差异」，而实测证明版本相符仍可能是工具链来源不同。本轮把这句改成两条可能（① 契约真变了→按本机模板重生成并提交；② 来源不等于 pin→换官方发行版复跑再判），并把「字符串相等不等于同源」写进测试文件头的注释。**判据一字未弱化**：仍然逐字节、仍然红、pin 两条仍先判。
  - 一次误判的实录（值得留着）：本轮第一次看到这条红时，按旧文案的指引认定「产物与 proto 漂了」，并用 `buf generate` 重出了 `log.pb.go`（把空格写进入库产物）——那正是 §7.9 第一句警告的「把工具差异当契约变更提交」。靠官方二进制复现出零差异后已从 `cp` 备份精确复原，`git status --porcelain bconf/gen` 干净。教训是这条腿的**取证顺序**：红的时候先用官方发行版复跑，再谈契约。
  - 未闭合（如实记）：没给 buf 来源加机器判据。可选做法是把官方发行包的 SHA-256 也 pin 进仓并比对 `$(command -v buf)`，但 Homebrew/源码安装的同版本二进制会被真红误伤——那等于把「用官方发行版」变成硬前提，收益不确定而代价明确，所以这轮只改文案与口径。真出现误提交产物时，兜底仍是字节门本身。

- `publish-bconf.yml` 的 `buf-setup-action` 不再跟随 latest，改成从 `bconf/buf.version` 读（一个 step 输出 → `buf_version` 输入）。理由：`lint` 规则集会随 buf 换代变严，tag push 时被新 buf 判红等于用工具版本绑架发版。本地以 1.50.0 跑 `buf lint`、`buf build` 均绿，`actionlint` 干净。**远端未取证**：该作业只在 `bconf/v*` tag 或手动 dispatch 触发，而 dispatch 会真往 BSR push 产物（共享状态），本轮不为此发 tag——这条 pin 的远端生效要等下一次 bconf 发版顺带验，属已知未闭合。
- 文档同步：`Bald 配置契约设计.md`（生成姿势 + 两把 pin + 模板不等价的实测）、`Bald 配置系统设计.md`（「改 proto 后」那条）、`Taskfile.yml` 注释、`scripts/acceptance-gate.sh` 的 scope 说明。~~`bconf` 这条腿仍标 `no`（不进 CI）：CI 装哪个版本的 buf 都只是「用工具版本换一条绿」，判据本身要求的是与产物同源的那套工具链，留在本地由 `task acceptance` / `task proto-contract-check` 跑。~~ —— 这句的前提（「装哪把都是赌」）被第四轮的单变量实验撤掉了：既然字节门只认「编译插件的那把 Go」，而 CI 的 Go 已经成文钉在零差异档，装工具链就变成可判的事。第五轮据此给这条腿单开 `proto-contract` 作业；`acceptance` 表里那条仍标 `no`，是不想装两遍，不是进不了 CI。

**第四轮（2026-11-04，第三轮那条归因被单变量实验推翻了）** ✅ 字节门的真正驱动变量是**编译 `protoc-gen-go` 用的 Go 工具链**，不是 buf 的版本也不是 buf 的来源

- 触发点：`task verify` 在本机红，而按第三轮的口径「换官方发行版 buf 复跑」跑不通——前置 1.50.0 后 `log.pb.go` 仍然差那一处空格。于是这轮把两个变量拆开，各跑一次、每次都只让一个变量动（临时目录里只放**一把 buf + 一把插件**的符号链接，`PATH="$bin:/usr/bin:/bin"`，避免上一轮那种「PATH 顺序把两行都指向同一把插件」的污染——第三轮的结论就是这么来的）。
- 实测矩阵（整棵 `gen/go` 逐字节 vs 入库）：

  | buf | `protoc-gen-go` | 编译该插件的 Go | 结果 |
  |---|---|---|---|
  | 1.50.0 | v1.36.12 | go1.27.1 | **零差异** |
  | 1.57.2 | v1.36.12 | go1.27.1 | **零差异** |
  | 1.50.0 | v1.36.12 | go1.27.3（本轮 `go install` 新编） | **零差异** |
  | 1.50.0 | v1.36.12 | go1.26.5（本轮 `go install` 新编） | 差 **1 个文件 1 行**：`bootstrap/v1/log.pb.go` 的 `//（…` → `// （…` |
  | 1.57.2 | v1.36.12 | go1.26.5（本机 9 月那把） | 差同一行 |
  | 1.50.0 | **v1.36.10** | go1.27.1 | 差 **全部 18 个 `.pb.go`**（头部版本行等），而 `//（…` 那行**不变** |

- 机制在插件里，不在 buf 里：protobuf 的 `compiler/protogen`，`(*GeneratedFile).Content()` 把生成的源码用 stdlib `go/parser` 解析、再用 `go/printer` 重排（`printer.Config{TabIndent|UseSpaces, Tabwidth:8}`）。`go/printer` 是**编译插件那把 Go** 的一部分，所以注释排版跟着工具链换代走，而插件自报的版本号对此毫无表示。
- 由此撤回三条旧表述（都在本节上面几轮里）：① 「buf 的注释排版会进字节（1.57.2 补空格）」——本轮 1.57.2 配合规插件零差异；② 「同版本号的源码构建 buf 与官方发行二进制不等价」——现存那把 1.50.0 本身就是 `go install` 源码构建（`go version -m` 带 buildinfo），它零差异；当时那对差异里唯一能动的另变量就是 PATH 里的插件，本轮把它钉住了，旧实验无法复现成「buf 来源」差异。③ 「`v1.36.12` 补空格、`v1.36.10` 不补」——同一把 Go 编译时两版都不补，差异在头部版本行（差 18 个文件），不在注释。
  - 未被推翻的那条：BSR 远端模板与本机模板不等价（远端容器里的 v1.36.12 也补空格）。它现在有了机制解释——容器编译插件用的 Go 与本机不同；也就是说，本节的「远端/来源」类差异和「本机两把」类差异是**同一件事**，旧文档把它们记成了三个来源。
- 收口：加**第三把 pin** `bconf/protoc-gen-go.goversion` = `1.27`（记 major.minor；补丁位实测无关，1.27.1 与 1.27.3 同为零差异）。测试在字节比对前用 `go version -m <protoc-gen-go>` 读插件的构建信息并比它，不等就判红并点名这条轴 + 补齐姿势（~~`go install google.golang.org/protobuf/cmd/protoc-gen-go@v<产物头部声明的版本>`~~ 第六轮起插件由判据自己现编，这一轴的补齐姿势只剩「换跑测试的那把 Go」：`GOTOOLCHAIN=go1.27 go test -run TestGeneratedMatchesProto -count=1 .`）。`go` 命令缺席时 `t.Skip`（与 buf/插件缺席同一类：环境缺失，不是判据失败）。
  - 三轴的机器判据各自验活（都在未改动的树上，`-count=1`）：① 合规组合（buf 1.50.0 + v1.36.12/go1.27.1）→ `--- PASS (0.24s)`；② buf 1.50.0 + v1.36.12/**go1.26.5** → 命中第三轴红（`gen_repro_test.go:81`）；③ 同一环境把 pin 临时改成 `1.26` → 穿过三轴、由字节门报出「内容不一致: bootstrap/v1/log.pb.go」——这条同时证明字节门没有因为新加的轴而空转。pin 文件从 `/tmp` 备份复原，`cmp` 与 `printf '1.27\n'` 一致。
  - 判据强度：三轴都保留 `t.Fatalf`（不是 Skip），字节门原样。加这条轴的净效果是**把「工具链口味」从「契约漂移」里摘出去**——以前它冒充契约红，现在它有名字。
- 对本机的如实交代：默认 PATH 那把 `protoc-gen-go` 是 9 月装的、由 go1.26.5 编译，且 buf 是 1.57.2 → 本机 `task verify` / `task proto-contract-check` 的 bconf 腿**按设计判红**（第一轴 buf 就拦下）。合规姿势是一条命令：`PATH="/Users/moweilong/go/bin:$PATH" task proto-contract-check`（GOBIN 里那把 buf 1.50.0 + 插件 v1.36.12/go1.27.1，实测 `ok`）；~~跨机器的通用姿势是 `go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.12` 用 go1.27 编一次~~（第六轮撤销：插件不再从 PATH/GOBIN 拿，判据在 module 里现编，跨机器只剩「buf 版本 = pin」与「跑测试的 Go major.minor = `bconf/protoc-gen-go.goversion`」两个前提）。本轮**没有**覆盖那把 9 月的二进制（不在仓内的用户工具，动它不在授权范围）。CI 不受影响：runner 无 buf，这条腿自己跳过。
- 已证（GitHub 实测，2026-11-04）：本节的三把 pin 与 §7.12 的夹具改动一起推在 head `019dac2`，run 41813828397 **七个作业全部 success**（`test`/`race`/`lint`/`vuln`/`acceptance`/`integration`/`integration-kubernetes`）。这不等于这条腿在远端跑过——runner 无 buf，`TestGeneratedMatchesProto` 依旧自己 Skip，远端能证明的只是「新加的第三轴与文案改动没有把任何现有作业带红」；`acceptance` 作业那侧同轮报 `scope=ci 的 6 条腿、72 条判据全部通过`，其中不含 bconf 那条（表里就标着本地腿）。
- 仍未闭合：① ~~这条腿依旧不进 CI~~ **第五轮收口**：新增专职作业 `proto-contract`，按 `bconf/buf.version` 装官方 buf 并要求真 `--- PASS`，见下面第五轮；② ~~更彻底的做法是**在测试里自己 `go build` 出插件**，让三轴全部落在仓内（go.mod 的 protobuf 版本 + 仓的 Go），彻底摆脱 PATH 考古——本轮没做，因为它要求 bconf/go.mod 的 protobuf 从 v1.36.11 升到 v1.36.12（产物头部声明的是 1.36.12），那是发布面变更，得走 sync/CHANGELOG/tag，与「修一条归因」不成比例~~ **第六轮收口**：那条「不成比例」的理由被实测撤掉（v1.36.11→v1.36.12 是全仓 pin 分布里的**多数对齐**，且 bconf 本来就在待发版批次里），插件改为测试内现编，两轴落进仓内，见下面第六轮；③ buf 来源这一轴~~仍无机器判据~~：判据本身依旧不查来源（第三轮那条「pin SHA-256 会误伤 Homebrew/源码安装」的顾虑没有被推翻），但**作业侧**从第五轮起有来源判据——官方发行二进制按 `bconf/buf.sha256` 校摘要才放行。

**第五轮（2026-11-04，未闭合①收口：这条腿有了专职 CI 作业——「按 pin 装出工具链」+「没有顶层 PASS 即红」）** ✅

- 为什么这轮敢给 runner 装工具链：第四轮把「装哪把才绿」从猜变成了可判。字节门的驱动变量是**编译插件的那把 Go**，而 `setup-go` 已经钉在 1.27.3，正好落在实测的零差异档；buf 的版本号与来源对这一处中性。于是另外两把都有了确定的取法：buf 按 `bconf/buf.version` 下载官方发行二进制并校验 SHA-256，插件按**入库产物头部自己声明的那行**读出版本现编——版本真相仍只有一处，作业不新建第二份。（后半句在第六轮被换掉：取版本的出处从产物头部改成 `bconf/go.mod`，「作业现编」改成「判据现编」，作业里那一步随之删除——见本节末第六轮那条「落点」。）
- `.github/workflows/ci.yml` 新增 `proto-contract` 作业（插在 `acceptance` 与 `lint` 之间），三步：
  - 装 buf：`v=$(tr -d '[:space:]' < bconf/buf.version)` → 下载官方 `buf-Linux-x86_64` → `echo "$want  $RUNNER_TEMP/buf" | sha256sum --check --status`。摘要从新文件 `bconf/buf.sha256` 按 **asset 名**取（不按版本号索引——版本已由 `bconf/buf.version` 单独 pin，这个文件只钉「该 asset 的字节」）。摘要不符即红，不给「拿到一个差不多版本」留活路。
  - ~~现编插件：从排序后第一个 `.pb.go` 的头部 `grep -oE 'protoc-gen-go v[0-9.]+'` 读出版本，`GOBIN="$RUNNER_TEMP/bin" go install google.golang.org/protobuf/cmd/protoc-gen-go@<那个版本>`，随后 `go version -m` 打印编译它的那把 Go——第三轴的证据由日志自己交出，不是靠人复述。~~ —— **这一步在第六轮整条删除**：判据改为自己现编，作业装出来的那把无人使用，留着就是仓外第二份版本真相。今天这个作业只剩「装 buf」+「跑判据并要求真 `--- PASS`」两步。
  - 判据步：`go test . -run TestGeneratedMatchesProto -count=1 -timeout 10m -v`，**除退出码外再断言日志里出现 `^--- PASS: TestGeneratedMatchesProto (`**。这一步才是整条作业的意义：判据在工具链缺席时 `t.Skip` 且返回 0，只比退出码的写法会把「没跑」读成「跑过」（§7.1 的「不可达即 Skip」、§8 的 per-command `dir` 是同一类失效）。
- `bconf/buf.sha256` 两行值的来历（写进文件头注释）：2026-11-04 从 GitHub 的 release 资产流式下载 v1.50.0 的两个 Linux asset（`www.github.com` 不通，走 `api.github.com` + `objects.githubusercontent.com`），字节数与 API 声明一致（x86_64 `42754200`、aarch64 `41418904`）、`buf --version` 自报 `1.50.0`，再 `sha256sum` 得到摘要。GitHub 侧的 `digest` 字段对这两个 asset 是 `null`，所以摘要是本机算出来的，不是抄来的。
- **校验来源只发生在作业，判据本身不查来源**：`bconf/gen_repro_test.go` 一字未动，本机用 Homebrew 或源码装的 buf 照样能跑。第三轮那条顾虑因此仍然成立，也不需要再解决——需要保证来源的是作业，不是判据。
- 验活（正向，按 runner 的真实形状取证）：容器 `linux/amd64` + 官方 `buf-Linux-x86_64` 1.50.0（sha256 与 pin 一致）+ 由 go1.27.3 现编的 `protoc-gen-go v1.36.12` → 判据步 exit 0、`ok github.com/kalandramo/bald/bconf 0.584s`，且作业步自己的断言在同一份日志里逐字命中 `^--- PASS: TestGeneratedMatchesProto`（断言不过脚本就红，所以这条 PASS 是被机器判过的，不是人读出来的）。插件版本从产物头部读出的过程也进了日志。
  - 同一形状重试时撞上一次**与判据无关**的崩溃：qemu 用户态模拟下 `go test` 在打印完 `ok` 之后被 runtime 以内存破坏/FATAL SIGBUS 打断，退出码变红而产物判据其实是 PASS 的（这条腿的取证轮次里出现过一次，重跑即恢复；第四轮那条「amd64 镜像在 qemu 下会以 runtime 崩溃收尾」的旧观察再次现身）。真实 runner 是原生 amd64，不带这层模拟，这也是容器取证不能替代真跑的具体理由之一。
- 三组变异（跑的是从 `ci.yml` 抽出的作业步原文，不是另写一份等价脚本），各自只打中对应那一层：
  - **M1** 把 PATH 换成只含 `go` 的最小值（buf 与插件都消失）→ 测试自己 Skip、`go test` 返回 0，作业仍 exit 1，红在断言行「没有顶层 PASS 记录」。这条是 M 组里最要紧的一层：它证明的不是「判据能红」，而是**静默绿进不来**。
  - **M2** 给 `bconf/gen/go/bootstrap/v1/log.pb.go` 追加一个字节 → 三轴全对上、落到字节门，红在「内容不一致: bootstrap/v1/log.pb.go」。
  - **M3** 把 `bconf/buf.version` 改成 `1.99.0` → 红在第一轴并点名两个数字，且不进字节比对。
  - 复原后复跑回到绿；`git status` 只剩本轮两处预期改动（`M .github/workflows/ci.yml`、`?? bconf/buf.sha256`），变异留下的字节为零。
- `scripts/acceptance-gate.sh` 表里 `bconf` 那条**仍标 `no`**，但理由换了：不是「这条腿没有远端证据」，而是「不在两条作业里各装一遍工具链」——真按 scope=ci 在 `acceptance` 作业里跑，runner 没装 buf，测试 Skip，脚本那条「必须逐条 `--- PASS`」会正确地判红。表头注释、§7.2 表格与配置契约设计文档同步换了这个口径。
- **远端绿证已取得**（2026-11-04，`workflow_dispatch` 打在分支 `ci/proto-contract-leg`（head `f1c7188`）上的 run 41823701064，作业 `proto-contract` = job 126552951834，`success`）：日志逐条交出三把 pin 的取证——`go version go1.27.3 linux/amd64`（`setup-go` 按 `1.27.3` 装）、`buf --version -> 1.50.0`（这一步只有在官方资产下载成功**且 SHA-256 与 `bconf/buf.sha256` 相符**之后才会打印）、`入库产物声明的插件版本：v1.36.12（出处 bconf/gen/go/bald/appspec/v1/appspec.pb.go）` + `/home/runner/work/_temp/bin/protoc-gen-go: go1.27.3`（现编，编译它的那把 Go 就是 runner 那把），最后判据步 `--- PASS: TestGeneratedMatchesProto (0.15s)`、`ok github.com/kalandramo/bald/bconf 0.153s`。这条腿从今天起不再是「只有本机跑过」。
  - 顺带测掉一条关于取路径本身的假设：`workflow_dispatch` **不需要先在默认分支上存在**——本轮 main 还是 `b8b0a9a`（没有这个键），`gh workflow run ci.yml --ref <branch>` 照样产出 run。曾按「GitHub 读默认分支的触发键」推论必须先合一个只加键的使能提交，为此准备的改动在实测后弃用（未提交、未推送），因为它换来的是一条不存在的约束。
  - ~~仍未闭合的那半条：字节判据只判「与入库产物一致」，判不了「入库产物本身是否落后于 proto」~~ —— **这句话是错的，第六轮实测否证**：判据是把**当前 proto** 生成到临时目录再与入库字节比，所以「改了 proto 没重生成产物」正是它能判的东西。实测：只把 `proto/bootstrap/v1/log.proto` 注释里的一句改一个词（`cp` 备份后精确复原，`cmp` 一致），`go test -run TestGeneratedMatchesProto -count=1 .` 就红在字节门、且只报一项「内容不一致: bootstrap/v1/log.pb.go」（三把 pin 全对上才走到这一步），复原后 `ok 0.454s`。这条腿真正的边界在别处，见下面第六轮末尾的「边界」。

**第六轮（2026-11-04，未闭合②收口：后两轴不再读 PATH——插件由测试在 module 里现编）** ✅

- 第五轮留下的形状是「判据读 PATH 上那把已安装的 protoc-gen-go」：版本轴拿它的 `--version` 比产物头部，口味轴拿它的 `go version -m` 比 `bconf/protoc-gen-go.goversion`。这两把的**出处**因此在仓外。本轮把它们搬进来：`selfBuiltPlugin(t, root)` 在 bconf module 里 `go build -o <t.TempDir()>/protoc-gen-go google.golang.org/protobuf/cmd/protoc-gen-go`，随后把那个目录**前置**进 `buf generate` 的 `PATH`。三轴里只剩 buf 版本还读 PATH（实测 buf 对字节中性，pin 保留只表示「这条腿在这套组合上验证过」）。
- **改造前根本测不到，本轮实测证伪的那条洞**：在 main 的 `d9d09c4`（worktree，未改一字）里，`bconf/go.mod` 的 protobuf 是 **v1.36.11**、18 个 `.pb.go` 头部声明的是 **v1.36.12**，而旧判据在 PATH=buf 1.50.0 + PATH 插件 v1.36.12 的组合下打印 `--- PASS: TestGeneratedMatchesProto (0.12s)`、`ok … 0.439s`。仓内的生成器版本与入库产物不一致，判据照样绿——因为被比的那两把工具都不来自仓内。这是本轮唯一一条「先复现假绿、再改判据」的证据，改后同一条变异红（见下）。
- 为什么第五轮那句「与修一条归因不成比例」不成立（数字，本轮新测）：全仓 95 个 module 里 44 个 require `google.golang.org/protobuf`（9 直接 + 35 `// indirect`），**升 bconf 前**分布 **v1.36.12 ×31 / v1.36.11 ×10 / v1.36.8 ×3**。含 `.pb.go` 的 module 只有两个：`bconf`（18 个，头部全声明 v1.36.12）与 `_example/bald`（`greet.pb.go` 声明 v1.36.12；`greet_grpc.pb.go` 头部声明的是 protoc-gen-go-grpc 的版本，不是 protobuf 的，按这条口径不算漂移）。所以**升前只有 bconf 一个 module 的自身 pin 与自身产物矛盾**，升到 v1.36.12 是把它对齐到仓内多数，而不是「新增一个发布面」；bconf 又本就在待发版批次里（§「发版前置」清单），发布代价早已被那一批吸收。整轮落定后分布为 **40 / 1 / 3**——唯一还留在 v1.36.11 的是 `encoding/proto`（直接 require，与本条链路无关，登记 §3.2 的口径）。
- 落点（五处同步，全是文案/机制，判据强度只增不减）：
  - `bconf/gen_repro_test.go`：`LookPath("protoc-gen-go")` 的 Skip 换成 `LookPath("go")`；版本轴文案改成点名 `bconf/go.mod`（不再说「本机插件」）；口味轴用刚编那把的 `go version -m`，并把「go1.27.1 与 go1.27.3 都与入库产物零差异」写成事实句而不是把 pin 插进句子（否则 pin 被改成 9.99 时文案会自称「go9.99 零差异」）；删掉旧 helper `pluginBuildGo`。
  - `bconf/go.mod` + `go.sum`：v1.36.11 → v1.36.12，摘要里 v1.36.11 那两行随 `go mod tidy` 一起掉。
  - 12 个下游 module 的 go.mod/go.sum 跟着 tidy（见下面那条连锁）。
  - `.github/workflows/ci.yml` 的 `proto-contract`：**删掉「按产物头部版本 `go install` 插件」整个作业步**——判据自己现编之后，那一步装出来的插件无人使用，留着就是「一份仓外真相冒充 pin」。作业只剩装 buf + 跑判据并要求真 `--- PASS`。
  - `Taskfile.yml` 的 `proto-contract` 注释、`bconf/buf.gen.local.yaml` 与 `buf.gen.yaml` 头注释、`scripts/acceptance-gate.sh` 的 scope 说明、三处设计文档（`Bald 配置契约设计.md` 生成/校验/CI 三段、`Bald 配置系统设计.md`「改 proto 后」、`Bald 声明式装配设计.md` 的验收表行）同步成新口径；旧的「补齐姿势 = `go install …protoc-gen-go@v<产物头部版本>`」撤掉，换成两件事：版本轴不符是 go.mod 与产物不同步，口味轴不符用 `GOTOOLCHAIN=go1.27` 复跑。
- **连锁 12 个 module（本轮实测，不是一条干净单行改动）**：`ai/{eino,langchaingo,openai}`、`cache/local`、`log/{aliyun,charm,loki,sentry,tencent}`、`transport/{grpc,http}`、`workflow/argo` 的 go.mod/go.sum 都要跟着动。原因是这 12 个 module 全部 `replace … => ../bconf`，而 `verify-module` 里带 `go mod tidy -diff` 这道门——bconf 的 protobuf 一升，它们的 `google.golang.org/protobuf` 就从 v1.36.11 漂到 v1.36.12（`log/aliyun`、`log/tencent` 是**直接** require，其余是 `// indirect`），tidy 不齐就红。九仓之外的视角这条红是**发版本来就会造的东西**：bconf v0.17.1 一旦发布，下游按版本号消费它时同样会把 protobuf 顶到 v1.36.12；本轮等于把那次连锁提前在本地跑完并入库，代价是一次提交里多 24 个文件。**除这 12 个之外没有别的 module 受影响**（全仓 `go mod tidy -diff` 复扫 0 条 DIFF，含 `_example/bald`）。
- 产出路径的连带口径（`task proto-contract` **不改**，仍拿 PATH 插件）：校验腿现编、产出腿读 PATH，两者不同版时产物头部会声明一个 go.mod 编不出的版本，判据红在版本轴并点名 go.mod——洞由判据兜住，不需要给产出加 shell 链（那会为 Windows 再造一条分叉）。同时给出对齐产出的可跑姿势并实测幂等：`cd bconf && bin=$(mktemp -d) && go build -o "$bin/protoc-gen-go" google.golang.org/protobuf/cmd/protoc-gen-go && PATH="$bin:$PATH" buf generate --template buf.gen.local.yaml` → `git status --porcelain bconf/gen` **无输出**（buf 1.50.0，插件 v1.36.12 由 go1.27.1 现编）。
- 验活（正向）：`gofmt -l` 空、`go vet -C bconf .` exit 0；默认 PATH 与 pin buf 前置各跑一轮 `go test -C bconf -run TestGeneratedMatchesProto -count=1 .` → `ok 1.145s` / `ok 0.498s`。
- 变异（每条 `cp` 备份后精确复原，各只打中一层）：
  - **N1 PATH 放一把假版本插件**（自建目录里塞一个 `protoc-gen-go --version` 报 v9.99.9 的脚本）→ 依旧 `ok`。这条是本轮的**核心反证**：PATH 上的插件已经不参与判据，旧形状下它会直接把腿判红。
  - **N2 `bconf/go.mod` 退回 v1.36.11** → `FAIL` 红在版本轴（`gen_repro_test.go:77`），文案同时给出两条退路（升 require / 用产物那版重生成并提交）。复原为 v1.36.12 后回到 `ok`。**复跑这条要连 go.sum 一起退**——升完后 tidy 已把 v1.36.11 的两行摘要删掉，只退 go.mod 会先撞 `[setup failed]`（缺摘要），那不是版本轴。
  - **N3 `bconf/protoc-gen-go.goversion` 改成 `9.99`** → `FAIL` 红在口味轴（`:89`），且日志给出的复跑命令带的就是 pin 那把（`GOTOOLCHAIN=go9.99 …`）。从 `/tmp` 备份复原为 `1.27`，`go version -m` 自编译二进制的 major.minor 与 pin 再对上，`ok`。
- 边界（别把这条腿读成「全仓生成物都有字节门」）：字节判据只有 `bconf` 一份，`_example/bald/api/gen` 是 gitignore 的本地产物，CI 的 `example` 作业用远端插件现生成后编译，不比对字节。第二轴现在钉的是 bconf 这一个 module 的 protobuf pin，其它 43 个 require protobuf 的 module 没有产物可比，也就不在这条腿的射程内。
- **远端绿证已取得**（2026-11-05 复核，`gh run view` 独立确认，非转述）：push 到 main 的 head `62b5035`（第六轮那两个提交 `1a31bc2`+`62b5035` 之后即 HEAD）触发 ci run 41862216627，`completed`/`success`，九个作业全 `success`——`proto-contract`（job 126686830899）/ `lint` / `race` / `acceptance` / `example` / `test` / `integration-kubernetes` / `integration` / `vuln`。`proto-contract` 日志交出的三件事：`go version go1.27.3 linux/amd64`（`setup-go` 按 `1.27.3` 装，正落在实测的零差异档）、`install-buf: buf --version -> 1.50.0`（只有官方资产下载成功**且 SHA-256 与 `bconf/buf.sha256` 相符**才打印）、`--- PASS: TestGeneratedMatchesProto (3.02s)` + `ok github.com/kalandramo/bald/bconf 3.028s`。
  - **这轮的判据强度由那个 3.02s 自己说话**：第五轮同一处远端日志是 `0.15s / ok 0.153s`，因为插件由作业步提前 `go install` 装好、判据只调用；本轮删掉了那个作业步、插件改由测试在 module 里现编，3.02s 里装的是那次 `go build`。作业里既没有 `go install protoc-gen-go`、runner 的 PATH 上也没有插件（`setup-go` 只装 Go），所以绿**不可能**来自 PATH——这正是 N1 变异（PATH 塞假版本插件 → 依旧 ok）在远端的等价形状。
  - 三把 pin 的取证仍只在失败路径打印（`t.Log` 在无 `-v` 的成功运行里不落日志），所以这条远端证据的判据是「**组合里根本没有仓外插件**」这一结构事实，加上真 `--- PASS` 行；不是「日志复述了三把版本号」。上一轮的 `入库产物声明的插件版本：v1.36.12（出处 …）` 那类行今天不再出现，属于删除作业步后的正常形状，别把它读成判据退化。


**7.10 `-tags nacos` 的注册中心接管是死的（中）** ✅ 已修（2026-11-04 同日登记并收口；发现路径：§7.1「示例的去仓库外依赖」那条迁移跑真机时——配置侧全绿，注册侧一条都没发生）

- 现象：`-tags nacos` 构建里那个 `nacosRegistrarProvider`（`_example/bald/internal/bootstrap/register_nacos.go`）**在计划期就被跳过**，所以示例从未向 nacos 注册过任何实例。证据三条，互相独立：
  - `bald-demo-api doctor`：`warn programmatic-takeover [registry]: registry segment is taken over by a programmatic Registrar instance; registry backend selection in the contract does not take effect`（另两行 `opaque:` / `override: registry (programmatic takeover)` 是同一条判据的不同投影）。
  - 真 nacos 命名 API：`GET /nacos/v1/ns/service/list?pageNo=1&pageSize=20` → `{"count":0,"doms":[]}`，而进程正跑着、契约里 `registry.type: nacos` 写着。
  - 装配代码：`_example/bald/internal/bootstrap/custom.go` 的 `Options()` 里**无条件**放了 `appkit.Registrar(inmemory.New())`。显式 Registrar 实例会把 `registry` 段标成「已消费」，纯段驱动的 Provider 于是按设计不生效——**框架行为是对的，示例的声明是自我矛盾的**：一边钉住内存注册中心，一边在契约里选 nacos 并期望它被接上。
- 为什么不当场修（原判断，保留）：这条的失败面与 §7.1 那条迁移完全不同（一个是配置源层，一个是注册中心接管），并进同一次提交，将来红的时候分不清是折叠改了配置语义还是接管拆了注册。所以它先登记，本轮单独成一次提交。
- **动手前那条未知已经实测答掉**（这是原登记的主要价值，答案是「不需要第二个折叠」）：`registry/nacos` 把契约的 `namespace` 原样塞进命名 SDK 的 `NamespaceId`，与配置侧那个静默空读同一形状——但命名 API 与配置 API 在这里**语义相反**：`"public"` 与 `""` 是**同一个租户**。实测（nacos v2.3.2 单机，SDK ephemeral 注册，两条腿互为反证）：按 `ns=public` 注册的实例，用 `ns=""` 的 Registry 一样 `GetService` 得到；按 `ns=""` 注册的，用 `ns=public` 一样查得到；`service/list` 带 `namespaceId=public` 与带空值返回同一份列表。SDK 侧的机制也读得到：`naming_client` 把空 `NamespaceId` 归一成 `DEFAULT_NAMESPACE_ID`，而那个常量就是 `public`（`common/constant/const.go`）。所以 `registry/nacos` **不折叠**是对的，折叠反而会把「用户显式写的名字」变成看不见的默认值；这条结论写在 `registry/nacos/registry.go` 的 `New` 注释里，与 `bconfig/nacos` 那条折叠注释互为对照。
- 修法（按原计划的第一种，拆构建形态）：
  - `custom.go` 不再无条件钉，改成 `opts = append(opts, registrarOptions()...)`。
  - 新建 `registrar_inmemory.go`（`//go:build !nacos`）钉 `appkit.Registrar(inmemory.New())`；`register_nacos.go`（`//go:build nacos`）里的同名函数返回 `nil`——**留空才是变体生效**，在这里钉一个 nacos 实例同样会把该段判成已消费，只是换了个自我矛盾的形状。
  - 端到端辅助 `e2e_helpers_test.go` 的 `startRoleWithConfig` 那批用例**继续**显式钉内存实例（它们断绑定/路由/限流/发现接线，不断后端语义），并把「钉/不钉」拆成 `startRoleWithOptions(..., extra)` 一条腿：`extra=nil` 就是「按本构建形态实际装配」，装配侧的真机判据只能靠不覆盖来跑。
- 装配侧的证据（`_example/bald/internal/bootstrap/nacos_assembly_e2e_test.go`，`//go:build nacos`）：起 api 角色后按 nacos **v1 HTTP `instance/list`** 查（独立于装配用的 SDK——用 SDK 读 SDK 写只能证明 SDK 自洽），判 `<服务名>.grpc` 与 `<服务名>.http` 各出现 1 个实例、其 ip:port 就是本进程实际监听的端口（不是契约里写的 `:9090`/`:8080`，也不是上一轮残留）、`healthy`/`enabled`/`ephemeral` 三真；`stop()` 后两侧实例归零——**这句停机判据后来被实测降级成终态守卫，见下面两条**。服务名按 `UnixNano` 唯一化，靠 env 覆盖 `app.name`（前缀仍是 `BALD_DEMO_`：env 前缀由契约文件的 app.name 先探后挂，见 `pkg/appkit/serve.go`）。地址跟着契约文件走，另有一条漂移判据先读 `configs/bald-demo-api.yaml` 确认 `127.0.0.1:8848` / `namespace: public` / `type: nacos` 还在，避免「改了契约、用例却在查老地址」。
- **停机摘除的真实归因（第二轮实测，四条数字）**：第一版把「`stop()` 后实例归零」当成「Deregister 生效」的证据，实测证明这个推断不成立。用 `registry/nacos` 的临时探针在 nacos v2.3.2 上分别量三条路（每条都用 v1 `instance/list` 独立读后端）：
  - 显式 Deregister（服务名拼对）→ 调用返回 3.6ms，实例 **0.56s** 归零。
  - **不**注销、只 `Close` naming client → 实例 **0.5s** 归零。ephemeral 实例随连接断开被服务端摘掉，不需要任何注销请求。
  - client 保持打开、既不注销也不关 → 心跳续租，**75s 后仍在**（每 5s 一次心跳）。
  - 服务名拼错的 Deregister（去掉 `.grpc` 后缀，= 第一轮那个变异）→ SDK 回 `ok=true, err=nil`，**实例一个都没少**（nacos 的删除口对「实例本来不存在」幂等）。
  结论：装配用例那句「归零」是「注销」**或**「关连接」，任一条单独坏掉都判不出来——这正是第一轮变异 #2 跑绿的原因（不是环境凑巧，是判据结构上就没有牙齿）。框架侧的 `registry/nacos/registry.go` 代码本身没错，错在证据的归因写法。
- 修法（证据侧，不动产品代码）：把两条机制各自钉成一条只走一条路径的用例，落点第一版是 `_example/bald/internal/bootstrap/discovery_registry_e2e_test.go`（`!grpcgw`）——**这一版落点第三轮搬走了**（示例 module 进不了 CI），用例本体与不变量不变，见下面「第三轮」。
  - `TestNacosDeregisterRemovesInstanceWithClientOpen`：**client 全程不关**，注册 `<名>.http` 与 `<名>.grpc` 两个服务名后只调 `Deregister`，判两个名字 10s 内归零。窗口出处即上面那组数字（正确注销 ≤0.56s / 泄漏 75s 仍在，差两个量级）。这条不看返回值——实测错名回 `ok=true`，§4.1 那条「反注册失败不再被吞掉」在这条路上无从判红，所以判据是绕开 SDK 的 HTTP 查询口。
  - `TestNacosContractCleanupClosesClient`：反过来**只调契约 Provider 交回的 cleanup、不调 `Deregister`**，判实例随后消失——这条只可能由连接释放达成，于是 cleanup 空转（`func() {}`）就是红。
  - 装配用例的停机段注释同步改成「终态守卫」并写明它是两条路径的或，另把窗口从 30s 收到 15s（单条路径实测 ≤0.6s，两条都不发生则 75s 仍在）。
  - HTTP 读口 `listNacosHostsAt` 与 `nacosHosts.assertMatches` 抽到无构建约束的 `nacos_http_probe_test.go`：装配用例是 `//go:build nacos`、这两条是 `!grpcgw`，两个 tag 集合的交集只有 `-tags nacos` 且非 grpcgw，`-tags nacos,grpcgw` 会编不出——共用材料必须放在两边都可见的那一层。
- 验活（第二轮，三道变异，各自 `cp` 备份后精确复原并 `grep` 核对）：
  - 变异 A（`registry/nacos` Deregister 的 `ServiceName` 去掉 `+ "." + u.Scheme`）→ `TestNacosDeregisterRemovesInstanceWithClientOpen` **红**（10.2s 超时，`<名>.http` 仍有 1 个实例）；同一变异下装配用例仍绿，与上面那条「判据结构上没牙齿」的结论一致。
  - 变异 B（`Deregister` 开头直接 `return nil`，整个注销变空转）→ 同一条用例**红**（10.2s）。
  - 变异 C（`registry/nacos/contract` 的 Provider 把 cleanup 改成 `func() {}`）→ `TestNacosContractCleanupClosesClient` **红**（10.3s，实例仍在）；装配用例仍绿——这第三道变异同时补齐了「关连接」那条机制的牙齿，第一轮它无人判。
  - 三道变异全部复原后：默认形态 `TestNacos*` 三条绿、`-tags nacos` 全包 16 条测试绿（etcd 那条按环境 Skip）、docscheck 归属闸门绿。
  - docscheck 侧补两条 `shortSkipExemptions`（新用例同样「要真 nacos + 示例 module 不在 CI」）；闸门牙齿复验一次：从表里删掉 `TestNacosContractCleanupClosesClient` 那条豁免 → 归属闸门**红**（`候选=8 豁免=5` 点名那条用例「没有任何执行证据」），复原 → 绿。**第三轮这两条豁免删掉了**（用例进了 CI 覆盖的包，改走「CI 作业文本点名」那条合法归属），见下面。
- 计划门的连带收口（`plan_acceptance_test.go`）：`registry` 那一舱现在**按构建形态派生**——判据取自变体激活状态（与 `Active` 那条同一个 `appkit.EvaluateBuildConstraint` 口），而不是硬编三元素清单。实测：默认与 `-tags grpcgw` 都是 `opaque = [registry server.grpc server.http]` 且 `programmatic-takeover [registry]` 在场；`-tags nacos` 是 `[server.grpc server.http]` 且告警消失。新增一条双向判据：`registry` 的 takeover 告警**在默认构建必须有、在 nacos 变体必须没有**——后者正是这条洞重新打开时的形状。`WithOpaqueBudget(3)` 是上界，变体里少一个不响，清单那条才是判据。
- 验活（变异一次，`cp` 备份后精确复原并核对）：把 `register_nacos.go` 的 `registrarOptions()` 改回钉 `inmemory` → **三条独立红**：装配用例 30s 内 `<名>.grpc` 恒 0 实例；计划门 `opaque nodes = [registry server.grpc server.http], want [server.grpc server.http]`；计划门 `registry programmatic-takeover warning = true, want false`。复原后两种形态各自全绿。
- 一次改动暴露的既有假设（连带修）：`TestGatewayBackendViaDiscovery`（`!grpcgw`）原先**依赖** `custom.go` 那句无条件钉住来拿到内存注册中心；nacos 变体生效后它转红——同一次装配接的是真 nacos，而网关写的是 `discovery:///bald-demo`（裸名），nacos 的命名口径要求 `<名>.grpc`（见上面那条命名口径用例）。这不是回归，是那条用例的隐含前提现形。修法：它本来就该自己钉内存实例（断接线不断后端），改成显式 `appkit.Registrar(inmemory.New())` 并写明理由。
- 实测细节（记一条会咬人的）：nacos v1 `instance/list` 的 `valid` 是**回包顶层**字段，不在 `hosts[]` 每一项上——把它写进 per-host 结构会得到一个恒假的零值，判据于是恒红。首版就是这样红的，`hosts[]` 上真实存在的是 `healthy` / `enabled` / `ephemeral`。
- **第三轮：把两条机制判据搬进 CI**（`registry/nacos/contract/instance_lifecycle_e2e_test.go`，`package contract`，无构建约束）：
  - 为什么要搬：第二轮的落点在示例 module，而那个 module **在 CI 里根本跑不了**——`_example/bald/api/gen` 是 gitignore 的 proto 产物，干净检出要先 `buf dep update` + `buf generate`，也就是要 pin buf 版本并放开到 BSR 的网络（那是 §7.9 那件事）。所以「本地两种形态全绿 + 三道变异红」是当时能给的最好证据形态，远端一条都没有。
  - 搬的理由成立在判据本身：这两条只需 `registry/nacos` 的 `Register`/`Deregister`、`registry/nacos/contract` Provider 交回的 cleanup，加一个 v1 HTTP 查询口，**不碰 proto 产物**；示例里留下的只有装配那条（`nacos_assembly_e2e_test.go`，`-tags nacos`），它要起整个 api 角色，留在示例是正确的层。
  - 文件内自含共享材料：`nacosE2EAddr`（`BALD_E2E_NACOS_ADDR`，默认 `127.0.0.1:8848`）、`newE2ERegistrar`、`e2ELiveAddr`、`e2EUniqueName`、`listNacosE2EHosts`（v1 `instance/list` 直读，独立于写入方用的 SDK）、`waitForNacosE2EHosts`（`want=0` 即归零）；后端不可达即 Skip。
  - CI 接线：`integration` 作业加 service `nacos/nacos-server:v2.3.2`（`MODE=standalone`、`NACOS_AUTH_ENABLE=false`、`JVM_XMS=256m`/`JVM_XMX=512m`），端口 **8848 与 9848 一起映射**——SDK v2 的 naming 走 gRPC，端口是 `8848+1000`；只映射 8848 时 HTTP 口活着、注册却静默上不去，判据会红在「注册后 20s 内不可见」这种看起来像时序问题的地方。探活改成 `wait_tcp 127.0.0.1 8848 nacos 90`（Java 起得比另外四个慢一个量级；探不到即判红，不放行成 Skip——这批用例的写法本来就是「不可达即 Skip」，不探活等于白加一个服务）。作业多一条腿 `cd registry/nacos && go test -count=1 -timeout 10m -v ./...`，并在「判据必须真的跑过」清单里点名两条 `--- PASS`（Skip 不算通过那条闸门原样适用）。
  - docscheck 归属随之改道：第二轮补的两条 `shortSkipExemptions` 删掉，改由「CI 作业文本点名」这条合法归属接住。闸门复跑：`判据腿=9 覆盖包=4 候选=6 进表=0 CI点名=2 豁免=4` 绿。
  - 验活（新落点，三道变异各**只**打红对应那条；`cp` 备份后精确复原并 `diff <(git show HEAD:…)` 核对 0 差异）：变异 A（`Deregister` 的 `ServiceName` 去 `.scheme`）→ 第一条红 10.16s；变异 B（`Deregister` 首行 `return nil`）→ 第一条红 10.15s；变异 C（Provider 的 cleanup 改 `func() {}`）→ 第二条红 10.46s。
  - 本机按 CI 形状跑（真 nacos 容器）：`TestNacosDeregisterRemovesInstanceWithClientOpen` 0.71s、`TestNacosContractCleanupClosesClient` 0.79s；`registry/nacos` `ok 0.553s`、`registry/nacos/contract` `ok 2.116s`。
- 仍未闭合（如实记）：① ~~装配那条（终态守卫，`-tags nacos`）仍只有本机证据——它在示例 module，进 CI 的前置是 §7.9 的 buf 工具链 pin 与 BSR 网络，属独立一件事~~ **同日已闭合**：`example` 作业按 pin 装 buf 现生成产物后跑这条（分支 head `b57dd1e`，run 41832136892 作业 `example` = job 126581704475，`--- PASS: TestNacosAssemblyRegistersIntoBackend (0.97s)`）；它判的仍是两条摘除路径的**或**，本来就不是机制判据。同一次取证顺带查出并修掉这条用例自己的断言竞态（`.http` 只查一次 ⇒ 单条跑 8 次红 1），见「7.1 补：示例 module 进 CI」。② nacos 的两条**机制**判据自本轮起有 CI 形状的执行路径（服务容器 + 逐条 `--- PASS` 点名）——**远端绿证已取得**（2026-11-04，head `019dac2`，run 41813828397 的 `integration` 作业 126519435965）：日志里 `--- PASS: TestNacosDeregisterRemovesInstanceWithClientOpen (0.89s)`、`--- PASS: TestNacosContractCleanupClosesClient (0.73s)`，随后「判据必须真的跑过」那一步对这两条各打一行 `PASS`；同一 run 七个作业（test/race/lint/vuln/acceptance/integration/integration-kubernetes）全部 success，`acceptance` 报 `scope=ci 的 6 条腿、72 条判据全部通过`。
- 已证（GitHub 实测）：同批 §7.1 迁移的两个提交（head `b8906b7`）的 `ci` run 41795844844 **七个作业全部 completed/success**（`test`、`race`、`acceptance`、`integration-kubernetes`、`vuln`、`lint`、`integration`）。
- 本文里那条被实测推翻的表述已改：《Bald 注册中心设计》原写「命名侧 namespace 未证（HTTP 探针判不出）」，现在写的是实测结论（`public` 与空串同租户，`registry/nacos` 无需折叠）。

**7.11 `sres` 冷窗口放行判据在 `race` 作业红（中）** ✅ 已修（2026-11-04；发现路径：§7.10/§7.9 那批提交推送后，run 41806067800 的 `race` 作业唯一红项——`circuitbreaker/sres` `TestExecute_AllSuccessNotThrottled` 报 `allowed 179/200 — expected at least 90%`，其余六个作业全绿；该提交只改 `bconf` 判红文案与文档，与 sres 无因果关系，是这条用例自己的尾巴）

- 形状与 §7.4 同源，机制不同条：`Allow` 的 `accept = requests/(requests+1)`，`requests` 只统计**已放行的**（`Execute` 在被拒时不调 `MarkSuccess`）。冷窗口下 `requests` 从 0 起，于是**拒绝会自我强化**——一次被拒不推进 `requests`，同一个低 `accept` 被重复采样，左尾比「独立二项」薄得多，不能用 `np=195±2` 那种正态估。
- 判据的准确尾巴用 DP 算（不是估）：状态 `r=已放行数`，`P(拒绝|r)=1/(r+1)`、`r=0` 时走 `stats.requests==0` 的早退分支必放行。逐步精确分布（`Fraction` 无浮点损失）：`total=200` ⇒ 均值 **194.157**、`P(放行≤179)=8.399e-5`（约 **1.19e4 次跑一红**）。旧判据 `< total*9/10` 即 `≤179` ⇒ 每次执行的红率就是它。
- 本机实测与 DP 对齐（这是「模型没错、环境不特殊」的证据）：`-race -count=3001` ⇒ 均值 **194.21**、min **178**、2 次红；CI 那次 179 落在同一尾巴里，不需要额外假设 runner 负载。
- 修法（**90% 这个语义一字未动**，扩样本而非松阈值）：`total` 200→**500** ⇒ 阈值 450。同一 DP：均值 **493.22**、`P(放行≤449)=1.994e-13`（约 **5e12 次跑一红**）。本机 `-race -count=3000` 复跑 ⇒ 0 红、min **478**、mean **493.16**（样本外余量 28，DP 与实测再次对齐）。
- 验活（两道变异，各自 `cp` 备份、`diff <(git show HEAD:circuitbreaker/sres/sres.go)` 核对 0 差异）：
  - 变异 M1（`MarkSuccess` 也 `errors++`，即「成功被当成失败记账」——这条正是该用例的本职）→ 红 `allowed 1/500`（只有 `requests=0` 那一次早退放行）。
  - 变异 M2（公式分母 `requests+1` → `2*requests+1`，accept 掉到约 1/2）→ 红 `allowed 250/500`；同一变异下 `TestHealthyWindowAcceptanceNearOne` 也红（`985/2000`），两条判据分别覆盖冷/热窗口。
  - 复原后 `go vet` + `go test -race -count=1 ./...` 该 module 绿（`ok 1.089s`），`gofmt -l` 空。
- 同类写法复扫（这次把**同文件**也算进去）：全仓非测试代码的随机源仍只有 `circuitbreaker/sres`、`retry`、`registry/consul`、`broker/kafka` 四处；测试侧的比例/计数型断言除本条与 §7.4 那三条外，`retry`/`registry/consul`/`kafka` 里没有新增（grep `rand.Float64|rand.Intn|rand.Int63` 的测试命中只有 `transport/{redis,rabbitmq,rocketmq,http3}` 的 payload 抖动取值，与判据无关）。
- 没做的：① 不给 breaker 加可注入随机源让断言完全确定（同 §7.4 的理由——生产 API 变更与「修一条假红」不成比例）；② 不把 90% 降到 85% 来消红（那是把判据的语义改掉）；③ ~~远端绿证以本次推送后的 `race` 作业为准~~ **已取得**（2026-11-04，head `019dac2`，run 41813828397 的 `race` 作业 126519436469）：`=== race: ./circuitbreaker/sres ===` → `ok github.com/kalandramo/bald/circuitbreaker/sres 1.017s`，且该作业整体 success——正是 run 41806067800 红掉的那条腿换了样本量之后第一次在远端过。尾巴仍是 2e-13 级，所以真正立得住的还是上面那两组 `-count≥3000` 实测 + DP，远端一次绿只是「没撞」。

**7.12 `cli` 的 buf 真编译判据自 691929c 起结构性判红（中）** ✅ 已修（2026-11-04；发现路径：§7.9 第四轮在本机追工具链归因时，顺手把装 buf 才会跑的其余腿逐条点名——`TestGenProto_CRUD_BufBuild` 在**未改动的树**上就红）

- 现象：`--- FAIL: TestGenProto_CRUD_BufBuild … buf build must compile the CRUD six-piece proto: exit status 1 / Failure: got a v2 buf.lock file for a v1 buf.yaml - this is not allowed, run buf mod update to update your buf.lock file`。
- 根因与工具链无关：夹具自己写 `api/proto/buf.yaml: version: v1`（`cli/internal/codegen/gen_domain_test.go`），而它复制的 lock 来自 `_example/bald/api/buf.lock`——那份 lock 自 `691929c`（示例迁生成树时被新版 buf 重出）起是 `version: v2`。buf 对「v1 配置 + v2 lock」直接拒绝，两个 buf 版本（1.50.0 与 1.57.2）实测都拒。
- 为什么远端一直没红：这条腿在 `buf` 缺席时 `t.Skip`，而 CI runner 不装 buf（§7.9 同一口径）。所以 691929c 之后它只在**装了 buf 的人**身上红——正是 §7.9 记的那类「本地腿没有远端证据」的形状，只是这次红的是夹具自己而不是判据强度。
- 修法：夹具的 `buf.yaml` 改成 v2（`version: v2` + `modules: [{path: ., name: buf.build/kalandramo/genproto-check}]`，deps/lint 原样），并把「lock 是 v2 ⇒ 配置必须 v2」写进夹具上方的注释——下次谁再动那份 lock 的格式，红的时候能一眼看到耦合在哪。判据本身一字未动（仍是真 `buf build` 出链接描述符）。
- 验活：两个 buf 版本各跑一次，`--- PASS (0.07s)` / `--- PASS (0.04s)`；`cli` module 全量 `go test -count=1 ./...` 绿（前置合规 PATH 的 buf 1.50.0）。
- 顺带扫了仓内其余 buf 配置的版本一致性：`bconf/buf.yaml`、`_example/bald/api/buf.yaml` 都是 v2，且仓内只有那一份 `buf.lock`——没有第二处同类耦合。
- 远端能证的部分（2026-11-04，head `019dac2`，run 41813828397 七作业全 success）：这条腿本身在 CI 里仍是 Skip（runner 无 buf），所以远端证明的是「`cli` module 其余判据没被这次改动带红」——同轮 `acceptance` 作业里 `cli → ./internal/codegen` 那条腿的 18 条判据逐条 `--- PASS` 通过。真正的执行证据只有本机那两个 buf 版本各一次。
- 没做的：① 没给 CI 装 buf 来拿这条腿的远端证据（它要的是 BSR 依赖解析，属 §7.9 未闭合①同一件事）；② 没把「lock 与配置的版本必须匹配」做成 docscheck 闸门——那需要解析 buf 的 config schema，收益抵不上为一个夹具再加一层元校验；红的时候文案已经指到耦合点。
  - ① 在同一天已闭合：`proto-contract` 作业按 pin 装 buf 并真跑这条生成腿（远端 cert：main head `55609df`，run 41825997444 八个作业全 completed/success）。`cli` 的这条夹具腿仍是本机证据——它的 `buf build` 需要的是**装 buf 的人**，`example` 作业新增的 `scripts/install-buf.sh` 目前只服务 `proto-contract` 与 `example` 两个作业，见 §7.9 第五轮与本节「7.1 补：示例 module 进 CI」。

**7.13 同进程密集的配置查询撞 nacos SDK 自己的「进程内」令牌桶 ⇒ 配置层 `Load` 判错（中）** ✅ 已修（2026-11-04；发现路径：为 `example` 作业定 `-tags nacos` 那一态的跑法时，整包与并集都稳定/间歇红，单条 12 次全绿。归因在这条上**错过两次**，见下面的「归因史」）

- 现象（不是 Skip、不是超时，是启动就失败）：`config: load layer "nacos": read config from both server and cache fail, maybe the server is down and you cannot get the configuration from the local cache … cache/config/bald-demo.yaml@@DEFAULT_GROUP@@ … file not exist`。
- 文案指向「本地缓存文件不存在」，那是**次生**错误。真因在 SDK 自己的日志里（`LogLevel: warn` 时仍打得出来）：`get config from server error:ConfigQueryRequest is limited`。链路是 nacos-sdk-go v2 的 `queryConfig` 拿到错误后回落到读本地缓存，两处都失败时报的**只有缓存那条**（`clients/config_client/config_client.go` 的 `GetConfigError` 路径），所以限流被完全遮蔽——接手者按文案去查 dataID/租户名会一无所获。
- 触发形状是**同一进程里同一个键的配置查询密度**，不是「哪条用例排在前面」。固定同一个测试二进制，每格一个新进程、`-count=1`，本机 etcd + nacos v2.3.2 容器实测：单条装配 **12/12 绿**；`TestE2E_`(6 条)+装配 绿；`TestGateway`(3)+`TestE2E_`(6)+装配 绿；naming 三条+装配 3/3 绿；naming+`TestE2E_`+装配 3 次红 1；四组并集（10 条判据）3 次红 2；整包（13 条判据）**3 次红 3**。

**归因史**（前两版的措辞本轮已从 `ci.yml`/`Taskfile.yml` 原地改掉；三版都留档，是因为下次这条再红，第一件该怀疑的就是文案本身）：

1. 「同进程多个应用复用一条 SDK 连接」——**不成立**。v2.3.5 的连接名是 `config-<taskId>-<uid>`，`uid` 是每个 `ConfigClient` 一个随机 UUID（`config_client.go:145`、`config_proxy.go:182`、`rpc/rpc_client.go:153` 的 `clientMap`），N 个应用就是 N 份独立连接。
2. 「服务端按请求类型限流」——**也不成立**，即本轮登记时的版本。错在把 SDK 日志里那句 `ConfigQueryRequest is limited` 当成了服务端的回包。
3. **真因：限流发生在 SDK 自己的进程内令牌桶，一个包都没发出去。** `clients/config_client/limiter.go:42-56`：`rate.NewLimiter(rate.Limit(5), 5)`（5/s、桶容量 5），键是 `util.GetConfigCacheKey(dataId, group, tenant)` = `dataId@@group@@tenant`（`util/common.go:65`），而 `checker` 是**包级变量** ⇒ 同进程内所有 `ConfigClient` 共用同一个桶。判限时 `config_proxy.go:116-127` 直接返回那句错误文本，不建请求。

- 源码读出来的机制要用无网络的实验钉住，否则和「服务端限流」在文案上无法区分。三格实测（真 nacos v2.3.2 容器，dataID `bald-demo.yaml`/`DEFAULT_GROUP`，每格一个新进程）：
  - **纯本地格**：只调 `IsLimited` 一个信号量都不建、全程零网络 ⇒ 20 次里恰好放行第 0..4 次，第 5 次起全拒。限流器在没有连接、没有服务端参与时就会判——这直接否掉第 2 条归因。
  - **零往返格**：`新建 client → 一次 GetConfig` 重复 60 次（同一个键）⇒ **60/60 全拒**，单次耗时 min/median/max 都是 `0s`。真到服务端再回来的错误不会耗时 0 秒；而「每次新建 client」本来是为了绕开「同 client 内部缓存」的变量控制，结果反证了桶按**键**而不是按 client。
  - **同 client 突发格**：1 个 client 连续 60 次 ⇒ 54 拒，首次拒在第 6 次；突发打完后要静置 **17.5–22.4s** 才重新放行一次（桶里欠的令牌按 5/s 回填，60 次突发欠约 11 秒以上，加上首次窗口）。
  - 恢复窗口（每次失败后 50ms 再试）：命中在第 3/3/4 次重试，对应 **160 / 160 / 220ms**。

- 落点为什么不在示例里而在 `bconfig/nacos`：`Config.Load` 原本是**一次** `GetConfig`、无重试，于是「进程内桶瞬时见底」被直接投影成「配置层加载失败 → 应用起不来」。密集启动在生产里是真实形状（一个进程叠多层 nacos 源、多 dataID 冷启动、滚动重启窗口里的并发实例），不只测试套件。
- 修法（已落地）：`Load` 改成**带上界的重试**（`bconfig/nacos/config.go` 的 `loadRetryAttempts = 4`、`loadRetryInterval = 250ms`），参数按上面那组实测取：**250ms 是最坏观测回填 220ms 的余量**，4 次约 1 秒，落在「同键突发后第一次回填」与「桶见底要 17s 才排空」之间——即它救的是**尾部缺一两个令牌**的形状，救不了持续超额，这是设计边界而不是缺陷。间隔是 `var` 不是常量：时序用例把它调小，为此不值得新增一个公开旋钮。错误文案同时把**上界**和**候选真因**写进去（`loadFailed`），因为下面那条遮蔽仍在。
- 判据（假 `config_client.IConfigClient`，不依赖真 nacos）四条：首错后成 ⇒ `Load` 成功且**恰好三次**调用；首次即成功 ⇒ 不多打一次；每次都错 ⇒ 在**上界**处放弃、`errors.Is` 拿到 SDK 原错、文案里带 `5 config queries per second` 与 dataID；ctx 已取消 ⇒ 不进入重试、`errors.Is(err, context.Canceled)`、耗时 <100ms。
- 四次变异实测（`cp` 备份后精确复原，改完 sha256 与备份一致）：① 上界 `4 → 1` ⇒ 红 2 条（回填那条 + 取消那条——上界变了以后桩里只剩一个错误，取消用例拿到的就不再是 `context.Canceled`）；② 循环上界写死成 `1`（即整体去掉重试）⇒ 红 3 条（上界那条的 `calls` 与文案都还在，只有次数判据先崩）；③ 删 `sleepCtx` 的 `ctx.Done()` 分支 ⇒ **只**红取消那条，且耗时从 0s 变 0.75s（正好等满 3 个间隔）；④ 删 `loadFailed` 里「候选真因」那句 ⇒ **只**红上界/文案那条。
- 没有机器判的那一轴要如实说：**`loadRetryInterval` 的具体数值（250ms）没有任何用例判得到**——四条时序用例都先用 `quickLoadRetry` 把它压到 1ms（取消那条刻意不压，它判的是「等待被取消打断」而不是「等待多长」）。所以 250 这个数只有上面那组实测出处 + 注释，改它不会红；这是「参数靠测量而不是靠闸门」的固有边界，不是遗漏。
- **第二条遮蔽（未修，owner-gated）**：限流未必报错。`config_proxy.go:137` 成功时写快照，`config_client.go:200-215` 在拿到错误时**回落读快照并返回 `err == nil`** —— 于是限流静默变成「配置没变」，应用带着上一份配置正常启动。仓内唯一能让真因浮出来的开关是 `DisableUseSnapShot: true`，而那是**删掉离线快照能力**（断网时配置源直接判错），属「能力不删除」里要先问 owner 的那一类，所以本轮没动，只把它写进 `loadFailed` 的注释与本节。
- 顺带查到的环境事实（下次做快照实验要用）：`go test` 下 SDK 的默认缓存目录**不是** `/tmp/cache`，而是从 `os.TempDir()` 派生的 `/var/folders/…/go-buildNNNN/b001/cache/config/…`——按前者清快照会清到空目录，实验照样「绿」，却什么都没测。
- 验活（并发对照，10 个 goroutine 各建一个 `Config`、同一个键、三轮）：**当前树（带重试）0 失败**，最坏单次 362ms；`HEAD`（无重试）冷启动那一轮 **10 个里 5 个直接判错**，后两轮 0 失败——后两轮的"绿"是上面那条陈旧快照回落吸收掉的，不是没限流。这一条是本次修复真正的牙齿所在。
- 现在的口径（诚实边界）：**修完之后，示例整包在本地不再复现红**——整包 13 用例共 9 次 `Load`、全落在同一个键上，最密一段是 300ms 里 5 次，正卡在桶容量 5 之内（有余量但极小）。所以「套件现在绿」**不能**当作重试生效的证据，证据只有上面那组 A/B 与四条单测。也正因为突发只是被摊开而不是消掉，`example` 作业与 `task example-e2e` 的形状**照旧**（`-tags nacos` 只跑装配单条），没有借此把判据放宽或收紧。
- CI/Taskfile 的注释同步：`ci.yml` 的 `example` 作业与 `Taskfile.yml` 的 `example-e2e` 都改成按第 3 版（进程内桶）叙述，并在原地写明前两版错在哪——先前两处都传抄过「服务端按请求类型限流 ⇒ `ConfigQueryRequest is limited`」，那是第 2 版的错误措辞。
- 远端那一条腿的形状未变，证据也仍在：`example` 作业自落地以来连续交出 `--- PASS: TestNacosAssemblyRegistersIntoBackend`（分支 run 41832136892 0.97s / main run 41834825763 0.77s / main run 41837444797），即「单发不触发」不只是本机 12 次的结论。
- 未证：nacos 服务端**自身**是否也有一道按请求类型的限流（本轮没有把客户端桶关掉再打的姿势，无法隔离），所以只证到「本机这一组红由进程内桶解释」。若将来出现「桶容量远大于观测密度仍判限」的案例，需要重新做一次这两格。

---

## 8. 文档漂移

`README.md`（81 行）**确实**描述了 v2 Provider（「段驱动的 Provider 契约」`:5`，appkit 一行含「段驱动 Provider」`:58`）——我原先的怀疑不成立。
`internal/docscheck/docscheck_test.go` 随 `go test ./...` 执行，校验两件事：每条相对 Markdown 链接的目标存在（`:21`）；Go 注释里引用的每个 `docs/*.md` 路径可解析（`:65`）。
- 缺口：**它只测链接完整性**，测不出「文字在描述已删除的 API」或「新增模块没进文档」。§2 那些未接线模块正是这类漂移的候选。
- 消解：加一条模块清单断言（`find` 结果 vs docs 中列举的模块集合），成本极低且能自动失效。

**8 收口（清单半，已落地；「描述已删除 API」半见本节末尾）**：`docs/architecture/current-architecture.md` §2 末尾加了一块 ` ```module-list ` 围栏（与 `scripts/list-modules.sh` 同源，现 94 行），`internal/docscheck` 新增两条断言：

- `TestModuleListMatchesDerivation`——Go 侧复刻同一条谓词（含 `go.mod` 的目录，排除 `.git`/`node_modules`/`vendor`/`_example*`，根记作 `.`），与文档块**双向**比对；另带两条防空转断言（派生数 <50、文档块为空 → `t.Fatal`），谓词一旦被改坏是响而不是静默通过。
- `TestModuleDerivationMatchesScript`——直接 `sh scripts/list-modules.sh` 与 Go 复刻逐行比对，把「测试里的谓词」钉在「CI 用的那一条派生」上，Windows 跳过（无 POSIX sh）。
- 变异验证（三条都实测过再复原）：删文档块里 `workflow/argo` → 「module workflow/argo 未出现在文档清单里」；塞 `not/a/real/module` → 「在仓库中不是 module」；把 Go 侧 `_example` 跳过改成 `if false &&` → 「Go 派生 93 个，脚本派生 92 个」。复原后 `go test ./internal/docscheck/` → ok。
- **「文字描述已删除的 API」这半：已按一次性测量收口（2026-11-04），结论是不造永久闸门**——测量的做法：`go/ast` 遍历全仓含 Go 文件的目录取导出名（194 个包目录、3124 个名字）作符号表，对 `docs/**/*.md` 扫 `(?<![\w./-])([a-z][a-z0-9_]{1,24})\.([A-Z][A-Za-z0-9_]{2,})(?![\w])`（尾边界必须有，否则「首参带括号的多参函数签名」会被读成组声明），两种口径各判一次：
  - **口径先把自己排除**：本登记册按定义要引用假名作证据，计入就自反馈——同一次扫描含 `risk-review.md` 比不含它多 6 条「宽松未解析」（40 vs 34 distinct）和多 8 条「严格错包候选」（48 vs 40 distinct），全是这一节自己写进去的假名，而且随着这一节被改写还会漂。下面的数字都取「`docs/**/*.md` 排除 `risk-review.md`」：修前 **61 份 / 777 distinct / 2322 occ** → 修后 **759 / 2360**（出现数上升是因为修的时候补进了**正确**的限定名引用）。修前树用 `git archive HEAD docs` 取出，与修后共用同一份符号表，两遍逐条可比。
  - **宽松口径**（叶子是仓内包目录名、但该符号在整仓任何包里都不存在）：**40 distinct / 68 occ → 34 / 62**。余量 34 条逐条看过，全为第三方与标准库同名（`gin.WrapH`、`grpc.ServerOption`、`http.ResponseWriter`、`prometheus.DefaultRegisterer`、`otel.SetTracerProvider`、`redis.NewUniversalClient`、`proto.Merge`、`mcp.CallToolRequest`、`sentinel.InitDefault`、`thrift.TStruct` 等），没有一条是本仓 API——即宽松口径现在**对本仓真漂移归零**。
  - **严格口径**（符号在仓内确实存在、但不在所写的那个叶子包里 ⇒ 错包候选）：**53 / 176 → 40 / 160**。余量三类，都核实过：接收者变量的散文（`app.Settings`/`app.Drift` 这类是 `*App` 的实例方法，写成 `appkit.Settings` 反而是错的）、第三方同名（叶子恰好与仓内目录重名，如 `log.Fatal`、`health.NewServer`、`config.Source` 指 kratos 而非本仓 `bootstrap/config`）、以及**叙事性**引用（明确写「该提案未落地过代码」的 `web.Context`/`web.Handle`/`web.Error`、proto getter `local.GetHost`、《Bald 重试设计》:355 用来解释「为什么类型不叫 `Retry`」的那一处 `retry.Retry`、《新增业务域》里举例的 `routes.Org`——`_example` 当前只生成了 `routes.Greet`）。
  - 本轮据此定位并修掉的真漂移（每处都先用 grep 确认符号/文件确实不存在，再改）：`bootstrap.ConfigRegistry` / `registry.Build(ctx, bootCfg)`（配置系统设计与 guide 各一处，含 `pkg/appkit/appkit.go` 的注释示例）、`AppKit` 类型与其 `New`/`(*AppKit).Config`（实际是 `App`/`NewApp`，且 `App.Config()` 已删）、「bootstrap 七域 Registry」（`bootstrap/{database,cache,registry}.go` 三个文件已随 Provider 收敛到 v2 删除，本包只有 config 源工厂 + server/logger/health 段构造）、`mech.ParseStringList`（真实为 `config.StringList`）、`std.StandardLoggerProvider`（在 `appkit`）、`log.FilterLogger`（`log.NewFilterLogger`）、`transport.WithCodec('json')`（`WithCodec` 属 `transport/websocket`、`transport/sse`、`broker`，`GetCodec` 属 `encoding`）、`bootstrap.BuildServers`（mermaid 节点）、`contextx.UserID`/`TenantID`/`TraceID`（`…FromContext`）、生成物锚点断言 `appkit.New` 在 `capOpts` 之后（该变量与断言都不存在，换成真实三条）、以及 `App.Database(typ)` / `app.Cache(...)` / `app.Workflow("argo")`（实例取用只有类型化句柄 `Key[T].Resource(app)`，如 `rediscontract.Redis.Resource(app)`）。`docs/architecture/current-architecture.md` 里那句 `registry.Registry` 同批改成 `registry.Registrar`/`Discovery`（根包 `registry` 只有 `Registrar`/`Discovery`/`Watcher` 三个接口，没有 `Registry` 类型；叫 `Registry` 的是 `registry/{etcd,consul,nacos,kubernetes}` 四个后端的实现结构体——两条口径别写反）。
  - **为什么不做成永久闸门**（三条都是量出来的，不是感觉）：① 判据要能绿就得逐条豁免第三方同名与接收者变量散文（严格口径余量 40 条），而**按叶子整体豁免**会直接把 `log`、`cache`、`app` 这些本轮真出过漂移的叶子看瞎——`log` 既是标准库同名又是本仓 `log/`；② 两类真漂移结构上扫不到：已删 API 在文档里常写成**裸名**（`` `WithGatewayRegister` ``，没有任何 `pkg.Sym` 可匹配），以及**别名式引用**（`storegorm.SQL` 的目录是 `contrib/store-gorm`、包声明其实是 `baldgorm`，正则 `[a-z][a-z0-9_]{1,24}` 连带连字符的目录名都进不了叶子表）——闸门交出的「绿」对这两类是虚假安全感；③ 全限定形态 `github.com/kalandramo/bald/<pkg>.<Sym>` 在 docs 里实测**只有 1 处**，还是 `-ldflags -X github.com/kalandramo/bald/pkg/appkit/appcli.Version` 这种构建参数——对它造判据分母为 0，属空转。所以这一半的兜底仍是人肉逐条 grep（§2/§5/§6.3/§7.13 与本轮都这么收），行号引用优先改成符号名（`bundle.go:171-211,224-241` 那类漂移点已换成 `Gin()`/`GRPCInterceptors()`）；本节留下的是**可复跑的做法**（符号表来自 `go/ast` 全仓遍历 + 上面那条正则与两种口径），下次重扫按此口径即可，成本是一次脚本而不是一份要长期维护的豁免表。
  - **测量的边界（别把「docs 绿」读成「全部文档面绿」）**：上面两口径都只扫 `docs/**`。生成物镜像 `.qoder/repowiki/**`（`.gitignore:50` 的 `.qoder/`，不在交付面里，刷新只能在知识中心点重生成）实测仍留着本轮已在 `docs/**` 修掉的 **3 处**假引用——`Registry.Build` ×2（配置系统页、热更新机制页，各一句）与 `contextx.UserID/TenantID` ×1（`knowledge/…/通用工具包集合（pkg_）/架构设计.md`，真实为 `contextx.UserIDFromContext`）。同批核对过**不算漂移**的：`log.NewFilterLogger`、`StandardLoggerProvider`（wiki 写它在 `pkg/appkit`，正是真的）、mermaid 里 `AppKit["pkg/appkit"]` 这类**包别名**（不是类型名）。`docscheck` 不扫这一层是既定口径（它判的是链接与入库 module 清单），所以这一层的唯一出路是重生成，不在仓内改文件能解决。
  - **第三类锚点（行号）的可判部分已扫**：`docs/**/*.md`（排除本登记册）里 `([\w./-]+\.go):(\d+)` 共 **102 处**，逐条解析到仓内文件后：**0 处指向不存在的文件**、**0 处行号越过文件末尾**（`_example`/`contrib` 这类带连字符或不在根 module 的路径按 basename 唯一命中兜住，歧义的不算命中）。这两条只是**必要条件**——「`bundle.go:171-211` 指向一个存在且行数够的文件」并不保证那段真是所描述的代码，所以这一类的真判据仍是「锚点旁边写符号名」，扫描只用来兜住最坏的那种漂移（文件已删/文件缩短）。
- **`dir` 写在 `cmds` 条目里会被 go-task 静默忽略**（本轮查出，已修）：`Taskfile.yml` 有 13 处这种写法，实测 go-task 3.52.0 里 `- cmd: pwd` 配 `dir: sub` 打印的仍是任务目录——不报错、不失败，只是按父目录跑。后果分两种，坏的是同一种形状：`go test ./pkg/appkit` 这类**带路径**的腿当场 setup failed（acceptance 的五条 appkit 腿自 `96582bf` 把 `pkg/` 拆成独立 module 起就一直红，红了一整轮没人看见，因为没有任何任务或作业调用 `acceptance`）；而 `go build ./...` / `go test ./...` 这类**不带路径**的腿退化成「把根 module 再跑一遍」——`task build` 与 `task test` 的 cli 腿（§3.4 走 B 时加的）就是这一种，全绿、0 覆盖。
  - 消解：跨目录一律拆成带任务级 `dir` 的 `internal: true` 子任务，由 `- task: <名>` 调用（`test`/`build` 各拆一条，`acceptance` 按 pkg/cli/bconf/_example 拆成五条）；`internal/docscheck` 加闸门 `TestTaskfileNoPerCommandDir`——缩进 >4 的 `dir:` 行即违规并报出行号，另配一条「任务级 `dir` 一条都没有即 `t.Fatal`」的防空转断言。两次变异实测：往 `build` 的 cli 腿写回 per-cmd 形式 → 红在 `Taskfile.yml:438`；删光任务级 `dir` → 命中空转断言。
  - 修好之后的实测覆盖面（此前不存在）：`task test` 的 cli 腿真跑出 `cli/internal/codegen` 64.6s、`cli/internal/release` 4.5s；`task acceptance` 五条 appkit 腿 + codegen 12.3s + 真实示例的 `gen app --check` 与清单比对 + bconf + 参考工程计划两态全部 `ok`。
  - 已闭合的那半条（2026-11-04）：`acceptance` 被文档称作「声明性指标的主门」，但**此前没有任何 CI 作业调用它**——现在有了：`.github/workflows/ci.yml` 的 `acceptance` 作业跑 `bash scripts/acceptance-gate.sh ci`，本地 `task acceptance` 跑同一个脚本的 `all`。**判据清单与执行由同一个脚本交出**，因为 `go test -run <正则>` 命中 0 个测试时**返回 0**：用例改名/被删/正则写错，作业照样全绿（本仓已被这类静默失效咬过两次——§7.1 的「不可达即 Skip」、上面这条 per-command `dir`）。脚本改成先 `go test -list` 取当前真实命中的顶层测试名（空即红），再 `-v` 跑同一正则并逐条断言 `--- PASS: <名字> (`，Skip/改名/没链上都会落到同一条判据；`-list` 而不是手写表格，增删用例不必维护第二份真相。表里两条标 `no` 的本地腿各有具体理由：`_example/bald` 的参考工程计划要编译已 gitignore 的 proto 产物（同 §7.1「刻意不含 `_example`」），`bconf` 的产物字节判据绑 buf 版本形态（新登记 §7.9）。
    - 验活（正向）：`scope=ci` 实测 6 条腿、72 条判据全通过（appkit 五腿 + codegen 18 名，逐条 `PASS`）；`scope=all` 在**未改动**的仓库上跑过两次，数字差一条腿：buf **1.57.2** 下 9 条腿、74 条判据，唯一未过的是 `bconf` 那条——报 `内容不一致: bootstrap/v1/log.pb.go`，正是这次登记 §7.9 的起因，不是闸门误判；把与入库产物同源的 buf **1.50.0** 前置到 PATH 再跑，9 条腿、75 条判据全过（`_example` 两态各 1 条、`bconf` 1 条都真 `PASS`，无 Skip）。两条合起来才是这条腿的口径：**它红时证明的是工具版本，不是契约**。脚本还过 shellcheck（`-S style`，容器内）与 macOS 自带 bash 3.2 的解析/执行（空数组用 `${arr[@]+"${arr[@]}"}` 而不是 `"${arr[@]}"`）。
    - 验活（变异，两次实测后复原）：① **正则改成命中 0 个**（`TestDrift` → `TestDriftZZRenamed`）→ `::error::-run 正则在 pkg ./appkit 命中 0 个测试`，exit 1，这就是「改名/删测试不再静默绿」的直接证据；② **在 `pkg/appkit` 放一条 `t.Skip` 的探针用例**（名字落在 `TestDrift` 前缀内）→ 该腿另外两条真判据照常 `PASS`，探针报 `::error::判据 TestDriftZZSkipProbe 没有顶层 PASS 记录` 并打出 `--- SKIP` 原文，exit 1。探针文件与临时脚本都已删除，`git status` 复原干净。
    - 已证（远端，head `559ac3d`）：`ci` run 41756214103 **六个作业全 success**，其中新增的 `acceptance` 作业第一次在 GitHub 上真跑——日志末行 `acceptance-gate: scope=ci 的 6 条腿、72 条判据全部通过`，三条本地腿各打一行 `SKIP（scope=ci 不含这条腿，理由见表头）`（跳过是**可见**的，不是静默）；`workflow-lint` run 41756214273 success。同一 head 的推前证据：`task verify` 绿（buf 1.50.0 前置 PATH）、`task acceptance`（scope=all）75 条判据绿、`task lint` 绿，并在**干净克隆**（`git clone --no-local`）里用 `golang:1.27` 容器（实测 `go version go1.27.3 linux/amd64`，与 CI 同补丁版本）跑同一条 `bash scripts/acceptance-gate.sh ci` → 6 腿 72 判据 exit 0。消费侧同期升级：bald-admin `0b745aa`（消费 `pkg v0.2.2`，就绪判据改为框架 `AfterStart` 运行态信号）的 backend run 41756410336 success。
    - 未闭合的形状 → **已按可执行口径造闸门**（2026-11-04 第二轮，`TestAcceptanceTableCoversShortSkippedTests`）。原先这条写的是「未造自动归属判据（那需要一条『什么叫验收用例』的可执行定义，成本不划算）」——不划算的是给全部 269 个 appkit 用例定义「该不该进表」；但**表内包里的一个具体洞**是能定义的：CI 的 `test`/`race` 作业跑 `go test -short`，而判据腿按 `-run` 正则挑用例，两条口径之间没有任何东西保证「-short 会跳过的用例」真被某条腿接住。先把「这个洞今天到底有没有」量出来：`pkg/appkit` 269 个顶层测试里 **0 个**调用 `testing.Short()`（五 appkit 腿不存在这类洞），`cli/internal/codegen` 恰好 **2 个**（`TestTree_AuthzClosedLoop`、`TestTree_EndToEndTimed`），而 `ci.yml` 第 96 行那一步本来就非 `-short` 点名跑它们——**结论是当时没有覆盖洞，缺的是「以后漂了会响」**。
      - 闸门形态：读 `scripts/acceptance-gate.sh` 的判据表本体（表与执行同文件，不抄第二份），对每个被覆盖的包做 AST 归因，`-short` 下会跳过的顶层测试必须三选一——① 被同包某条腿的正则命中，② 名字出现在 `.github/workflows/` 里（有别的作业点名跑），③ 在 `shortSkipExemptions` 里带理由豁免。归因做**一层**：守卫写在 helper 里也算（示例包的 `etcdDiscovery`/`nacosDiscovery` 正是这种形状，只查测试函数自己会全数漏掉）。
      - 实测数字：判据腿 9、覆盖包 4、候选 5（codegen 2 + 示例 3）、进表 0、CI 点名 2、豁免 3。三条豁免各有具体理由：示例 module 整体不在 CI（今天的阻塞是 `api/gen` 属 gitignore 的 proto 产物、编译前需 buf + BSR 网络，见本节末与 §7.1；原先写的「`go.mod` 有仓库外的 kratos contrib replace」已在 2026-11-04 随那条迁移失效，理由文本同步改过），且它们要真 etcd/真 nacos 才能跑——进表会让没后端的机器必红，那正是 §7.1「不可达即 Skip」那类静默的形状。
      - 按 owner 口径复核过这组数字不是本轮搬运造成的：`git grep -l "testing.Short()" <rev>` 在 `96582bf`、`9cf17db`、HEAD 三个点上，`pkg/appkit` **都是 0 命中**（269 这个量是本轮现数的，但「appkit 不用 -short 门」这件事一直如此），两份 `-short` 门用例（`appspec_authz_e2e_test.go`、`appspec_timed_test.go`）在 `96582bf` 就已存在——那时还在根 module 的 `internal/codegen` 下，`7c345a9` 才连人带测试搬进 `cli/`。所以「今天没有洞」是既有事实，不是这次改出来的。
      - 验活（三次，各自打中一条判据，跑完复原）：① 往 `pkg/appkit` 塞一条 `-short` 跳过、名字不在任何正则与 workflow 里的探针 → 红在「它现在没有任何执行证据」；② 把一条豁免的键改成不存在的名字 → 同时红两条：「豁免已不再是要豁免的用例，请删掉」+「该用例没有执行证据」，即豁免不会烂在名单里；③ 把一层归因改成 `if false &&` → 候选掉到 2，命中 `candidates<3` 的空转 Fatal，也就是**去掉归因不会让闸门静默变绿**。另有一条空转断言已经在开发中真响过一次：表解析漏掉 `printf` 续行的行尾反斜杠，`len(legs)<6` 当场报「只解析出 1 条判据腿」——防空转的方向是误报红，不是误报绿。
      - 仍未覆盖的那半（如实记）：闸门只判「`-short` 跳过的用例有没有被执行证据接住」，不判「有没有一条**新写的、不跳过的**重要用例该进表而没进」——后者仍靠 `-list` 命中非空（改名/删除会响）加人工加行。
  - 同类残留：这条闸门管的是**形状**，管不了「`dir` 写对了但目录不存在」（那种是响的），也管不了 workflow 文件里的 `working-directory`（GitHub Actions 认它，且已由 `task ci-workflow-lint`/`actionlint` 覆盖）。
- 补上的一类（§3.4 的教训）：**文档给的命令装不装得上**现在能自动失效——`TestDocsInstallsAreReplaceFree` 把围栏里的 `go install <path>@<ver>` 与目标 module 的 `go.mod` 对上（Go 判据 `len(f.Replace) > 0`）。它测的不是描述而是承诺，成本一条断言；四次变异验活见 §3.4。同类缺口还有「围栏里的 `curl`/`bald gen` 参数是否还存在」，那要跑真命令，未做。
- §3.4 落地时顺带查出的一条（**已修**，2026-11-03）：`_example/bald` 的两份生成清单里，只有 `gen app` 的那份跟着声明走。实测证据——`_example/bald/appspec.json` 在 `2d094ca` 改过，`appspec.manifest.json` 的 `spec` 摘要当场归位，而 `domains.manifest.json` 此后 13 个提交一直是旧摘要，直到本轮真的跑了一次 `gen domain` 才补上（差异只有 `"spec"` 那一行，五个域生成物的字节没动）。机制：`checkTree` 逐字节比对清单本身，所以 `gen domain --check` 一定能抓到这行漂移——但**没有任何闸门对 `_example/bald` 跑它**：`TestGen_*`/`TestCheckTree_*`/`TestTree_*`（CI 与 `task acceptance` 都覆盖）用的是临时目录里的合成声明，`task acceptance` 打进真实示例树的只有 `_example/bald` 侧的 `TestPlan_ReferenceProject`，而《Bald 声明式装配设计》里那两条 `--check` 是人工步骤。修法本来看着很便宜（CI 加一步 `gen app --check` 与 `gen domain --check`），代价是这条承诺要先把 `cli` 的 `go run` 接进作业。**实际落地分了两层——域那半边在干净检出里跑不动（2026-11-03 实测）**：两条 `--check` 一起提交后 CI 当场红在域那条，`bindings import proto Go package "github.com/kalandramo/bald/example/bald/api/gen", but its generated Go files are missing; run buf generate … or pass --no-bind`（app 那条同期是 `clean: 21 files`）。原因是 bind 文件的前置是 proto 的 Go 产物，而 `_example/bald/api/gen` 已 gitignore，只有本地跑过 `task proto-example`（要 buf）才存在。`--no-bind` 不是替代：它改期望文件集合，已入库的 bind 与路由文件反被判 `stale`（实测 `out-of-date: domains.manifest.json` + `stale: internal/apiserver/handler/gin/greet.bind.gen.go` + `stale: internal/apiserver/routes/greet.gen.go`，exit 1）。所以域面拆两处——**CI 跑比对**：`cli/internal/codegen/example_manifest_test.go` 的 `TestExampleManifestsMatchSpecAndDisk` 随 `go test ./...` 跑，逐条比对两份清单的 `spec` 摘要与当前 `appspec.json` 的摘要、以及每个 owned 条目的清单摘要与磁盘字节，不吃 proto 产物，抓的正是上面那行潜伏 13 个提交的摘要漂移；**重算级校验本地跑**：`task acceptance-example-domain-check`（dep `proto-example`，需要 buf）。`ci.yml` 的 `test` 作业那步只留 `gen app --check`，`task acceptance` 的腿与 CI 同内容。验活是两轮「只动一侧」的变异：只改声明（`appspec.json` 首个 `desc` 加一个字符）→ 两份清单一起报「清单的 spec 摘要 … 与当前 appspec.json 的 … 不符」；只改生成物字节（`internal/apiserver/authz/alias.gen.go` 尾部加一行）→ 单报「生成物 … 的磁盘摘要 … 与清单记录 … 不符」。复原后 `clean: 21 files` / `clean: 5 files`、测试转绿。`--check` 全程不写文件（变异跑完 `git status` 只有被改的那份），seeded 条目按存在性判定、不比字节——种子之后的内容本就归人改。顺带一条口径陷阱（**2026-11-04 已闭合**）：`task acceptance-example-domain-check` 先跑 `proto-example`，而入库的 `api/buf.lock` 当时还是 v1 形态、`buf.yaml` 已是 v2，本地 buf 会把它改写成 v2——那是工具版本差异，跑完要复原。现在锁文件已按 v2 提交（并因此暴露出 `buf generate` 在 v1 锁 + v2 `buf.yaml` 下**直接拒绝**，旧版靠 `task proto-example` 里的 `buf dep update || true` 盖住），实测重跑 `task proto-example` 对 `api/buf.lock` **零 diff**，这条「跑完要复原」的口头约束随之消失；细节见 §7.1「补：示例的去仓库外依赖」。

---

## 9. 本次评审自身的局限（architecture-health）

| 局限 | 说明 |
|---|---|
| 无运行时证据 | 未接触任何真实部署、日志、trace。所有「如何运行」均来自代码静态阅读，运行时拓扑标 unknown。 |
| 图的校验深度 | 已装工具并真实校验：`module-deps.dot`、`flows.dot` 用 graphviz 16.1.0 `dot -Tsvg` 渲染（退出码 0、stderr 为空）；`bald-c4.dsl` 用官方 `structurizr-dsl` 6.2.4 解析器 parse + `DOTExporter` 导出 3 张视图，导出物再渲染通过。渲染与解析确实抓到了纯结构检查看不见的缺陷：`module-deps.dot` 里 `{ rank=same; retry; cb; }` 与 cluster 冲突（graphviz 报 "already in a rankset, deleted from cluster"）；`bald-c4.dsl` 用了 6.x 已不存在的 `system` 关键字、`workspace` 位置参数多写版本号、container 的第 4 个位置串会被解析成 **tags**（证据文本落错字段），且描述里的裸 `>` 会让导出的 HTML label 语法报错——均已修正。**仍未做的**：无人工判读「图是否画得对」以外的语义，即渲染通过 ≠ 建模正确。 |
| 引用核对方式 | `路径:行号` 由脚本从三份制品中抽出后逐条打开源文件核对（含 `.dot` 中被 `\n` 拆行的标签）。散文结论（如「零 importer」「13 个 importer」）靠全仓 grep 计数。行号会随代码演进漂移，`README.md` 列了重核锚点。 |
| 依赖结论的边界 | §3.2 只解析各 `go.mod` 的 `require` 行，未展开 `go mod graph`，也未验证最终二进制是否同时链接两个 major。 |
| 外部消费方未纳入 | bald-admin 在另一仓库。§2/§3 的「无消费方」仅指 bald 仓库内；若 bald-admin 使用了某能力，结论需下调。 |
| 建议≠授权 | §3.1 的抽叶子 module 是 breaking 动作（涉及 22 个模块的 require 与发版批次），按既定规则需 owner 决策后才动——owner 2026-11-02 选定 C-A（整个 `pkg/` 成 module）并要求跑 `task verify`；代码与文档已在 2026-11-03 以 `96582bf` 进入 main（四道闸门本机全绿，其中 verify/vuln 按 CI 的 go1.27.3 复跑）；**发版批次已按 owner 授权全部发完**（`pkg/v0.1.0` + `9cf17db` 的 30 个模块，外加取代坏根的补救版根 `v0.36.0`），外部装载校验也已收口，见 §3.1 收口。 |

## 10. 如果要动手，按这个顺序

1. **接上 gRPC TLS**（§6.5）——静默明文是本轮最该先修的项，改动集中在 `GrpcServerProvider` + 生成模板 + gateway 出站三处。✅
2. **`bundle` 加未配 Authn 的显式警告/契约开关**（§6.1）——改动小，风险面最高。✅
3. **`_ = deregister` 补日志**（§4.1）——一行。✅（连带 §4.2 的等待上界）
4. **race 清单去人工化**（§1.1）✅：两处手写清单已换成同一条 `find` 派生 + 单份 `.github/race-skip.txt`，webrtc/kafka/contrib 全量入闸，`metrics` 这类死条目随派生自然消失；扩范围当场抓到并修掉 `transport/graphql` 自身的生命周期竞争。本地 `task verify`（§1.2）也已接上同一条派生（`scripts/list-modules.sh`，92 模块实测 exit=0）。
5. **kafka 的 writer 重建 + 订阅退避补测试**（§1.3）✅：`broker/kafka` 与 `broker/rabbitmq` 各有了第一份测试，全部不起真实 broker（死端口造 ECONNREFUSED）。过程中揪出并修掉 4 个生产缺陷，其中「重连后发布 panic + 写锁泄漏」和「`retriesCount=0` 吞掉写失败」都只在失败/热更路径上，原先没有任何执行证据。
6. **给 §2 的未接线面做决策** ✅ 口径已定：reference-only 保留 + README/当前架构 §6 标注；其中 **Discovery 走的是选项 1（接线）而不是标注**——`transport/gateway` 出站现在真消费 `GetService`/`Watch`（见 §2「接线半」）。**circuitbreaker 同样走选项 1**（见 §2「熔断半」：契约段 + 两面中间件 + bundle 链序，opt-in）。剩下两项：`retry` 刻意不进服务入口链（服务端重试会重复执行有副作用的 handler），它的诚实接线处是替换三处手写退避，属重构面；「移出主线发布批次」仍要 owner 单独定（改 `cli/internal/release/repo.go` 等于改变下游能拉到什么）。
7. **`berrors` 边界口径**（§5）✅ 选 (b)：定位为边界投影模型，不改码。**Discovery 消费侧**（§2）已做——watch-based resolver + 装配作用域自动派生来源，消费方只改一行配置。
8. **抽离 appkit 契约面，把适配器从「require 聚合根」解耦**（§3.1）✅ 已落地（owner 选定 C-A：整个 `pkg/` 成 module，非最小面）：require 根的适配器 22→0，`task verify` 全绿。收益按实测收窄——`replace` 数量不降反升（410→423，那句旧表述已撤回），真正消掉的只有「本仓工具的改动不再产出适配器要追的根 tag」；发版批次已随 2026-11-03 的整批 tag 落地。
9. **gRPC 链补限流与超时**（§6.2）✅ unary 面：`RateLimitInterceptor`/`TimeoutInterceptor` + bundle 两侧共用同一对 Option，当场揪出并修掉 `tokenbucket.Close()` 不唤醒 `Wait` 的真缺陷。✅ stream 面：`GRPCStreamInterceptors()` 出 `Error → RateLimit`，`GRPCChain()` 仅在 gRPC 面有限流时附带 stream 选项；按「每流建立一令牌」计费、与 unary 同桶、超时按口径不默认挂。✅ 契约面：`Server.Grpc.Middleware` 补 rate_limit/timeout 两段 + `FromGRPCMiddleware` 桥（gRPC 段产出只作用于 gRPC 的 Option，「更具体的段管更具体的面」）。✅ **装配面（对称收口）**：`appkit.WithHTTPContract`/`WithGRPCOptionsContract` 把「谁在启动时读那两个段」做成装配的固定环节，codegen 两面脚手架对称改形（seeded 的 `NewRouter(mw)` 与 `newGRPCChain(mw)`），角色契约经 `inheritMiddleware` 继承两段；`_example` 起真进程实测 429 / `RESOURCE_EXHAUSTED` / 提前 503 / `DEADLINE_EXCEEDED`，六处删接线逐个转红。接线过程中另修掉一处真缺陷：`grpcerr.ToStatus` 把 `context.DeadlineExceeded` 兜成 `codes.Unknown`（比不装收口拦截器更差——超时看起来不像超时）。**发版批次已落地（2026-11-03，owner 授权后按 `bconf` 先行的次序推送）**：`bconf/v0.17.0`、`berrors/v0.1.2`、`bootstrap/v0.13.6`、`pkg/v0.2.0`、根 `v0.37.0` 五个 tag 全部指向发布提交 `63f2cec`。该提交是 `release sync`（44 个 `go.mod` 的 require 归位）+ `release changelog` 的产物，`chore:` 型因而不改变批次；tag 之前它那一轮 GitHub `ci` 五作业（test/integration/lint/vuln/race）与 `publish-bconf` 实测全 success，且 push 到 tag 之间没有再推任何东西（`concurrency` + `cancel-in-progress` 会把未跑完的 job 取消，绿证就作废）。破面在**根模块的生成模板**——seeded 的 `NewRouter` 签名变了，存量生成工程重跑 `bald gen app` 后编译失败（迁移见 §6.2）；`pkg` 自己的 Go API 是纯新增（`WithHTTPContract`/`WithGRPCOptionsContract`/两个拦截器），所以对已发布 API 的消费者不算破。`bconfig` **不在**批次里：它自 `v0.2.2` 以来只有测试改动（`test(...)` 属 `nonReleasingTypes`，不触发发版）。**代理侧已证**：零 `replace` 的 scratch module 按 `GOPROXY=https://goproxy.cn` 解析全部五个新版本并 `go build` 通过，运行期打出 `FoldEnvKeys` 折回后的 `burst=7`、`grpcerr.ToStatus(context.DeadlineExceeded)` 返回 `DeadlineExceeded`——`bconf` 的新 API 与 `berrors` 的修复确实在 published 形态里；tag 推完立刻探测会拿到 `404 not found: temporarily unavailable`，代理侧要等几分钟才建好条目。**发版说明的破面是手写的**：`v0.36.0..HEAD` 与 `pkg/v0.1.0..HEAD` 两段里没有任何提交带 `!` 或 `BREAKING CHANGE:` 页脚，CHANGELOG 的 **(breaking)** 徽标一个都不会出现，所以在 `63f2cec` 里给根 `v0.37.0` 一节显式补了一段「重跑 `bald gen app` 后 seeded 文件编译失败，迁移三步见《代码生成工具设计》§8」——工具的 bump 等级本身没错（0.x 的破由 minor 承载），漏的是提交标记。
10. **限流链序**（§6.3）✅ 口径已定：保持「认证之后」，落文档；改默认会把按 subject 的配额静默变成全局配额。顺带修掉设计文档里写反的链序。
11. **文档漂移闸门**（§8）✅ 清单半：`module-list` 围栏 + 双向断言 + 与脚本逐行比对（三条变异实测）。「描述已删除 API」半 ✅ 已按一次性 AST 测量收口（2026-11-04）：修掉 12 族假引用、宽松口径对本仓真漂移归零，三条量化理由说明这一半的兜底是「可复跑的一次性脚本 + 人肉 grep」而不是永久闸门（永久判据需要一份会误伤 `log`/`cache`/`app` 的豁免表，且对裸名叙述与别名式引用结构上失明）。
12. **依赖卫生只读报告**（§3.2）✅：`scripts/multi-major.sh` + `task deps-multi-major`，再用 `go list -deps` 把「是否真同链」从推测改成证据；不加 exclude/replace。
13. **CI 集成作业**（§7.1）✅：`integration` 作业注入 etcd/consul/redis/mongo 跑四条套件（10 条判据），另开 `integration-kubernetes` 作业起真 kind 集群跑第三个 registry 后端（3 条判据），两侧同构——先探活、后以「判据必须出现 `--- PASS`」兜住不可达即 Skip 的静默失效；作业文件自身的 schema 闸门另开 `workflow-lint`。远端取证：三件套 run 41413712267、含 consul 的十字判据 run 41782592576（当时六作业全绿）、kubernetes 腿 run 41787082957（七作业全绿，含 `integration-kubernetes` 的起集群/探活/判据三步）——见 §7.1 kubernetes 补条。
14. **覆盖率闸门**（§7.2）✅ 已修（口径：先立不变量再谈比例）：`TestModulesWithLogicHaveTests` 判「有可执行逻辑的 module 必须有测试」，纯声明 module 由 `go/ast` 的带体函数判据自动豁免（实测豁免 `metrics`、`transport/subscribe` 与只剩 `internal/docscheck` 的根 module），两次变异实测（移走 kafka 测试 → 红；把豁免判据改恒假 → 两个契约 module 一起红）。比例阈值刻意未做，理由见 §7.2 收口。**统计量断言的复查**（§7.4）✅：`sres` 并发用例的假红已按「灌满窗口」修掉，阈值一字未动；同类写法已 grep 核过其余四个用随机源的包。**Windows 周界适配**（§7.5）✅：schedule 实测红的三条（产物扩展名 / 清单差异的路径身份 / 检出 CRLF）已修，并用 `.gitattributes` 收口检出形态；dispatch run 41445557462 实测该作业七个 step 全绿，含上次被连坐跳过的 `bootstrap`、`contrib/authz-casbin`、`TestTree_EndToEndTimed`。**Windows 工具链对齐**（§7.5 的「刻意没做 ②」，2026-11-04 撤销）✅：`windows.yml` 的 `go-version` 从 `1.27.1` 升到与主 ci 一致的 `1.27.3`，并新增 `TestWorkflowGoVersionsAgree` 钉住所有作业文件的 pin 一致（两次变异实测）。`integration` 的 Windows 对偶确认做不了——服务容器要求 Linux runner + Docker（见 §7.1「未验证」条）。
15. **env 表达不出契约的多词键**（§7.7）✅ 已修：`bconf` 新增 `FoldEnvKeys`，env 层在**合树之前**按契约描述符把下划线切出的嵌套链折回规范键（`bootstrap/config` 的 `envLayer`，`Load` 与 `MountEnv` 两条挂载路径同走）。严格门禁不再把合法注入判成笔误，env > file 的优先级也不再被文件层的规范键倒压。示例两条 env 姿势的 e2e（HTTP `429` / gRPC `RESOURCE_EXHAUSTED`）是产品级实证；业务自持键因没有描述符可折仍避开下划线。
16. **`TestWatchValue_NoTornEmptySnapshot` 的逐值断言强于 `ValueWatcher` 契约**（§7.8）✅ 已按「断伤害」放宽：删掉「每个中间值都要被派发」，换成 0 次空载荷 / 派发序单调不减 / 末值必为真实终值三条时序无关的不变量；三条变异各自只打中一条判据（补发历史快照那条**只**红回退判据，证明它有独立牙齿）。今后这条再红，先按 §7.8 的机制核对，不要靠重跑消音。
17. **官方 CLI 按 tag 装不出来**（§3.4）✅ 已修（走 B，含发版）：`cmd/bald` + `internal/{codegen,release}` 拆成独立 module `github.com/kalandramo/bald/cli`，`cli/go.mod` 零 `replace`；联合开发姿势取 B-iii（`go.work` 只留本机且入库即红，Taskfile 与 CI 顶层写死 `GOWORK=off`，`verify-modules` 另加一条自检）；docscheck 新增 `TestCliModuleIsReplaceFree` 与 `TestGoWorkIsNotTracked` 两条闸门（各两次变异实测）。`cli/v0.1.0` 与同批的根 `v0.37.1`、`pkg/v0.2.1` 已于 2026-11-03 打出（`d4396185`，tag 前 `ci` 五作业 + 手动 `ci-windows` 全绿），零 `replace` 夹具实装 + 生成骨架起进程 `curl :8080/healthz` 200 两段证据见 §3.4；五处文档已升格为默认按 tag、源码检出降为备选。唯一没本机复跑的是 `git clone` 那半段（github 直连不通），它只影响备选姿势。
18. **`gen domain` 的清单不在闸门口径里**（§8）✅ 已修。`_example/bald/domains.manifest.json` 的 `spec` 摘要潜伏 13 个提交才被发现（`appspec.json` 在 `2d094ca` 改过，`gen app` 那份当场归位，`gen domain` 这份没人管）。闭合方式分两层（域那半边在干净检出跑不动，见 §8 同条）：`ci.yml` 的 `test` 作业只跑 `gen app --check` 那一步，`gen domain` 的清单改由 `TestExampleManifestsMatchSpecAndDisk` 比对（清单 `spec` 摘要 vs 当前声明、owned 条目摘要 vs 磁盘字节），重算级校验留在需要 buf 的 `task acceptance-example-domain-check`；`task acceptance` 与 CI 同内容。变异两轮（只改声明、只改生成物字节）都实测转红再复原。
19. **Taskfile 的 per-command `dir` 会被 go-task 静默忽略**（§8）✅ 已修：13 处这种写法全部改成 `internal: true` 子任务 + 任务级 `dir`；顺带查出 `acceptance` 的五条 appkit 腿自 `pkg/` 拆分起就一直 setup failed、`task build`/`test` 的 cli 腿是「根 module 跑两遍」的假绿。新增 docscheck 闸门 `TestTaskfileNoPerCommandDir`（缩进判据 + 防空转断言，两次变异实测）。~~未闭合：`acceptance` 仍没有 CI 作业调用它~~ **2026-11-04 已闭合**：判据表与执行合进 `scripts/acceptance-gate.sh`，`task acceptance`（scope=all）与 `ci.yml` 的新 `acceptance` 作业（scope=ci）消费同一个脚本；防的正是「`-run` 命中 0 个测试也返回 0」这一类静默绿，两次变异（正则改空、塞一条 Skip 探针）实测转红，见 §8 与下面的 §7.9。
20. **契约产物的字节判据绑 buf 版本**（§7.9）✅ 第二轮收口（2026-11-04）：入库产物由 buf 1.50.0 复现零差异，1.57.2 重跑会给中文注释的 `//` 补一个空格（`bootstrap/v1/log.pb.go`）——于是**没改 proto 也红**，而按新版本重生成会把排版差异当成契约变更写进 `bconf/gen/go`。第一轮只定了判红姿势（改文案，不改判据），第二轮把版本 pin 变成仓内真相 + 机器判据：`bconf/buf.version`（buf 驱动器）与产物头部的 `protoc-gen-go` 版本行（不新建第二个文件），两把都在字节比对之前单独判红，`buf.version` 缺失/为空也是红。顺带查出并修掉一条更糟的坑：`task proto-contract` 原本走 BSR 远端模板，而**远端插件与本机模板不等价**（同版本 v1.36.12，差的就是那一处空格），跑一次产出任务就会污染 `gen/go`；现在产出与校验同走 `buf.gen.local.yaml`，`task proto-contract` 后 `git status` 对 `bconf/gen` 无输出即幂等（实测）。`publish-bconf.yml` 的 `buf-setup-action` 也从 latest 改成读 `bconf/buf.version`（本地 `buf lint`/`buf build` 1.50.0 绿 + `actionlint` 干净；**远端未取证**——该作业只在 bconf tag 上触发，本轮不为此发 tag，详见 §7.9 第二轮收口）。这条腿仍标 `no` 留本地。**第三轮补**：pin 的 `1.50.0` 只钉版本字符串，钉不住来源——`go install` 的源码构建报同号却字节红（官方发行二进制零差异），所以红的时候先用官方发行版复跑再判契约；失败文案已改成给两条可能，判据未弱化。**第四轮撤掉第三轮那句归因**：单变量矩阵证明驱动字节的是**编译 protoc-gen-go 用的那把 Go**（go1.26.5 补空格、go1.27.1/1.27.3 零差异），buf 的版本与来源对字节都中性——第三轮那次「源码构建 buf 字节红」其实是 PATH 里两把插件不同。**第五轮**给这条腿单开 CI 作业 `proto-contract`（按 `bconf/buf.version` + `bconf/buf.sha256` 装官方 buf、要求真 `--- PASS`），远端绿证已取得。**第六轮**把插件的两轴搬进仓内：判据在 bconf module 里现编 protoc-gen-go，版本轴改读 `bconf/go.mod`（protobuf v1.36.11 → v1.36.12，对齐产物头部与仓内多数 pin），口味轴改读那把自编译二进制的 `go version -m`；CI 里「按产物头部 `go install` 插件」那一步随之删除。**这条改造前有个实测复现过的假绿**：main 的 `d9d09c4` 上 go.mod 是 v1.36.11、18 个产物头部是 v1.36.12，旧判据却 `--- PASS`——被比的工具全在仓外。
21. **热更新的「瞬态空载荷」**（§7.6 的固有上限）✅ 按口径收口（2026-11-04，不再挂「未修」）：读侧无法区分「运维清空」与「写了一半」（已实测：8 次泄漏里 3 次立刻读到 4 字节内容，属真实空窗），所以不在消费侧改语义、不把确认窗开大（无效且有代价），而是把「写入方原子发布（同目录临时文件 + `rename`）」写成《Bald 配置源层设计》里 `file` 源的更新姿势。仓内判据仍是 §7.8 那三条时序无关的不变量。
22. **acceptance 判据表的归属判据**（§8）✅ 已造（2026-11-04）：`internal/docscheck` 新增 `TestAcceptanceTableCoversShortSkippedTests`——表内包中「`-short` 下会跳过的顶层测试」必须进表 / 被 CI 作业点名 / 带理由豁免，三选一。先量再判：appkit 269 个用例 0 个用 `-short` 门、codegen 恰好 2 个且 `ci.yml` 那步非 `-short` 点名跑它们，所以**今天没有覆盖洞**，闸门买的是「以后漂了会响」。三层防空转 + 三次变异实测（探针用例、改豁免键、拆一层归因），见 §8。
23. **kubernetes 后端的注销从来没生效过**（§7.1 的 kubernetes 补条查出的生产缺陷）✅ 已修（2026-11-04）：`registry/kubernetes` 的 `Deregister` 实现是「借道 `Register` 传一个只有空 `Metadata` 的实例」，而 `registry.ServiceInstance.Validate` 闸门（`3647d71`，2026-10-08）第一句就拒空 Name——从闸门落地那天起每次调用必然返回 `service instance Name is required`。借道形状来自 `30f2bc5`（2026-09-17 后端下放独立 module）；该提交与闸门落地那次提交里，这个模块**一个测试文件都没有**（`git ls-tree -r --name-only 3647d71 registry/kubernetes` 只有 `contract/`、`go.mod/go.sum`、`options.go`、`registry.go`），所以没有任何东西能响。按 §4.1「反注册失败不再被吞掉」的口径，它现在每次优雅停机都会报出来，而 Pod 仍带着 `bald-service-*` 标签被继续发现。修法：`Deregister` 自己发 strategic merge patch，把三个标签与两份注解一起置 null（值 null 即删键）。回归：`TestDeregister_RemovesPodFromDiscovery`（真 kind 集群；这条在旧实现上必红，即变异 A 实测的红消息就是缺陷症状本身），CI 由新作业 `integration-kubernetes` 点名。
24. **示例的仓库外 `replace` + 一处静默空读的配置源**（§7.1「补：示例的去仓库外依赖」）✅ 已修（2026-11-04）：`_example/bald` 的 nacos 配置源从「kratos contrib 桥 + 仓库外 `replace`」改成仓内 `bconfig/nacos` + `appkit.ConfigLayers`，两条 `../../../kratos/...` 与 nacos-sdk-go **v1** 的 require 一起消失；在**新 worktree 的干净检出**里 `go mod download` exit 0、`go list -m all` 里 contrib 条目 0 条，也就是 §7.1「刻意没做 ①」的前提被拆掉（~~示例仍不进 CI，因为 `api/gen` 是 gitignore 的 proto 产物、且要真 etcd/真 nacos~~ —— 同一天这两条阻塞由 `example` 作业接住：按 pin 装 buf 现生成产物、`services:` 注入 etcd + nacos，见「7.1 补：示例 module 进 CI」）。真机五件（nacos-server v2.3.2）：配置进应用、热更不重启（约 6s）、无 tag 构建不受影响、env 压在源层之上、SIGTERM 干净停机。当场查出并修掉一条**生产缺陷**：`bconfig/nacos` 把 `namespace` 原样塞进 `NamespaceId`，而 nacos 默认命名空间在 `tenant` 参数里必须是空串——`"public"` 时 `GetConfig` 返回空内容**且不报错**（源接上、启动不报错、值一个没进来）；修法是 `namespaceID` 折叠 + 两条钉住测试，删折叠即红。这条缺陷原先被示例的绿色掩盖：两个端到端断言的 `hello` 恰是「远端贡献零键」的产物——即「跑过 ≠ 跑对」。折叠落地后转红的三条各按根因处理（变体激活断言改走 `appkit.EvaluateBuildConstraint`、两条 e2e 把业务值钉在 env 层而断言文案一字不改），四条变异实测后精确复原，四种 tag 组合全 `ok`。顺带闭合 §8 那条 `api/buf.lock` v1/v2 的口头约束：入库锁改成 v2 后 `task proto-example` 对锁文件零 diff，而 v1 锁 + v2 `buf.yaml` 会让 `buf generate` 直接拒绝（旧版靠 `buf dep update || true` 盖住）。
25. **`-tags nacos` 的注册中心接管是死的**（§7.10）✅ 已修（2026-11-04 同日）：示例 `Options()` 里**无条件**钉 `appkit.Registrar(inmemory.New())`，于是 `registry` 段被判「已消费」、契约驱动的 `nacosRegistrarProvider` 在计划期跳过——三条独立证据：doctor 的 `programmatic-takeover [registry]` 告警、真 nacos `service/list` 返回 `{"count":0,"doms":[]}`、以及 `custom.go` 那句钉住本身。框架语义是对的，自我矛盾的是示例声明。先不当场修的理由是失败面隔离（与上面那条配置源迁移并一次提交，红了分不清坏在哪侧），所以它单独成批。动手前的必经一步——**命名 API 是否也要 `public`→空串折叠**——实测答案是**不要**：nacos 的命名侧 `public` 与 `""` 是同一个租户（SDK ephemeral 注册双向可见，`service/list` 两种 `namespaceId` 返回同一份列表；SDK 自己就把空 `NamespaceId` 归一成 `DEFAULT_NAMESPACE_ID`），与配置 API 那个静默空读**正好相反**，结论写进 `registry/nacos/registry.go` 的 `New` 注释。接线：钉住拆成 `registrar_inmemory.go`（`!nacos`）与 `register_nacos.go` 里返回 `nil` 的同名函数（`nacos` 变体不覆盖，段驱动 Provider 才生效）；端到端辅助把「钉 / 不钉」做成 `startRoleWithOptions(..., extra)` 一条腿，`extra=nil` 才是「按本构建形态实际装配」。证据：`TestNacosAssemblyRegistersIntoBackend`（`//go:build nacos`，真 nacos）按 v1 HTTP `instance/list`（独立于装配用的 SDK）断 `<名>.grpc` / `<名>.http` 各 1 个实例、地址就是本进程监听端口、`healthy`/`enabled`/`ephemeral` 三真，停机后归零；计划门的 opaque 清单改成按变体激活派生，另加一条双向判据（takeover 告警在默认构建必须在、在 nacos 变体必须没有），实测两种形态分别是 `[registry server.grpc server.http]` 与 `[server.grpc server.http]`。变异一次三红（装配用例 0 实例 / opaque 清单 / takeover 告警），`cp` 备份后精确复原。连带修：`TestGatewayBackendViaDiscovery` 原先**依赖**那句无条件钉住，变体生效后转红（同一次装配接真 nacos，而 `discovery:///bald-demo` 是裸名，nacos 要求 `<名>.grpc`）——不是回归，是隐含前提现形，改成用例自己钉内存实例。仍未闭合：~~`-tags nacos` 不是 CI 的构建形态，所以装配那条**没有远端绿证**，只有本地两种形态全绿 + 变异三红~~ **同日 `example` 作业补上**：`-tags nacos` 现在是 CI 的第三种构建形态（单条装配），远端 `--- PASS (0.97s)`（run 41832136892 / job 126581704475），见「7.1 补：示例 module 进 CI」与 §7.13。两条**机制**判据随后从示例 module 搬到 `registry/nacos/contract`（不依赖 proto 产物）并由 `integration` 作业的真 nacos 判，示例侧的 `shortSkipExemptions` 随之删除——见下面第三轮。
26. **示例 module 在远端零证据**（§7.1「刻意没做 ①」的最后一半）✅ 已落地（2026-11-04）：新增 `example` 作业——etcd `v3.5.14` + `nacos-server:v2.3.2` 服务容器（8848/9848 都探活，9848 那条是唯一能看出「半死的 nacos」的线索）、`scripts/install-buf.sh` 按 `bconf/buf.version` + `bconf/buf.sha256` 装 pin 的 buf（与 `proto-contract` 同源，不再各装一份）、`buf generate` 出 gitignore 的 `api/gen` 并用 `git diff --quiet -- _example/bald/api` 判「生成不该动受版本管理的文件」、三态构建（默认 / `-tags grpcgw` / `-tags nacos` 单条装配）留档后按 `EXPECTED` 24 行逐条断言真 `--- PASS`。docscheck 侧的耦合同步收：那几条示例豁免条目因「在 `ci.yml` 里被点名」而变成过期豁免，`shortSkipExemptions` 清成空 map（**空本身是判据**）。本机正向 24/24 + 三道变异各红（改坏判据名 / 删日志 / 注释写进 `EXPECTED` 字符串），actionlint 干净；重放 `EXPECTED` 循环必须用 **bash**（zsh 不拆词会跑成假绿）。**远端取证**：main head `6d7eda7` 的 run 41834825763 九个作业全 success，`example` 作业（job 126590928265）日志 24 行 `PASS  <名>` 全数命中（含 `-tags nacos` 那条装配 0.77s），与分支上那次（run 41832136892）同形。顺带查出并修掉装配用例自己的一条**断言竞态**：终态守卫对 `<app>.grpc` 轮询到零、对 `<app>.http` 只查一次，而 nacos 把 `<服务名>.<scheme>` 存成两个独立服务、摘除时刻各自独立 ⇒ 单条跑 8 次红 1；两条都轮询后 12/12 绿，牙齿用 `stop()` → `_ = stop` 重证（红在 15.52s）。整包/并集跑 `-tags nacos` 的红是另一件事，登记 §7.13。
27. **同进程密集的配置查询撞 nacos SDK 进程内令牌桶 ⇒ 配置层 `Load` 判错**（§7.13）✅ 已修（2026-11-04）：`bconfig/nacos.Config.Load` 改成带上界的重试（4 次 × 250ms），间隔取最坏观测回填 220ms 的余量，错误文案同时带上界与候选真因。归因在这条上错过两次，最后是**客户端**：v2.3.5 `clients/config_client/limiter.go:42-56` 的包级 `rate.NewLimiter(5, 5)`，按 `dataId@@group@@tenant` 分键、同进程所有 `ConfigClient` 共用，判限时**一个包都不发**（三格无网络/零往返实测钉住，详见 §7.13 的「归因史」）。判据四条用假 `config_client.IConfigClient`（首错后成 / 首次即成 / 上界放弃且文案带原因 / ctx 取消不进重试），各配变异。真正的牙齿是同键并发 10 次 `Load` 的 A/B：`HEAD` 冷启动轮 10 个里 5 个判错，当前树三轮 0 失败（最坏 362ms）。**注意**：修完后示例整包在本地不再复现红（9 次 `Load` 全在同键，最密 300ms 里 5 次，正卡在桶容量内），所以「套件绿了」不是这条修复的证据。未修的第二面：SDK 在有快照时把限流**静默成「配置没变」**（读快照 + `err == nil`），唯一开关 `DisableUseSnapShot` 是删离线快照能力，owner-gated，本轮未动。
