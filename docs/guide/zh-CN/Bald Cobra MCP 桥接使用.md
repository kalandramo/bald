# Bald Cobra MCP 桥接使用

操作手册：把已有的 Cobra CLI 接成 MCP 服务。只讲怎么用——四种暴露形态怎么选、各自的完整样例、自带子命令怎么调、工具怎么打磨、认证怎么开、编辑器怎么接、出问题怎么查。

设计论证见 `docs/devel/zh-CN/Bald Cobra MCP 桥接设计.md`；模块内部实现见 `cobramcp/docs/design.md`。

## 0. 一分钟选型

cobramcp 有四种把命令暴露出去的方式。先按这张表定位：

| 形态 | 入口 | 传输 | 什么时候用 |
| --- | --- | --- | --- |
| **子进程 + stdio** | `my-cli mcp start` | stdio | 编辑器（VSCode / Cursor）拉起本地进程。**最常用** |
| **子进程 + SSE** | `my-cli mcp stream` | SSE | 远端 / 容器接入，跨机器 |
| **子进程 + REST** | `my-cli mcp rest` | HTTP JSON | 脚本、CI、网关直接调用（不走 MCP 协议） |
| **进程内 + SSE** | `NewMCPServer(...).Start(ctx)` | SSE | 命令依赖进程内状态（DB 连接、缓存、客户端），不想为每次调用冷启动 |

前三种是**子进程模型**：每次工具调用都重新执行一次自身二进制，参数由 MCP 入参还原。第四种是**进程内模型**：在当前进程里直接跑命令树。

## 1. 引依赖

```bash
go get github.com/kalandramo/bald/cobramcp@latest
```

已发布 tag：`cobramcp/v0.1.1`（依赖 `transport/mcp/v0.1.0`）。

依赖方向单向：`cobramcp → transport/mcp → bald/log`。只想要 MCP 传输的消费者不会被拖入 cobra / pflag / jsonschema。

> 在 bald 仓内本地开发时，`cobramcp/examples/*` 用 `replace` 指向本仓目录；对外部消费者，`replace` 不生效，直接 `go get` 真实版本即可。

## 2. 模式一：子进程 + stdio（编辑器接入，最常用）

### 2.1 接入（3 行）

```go
package main

import (
    "os"

    "github.com/kalandramo/bald/cobramcp"
)

func main() {
    rootCmd := buildRootCmd()                 // 你已有的 Cobra 命令树
    rootCmd.AddCommand(cobramcp.Command(nil)) // 注入 mcp 子命令树，默认名 "mcp"

    if err := rootCmd.Execute(); err != nil {
        os.Exit(1)
    }
}
```

### 2.2 验证工具清单

```bash
my-cli mcp tools          # 导出到 ./mcp-tools.json
cat mcp-tools.json        # 检查工具名、描述、inputSchema
```

> 产物写**当前工作目录**的 `mcp-tools.json`，不是二进制所在目录。

### 2.3 注册进编辑器

```bash
my-cli mcp vscode enable    # 写 VSCode 配置（需 Copilot Agent Mode）
my-cli mcp cursor enable    # 写 Cursor 配置
```

改完配置**重启编辑器**（或其 MCP 面板）才会重新拉起进程。

### 2.4 手动起服务调试

```bash
my-cli mcp start            # 以 stdio 起 MCP 服务（通常由编辑器拉起，手动跑用于排查）
```

stdio 模式下 stdout 是协议通道，日志走 stderr——所以看到日志打在终端不影响协议。

## 3. 模式二：子进程 + SSE（远端 / 容器）

```bash
my-cli mcp stream --host 0.0.0.0 --port 8080
```

| flag | 默认 | 说明 |
| --- | --- | --- |
| `--host` | `""`（全部接口） | 监听地址 |
| `--port` | `8080` | 监听端口 |
| `--log-level` | `info` | `debug`/`info`/`warn`/`error`，大小写不敏感 |

服务端在 `http://<host>:<port>/sse` 暴露 SSE 端点。MCP 客户端配置里填这个地址即可。

若服务在反向代理后面，用 `Config.BaseURL` 告诉它对外可见的基址（否则 SSE 消息端点会广告成内网地址）：

```go
config := &cobramcp.Config{
    BaseURL: "https://mcp.example.com",
}
```

> SSE 形态支持认证，见第 8 节。

## 4. 模式三：子进程 + REST（脚本 / CI / 网关）

```bash
my-cli mcp rest --host 0.0.0.0 --port 9090
```

每条命令一个路由，路径就是工具名：

