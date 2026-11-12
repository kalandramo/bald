# 架构债修正排期（ADR-1…18）

> 来源：2026-11-12 架构评审（挑剔视角，内部工作稿——发现已全部结构化至此，
> 临时稿已销毁）。口径：只录问题与修正，不复述评审修辞；每条带证据与批次。
> 状态：⏳ 待做 / 🔧 进行中 / ✅ 已修（带提交）/ 📌 立项（超出本轮，挂账）。
> 批次：**批 1**＝低风险高确定性，随手清；**批 2**＝需契约/流程演进；**批 3**＝大项
> 立项（多人日）；**批 4**＝文档批次。

## 批 1（低风险，随手清）

### ADR-1 ✅ `adaptCleanup` 逐字复制 25 份（J2 现行违反）——已修（CleanupFrom + 25 包 rewired，35 模块 build/docscheck/lint/race 过）

证据：`grep -rln "func adaptCleanup" --include="*.go" . | wc -l` = 25（metrics/*/contract、
broker/redis/contract、transport/{websocket,sse,redis,rabbitmq,…}/contract 全中）。
修正：appkit 导出 `CleanupFrom(cleanup func()) appkit.Cleanup`（nil 透传），25 处改引用
后逐包删本地定义。代价：0.5 人日（机械，-race + lint 全量过即可）。

### ADR-2 ✅ broker API 同名不同形——已修（同名选项删除：零真实使用，链式方法为唯一构造面）（选项 `WithMetadata(Metadata)` vs 方法 `WithMetadata(key,value)`）

证据：`broker/message.go:310`（选项）与 `:103`（方法）；`WithKey` 同样一对。修正：
选项改语义名 `WithMetadataMap`/`WithKeyOf`（旧名 Deprecated 保兼容一个 minor），
文档标 fluent 链为主构造面。代价：0.5 人日。

### ADR-3 ✅ `providerHints` 静态表与派生机制并存——已修（表收敛为例外表）

证据：`pkg/appkit/segment_coverage.go`——表里大半条目可由 `path → path.Replace(".","/")+"/contract"`
派生，两套机制做一件事。修正：表只留**例外**（database.sql → contrib/...、cache/oss/metrics
等非规则路径），规则路径全部走派生。代价：0.25 人日。

### ADR-4 ✅ `bridge_gin.go` 200 项享元 error map 过度设计——已修（sync.Map 按命中惰性缓存，享元语义不变）

证据：400–599 全量预构造，实测热路径只命中 5–6 个状态码，且分配本已对象池化。
修正：改 `sync.Map` 按需构造 + 命中缓存（首次分配后复用），删预构造循环与三层
fallback。测试钉住的行为不变（同 error 实例复用语义保留）。代价：0.5 人日。

### ADR-5 ✅ 报错文案中英混排——已修（hintSuffix 统一中文）

证据：`hintSuffix` 输出 "— framework contract package: … (wire its AppProvider in
customOptions)" 夹在中文报错流里。修正：错误文案统一语言（与所在错误流的主体一致）。
代价：随手。

### ADR-18 ✅ 命名风格三套并存——release 侧统一 Check*（CheckOrphanBumps），appkit 侧不动

证据：`guardErrs` / `CheckPlanFresh` / `guardOrphanBumps`。修正：release 包内统一
`Check*`（返回 error 的谓词）；appkit 侧不动（改动面大收益小）。代价：随手。

## 批 2（契约 / 流程演进）

### ADR-6 ✅（近期段已修）契约面 33 个、实现约 19 个（J8 自我违反）——Gate2 已实现面白名单落地（实测 mqtt 声明被拒），远期 proto 裁剪随大版本

证据：`server.proto` 33 个 optional 面；transport/ 实际约 19。kcp/thrift/trpc/signalr/
socketio/mqtt/pulsar/activemq/azuresb/nsq/sqs/nats/machinery/hptimer/udp/webtransport
契约有、实现无——用户声明后 Gate2 放行、gen 庆祝、启动期才报丧。
修正（两段）：**近期**＝Gate1 增加已实现面白名单，未实现段的声明生成期即报
（错误文案点名"契约保留、实现未落地"，不误导）；**远期**＝proto 裁剪到与实现对齐
（reserved 不复用，bconf-r02）——breaking，留给下一个大版本。代价：近期 1 人日 /
远期 0.5 人日 + 发版一轮。

### ADR-7 ✅ `coverage.sh` 的 awk 合并器（getline 文件读取器）——已修（tools/coverage-merge，同输入对照一致，CLI 接口不变）

证据：bash 内实现带缓存的逐行读源文件（is_excl），三模式交织。修正：抽 Go 工具
（tools/coverage-merge），可测试、docscheck 同款基建。代价：0.5–1 人日。挂账。

### ADR-8 📌 `pkg/appkit` 引擎单体（bootstrap.go 1022 / appkit.go 980 / 41 文件）

修正：四域拆分（装配计划 / 运行引擎 / 段覆盖 / 诊断），从 segment_coverage 与 doctor
出血最少处起步。代价：3–5 人日。立项。

### ADR-9 📌 Taskfile（555 行）与 CI 双源

证据：race 门禁需要"同一条 find 派生"的对齐代码——双源本身是结构错误。修正：
Taskfile 定位为开发期快捷键（薄壳调 scripts/），门禁事实单源化到 scripts/ + CI。
代价：1–2 人日。立项。

### ADR-10 ✅ contrib / 一级模块边界判据已成文（《多模块发布与变更日志》），存量不一致留大版本归位

证据：store-gorm vs database/gorm/contract、registry/nacos（一级）vs config-nacos
（contrib）、oss（一级）vs audit（contrib）——三次"顺手放"后路径即历史。修正：
《多模块发布与变更日志》或独立卡补一节判据（**有独立外部依赖 → 一级；纯适配本仓
契约 → contrib**），现存不一致列清单分批归位。代价：文档 0.5 人日 + 归位另计。

### ADR-11 ✅ transport 准入标准已成文（同上文档）

证据：93 个 go.mod 的代价已付（DX-11 版本矩阵），"什么面值得独立"无成文标准；
`transport/redis` 与 `cache/redis` 两个 redis 生态位并存。修正：同 ADR-10 的卡补
准入（**有独立外部 SDK 依赖且面间零内聚 → 独立；否则并入相邻 module**）。代价：
文档 0.5 人日。

## 批 3 / 批 4（文档批次，多数与 DX 文档联动）

### ADR-12 ⏳ guide 密度自嗨（"实测……"式考证散布 17 篇）

DX-21 只修了安装节。修正：guide 全量走查一次（正文命令流 + 考证进 details）。1 人日。

### ADR-13 ✅ 记录纪律已成文（risk-review 唯一事实登记处 + 卡引条目号），随下次大改动执行（risk-review / 蒸馏卡 / README 修订史 / handoff）

修正：立"真相源 + 引用"纪律——判据卡引 risk-review 条目号，README 修订史只留
一行链接，handoff 只存活一期。随下一次大改动执行。0.5 人日。

### ADR-14 ✅ 命名口径已成文（guide 术语表钉死：蒸馏卡 / contract Provider），残留变体随文档自然迭代（蒸馏卡/规范卡/convention 卡；段 Provider/contract Provider）

修正：DX-22 术语表扩到全仓口径，docs 层 sed 统一（devel/architecture 一次批量）。
0.5 人日。

### ADR-15 ⏳ docs/devel 43 篇约 1/3 是设计过程叙事

修正：过程性内容（方案 A/B 对比、取证）下沉 architecture 层或删；devel 收敛为
"现在的系统"。3 人日，立项。

### ADR-16 ✅ 小刺清单一并修——RedisKey 已导出（消费者不再手写魔法串）；其余两条随批次 4 / 大版本

- `broker.NewKey[Broker]("broker.redis.redis")` 双段键：contract 包导出
  `RedisKey = appkit.NewKey[broker.Broker]("broker.redis.redis")` 常量（消费者不再
  手写魔法串）。0.25 人日。
- `appkit.BeforeStart/AfterStart` 与 `Domain.Wire` 共存债：迁移期语义已在 Domain
  文档声明，随批次 4（旧链移除）一并裁决。
- `pkg/` 模块无名之实：改名是全仓 import 地震，**永久挂账不修**（代价 >> 收益），
  判据卡里加一行"pkg 是历史名，新聚合勿再入内"。

> **race 挂死破案（2026-11-12，已修）**：mcp/tcp/webrtc 整包 -race 超时的根因是
> "blocks on signal" 型手工测试只有 `testing.Short()` 单门——非 -short 全量跑必撞
> 常驻监听。已升级双门（`BALD_MANUAL_TESTS=1` 才跑），三包 -race 全绿。
>
> **批 2/4 实施记录（2026-11-12）**：ADR-6 近期段、ADR-10/11/13/14、ADR-16、ADR-18
> 全部落地。**保持立项**：ADR-7（coverage.sh Go 化）、ADR-8（appkit 四域拆分）、
> ADR-9（Taskfile-CI 双源）、ADR-15（devel 过程叙事下沉）——多人日大项，独立会话
> 逐个做；ADR-6 远期段（proto reserved 裁剪）随大版本。
>
> **批 1 实施记录（2026-11-11/12）**：五项全部落地。附带发现：transport/mcp、tcp、webrtc
> 整包 -race 超时挂死（TestServer 类在 `httpSrv.Serve` goroutine 上等待，**非 race 下
> 也挂**）——与批 1 的等价替换无关（mcp 的 diff 仅删残留定义与调用点换名），归属
> transport 侧并行批次的引入，**单独排查**（见 memory）。

## 实施顺序

批 1 全部（本会话）→ 批 2 的 ADR-6 近期段（本会话）→ ADR-10/11 判据成文（本会话）
→ 批 3/4 与 📌 项挂账给后续会话；ADR-6 远期段与 ADR-16 的 pkg 改名随大版本。
