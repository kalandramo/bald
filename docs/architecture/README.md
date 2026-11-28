# docs/architecture — 架构制品索引

## 按问题找文档（先读这里）

| 你关心的事 | 先读 |
|---|---|
| 这个仓库现在是什么结构 | [current-architecture.md](current-architecture.md) |
| 哪里有风险、消解条件与动手顺序 | [risk-review.md](risk-review.md) |
| 最新一轮整体架构评审发现了哪些新缺陷 | [architecture-review-20261128.md](architecture-review-20261128.md)（增量评审 N-1…N-6：新登 1 条结构性缺陷 + 执行登记存量核销；N-1 附冻结规格）；上一轮 [architecture-review-20261118.md](architecture-review-20261118.md)（AR-1…31，已全量回填） |
| 请求/消息/生命周期怎么流 | [flows.md](flows.md) |
| bald 与 onexai（AI 化）的差距与落地设计 | [onexai-gap-review.md](onexai-gap-review.md) §4 状态总览、§7 CLI 缺口复核 |
| onexai 用起来是什么样、bald CLI 还缺什么 | [onexai-harness-walkthrough.md](onexai-harness-walkthrough.md)（走查原文）→ [onexai-gap-review.md](onexai-gap-review.md) §7 |
| onexmesh 移植了什么、裁决理由 | [onexmesh-comparison.md](onexmesh-comparison.md) §6 完成度 |
| 传输统一（方案 S）的取舍依据 | [transport-unification-assessment.md](transport-unification-assessment.md) |
| 框架哪里对用户不友好、快赢排期 | [developer-experience-review.md](developer-experience-review.md) |
| 历史会话成果与避坑清单 | [handoff-session-20261108.md](handoff-session-20261108.md) §2 |

生成日期 **2026-11-01**（`main` @ `ca981b1`），由架构可视化插件的 `system-modeler` / `flow-visualizer` / `risk-quality-reviewer` 三条场景技能产出，格式源使用 `c4model`（Structurizr DSL）与 `graphviz`（DOT）。历次重核的修订注记收敛在文末「修订记录」，不占导航位。