```bash
curl -s -X POST http://localhost:9090/my-cli_oncall \
  -H 'Content-Type: application/json' \
  -d '{"module":"bke","title":"pod crash","level":2}'
```

| 项 | 约定 |
| --- | --- |
| 请求 | `POST /{toolName}`；body 是**扁平 JSON**（flag 与位置参数同层），空 body 视为零参数 |
| 成功响应 | HTTP 200 + `{"stdout":"...","stderr":"...","exitCode":0}`——**命令失败也是 200**，要看 `exitCode` |
| HTTP 400 | body 不是合法 JSON |
| HTTP 404 | 工具名不存在 |
| HTTP 405 | 用了 POST 以外的方法 |
| HTTP 500 | 子进程无法拉起 |

| flag | 默认 | 说明 |
| --- | --- | --- |
| `--host` / `--port` | `""` / `8080` | 监听地址 |
| `--base-url` | `""` | 仅用于启动日志的对外基址 |
| `--log-level` | `info` | 日志级别 |

> REST 形态支持认证，见第 8 节。

## 5. 模式四：进程内 + SSE（依赖进程内状态）

命令依赖不可序列化的状态（API 客户端、DB 句柄、业务对象）时用这一形态——工具调用不 fork 自身，直接在当前进程里跑一棵**全新**的命令树。

```go
package main

import (
    "context"
    "log"

    "github.com/kalandramo/bald/cobramcp"
    "github.com/spf13/cobra"
)

func main() {
    // 依赖构造一次，之后只读共享。
    biz := newBotSreBiz(apiClient, dbClient)

    // 工厂：每次调用返回一棵全新命令树（闭包持有 biz）。
    factory := func() *cobra.Command {
        return buildRootCmd(biz)
    }

    srv, err := cobramcp.NewMCPServer(cobramcp.MCPOptions{
        Enabled: true,
        Addr:    ":8090",
        Name:    "myapp",
        Version: "1.0.0",
    }, factory)
    if err != nil {
        log.Fatal(err)
    }

    if err := srv.Start(context.Background()); err != nil { // 阻塞至 ctx 取消
        log.Fatal(err)
    }
}
```

### 5.1 关键语义

- **`cmdFactory` 的语义**：构造时调用一次（只读遍历取 schema）；此后**每次工具调用再调用一次**生成全新命令树。所以不存在 flag 值、Options 结构、闭包变量在调用间串味——**并发调用天然安全，无需加锁、无需 reset**。
- **`Enabled=false`**：`Start` 直接返回 `nil`（特性开关）。
- **`Addr==""` 且 `Enabled=true`**：`Start` 立即报错 `MCPOptions.Addr must not be empty when Enabled is true`。
- **工具调用的 ctx 取消不向内传播**：执行用 `context.WithoutCancel` 保留 ctx 值（trace / 租户）但屏蔽取消——避免上游「拿到结果就 cancel」把已成功的工作打断。

### 5.2 交宿主装配

`MCPServer` 满足 bald 的 `transport.Server` 契约，可直接交给 appkit 并行启停：

```go
appkit.New(
    appkit.WithExtraServers(srv),
    // ...
)
```

进程信号（SIGINT/SIGTERM）由宿主负责——cobramcp 不捕获信号，不要自己抓。

### 5.3 注册非 Cobra 来源的工具

```go
srv.Transport().RegisterHandler(myTool, myHandler)
```

`srv.Tools()` 看当前工具清单；`srv.Transport()` 拿到底层 `transport/mcp` 服务端。

## 6. 自带子命令速查

`cobramcp.Command(nil)` 注入的子命令树（默认命令名 `mcp`，可用 `Config.CommandName` 改）：

```
mcp
├── start            # stdio 服务端
├── stream           # SSE 服务端
├── rest             # REST 服务端
├── tools            # 导出工具清单为 ./mcp-tools.json
├── vscode           # enable / disable / list
└── cursor           # enable / disable / list
```

| 子命令 | 作用 | 参数 |
| --- | --- | --- |
| `mcp start` | stdio 暴露 | `--log-level` |
| `mcp stream` | SSE 暴露 | `--log-level`、`--host`、`--port`、`--auth-token`、`--oauth-resource`、`--oauth-auth-server` |
| `mcp rest` | REST 暴露 | `--log-level`、`--host`、`--port`、`--base-url`、`--auth-token`、`--oauth-resource`、`--oauth-auth-server` |
| `mcp tools` | 导出工具清单 | `--log-level` |
| `mcp vscode enable\|disable\|list` | 管理 VSCode 配置 | 见下 |
| `mcp cursor enable\|disable\|list` | 管理 Cursor 配置 | 见下 |

