# Bald Cobra MCP 桥接设计：工具从哪来归 cobramcp，协议与生命周期归 transport/mcp

> Author(s): bald 团队
>
> Last updated: 2026-09-17
>
> Discussion at: 源码 `cobramcp/`、`transport/mcp/`、`cobramcp/{server,config,logger,annotations}.go`、`cobramcp/examples/`
>
> Status: Accepted（已落地；发版动作见「发版与遗留」）

## 摘要

`cobra CLI → MCP 工具` 这件事从上游 `onexstack/cobrax` 收编进 bald 仓，命名为 **`github.com/kalandramo/bald/cobramcp`**，与 `transport/mcp` **平级**（顶层独立 module），**不是** `transport/mcp` 里的一个包。

分工被一句话钉死：

| 关注点 | 归属 |
|---|---|
| MCP 协议实现、监听、优雅停机、`Endpoint()` | `transport/mcp` |
| 日志后端 | `bald/log` 契约（进程内模型用宿主装配的 Logger；自带 CLI 子命令安装 stderr 后端） |
| 进程信号与生命周期编排 | 宿主（appkit / bootstrap）；cobramcp **不**捕获信号 |
| **工具从哪来**（Cobra 命令树 → MCP 工具） | `cobramcp` |

三条承诺：

1. **一个 MCP 服务端**：cobramcp 不再自建 mcp-go server，一切工具注册都经 `transport/mcp` 的 `RegisterHandler`。
2. **协议实现不认识 Cobra**：`transport/mcp` 的依赖里没有 cobra/pflag/jsonschema，只想要 MCP 传输的消费者不被拖累。
3. **生命周期归宿主**：cobramcp 的 `MCPServer` 满足 bald 的 `transport.Server` 契约（`Start` 阻塞至 ctx 取消、`Stop` 优雅停机、`Endpoint` 返回真实监听地址），可直接交给 `appkit`。

## 背景与动机

### 动机一：上游要自维护，落点必须先定

`onexstack/cobrax` 的职能是「Cobra 命令树 → MCP tools」的桥接，附带编辑器配置管理（`mcp vscode/cursor enable|disable|list`；Claude Desktop 支持已移除）。要收进 bald 生态，落点有三个候选：

| 候选 | 裁定 |
|---|---|
| `transport/mcp` 内的一个包 | **否决**。传输实现里出现「编辑器配置管理」无法自洽；且只想要 MCP 传输的消费者被迫编译 cobra/pflag/jsonschema/cfgmgr |
| `transport/mcp/cobra/`（独立子 module） | 可行但别扭。package 名会被迫叫 `cobra`，与 `spf13/cobra` 抢 import 别名 |
| **顶层独立 module `bald/cobramcp`** | **采纳**。与 bald 既有惯例一致：顶层目录（独立 go.mod）= 可独立发布的桥接/插件模块 |

### 动机二：两边的缺口刚好互补

改动前的两难：

- `transport/mcp`：**全仓零消费者**（grep 无 import）。它有 `Server`/`RegisterHandler`/`ServerType{SSE,HTTP,STDIO,IN_PROCESS}`，但没有任何东西往里注册工具——缺的正是「工具从哪来」。
- `cobrax`：**go.mod 零 bald 依赖**。它只有 cobra/pflag/mcp-go/jsonschema-go，自己 new mcp-go server、自己抓 SIGINT/SIGTERM、自己把日志写 stderr——缺的正是「协议之外的运行期契约」。

### 动机三：`transport/mcp` 的生命周期其实是坏的

这是 L2 融合真正的阻塞点。改动前：

| 症状 | 代码事实 | 后果 |
|---|---|---|
| 启动不报错 | `Start` 起一个 goroutine 调 `startMCPServer()`，绑定失败只塞进 `s.err` | 地址被占用也「启动成功」，直到 `Stop` 才暴露 |
| Endpoint 是假的 | 构造期由配置地址拼 URL，`:0` 就永远是 `:0` | 动态端口场景下 `Endpoint()` 没用 |
| Stop 停不掉 | SSR/HTTP 的 `*SSEServer`/`*StreamableHTTPServer` 句柄根本没留 | SSE 长连接挂着，`Shutdown` 会一直等 |
| 死代码 | `startMCPServer`、`waitGroup`（无人调用） | — |

所以这次先修 `transport/mcp`，再让 cobramcp 站在它上面——否则「复用」等于把一套坏生命周期换个地方继续用。

### 判别原则

- **协议实现只承诺协议形状**：监听、TLS、`Start`/`Stop`/`Endpoint`、工具注册插槽。它不认识「Cobra」「编辑器配置」这类业务语义。
- **桥接层只回答"东西从哪来"**：工具来源归 cobramcp，传输与运行期契约归框架。
- **依赖方向单向**：`cobramcp → transport/mcp → bald/log`，反向不存在。

## 设计

### 1. `transport/mcp`：生命周期修正（先行）

