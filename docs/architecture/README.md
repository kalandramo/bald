# docs/architecture — 架构制品索引

生成日期 **2026-11-01**（`main` @ `ca981b1`），由架构可视化插件的 `system-modeler` / `flow-visualizer` / `risk-quality-reviewer` 三条场景技能产出，格式源使用 `c4model`（Structurizr DSL）与 `graphviz`（DOT）。

| 文件 | 回答的问题 | 状态 |
|---|---|---|
| `current-architecture.md` | 这个仓库现在是什么结构？分层、SPI、装配主干、模块环 | 现状，引用已逐条核对 |
| `bald-c4.dsl` | C4 landscape / 服务上下文 / 模块容器视图源 | **未机器校验**（本机无 structurizr-cli；只验了括号平衡，74 行） |
| `module-deps.dot` | 93 个模块的依赖分层，跨层回边高亮 | **结构已查、未渲染**（括号/方括号配平、29 个节点、28 个边端点均已声明） |
| `flows.md` | 请求/消息/生命周期/发现/缓存/可观测/WebRTC 怎么流；哪些真接线 | 现状，引用已逐条核对 |
| `flows.dot` | 上述三条主流程的图源 | **结构已查、未渲染**（括号配平、40 个节点、40 个边端点均已声明） |
| `risk-review.md` | 哪里有风险、严重度、消解条件、动手顺序 | 含自评局限一节（§9） |

> 「引用已逐条核对」= 用脚本把制品里的每个 `路径:行号` 抽出来（行内省略文件名的简写按就近文件解析），逐条打开源文件回读该行内容并人工比对叙述；不是抽样。本轮结果：**308 条唯一 `文件:行号`（其中 78 条由简写解析得到），0 条无法解析、0 条越界**。
>
> 这个检查的边界：它只能证明「被写成引用的那些行号」对得上，**不能证明叙述本身正确**——没有行号的定性判断（如「零 importer」「性能更好」）仍需人工复核。DOT/DSL 的检查同样只是结构层面的，不等于渲染成功。

## 重新生成

三份 `.md` 是人工整理的证据结论，不自动重生成。两个 `.dot` 与 `.dsl` 是文本源，改动后请：

```bash
brew install graphviz            # 或任何 dot 发行版
dot -Tsvg module-deps.dot -o module-deps.svg
dot -Tsvg flows.dot -o flows.svg
structurizr-cli validate -in bald-c4.dsl
```

行号引用会随代码演进漂移。核对锚点：`pkg/appkit/appkit.go`（Run/stopAll/waitForEndpoints）、`pkg/middleware/bundle/bundle.go`（Gin/GRPCInterceptors 链序）、`transport/server.go:14-22`、`broker/broker.go:9-33`、`.github/workflows/ci.yml`、`Taskfile.yml`。

## 已知未覆盖

- 运行时/部署拓扑（仓内无 IaC，无 trace/日志样本）
- bald-admin 侧的消费真实情况（另一仓库）
- `transport/mcp` + `cobramcp` 的工具调用回合、`workflow/argo`、`ai/*`、`transport/graphql`（仓内零消费者）