编辑器子命令参数：

| 子命令 | 参数 | 说明 |
| --- | --- | --- |
| `enable` | `--config-path` | 覆盖配置文件路径（默认按平台推导） |
| | `--server-name` | 写入的 server 名（默认取可执行文件名去扩展名） |
| | `--env` / `-e` | `KEY=value`，可重复；与 `Config.DefaultEnv` 合并，同名时 `--env` 优先 |
| | `--workspace` | 写工作区 `.vscode/mcp.json` / `.cursor/mcp.json` 而不是用户级配置 |
| `disable` | `--config-path`、`--server-name`、`--workspace` | 幂等：条目不存在时只告警，退出码仍是 0 |
| `list` | `--config-path`、`--workspace` | 打印配置里的全部 server；顺序不保证 |

> 子命令名默认 `mcp`。若你的 CLI 已有叫 `mcp` 的业务命令，用 `Config.CommandName`（如 `"agent"`）改名——命令树、编辑器配置写入、内建过滤三处自动跟随。

## 7. 工具打磨

### 7.1 哪些命令会变成工具

先过滤、再选匹配（前者不可被 selector 绕过）：

| 阶段 | 规则 |
| --- | --- |
| 安全过滤（恒生效） | `Hidden` 或 `Deprecated` 的命令排除；没有 `Run`/`RunE`/`PreRun`/`PreRunE` 的命令排除 |
| 内建排除（恒生效） | `mcp`（或用 `CommandName` 改的名）命令组、`help`、`completion` |
| flag 过滤（恒生效） | `--help`、`Hidden`、`Deprecated` 的 flag 一律不进 schema |
| selector | 按声明顺序取**第一个命中**的 `CmdSelector`；都不命中则该命令不暴露 |

内建排除用**精确路径段匹配**：`helper`、`completion-status`、`mcp-tools` 这类名字不会被误排除。

### 7.2 工具名与描述

- **子进程模型**工具名 = `<根命令名>_<子命令路径>`，空格换下划线，例如 `my-cli sre open` → `my-cli_sre_open`。`Config.ToolNamePrefix` 可替换根命令名那段（应对 MCP 的 64 字符工具名上限）。
- **进程内模型**工具名不含根命令名（`sre_open`），因为根命令名可能含 MCP 工具名非法字符（如 `/`）。
- 描述取 `cmd.Long`，为空回退 `cmd.Short`，再回退 `Execute the <name> command`；`cmd.Example` 非空会追加为 `Examples:` 段落。

### 7.3 位置参数：用注解写描述

从 `cmd.Use` 解析：`<name>` 必填、`[name]` 可选、末尾 `...` 变参。参数名统一转小写、非字母数字换 `_`。

描述默认由 token 生成（英文），建议用注解改写——**下标从 0 开始**：

```go
cmd := &cobra.Command{
    Use:   "oncall <module> [title]",
    Short: "发起 oncall",
    Annotations: map[string]string{
        cobramcp.AnnotationArgPrefix + "0": "值班模块名（如 bke、kafka）",
        cobramcp.AnnotationArgPrefix + "1": "一句话事件标题",
    },
}
```

> **务必避免位置参数与 flag 重名**：归一化后同名会导致 schema 有两个写入源、执行期把同一输入值同时当 flag 和位置参数。cobramcp 会在注册期**拒绝**这类命令（返回错误，不静默降级）。

### 7.4 注解：告诉模型工具语义

写进 `cmd.Annotations`：

| 注解常量 | 键 | 作用 |
| --- | --- | --- |
| `AnnotationTitle` | `title` | 人类可读标题 |
| `AnnotationReadOnly` | `readOnlyHint` | 只读、不改环境 |
| `AnnotationDestructive` | `destructiveHint` | 可能造成破坏 |
| `AnnotationIdempotent` | `idempotentHint` | 重复调用无额外副作用 |
| `AnnotationOpenWorld` | `openWorldHint` | 会与外部实体交互 |

```go
cmd.Annotations = map[string]string{
    cobramcp.AnnotationReadOnly: "true",
    cobramcp.AnnotationTitle:    "列出 Pod",
}
```

布尔值用 `strconv.ParseBool` 解析（`"1"`/`"true"`/`"0"`/`"false"`），解析失败只告警并跳过，不会让注册失败。

### 7.5 入参 schema 长什么样

**扁平结构**：所有 flag 与位置参数都是顶层属性，没有嵌套的 `flags` / `args` 子对象。