```go
// 新增字段：运行期传输句柄（Start 填充、Stop 释放）
httpSrv   *http.Server
sseServer *server.SSEServer
ln        net.Listener
done      chan struct{}   // 服务结束（stdio 对端断开）或 Stop 完成时关闭
```

- **`Start` 同步绑定**：SSE/HTTP 先 `net.Listen`，失败立刻 `return err`（不再"启动成功却没在听"）；随后自建 `http.Server{Handler: sseServer|streamableServer}` 并 `Serve(ln)`。stdio 仍在后台 goroutine 里 `ServeStdio`。
- **`Endpoint()` 只在 Start 之后有值**：由真实 listener 推导（`:0` → 系统分配端口），Start 之前为空——上层可据此区分「未监听」与「已监听」。删掉了构造期的占位 endpoint。
- **`Stop` 真正停机**：SSE 先 `CloseSessions()`（长连接不关，`Shutdown` 会等到 ctx 超时）再 `http.Server.Shutdown(ctx)`；`defer markDone()`。
- **`Done() <-chan struct{}`**：stdio 场景客户端断开（stdin EOF）后服务即结束，CLI 需要据此退出进程——`Start` 是非阻塞的，没有这个通道就没法知道「对端走了」。
- **`WithSSEOptions(...)`**：开放 SSE 传输选项（`server.WithBaseURL`），让上层能把对外可见基址透给 SSE。
- **删死代码**：`startMCPServer`、`waitGroup`（无人调用）。

### 2. `cobramcp`：融入框架

**`MCPServer`（进程内模型）** 变成 `transport/mcp.Server` 的包装：

```go
type MCPServer struct {
    opts      MCPOptions
    transport *mcptransport.Server   // 协议与生命周期都在这
    cmdFactory func() *cobra.Command
    tools     []*mcp.Tool
    selectors []Selector
    toolMetas map[string]toolMeta
    toolPaths map[string][]string
}
```

- 构造时把 Cobra 工具逐个 `RegisterHandler` 进 transport 服务端；
- `Start(ctx)`：`!Enabled` → 直接返回；`Addr == ""` → fail-fast；否则 `transport.Start` 后 `<-ctx.Done()`（阻塞语义与 upstream 一致，同时满足 bald 契约）；
- `Stop(ctx)` / `Endpoint()` 直接委托；新增 `Transport()` 暴露底层服务端（注册非 Cobra 来源的工具）；
- **删掉 `signal.Notify(SIGINT/SIGTERM)`**：信号归宿主，appkit 会在 ctx 取消后调 `Stop`。

**`Config`（子进程模型）** 把「工具发现」与「服务端构造」解耦：

```go
registerTools(cmd)                       // 只发现：填充 tools / toolMetas / toolSelectors
newTransportServer(cmd, type, addr)      // 按本次传输类型构造并注册
serveStdio / serveHTTP                   // start → 等 ctx.Done 或 srv.Done() → Stop(带 5s 超时)
```

- 每条工具记下自己的 `Selector`（`toolSelectors`），替代原先的闭包捕获，于是注册逻辑可以推迟到服务端构造时执行；
- 子进程 handler 未变：仍是「还原参数 → 重新执行自身二进制」；
- `serveStdio` 用 `select { <-ctx.Done(); <-srv.Done() }`：stdio 客户端断开就退出，不必等信号。

### 3. 日志：只走契约，CLI 子命令自装 stderr 后端

- 所有调用点从 `log/slog` 全局改为 `baldlog.{Debug,Info,Warn,Error}(ctx, ...)`（无 ctx 处传 `nil`，契约允许）。
- 删除 `Config.SloggerOptions *slog.HandlerOptions`（它意味着"库自己装全局后端"，与 bald「宿主拥有日志」相悖）。
- CLI 子命令（`mcp start/stream/rest/tools`）运行期调 `installStderrLogger(parseLogLevel(f.logLevel))`：stdio 模式下 **stdout 是协议通道**，日志必须写 stderr。后端是包内 ~40 行的 `stderrLogger`（实现 `bald/log.Logger`，底层 `slog` + `os.Stderr`），刻意不复用 `log/bslog`——避免为 CLI 场景引入 lumberjack 轮转依赖。
- 进程内模型**不安装**任何后端：日志由宿主（appkit/bootstrap 经 `bald/log.SetLogger`）决定，未装配时为静默 nop。

### 4. 其他修正（顺手但必要）

- **`MCPOptions.BaseURL` 从死字段变成真能力**：上游算出来只用来打日志，SSE server 从未收到它；现在透传 `mcptransport.WithSSEOptions(mcpserver.WithBaseURL(...))`。
- **`annotations.go` 去重**：四个布尔注解的解析收敛为「键 → 写入口」表驱动。
- **mcp-go 对齐**：`v0.30.0` → `v0.54.1`（与 `transport/mcp` 一致）。实测跨 24 个 minor **零代码改动**。
- **上游遗留清理**：品牌名 `Ophis`（9 处）、`examples/positional-args-mcp-server/go.mod` 的**重复 module path**、`examples/make` 里 `main.go`/`main2.go` 同包重复声明导致模块根本编译不过（`main2.go` 拆为 `examples/make/mcpserver/` 子包）、`commands.go~` 备份文件。

