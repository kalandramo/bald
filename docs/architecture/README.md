# docs/architecture — 架构制品索引

生成日期 **2026-11-01**（`main` @ `ca981b1`），由架构可视化插件的 `system-modeler` / `flow-visualizer` / `risk-quality-reviewer` 三条场景技能产出，格式源使用 `c4model`（Structurizr DSL）与 `graphviz`（DOT）。

| 文件 | 回答的问题 | 状态 |
|---|---|---|
| `current-architecture.md` | 这个仓库现在是什么结构？分层、SPI、装配主干、模块环 | 现状，引用已逐条核对 |
| `bald-c4.dsl` | C4 landscape / 服务上下文 / 模块容器视图源 | **已机器校验**：`structurizr-dsl` 6.2.4 解析通过，3 张视图导出 + graphviz 渲染均无告警 |
| `module-deps.dot` | 93 个模块的依赖分层，跨层回边高亮 | **已渲染**：`dot -Tsvg` 退出码 0、stderr 为空（29 节点 / 43 边） |
| `flows.md` | 请求/消息/生命周期/发现/缓存/可观测/WebRTC 怎么流；哪些真接线 | 现状，引用已逐条核对 |
| `flows.dot` | 上述三条主流程的图源 | **已渲染**：`dot -Tsvg` 退出码 0、stderr 为空（40 节点 / 39 边） |
| `risk-review.md` | 哪里有风险、严重度、消解条件、动手顺序 | 含自评局限一节（§9） |
| `render/*.svg` | 上面两份 `.dot` 与 `.dsl` 三张视图的渲染快照 | 由 graphviz 16.1.0 生成，改源后需重跑（见「校验工具与命令」） |

> 「引用已逐条核对」= 用脚本把制品里的每个 `路径:行号` 抽出来（行内省略文件名的简写按就近文件解析），逐条打开源文件回读该行内容并人工比对叙述；不是抽样。本轮结果：**308 条唯一 `文件:行号`（其中 78 条由简写解析得到），0 条无法解析、0 条越界**。
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

本轮实测环境：graphviz 16.1.0、Temurin JDK 21.0.13+9、Maven 3.9.16、`structurizr-dsl`/`structurizr-export` 6.2.4。

## 重新生成

三份 `.md` 是人工整理的证据结论，不自动重生成。两个 `.dot` 与 `.dsl` 是文本源，改动后按上一节「校验工具与命令」重跑 1)–4)。

行号引用会随代码演进漂移。核对锚点：`pkg/appkit/appkit.go`（Run/stopAll/waitForEndpoints）、`pkg/middleware/bundle/bundle.go`（Gin/GRPCInterceptors 链序）、`transport/server.go:14-22`、`broker/broker.go:9-33`、`.github/workflows/ci.yml`、`Taskfile.yml`。

## 已知未覆盖

- 运行时/部署拓扑（仓内无 IaC，无 trace/日志样本）
- bald-admin 侧的消费真实情况（另一仓库）
- `transport/mcp` + `cobramcp` 的工具调用回合、`workflow/argo`、`ai/*`、`transport/graphql`（仓内零消费者）