```json
{
  "type": "object",
  "properties": {
    "namespace": { "type": "string", "description": "namespace to use" },
    "level":     { "type": "integer", "default": 1 },
    "module":    { "type": "string", "description": "值班模块名（如 bke、kafka）" }
  },
  "required": ["module"],
  "additionalProperties": { "not": {} }
}
```

`additionalProperties` 用 `{"not":{}}` 表达「不允许额外字段」。

常见类型映射（完整表见 `cobramcp/docs/design.md`）：

| pflag 类型 | JSON Schema |
| --- | --- |
| `bool` | `boolean` |
| `int` / `uint` / `count` 等 | `integer` |
| `float32` / `float64` | `number` |
| `string` | `string`（可用 flag 注解 `jsonschema` 覆盖成任意子 schema） |
| `stringSlice` / `stringArray` | `array<string>` |
| `stringToString` | `object<string>` |
| `duration` | `string` + Go duration 正则 |

`cmd.MarkFlagRequired("x")` 的 flag 进 `required`；`flag.DefValue` 非空时写入 `default`。

## 8. 收窄暴露面与认证

### 8.1 Selector：只暴露该暴露的

默认（`Config.Selectors` 为空）等价于「全收」。典型场景是**只暴露读命令、并给危险 flag 脱敏**：

```go
config := &cobramcp.Config{
    Selectors: []cobramcp.Selector{
        {
            CmdSelector:           cobramcp.AllowCmdsContaining("get", "list", "status"),
            LocalFlagSelector:     cobramcp.ExcludeFlags("token", "secret", "kubeconfig"),
            InheritedFlagSelector: cobramcp.NoFlags, // 排除 persistent flags
        },
    },
}
```

可用的 selector 构造器：

| 构造器 | 匹配对象 | 语义 |
| --- | --- | --- |
| `AllowCmdsContaining(...)` / `ExcludeCmdsContaining(...)` | `cmd.CommandPath()` 子串 | 路径**含**任一短语 |
| `AllowCmds(...)` / `ExcludeCmds(...)` | `cmd.CommandPath()` 全等 | 路径**等于**其中之一 |
| `AllowFlags(...)` / `ExcludeFlags(...)` | `pflag.Flag.Name` | flag 名白/黑名单 |
| `NoFlags` | — | 排除全部 flag |

要点：

- 匹配的是**完整命令路径**（含根命令名），不是工具名。
- flag 分两组：`LocalFlagSelector` 管命令自身 flag，`InheritedFlagSelector` 管 persistent flag——`InheritedFlagSelector: cobramcp.NoFlags` 是最常用的一行。
- 多个 selector 时**第一个命中的生效**；约定「具体规则在前、兜底规则在后」。

### 8.2 Middleware：包在工具执行外面

```go
Middleware: func(ctx context.Context, req mcp.CallToolRequest, in cobramcp.ToolInput,
    next cobramcp.ExecuteFunc) (*mcp.CallToolResult, cobramcp.ToolOutput, error) {
    ctx, cancel := context.WithTimeout(ctx, time.Minute)
    defer cancel()
    return next(ctx, req, in)
}
```

可做超时、日志、结果改写、错误转换。执行本身已自带 panic 恢复。

### 8.3 认证（SSE / REST）

认证作用于 SSE 与 REST 形态（stdio 无网络面）。三种用法：

**自定义中间件**——注入任意 `func(http.Handler) http.Handler`：

```go
config := &cobramcp.Config{
    AuthMiddleware: func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            if !verify(r.Header.Get("Authorization")) { // 你的校验逻辑
                http.Error(w, "unauthorized", http.StatusUnauthorized)
                return
            }
            next.ServeHTTP(w, r)
        })
    },
}
```

**内置静态 token**——最简场景：

```go
config := &cobramcp.Config{ AuthToken: "s3cr3t" }
```

```bash
my-cli mcp stream --auth-token s3cr3t
```

**OAuth 资源元数据**（RFC 9728）——暴露 `/.well-known/oauth-protected-resource`：

```bash
my-cli mcp stream \
  --oauth-resource https://mcp.example.com \
  --oauth-auth-server https://auth.example.com
```

验证：

```bash
curl -s http://localhost:8080/.well-known/oauth-protected-resource   # 无 token → 200 + JSON
curl -s -X POST http://localhost:9090/my-cli_ping                    # 无 token → 401
curl -s -X POST http://localhost:9090/my-cli_ping -H "Authorization: Bearer s3cr3t"  # → 200
```