| 文件 | 回答的问题 | 状态 |
|---|---|---|
| `current-architecture.md` | 这个仓库现在是什么结构？分层、SPI、装配主干、模块拓扑 | 现状，引用已逐条核对 |
| `bald-c4.dsl` | C4 landscape / 服务上下文 / 模块容器视图源 | **已机器校验**：`structurizr-dsl` 6.2.4 解析通过，3 张视图导出 + graphviz 渲染均无告警 |
| `module-deps.dot` | 95 个 `go.mod` 的依赖分层，跨层回边高亮 | **已渲染**：`dot -Tsvg` 退出码 0、stderr 为空（32 节点 / 52 边；回边目标已由聚合根改为 `pkg` module） |
| `flows.md` | 请求/消息/生命周期/发现/缓存/可观测/WebRTC 怎么流；哪些真接线 | 现状，引用已逐条核对 |
| `flows.dot` | 上述三条主流程的图源 | **已渲染**：`dot -Tsvg` 退出码 0、stderr 为空（40 节点 / 39 边） |
| `risk-review.md` | 哪里有风险、严重度、消解条件、动手顺序 | 含自评局限一节（§9） |
| `onexai-gap-review.md` | 对照 OneX Harness AI Coding（onexai）的六层依赖清单，bald 有什么、缺什么；含规范蒸馏卡目录（§5）与 `bald convention` CLI + cobramcp 挂载落地设计（§6）、实战走查复核的 CLI 缺口清单（§7） | 2026-11-07 首版；P0 与 P1-4/5 已落地（§4 状态总览），P1-6…8 与 P2 未做；§7 复核于 2026-11-15 |
| `onex-ai.md` | onexai 工具输出的逐字存档（46 convention 描述 / overview 完整输出 / 20 create 命令），补 PDF 文本解析的截断 | 源材料，保持原样不做编辑 |
| `onexai-harness-walkthrough.md` | onexai 实战走查的逐字存档：一条命令起 121 文件骨架、加资源 9+2 文件、`ide init` 接 Claude Code、`convention check` 的输出与退出码、三处失手点 | 源材料，仅做 HTML→Markdown 格式转换；抓取于 2026-11-15 |
| `onexmesh-comparison.md` | 对照 `../onexmesh`（运行时微服务框架）的设计盘点与路线分歧分析：三条深层观察、双向借鉴清单、关系判断、移植评估（§6 决策表） | 2026-11-08 首版；三批移植全部落地（§6 完成度已回填） |
| `transport-unification-assessment.md` | 传输统一抽象重构评估：痛点拆解（handler/错误已统一，真缺口是中间件双链/手写路由双轨/无路由级中间件）、S/M/L 三档方案与代价、四批迁移路径 | 2026-11-08 首版；**方案 S 已全部落地**（§3 实施进展），旧链保留至下一 major |
| `handoff-session-20261108.md` | 会话成果记录（onexmesh 吸收 × 方案 S 批次 1–3、九 tag）+ 避坑清单（网络工具链/契约生成/运行时陷阱/断言口径） | 交接已闭环（2026-11-10）；时效性章节（阻塞/待办/闸门现状）已移除，避坑清单长期有效 |
| `developer-experience-review.md` | 用户视角的框架体检：四类用户旅程（新手/业务开发/维护者/AI 会话）的摩擦点清单（DX-1…17，实证采集），含快赢清单（7 项按性价比排序） | 2026-11-11 首版；留待优化，修复时逐条勾销 |
| `architecture-review-20261118.md` | 全仓五切片并行评审（`main` @ `984a9655`）：31 条新缺陷（高 11 / 中 13 / 低 7），按已复现 / 读码确证 / 疑似分级，已对既有登记册去重，附修复批次与排除清单 | 2026-11-18 首版；2026-11-26 状态已回填（31 条全部有修复提交，AR-13 范围关闭、AR-9 留有契约 tls 残余，详见文首「状态回填」） |
| `architecture-review-20261128.md` | 增量评审（`main` @ `058f4756`，参照消费者 `bald-admin`）：新登 N-1 http/grpc 装配缝双轨与 bootstrap→transport 倒挂（附冻结收敛规格）；执行 N-2 `_example` logger 示范收口、N-3 Bind 注释失真修正、N-4 cobramcp 双引擎遍历收敛；核销 N-5 cli 原子写、降级 N-6 bconf 双折叠 | 2026-11-28 首版；N-1 已随 dx-flatten S3 执行（风险登记 #56），N-7 登记冻结归 bconf 窗口；S4 收口见风险登记 #57（2026-11-29） |
| `render/*.svg` | 上面两份 `.dot` 与 `.dsl` 三张视图的渲染快照 | 由 graphviz 16.1.0 生成，改源后需重跑（见「校验工具与命令」） |

> 「引用已逐条核对」= 用脚本把制品里的每个 `路径:行号` 抽出来（行内省略文件名的简写按就近文件解析），逐条打开源文件回读该行内容并人工比对叙述；不是抽样。最新一次重跑（§3.1 收口后，脚本原始输出）：**`checked cites: 324, problems: 12, bare-resolved: 65, bare-unresolved: 0`**（比上一轮少一条引用：评审快照里那条 go.mod 的行号在两轮改动后已不对应任何事实，改成了不带行号的快照描述）。
> 这 12 条不是本轮引入：与拆分前的 `main` 逐条比对，同一集合：多包同名的裸文件名（测试文件、中间件里的超时与限流实现）与只写了裸文件名的 workflow 作业文件，本轮新增 0 条；
> 上面那句 `problems: 0` 是 §7.5 Windows 那轮之前的状态，之后写进风险登记册的若干裸文件名引用把它带成了 12，此前没人重跑这条校验。
> **2026-11-29 扁平化后重跑**（dx-flatten S4 收口，openjdk@21 已装、c4 三视图重渲染入库）：现状文档 `current-architecture.md` 与 `flows.md` 的全部旧拓扑引用已清账（34→0 条），脚本输出 **`checked cites: 335, problems: 292, bare-resolved: 59, bare-unresolved: 22`**——292 条全部位于 `risk-review.md` 历史账目（引用已删除文件与已退役后端属当时事实，按「账目只追加不改写」原则保留）。同轮修了脚本一处误报：shell 命令里的 glob 与测试过滤参数（`grep -v '*_test.go'` 一类）不再被当作引用（lookbehind 加 `*` 排除；本仓文档命令里的裸测试过滤也已改成不带后缀的 `_test` 形态）。
>
> 这个检查的边界：它只能证明「被写成引用的那些行号」对得上，**不能证明叙述本身正确**——没有行号的定性判断（如「零 importer」「性能更好」）仍需人工复核。同理，渲染/解析通过只证明图源语法与视图装配合法，不证明建模判断正确。

