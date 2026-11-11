# docs/architecture — 架构制品索引

## 按问题找文档（先读这里）

| 你关心的事 | 先读 |
|---|---|
| 这个仓库现在是什么结构 | [current-architecture.md](current-architecture.md) |
| 哪里有风险、消解条件与动手顺序 | [risk-review.md](risk-review.md) |
| 请求/消息/生命周期怎么流 | [flows.md](flows.md) |
| bald 与 onexai（AI 化）的差距与落地设计 | [onexai-gap-review.md](onexai-gap-review.md) §4 状态总览 |
| onexmesh 移植了什么、裁决理由 | [onexmesh-comparison.md](onexmesh-comparison.md) §6 完成度 |
| 传输统一（方案 S）的取舍依据 | [transport-unification-assessment.md](transport-unification-assessment.md) |
| 框架哪里对用户不友好、快赢排期 | [developer-experience-review.md](developer-experience-review.md) |
| 历史会话成果与避坑清单 | [handoff-session-20261108.md](handoff-session-20261108.md) §2 |

生成日期 **2026-11-01**（`main` @ `ca981b1`），由架构可视化插件的 `system-modeler` / `flow-visualizer` / `risk-quality-reviewer` 三条场景技能产出，格式源使用 `c4model`（Structurizr DSL）与 `graphviz`（DOT）。

**2026-11-02 修订**：§3.1 的模块环已断（删掉 `bootstrap`/`pkg/appkit` 里与 `transport/gateway/contract` 重复的转码面机制），
三份制品的拓扑叙述、`render/` 快照与全部行号引用已按新代码重核；`bald-c4.dsl` 重新机器校验（0 problems）。

**2026-11-02 二次重核**（`main` @ `5f771e0`，该提交清掉 16 个模块里陈旧的 `transport/gateway // indirect`）：
三份 `.md` 的断言逐条回读代码后仍然成立——22 个非示例模块直接 require 根 module 的清单、93 个 `go.mod`、30 个 `*/contract`、
18 个 `transport/*`、§3.2 的多 major 分组（21 / 20 / 3）与「内部依赖图是 DAG」均实测复现（独立脚本：0 环）。
`module-deps.dot` 补上了此前漏画的根扇入：`oss/{s3,minio}` 两个节点及其 SPI/回边、`contrib → 根 module` 回边，
使图上 22 条根回边与 `current-architecture.md` §4 的叙述一致；版本串 `v0.34.0` 更正为 `v0.34.1`，快照已重渲染。

**2026-11-02 三次重核**（§1.1 race 门禁去人工化）：CI 的 race 作业与 `task race` 改成同一条
`find` 派生（按 CI 谓词 92 个 module）+ 单份 `.github/race-skip.txt`；全量 `-race` 实测 90 通过 /
2 失败，失败之一的 `transport/graphql` 是本轮新覆盖到的真实竞争（`Start` 写、`Stop`/`Endpoint` 读，
无同步），已修；另一条 `log/aliyun` 的竞争在上游 SDK 内部，进排除清单。依赖图与拓扑叙述不变
（修复只加 `sync` 导入），行号引用重核后 `checked cites: 317, problems: 0`。

**2026-11-02 四次重核**（§1.3 补测试）：`broker/kafka`、`broker/rabbitmq` 不再是零测试模块，
补测试当场揪出并修掉 4 个生产缺陷（nil map 复位、重建路径吞掉写失败、`closeLocked` 事后改写 `done`
与 `run()` 的无锁读并发命中、致命错误退出不 close），`risk-review.md` §1.3 逐条记了修法与验活。
依赖图与拓扑叙述不变，但 `flows.md` §④ 与 `flows.dot` 里 kafka 发布/订阅的行号引用因这些改动整体位移，
已按新行号重核并重渲染 `render/flows.svg`；`check_cites.py` 输出 `checked cites: 335, problems: 0, bare-resolved: 68`。