> **安全要点**：well-known 发现端点始终**免认证**（客户端未持 token 时也要能取，否则 OAuth 发现流程死锁）。内置静态 token 中间件自动放行；**自定义 `AuthMiddleware` 需自行放行该路径**。
>
> 自定义中间件若包装 `http.ResponseWriter`，必须透传 `http.Flusher`（SSE 依赖 `Flush()`）。

## 9. 编辑器集成细节

### 9.1 `enable` 写了什么

```json
{
  "servers": {
    "my-cli": {
      "type": "stdio",
      "command": "/usr/local/bin/my-cli",
      "args": ["mcp", "start"],
      "env": { "PATH": "/opt/homebrew/bin:/usr/bin:/bin" }
    }
  }
}
```

- `command` = 当前可执行文件绝对路径；
- `args` = 复用当前调用路径 + `start`（所以 `my-cli mcp cursor enable` 与 `my-cli agent cursor enable` 写出的 args 前缀不同）；
- `env` = `Config.DefaultEnv` 合并用户 `--env`，用户值优先。

| 编辑器 | 用户级配置文件 | 顶层字段 | 备注 |
| --- | --- | --- | --- |
| VSCode | macOS `~/Library/Application Support/Code/User/mcp.json`；Linux `~/.config/Code/User/mcp.json`；Windows `%USERPROFILE%\AppData\Roaming\Code\User\mcp.json` | `servers` | `--workspace` 时写 `<cwd>/.vscode/mcp.json`；需 Copilot Agent Mode |
| Cursor | 各平台 `~/.cursor/mcp.json`（Windows `%USERPROFILE%\.cursor\mcp.json`） | `mcpServers` | `--workspace` 时写 `<cwd>/.cursor/mcp.json` |

> Claude Desktop 不在支持范围——`enable` 不会读写其配置文件，请手工编辑。

### 9.2 带上 PATH

编辑器拉起的子进程环境很干净（macOS 上 PATH 常常只有 `/usr/bin:/bin:/usr/sbin:/sbin`），`helm`/`kubectl`/`docker` 可能找不到：

```go
config := &cobramcp.Config{
    DefaultEnv: map[string]string{"PATH": os.Getenv("PATH")},
}
```

合并结果为空时 JSON 里**不出现** `env` 键。

### 9.3 写入行为与陷阱

- **每次保存前备份**为同目录 `<名字>.backup.json`（同名备份每次覆盖，只保留上一次保存前的状态）。
- 文件不存在会被创建（自动 `MkdirAll` + 写 `0644`）；`disable`/`list` 不会创建。
- 文件必须是**严格 JSON**：带注释/尾逗号的 JSONC 会报 `invalid JSON format`，不静默覆盖。
- 保存是**整份重新序列化**：结构体未建模的顶层键会被丢弃。
- `enable` 遇到同名 server 直接覆盖，**没有二次确认**；server 名默认取可执行文件名，可用 `--server-name` 固定。
- 改完配置需**重启编辑器**。

## 10. 报错怎么查

**工具调用看不到效果，编辑器里也没报错？** 先跑 `my-cli mcp tools` 确认工具数量与名字；再看 `enable` 写出的 `command`/`args` 是否指向正确二进制与子命令名（改了 `CommandName` 后要重新 `enable`）。

**命令暴露得太多了？** 用 `Selectors` 白名单收窄（§8.1），并记得 `InheritedFlagSelector: cobramcp.NoFlags`。

**同名 flag 和位置参数冲突？** 注册期会直接报错（`positional argument %q collides with a flag of the same name`）——改位置参数名或 flag 名，改名比调 selector 划算。

**编辑器配置写坏了怎么回滚？** 用同目录的 `<名字>.backup.json` 覆盖回去，或 `my-cli mcp <editor> disable` 删掉条目（幂等，不存在也只告警）。

**`mcp start` 日志污染协议？** stdio 下 stdout 是协议通道。cobramcp 的 CLI 子命令已把日志装到 stderr；自查业务代码里的 `fmt.Println` 同理。

**进程内模型没有日志？** 它不安装任何后端，未装配时是静默 nop——要日志就在宿主里经 `bald/log` 装配（`SetLogger`）。

## 相关文档

- 设计论证（边界与契约）：`docs/devel/zh-CN/Bald Cobra MCP 桥接设计.md`
- 模块内部设计：`cobramcp/docs/design.md`
- 可运行样例：`cobramcp/examples/`（`task` 最小示例、`positional-args` / `positional-args-mcp-server` 位置参数四模式）
- 日志契约：`docs/guide/zh-CN/Bald 日志使用.md`
- 健康检查与就绪：`docs/guide/zh-CN/Bald 健康检查.md`