## 校验工具与命令

`tools/` 下是三份可复跑的校验器（本轮真实执行过，命令与结论见上表）：

```bash
# 1) 引用回读：抽每个 路径:行号 并打开源文件比对（在仓库根执行）
python3 docs/architecture/tools/check_cites.py            # 末尾打印 checked/problems
# 2) DOT 结构自检（括号配平、边两端均已声明）
python3 docs/architecture/tools/check_dot.py docs/architecture/module-deps.dot docs/architecture/flows.dot
# 3) 真实渲染（需 graphviz），产物入库到 render/
dot -Tsvg docs/architecture/module-deps.dot -o docs/architecture/render/module-deps.svg
dot -Tsvg docs/architecture/flows.dot -o docs/architecture/render/flows.svg
# 4) C4 DSL 解析 + 视图导出（需 JDK 21 + Maven；structurizr-cli 不在 Maven Central，故用官方库）
mvn -f docs/architecture/tools/dsl-validate/pom.xml \
    org.apache.maven.plugins:maven-dependency-plugin:3.7.1:copy-dependencies -DoutputDirectory=libs
javac -cp "docs/architecture/tools/dsl-validate/libs/*" -d /tmp/dslclasses \
    docs/architecture/tools/dsl-validate/Validate.java
java -cp "/tmp/dslclasses:docs/architecture/tools/dsl-validate/libs/*" Validate \
    docs/architecture/bald-c4.dsl /tmp/c4out   # 打印 PARSE OK / 视图清单 / VALIDATION: n problems
dot -Tsvg /tmp/c4out/landscape.dot -o docs/architecture/render/c4-landscape.svg
dot -Tsvg /tmp/c4out/services_view.dot -o docs/architecture/render/c4-services_view.svg
dot -Tsvg /tmp/c4out/modules_view.dot -o docs/architecture/render/c4-modules_view.svg
```

`render/` 是上面这些命令的渲染快照（5 张矢量 SVG），入库是为了不装 graphviz 也能看图；改了 `.dot` / `.dsl` 之后请重跑 3)、4) 一并更新，否则快照会与源不一致。

> **4) 的前置条件本轮缺失**：本机 `java` 与 `mvn` 均不在 PATH（`/usr/libexec/java_home` 无可用 JDK，Homebrew 无 temurin/openjdk/maven），
> 因此 `bald-c4.dsl` 沿用上一轮「`structurizr-dsl` 6.2.4 解析通过、VALIDATION 0 problems」的结论——该结论仍成立的前提是**本轮没有改动 `.dsl`**（确实未改）。
> jar 依赖仍缓存在 `tools/dsl-validate/libs/`，装回 JDK 21 即可复跑。唯一待精确化的 `.dsl` 措辞：`contrib` 容器描述里的 `database` 其实无独立 `go.mod`（属根 module）。

本轮实测环境：graphviz 16.1.0、Temurin JDK 21.0.13+9、Maven 3.9.16、`structurizr-dsl`/`structurizr-export` 6.2.4。

## 重新生成

三份 `.md` 是人工整理的证据结论，不自动重生成。两个 `.dot` 与 `.dsl` 是文本源，改动后按上一节「校验工具与命令」重跑 1)–4)。

行号引用会随代码演进漂移。核对锚点：`pkg/appkit/appkit.go`（Run/stopAll/waitForEndpoints）、`pkg/middleware/bundle/bundle.go`（Gin/GRPCInterceptors 链序）、`transport/server.go:14-22`、`broker/broker.go:9-33`、`.github/workflows/ci.yml`、`Taskfile.yml`。

## 已知未覆盖

- 运行时/部署拓扑（仓内无 IaC，无 trace/日志样本）
- bald-admin 侧的消费真实情况（另一仓库）
- `transport/mcp` + `cobramcp` 的工具调用回合、`workflow/argo`、`transport/graphql`（仓内零消费者）

## 修订记录

> 索引入口只留「哪份文档回答什么问题」与校验命令。历次重核的过程性注记归档于此，
> 避免把导航位挤掉（DX-18）。结论性状态以各文档文首的「状态回填/完成度」节为准。