**2026-11-02 五次重核**（§3.1 收口：`pkg/` 独立成 module `github.com/kalandramo/bald/pkg`）：
`current-architecture.md` §2/§4 的枢纽叙述改指 `pkg`，`module-deps.dot` 的 22 条红边目标随之重画并重渲染，
`flows.md` 与 `Bald 指标设计.md` 里 `observability/metricshttp` 的旧路径更新为 `pkg/metricshttp`；
`risk-review.md` §3.1 加了「收口」一节，按 `git archive main` 快照与工作树各跑一遍同一段 Tarjan 脚本，
把 require 根的适配器 22→0、`replace` 410→423（**不降反升，当年那句「收益是 replace 数量下降」已撤回**）
写成可复现数字。拆分本身由 `internal/docscheck` 新增的三条闸门守住（`TestNoModuleRequiresAggregateRoot` /
`TestPkgModuleIsLeaf` / `TestModuleOfImport`），五条变异实测与四道闸门（`verify` / `race` / `lint` / `vuln`）的
本机结果——含 `task lint` 首跑红在我自己新增的测试代码上、`task vuln` 本机红在 go1.27.1 标准库这两件不体面的事——
都记在 `risk-review.md` §3.1「验活」。

| 文件 | 回答的问题 | 状态 |
|---|---|---|
| `current-architecture.md` | 这个仓库现在是什么结构？分层、SPI、装配主干、模块拓扑 | 现状，引用已逐条核对 |
| `bald-c4.dsl` | C4 landscape / 服务上下文 / 模块容器视图源 | **已机器校验**：`structurizr-dsl` 6.2.4 解析通过，3 张视图导出 + graphviz 渲染均无告警 |
| `module-deps.dot` | 95 个 `go.mod` 的依赖分层，跨层回边高亮 | **已渲染**：`dot -Tsvg` 退出码 0、stderr 为空（32 节点 / 52 边；回边目标已由聚合根改为 `pkg` module） |
| `flows.md` | 请求/消息/生命周期/发现/缓存/可观测/WebRTC 怎么流；哪些真接线 | 现状，引用已逐条核对 |
| `flows.dot` | 上述三条主流程的图源 | **已渲染**：`dot -Tsvg` 退出码 0、stderr 为空（40 节点 / 39 边） |
| `risk-review.md` | 哪里有风险、严重度、消解条件、动手顺序 | 含自评局限一节（§9） |
| `onexai-gap-review.md` | 对照 OneX Harness AI Coding（onexai）的六层依赖清单，bald 有什么、缺什么；含规范蒸馏卡目录（§5）与 `bald convention` CLI + cobramcp 挂载落地设计（§6） | 2026-11-07 首版；P0 与 P1-4/5 已落地（§4 状态总览），P1-6…8 与 P2 未做 |
| `onex-ai.md` | onexai 工具输出的逐字存档（46 convention 描述 / overview 完整输出 / 20 create 命令），补 PDF 文本解析的截断 | 源材料，保持原样不做编辑 |
| `onexmesh-comparison.md` | 对照 `../onexmesh`（运行时微服务框架）的设计盘点与路线分歧分析：三条深层观察、双向借鉴清单、关系判断、移植评估（§6 决策表） | 2026-11-08 首版；三批移植全部落地（§6 完成度已回填） |
| `transport-unification-assessment.md` | 传输统一抽象重构评估：痛点拆解（handler/错误已统一，真缺口是中间件双链/手写路由双轨/无路由级中间件）、S/M/L 三档方案与代价、四批迁移路径 | 2026-11-08 首版；**方案 S 已全部落地**（§3 实施进展），旧链保留至下一 major |
| `handoff-session-20261108.md` | 会话成果记录（onexmesh 吸收 × 方案 S 批次 1–3、九 tag）+ 避坑清单（网络工具链/契约生成/运行时陷阱/断言口径） | 交接已闭环（2026-11-10）；时效性章节（阻塞/待办/闸门现状）已移除，避坑清单长期有效 |
| `developer-experience-review.md` | 用户视角的框架体检：四类用户旅程（新手/业务开发/维护者/AI 会话）的摩擦点清单（DX-1…17，实证采集），含快赢清单（7 项按性价比排序） | 2026-11-11 首版；留待优化，修复时逐条勾销 |
| `render/*.svg` | 上面两份 `.dot` 与 `.dsl` 三张视图的渲染快照 | 由 graphviz 16.1.0 生成，改源后需重跑（见「校验工具与命令」） |