### 5. 破坏面清单（cobramcp 作为新 module 首版，可自由取舍）

| 变更 | 影响 |
|---|---|
| module path `onexstack/cobrax` → `bald/cobramcp`；package `cobrax` → `cobramcp` | 全部 import 与限定名 |
| `Config.SloggerOptions` 删除 | 改用 `bald/log` 契约；CLI 子命令自动装 stderr 后端 |
| `MCPServer.RawMCPServer() *mcpserver.MCPServer` → `Transport() *mcptransport.Server` | 底层类型换成 bald 的服务端 |
| `MCPServer.Start` 语义：非阻塞 → 阻塞至 ctx 取消 | 与 bald `transport.Server` 契约一致 |
| `MCPOptions.BaseURL` 由死字段变为生效 | 行为增强 |

## 已验证事实（实测，非推断）

1. `go build` / `go vet` / `go test` 在 mcp-go **v0.30.0 → v0.54.1** 下全部通过，**零代码改动**（先做临时副本验证，再落到 `cobramcp/go.mod`）。
2. `transport/mcp` 新增可自动回归的测试：`:0` 动态端口 → 同步绑定 → `Endpoint()` 解析出真实端口 → 注册工具 → Streamable HTTP 客户端 `ListTools`/`CallTool` 往返 → `Stop`；端口占用时 `Start` 同步报错。
3. `cobramcp` 新增端到端测试：Cobra 命令树 → 工具注册进 transport 服务端 → **SSE** → MCP 客户端可见并可调用 → ctx 取消后 `Start` 返回。
4. 三个 `examples/` 模块（`make`、`positional-args`、`positional-args-mcp-server`）全部 `go test` 通过。其中 6 + 7 条 schema 断言与 1 处重复声明是**上游既存红灯**（改动前同样失败，已在原始仓库复现确认），本次一并修到扁平 schema 与可编译。

## 发版（2026-09-17 已完成）

- **两个 lightweight tag 已打并推送 origin**：`transport/mcp/v0.1.0`（指向 `db02942`）、`cobramcp/v0.1.0`（指向 `2811933`）。按既定顺序执行：① 先打 `transport/mcp/v0.1.0` → ② cobramcp 与三个 examples 的 require 升真实版本（`transport/mcp v0.0.0→v0.1.0`、examples 的 `cobramcp v0.0.0→v0.1.0`）→ ③ 再打 `cobramcp/v0.1.0`。
- **`replace` 保留**（`=> ../transport/mcp`、`=> ../../log` 等）：与仓内已发版 module 惯例一致（`bootstrap`、`transport/http` 同形），仅供本地开发；Go 只认主模块的 replace，**依赖模块内的 replace 对外部消费者无效**，故不损害可构建性。
- **发版两查结论**：① **tag 自洽**——`git show <tag>:<module>/go.mod` 的 require 全为真实版本（`log v0.5.1` / `transport/mcp v0.1.0` / `cobramcp v0.1.0`），无 `v0.0.0`；② **外部可构建性**——临时消费模块 `require github.com/kalandramo/bald/cobramcp v0.1.0` 后 `go mod tidy` + `go build ./...` 全绿，依赖树解析为 `cobramcp v0.1.0` / `transport/mcp v0.1.0` / `log v0.5.1`，proxy 即刻可取（无需 direct 绕行）。
- **本地 replace 不跨 module 传递**：`cobramcp/examples/*` 三个子 module 各自补了 `cobramcp`/`log`/`transport/mcp` 三条 replace，否则会去解析不存在的版本。
- 上游 `cobramcp/docs/mcp-design.md`（对旧实现的分析）保留为参考，正文已标注状态并指向本文。

## 实施记录（2026-09-17）

- 新增：`cobramcp/`（独立 module，package `cobramcp`）、`cobramcp/logger.go`、`cobramcp/server_lifecycle_test.go`、`transport/mcp/server_lifecycle_test.go`、`cobramcp/examples/make/mcpserver/main.go`。
- 重写：`cobramcp/{server.go,config.go,utils.go,utils_test.go,annotations.go,doc.go,README.md}`、两个示例的 `main_test.go`。
- 修正：`transport/mcp/{server.go,options.go,go.mod}`、`cobramcp/{start,stream,restcmd,tools,execute,rest}.go`、`internal/bridge/flags/*`、`docs/{schema,execution,mcp-design}.md`、`cobramcp/{README,CONTRIBUTING}.md`、bald 根 `README.md` 架构树。
- 验证：`transport/mcp` 与 `cobramcp`（含 3 个 examples module）build/vet/test 全绿。