- **2026-11-01**：首版，`main` @ `ca981b1`，三条场景技能产出。
- **2026-11-02 修订**：§3.1 的模块环已断（删掉 `bootstrap`/`pkg/appkit` 里与 `transport/gateway/contract` 重复的转码面机制），三份制品的拓扑叙述、`render/` 快照与全部行号引用已按新代码重核；`bald-c4.dsl` 重新机器校验（0 problems）。
- **2026-11-02 二次重核**（`main` @ `5f771e0`，清掉 16 个模块里陈旧的 `transport/gateway // indirect`）：三份 `.md` 的断言逐条回读代码后仍成立——22 个非示例模块直接 require 根 module、93 个 `go.mod`、30 个 `*/contract`、18 个 `transport/*`、多 major 分组（21/20/3）与「内部依赖图是 DAG」均实测复现（独立脚本 0 环）。`module-deps.dot` 补上根扇入：`oss/{s3,minio}` 与 `contrib → 根 module` 回边，图上 22 条根回边与 `current-architecture.md` §4 一致；版本串 `v0.34.0` 更正为 `v0.34.1`，快照重渲染。
- **2026-11-02 三次重核**（§1.1 race 门禁去人工化）：CI race 作业与 `task race` 改成同一条 `find` 派生 + 单份 `.github/race-skip.txt`；全量 `-race` 90 通过 / 2 失败——`transport/graphql` 的真实竞争（`Start` 写、`Stop`/`Endpoint` 读无同步）已修，`log/aliyun` 上游 SDK 竞争进排除清单。行号重核 `checked cites: 317, problems: 0`。
- **2026-11-02 四次重核**（§1.3 补测试）：`broker/kafka`、`broker/rabbitmq` 不再是零测试模块，补测试揪出并修掉 4 个生产缺陷（nil map 复位、重建吞写失败、`closeLocked` 事后改写 `done`、`run()` 无锁读并发），`risk-review.md` §1.3 逐条记了修法。`flows.md` §④ / `flows.dot` 行号重核并重渲染，`check_cites.py` 输出 `checked cites: 335, problems: 0, bare-resolved: 68`。
- **2026-11-02 五次重核**（§3.1 收口：`pkg/` 独立成 module）：`current-architecture.md` §2/§4 枢纽叙述改指 `pkg`，`module-deps.dot` 22 条红边重画，`observability/metricshttp` 旧路径更新为 `pkg/metricshttp`。按 `git archive main` 与工作树各跑一遍 Tarjan：require 根适配器 22→0、`replace` 410→423（**不降反升，当年「收益是 replace 数量下降」已撤回**），记在 `risk-review.md` §3.1「验活」。
- **2026-11-06 增量**（声明式装配方向②前两批）：`*/contract` 30→**39**（第一批 `transport/{tcp,websocket,http3}`、第二批 `transport/{graphql,webrtc,mcp}`，判据见 `risk-review.md` §7.28）。**未重渲染**：`module-deps.dot` 的「回边（30 个子包）」与 `bald-c4.dsl` / `render/*.svg` 同批未动；零散改标签会造出「数字新、边旧」的第三种不一致，要整轮重核。
- **2026-11-10 梳理**：`handoff-session-20261108.md` 交接闭环（时效章节移除、避坑清单保留）；`onexmesh-comparison.md` §6/§6.3 ⏳ 项回填 ✅；`transport-unification-assessment.md` 头部加完成态；`onexai-gap-review.md` §4 增状态总览。`onex-ai.md` 逐字存档不动。
- **2026-11-26**：DX-18 后半落地，索引表与修订史分离；各文档状态回填见文首「状态回填」节。
- **2026-11-28 增量（S2 拓扑扁平化落地）**：`scripts/list-modules.sh` 派生的发布单元 70→**4**（`.` / `bconf` / `cobramcp` / `cli`；`_example/bald` 与 `cobramcp/examples/*` 分别为独立消费者/已并入 cobramcp，不在计数内）。模块级拓扑收敛为 `cli → {bconf, cobramcp}`、`cobramcp → 聚合根`、`聚合根 → bconf` 四条边（判据与实施记录见 `tasks/dx-flatten-design.md` 与评审 N-1/A5 条目）。**未重渲染**：`module-deps.dot` 的「95 个 go.mod」分层图与 `bald-c4.dsl` / `render/*.svg` 是同批历史形态——包级分层语义仍在（`check-arch.py --verify` 142/142 归层、100%），但 module 级图要整轮重核，随 S4 文档同步批一起做；零散改标签造「数字新、边旧」的老教训（2026-11-06）继续适用。