> 「引用已逐条核对」= 用脚本把制品里的每个 `路径:行号` 抽出来（行内省略文件名的简写按就近文件解析），逐条打开源文件回读该行内容并人工比对叙述；不是抽样。最新一次重跑（§3.1 收口后，脚本原始输出）：**`checked cites: 324, problems: 12, bare-resolved: 65, bare-unresolved: 0`**（比上一轮少一条引用：评审快照里那条 go.mod 的行号在两轮改动后已不对应任何事实，改成了不带行号的快照描述）。
> 这 12 条不是本轮引入：与拆分前的 `main` 逐条比对，同一集合：多包同名的裸文件名（测试文件、中间件里的超时与限流实现）与只写了裸文件名的 workflow 作业文件，本轮新增 0 条；
> 上面那句 `problems: 0` 是 §7.5 Windows 那轮之前的状态，之后写进风险登记册的若干裸文件名引用把它带成了 12，此前没人重跑这条校验。
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

**2026-11-06 增量（声明式装配方向②前两批，代码已改、本目录只动文字）**：`*/contract` 从 30 涨到 **39**（`find . -type d -name contract` 复跑即得；第一批 `transport/{tcp,websocket,http3}`、第二批 `transport/{graphql,webrtc,mcp}`，判据与变异见 `risk-review.md` §7.28 两节）。
**未重渲染**：`module-deps.dot` 的「回边（30 个子包）」是 2026-11-03 谓词下的聚合标签（当时数的是图上 require `pkg` 的 contract 子包，本轮实测该谓词为 24——两者都已旧），`bald-c4.dsl` 与 `render/*.svg` 同批未动；这三份图源要按「校验工具与命令」整轮重核才有意义，**零散改标签会造出「数字新、边旧」的第三种不一致**。`current-architecture.md` 的 §2/§5/§6 文字已同步（目录计数 39 并带 `find` 判据、方向②剩余清单 7→4）。

**2026-11-10 梳理（文档噪音清理，只动过程性文档）**：`handoff-session-20261108.md` 交接闭环——阻塞/待办/闸门现状/检查清单四节时效内容移除，保留成果总览与避坑清单；`onexmesh-comparison.md` §6/§6.3 的 ⏳ 项（semconv/otelslog/circuitbreaker/resiliency/client SDK）回填 ✅（第三批落地，resiliency/client SDK 形态与预估不同，理由见完成度注记）；`transport-unification-assessment.md` 头部加完成态（方案 S 四批全落地）；`onexai-gap-review.md` §4 增状态总览（P0 与 P1-4/5 落地，P1-6…8 与 P2 未做，mqserver/clitool 预设的阻塞条件写明）。`onex-ai.md` 逐字存档维持不动。

## 重新生成

三份 `.md` 是人工整理的证据结论，不自动重生成。两个 `.dot` 与 `.dsl` 是文本源，改动后按上一节「校验工具与命令」重跑 1)–4)。

行号引用会随代码演进漂移。核对锚点：`pkg/appkit/appkit.go`（Run/stopAll/waitForEndpoints）、`pkg/middleware/bundle/bundle.go`（Gin/GRPCInterceptors 链序）、`transport/server.go:14-22`、`broker/broker.go:9-33`、`.github/workflows/ci.yml`、`Taskfile.yml`。

## 已知未覆盖

- 运行时/部署拓扑（仓内无 IaC，无 trace/日志样本）
- bald-admin 侧的消费真实情况（另一仓库）
- `transport/mcp` + `cobramcp` 的工具调用回合、`workflow/argo`、`ai/*`、`transport/graphql`（仓内零消费者）
