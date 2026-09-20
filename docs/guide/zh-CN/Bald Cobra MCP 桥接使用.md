# Bald Cobra MCP 桥接使用

面向使用者的操作手册：怎么把已有 Cobra CLI 接成 MCP 服务、两种执行模型怎么选、
自带的 `mcp` 子命令各干什么、哪些命令会变成工具、入参 schema 长什么样、怎么用
selector 收窄暴露面、编辑器 `enable` 到底写了什么、报错怎么查。设计论证（为什么
桥接层独立成 module、为什么日志与信号不归它、为什么必须先修 `transport/mcp`
生命周期）见 `docs/devel/zh-CN/Bald Cobra MCP 桥接设计.md`，本文只讲操作。

## 心智模型：一句话版

**`cobramcp` 只回答「工具从哪来」——把 Cobra 命令树翻译成 MCP 工具（含 JSON
Schema）；协议、监听、停机在 `bald/transport/mcp`；日志在 `bald/log`；进程信号与
生命周期编排在宿主（appkit / bootstrap）。**

| | 子进程模型（`cobramcp.Command`） | 进程内模型（`cobramcp.NewMCPServer`） |
| --- | --- | --- |
| 接入方式 | 根命令上挂一行 `AddCommand(cobramcp.Command(cfg))` | 构造 `MCPServer` 后交宿主启动 |
| 一次工具调用怎么执行 | 把入参还原成 CLI 参数，**重新执行自身二进制** | 在**当前进程**内执行（每次调用新建命令树） |
| 传输 | stdio（`mcp start`）/ SSE（`mcp stream`）/ REST（`mcp rest`） | SSE（`MCPOptions.Addr`） |
| 适用 | 命令本身自洽、想零改造暴露既有 CLI | 命令依赖进程内状态（DB 连接、缓存、客户端） |
| 生命周期 | 自带 `mcp start/stream/rest` 子命令自行管理 | 交宿主，满足 `transport.Server` 契约 |
| 日志后端 | CLI 子命令自动装 stderr 后端 | 不装，由宿主经 `bald/log` 决定 |

## 0. 引依赖

```bash
go get github.com/kalandramo/bald/cobramcp
```

依赖方向单向：`cobramcp → transport/mcp → bald/log`，反向不存在——只想要 MCP 传输的
消费者不会被拖入 cobra/pflag/jsonschema。

> `transport/mcp` 与 `cobramcp` 目前均**未打 tag**（首版计划为 `transport/mcp/v0.1.0`
> → `cobramcp/v0.1.0`，后者 require 前置 tag 的真实版本）。在此之前本地接入需用
> `replace` 指向本仓目录，不能直接 `go get`。

## 1. 五分钟接入（子进程模型）

```go
package main

import (
    "os"

    "github.com/kalandramo/bald/cobramcp"
)

func main() {
    rootCmd := buildRootCmd()                 // 你已有的 Cobra 命令树
    rootCmd.AddCommand(cobramcp.Command(nil)) // 默认子命令名 "mcp"

    if err := rootCmd.Execute(); err != nil {
        os.Exit(1)
    }
}
```

```bash
my-cli mcp tools                                   # 导出工具清单到 ./mcp-tools.json
my-cli mcp cursor enable                           # 写 Cursor 配置
my-cli mcp vscode enable                           # 写 VSCode 配置
my-cli mcp start                                   # 以 stdio 起 MCP 服务（由编辑器拉起）
my-cli mcp stream --host 127.0.0.1 --port 8080     # 以 SSE 起服务，便于远端/容器调试
```

> 子命令名默认 `mcp`。若你的 CLI 已经有一个叫 `mcp` 的业务命令，用
> `Config.CommandName`（如 `"agent"`）改名——命令树、编辑器配置写入、内建过滤
> 三处会自动跟随。

## 2. 自带子命令参考

`cobramcp.Command(nil)` 会往根命令挂这些子命令（`--log-level` 只作用于**该次 CLI
运行的日志后端**，取值 `debug`/`info`/`warn`/`error`，大小写不敏感，未知值回退
`info`）：

| 子命令 | 作用 | 参数 |
| --- | --- | --- |
| `mcp start` | 以 stdio 暴露命令为工具（编辑器拉起的形态） | `--log-level` |
| `mcp stream` | 以 SSE 暴露，供远端/容器接入 | `--log-level`、`--host`（默认 `""` 全部接口）、`--port`（默认 `8080`） |
| `mcp rest` | 以普通 HTTP JSON API 暴露（非 MCP 协议） | `--log-level`、`--host`、`--port`（默认 `8080`）、`--base-url`（仅用于启动日志） |
| `mcp tools` | 导出工具定义为 `./mcp-tools.json` | `--log-level` |
| `mcp vscode enable\|disable\|list` | 管理 VSCode 配置 | 见下表 |
| `mcp cursor enable\|disable\|list` | 管理 Cursor 配置 | 见下表 |

> **不支持 Claude Desktop**：cobramcp 不提供 `mcp claude` 子命令，也不读写
> `claude_desktop_config.json`。需要接入 Claude 时请自行手工编辑其配置。

编辑器三组子命令的参数：

| 子命令 | 参数 | 说明 |
| --- | --- | --- |
| `enable` | `--config-path` | 覆盖配置文件路径（默认按平台推导，见第 8 节） |
| | `--server-name` | 写入的 server 名（默认取**可执行文件名**去扩展名） |
| | `--env` / `-e` | `KEY=value`，可重复；与 `Config.DefaultEnv` 合并，同名时 **`--env` 优先** |
| | `--workspace` | 仅 vscode/cursor：写工作区 `.vscode/mcp.json` / `.cursor/mcp.json` 而不是用户级配置 |
| | `--log-level` | 仅 `enable` 有 |
| `disable` | `--config-path`、`--server-name`、`--workspace` | 幂等：条目不存在时只告警，退出码仍是 0 |
| `list` | `--config-path`、`--workspace` | 打印该配置文件里的全部 server；**顺序不保证**（map 遍历） |

> `mcp tools` 的产物写**当前工作目录**的 `mcp-tools.json`，不是二进制所在目录——在
> 别的目录执行时要留意文件落点。调试与测试可直接 import `cobramcp/test`，它提供了
> `GetTools` / `GetInputSchema` / `ToolNames` 等断言助手。

## 3. 哪些命令会变成工具

先过滤、再选匹配，规则如下（前者不可被 selector 绕过）：

| 阶段 | 规则 |
| --- | --- |
| 安全过滤（恒生效） | `Hidden` 或 `Deprecated` 的命令排除；没有 `Run`/`RunE`/`PreRun`/`PreRunE` 的命令排除 |
| 内建排除（恒生效） | `mcp`（或用 `CommandName` 改的名）命令组、`help`、`completion` |
| flag 过滤（恒生效） | `--help`、`Hidden`、`Deprecated` 的 flag 一律不进 schema |
| selector | 按声明顺序取**第一个命中**的 `CmdSelector`；都不命中则该命令不暴露 |

命名与描述：

- **子进程模型**工具名 = `<根命令名>_<子命令路径>`，段间空格换下划线，例如
  `my-cli sre open` → `my-cli_sre_open`（连字符原样保留）；`Config.ToolNamePrefix`
  可替换根命令名那段（应对 MCP 的 64 字符工具名上限等）。
- **进程内模型**工具名不含根命令名（`sre_open`），因为根命令名可能含 MCP 工具名
  非法字符（如 `/`）。
- 工具描述取 `cmd.Long`，为空回退 `cmd.Short`，再回退 `Execute the <name> command`；
  `cmd.Example` 非空会追加为 `Examples:` 段落。

> 内建排除用的是**路径子串匹配**（`AllowCmdsContaining(commandName, "help",
> "completion")`），不是精确命令名：路径里含 `help` 的命令（例如叫 `helper`）会
> 被一并排除。反过来，把 `CommandName` 改成 `agent` 后，业务自己的 `mcp` 命令就
> 不再被内建规则挡住——这正是改名场景想要的（业务 `mcp` 命令可以照常暴露），但
> 需要知情。

## 4. 工具入参 schema：扁平结构

**所有参数都在顶层，没有嵌套的 `flags` / `args` 子对象**——LLM 只需按
`inputSchema.properties` 逐项填：

```json
{
  "type": "object",
  "properties": {
    "namespace": { "type": "string", "description": "namespace to use" },
    "level":     { "type": "integer", "default": 1 },
    "module":    { "type": "string", "description": "The on-call module name (e.g. 'bke')" }
  },
  "required": ["module"],
  "additionalProperties": { "not": {} }
}
```

> `additionalProperties` 用 `{"not":{}}` 表达「不允许任何额外字段」（等价于
> `false` 的语义），模型多传字段会被判为不合法。

flag → schema 类型映射（`--help`/隐藏/废弃 flag 永不出现）：

| pflag 类型 | JSON Schema |
| --- | --- |
| `bool` | `boolean` |
| `int`/`int8..64`/`uint`/`uint8..64`/`count` | `integer` |
| `float32`/`float64` | `number` |
| `string` | `string`（可用 flag 注解 `jsonschema` 覆盖成任意子 schema） |
| `stringSlice`/`stringArray` | `array<string>` |
| `intSlice`/`int32Slice`/`int64Slice`/`uintSlice` | `array<integer>` |
| `float32Slice`/`float64Slice` | `array<number>` |
| `boolSlice` | `array<boolean>` |
| `stringToString` | `object<string>`（描述追加 `(format: key-value pairs)`） |
| `stringToInt`/`stringToInt64` | `object<integer>` |
| `duration` | `string` + Go duration 正则，描述标注 `'10s'`/`'2h45m'` |
| `ip` / `ipNet` / `ipMask` / `bytesHex` / `bytesBase64` | `string` + 对应正则或格式说明 |
| 未知类型 | `string`，描述追加 `(type: <原类型>)` |

- **默认值**：从 `flag.DefValue` 写入 `default`（空字符串不写；数组按 pflag 的
  `[a,b]` 形式解析，对象按 `k=v` 解析）。
- **必填**：`cmd.MarkFlagRequired("x")` 的 flag 会进 `required`。
- **位置参数**：从 `cmd.Use` 解析，`<name>` 必填、`[name]` 可选、末尾 `...` 变参
  （schema 为 `array<string>`）；`[flags]` 这个 cobra 哨兵会被忽略。参数名统一
  转小写、非字母数字换 `_`。

位置参数的描述默认由 token 生成（英文），建议用注解改写——**下标从 0 开始**：

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

## 5. 注解：告诉模型工具语义

写进 `cmd.Annotations`，同名 flag/arg 注解互不冲突（键名不同）：

| 注解常量 | 键 | 作用 |
| --- | --- | --- |
| `AnnotationTitle` | `title` | 人类可读标题 |
| `AnnotationReadOnly` | `readOnlyHint` | 只读、不改环境 |
| `AnnotationDestructive` | `destructiveHint` | 可能造成破坏（仅当只读为 false 时有意义） |
| `AnnotationIdempotent` | `idempotentHint` | 重复调用无额外副作用 |
| `AnnotationOpenWorld` | `openWorldHint` | 会与外部实体交互 |

布尔值用 `strconv.ParseBool` 解析（`"1"`/`"true"`/`"0"`/`"false"` 等均可），
解析失败只告警并跳过该键，不会让工具注册失败。

## 6. 收窄暴露面：Selector 与 Middleware

默认（`Config.Selectors` 为空）等价于一个「全收」selector。典型场景是**只暴露读
命令、并给危险命令脱敏**：

```go
config := &cobramcp.Config{
    Selectors: []cobramcp.Selector{
        {
            CmdSelector:           cobramcp.AllowCmdsContaining("get", "list", "status"),
            LocalFlagSelector:     cobramcp.ExcludeFlags("token", "secret", "kubeconfig"),
            InheritedFlagSelector: cobramcp.NoFlags, // 排除 persistent flags
            Middleware: func(ctx context.Context, req mcp.CallToolRequest, in cobramcp.ToolInput, next cobramcp.ExecuteFunc) (*mcp.CallToolResult, cobramcp.ToolOutput, error) {
                ctx, cancel := context.WithTimeout(ctx, time.Minute)
                defer cancel()
                return next(ctx, req, in)
            },
        },
    },
}
```

可直接使用的 selector 构造器：

| 构造器 | 匹配对象 | 语义 |
| --- | --- | --- |
| `AllowCmdsContaining(...)` / `ExcludeCmdsContaining(...)` | `cmd.CommandPath()` 子串 | 路径**含**任一短语 |
| `AllowCmds(...)` / `ExcludeCmds(...)` | `cmd.CommandPath()` 全等 | 路径**等于**其中之一 |
| `AllowFlags(...)` / `ExcludeFlags(...)` | `pflag.Flag.Name` | flag 名白/黑名单 |
| `NoFlags` | — | 排除全部 flag |

要点：

- 匹配的是**完整命令路径**（含根命令名），不是工具名。
- flag 分两组：`LocalFlagSelector` 管命令自身 flag，`InheritedFlagSelector` 管
  persistent flag——想让工具入参干净，`InheritedFlagSelector: cobramcp.NoFlags`
  是最常用的一行。
- 多个 selector 时**第一个命中的生效**，后面不再评估；因此约定「具体规则在前、
  兜底规则在后」。
- `Middleware` 包在真正的执行外面，可做超时、日志、结果改写、错误转换；执行本身
  已自带 panic 恢复（panic 会变成工具错误结果）。

## 7. 进程内模型：`NewMCPServer`

命令依赖进程内状态时用这一形态——工具调用不再 fork 自身，而是直接在当前进程里
跑一棵**全新**的命令树：

```go
srv, err := cobramcp.NewMCPServer(
    cobramcp.MCPOptions{
        Enabled: true,
        Addr:    ":8090",
        Name:    "myapp",   // 默认取根命令名
        Version: "1.0.0",
        BaseURL: "https://api.example.com", // 可选：SSE 对外基址（在代理后面时用）
    },
    func() *cobra.Command { return buildRootCmd() }, // cmdFactory：必须非 nil、每次返回非 nil
    cobramcp.WithSelectors(cobramcp.Selector{
        CmdSelector:       cobramcp.AllowCmdsContaining("get", "status"),
        LocalFlagSelector: cobramcp.ExcludeFlags("token"),
    }),
)
if err != nil {
    return err
}
return srv.Start(ctx) // 阻塞至 ctx 取消
```

行为要点：

- **`cmdFactory` 的语义**：构造时调用一次（只做只读遍历，取 schema）；此后**每次
  工具调用再调用一次**生成全新命令树。因此不存在 flag 值、Options 结构、闭包变量
  在调用之间串味的问题——**并发调用天然安全，无需加锁，也无需 reset**。
- **工具调用的 ctx 取消不向内传播**：执行用 `context.WithoutCancel` 保留值
  （trace/租户等）但屏蔽取消，避免上游「拿到结果就 cancel」把已经成功的工作打断。
- `Enabled=false` 时 `Start` 直接返回 `nil`（特性开关）；`Enabled=true` 而
  `Addr==""` 时 `Start` **立即报错**（`MCPOptions.Addr must not be empty when
  Enabled is true`）。
- `srv.Tools()` 看工具清单；`srv.Transport()` 拿到底层 `transport/mcp` 服务端，
  用于注册**非 Cobra 来源**的工具。
- 与 appkit 装配：`MCPServer` 满足 `transport.Server` 契约（`Start` 阻塞至 ctx
  取消、`Stop(ctx)` 优雅停机、`Endpoint()` 在 Start 后返回真实监听地址），可直接
  `appkit.WithExtraServers(srv)` 交宿主并行启停；进程信号不要自己抓。

> 现状：装配层**尚无 MCP 专用装配项**（bconf 契约里存在 `server.mcp`
> 段：`name`/`version`/`address`，但 `bootstrap`/`appkit` 暂无消费方）。因此进程内
> 模型目前是手工构造 + `WithExtraServers`，配置驱动的装配待后续补齐。

## 8. 编辑器集成：`enable` 写了什么

`enable` 写入的条目形态（`command` 是**当前可执行文件绝对路径**，`args` 是「复用
当前调用路径 + `start`」，因此 `my-cli mcp cursor enable` 与
`my-cli agent cursor enable` 写出的 args 前缀不同）：

```json
{
  "mcpServers": {
    "my-cli": {
      "type": "stdio",
      "command": "/usr/local/bin/my-cli",
      "args": ["mcp", "start"],
      "env": { "PATH": "/opt/homebrew/bin:/usr/bin:/bin" }
    }
  }
}
```

| 编辑器 | 用户级配置文件 | 顶层字段 | 备注 |
| --- | --- | --- | --- |
| VSCode | macOS `~/Library/Application Support/Code/User/mcp.json`；Linux `~/.config/Code/User/mcp.json`；Windows `%USERPROFILE%\AppData\Roaming\Code\User\mcp.json` | `servers` | `--workspace` 时写 `<cwd>/.vscode/mcp.json`；需 Copilot Agent Mode |
| Cursor | 各平台 `~/.cursor/mcp.json`（Windows `%USERPROFILE%\.cursor\mcp.json`） | `mcpServers` | `--workspace` 时写 `<cwd>/.cursor/mcp.json` |

> Claude Desktop 不在支持范围——`enable` 不会读写其配置文件，请手工编辑。

`--config-path` 可覆盖上表任一默认路径（推荐在容器/CI/多配置场景显式指定）。

环境变量：编辑器拉起的子进程环境很干净（macOS 上 PATH 常常只有
`/usr/bin:/bin:/usr/sbin:/sbin`），`helm`/`kubectl`/`docker` 可能找不到。用
`Config.DefaultEnv` 在 `enable` 时把当前 PATH 带进去：

```go
config := &cobramcp.Config{
    DefaultEnv: map[string]string{"PATH": os.Getenv("PATH")},
}
```

合并规则：先铺 `DefaultEnv`，再用 `--env` 覆盖同名键；合并结果为空时 JSON 里
**不出现** `env` 键。

写入行为与陷阱（均为代码可证实的边界）：

- **每次保存前会备份**：已存在的配置文件复制为同目录 `<名字>.backup.json`
  （如 `mcp.backup.json`）。同名备份**每次覆盖**，只保留「上一次
  保存前」的状态。
- **文件不存在会被创建**（自动 `MkdirAll` + 写 `0644`）；`disable`/`list` 不会创建。
- 文件必须是**严格 JSON**：带注释/尾逗号的 JSONC 会直接报
  `failed to parse configuration file at "...": invalid JSON format`，不会静默覆盖。
- 保存是**整份重新序列化**：结构体未建模的顶层键会被丢弃——别把不相关配置塞进这些
  文件。
- `enable` 遇到同名 server 只打印 `⚠️  MCP server "x" already exists and will be
  overwritten` 然后覆盖，**没有二次确认**；server 名默认取可执行文件名，可用
  `--server-name` 固定。
- 改完配置需**重启编辑器**（或其 MCP 面板）才会重新拉起进程。

## 9. REST 接口：`mcp rest`

不带 MCP 协议、给脚本/网关直接调用的形态（每条命令一个路由，路径就是工具名）：

```bash
my-cli mcp rest --port 9090

curl -s -X POST http://localhost:9090/my-cli_oncall \
  -H 'Content-Type: application/json' \
  -d '{"module":"bke","title":"pod crash"}'
```

| 项 | 约定 |
| --- | --- |
| 请求 | `POST /{toolName}`；body 是**扁平 JSON**（flag 与位置参数同层），空 body 视为零参数 |
| 成功响应 | HTTP 200 + `{"stdout":"...","stderr":"...","exitCode":0}`——**命令失败也是 200**，要看 `exitCode` |
| HTTP 400 | body 不是合法 JSON |
| HTTP 404 | 工具名不存在 |
| HTTP 405 | 用了 POST 以外的方法 |
| HTTP 500 | 子进程无法拉起 |

## 10. 边界与注意事项

- **`mcp start`（stdio）下 stdout 是协议通道**：任何往 stdout 打的日志都会污染协议，
  所以 cobramcp 的 CLI 子命令强制把日志装到 stderr；自查代码里的 `fmt.Println` 同理。
- **日志归宿主**：进程内模型不安装任何后端，未装配时是静默 nop——要日志就在宿主里
  经 `bald/log` 装配（`SetLogger`），不要在库层装全局后端。
- **信号与停机**：cobramcp 不捕获 SIGINT/SIGTERM；CLI 形态下 stdio 对端断开
  （stdin EOF）即结束服务，宿主形态下由 appkit 在 ctx 取消后调 `Stop`。CLI 子命令
  停机等待上限为 5 秒（`shutdownTimeout`）。
- **工具失败怎么呈现**：命令返回非零退出码时，stdout+stderr 合并作为工具错误结果
  返回给模型；MCP 协议层不会把它当传输错误。
- **`Endpoint()` 只在 Start 之后有值**：这是 `transport/mcp` 的既定语义（`:0` →
  系统分配端口），Start 之前为空字符串。

## 11. FAQ

**为什么工具调用看不到效果，编辑器里也没报错？** 先跑 `my-cli mcp tools` 确认工具
数量与名字；再看 `enable` 写出的 `command`/`args` 是否指向正确二进制与子命令名
（改了 `CommandName` 后要重新 `enable`）。

**命令暴露得太多了怎么办？** 用 `Selectors` 白名单收窄（第 6 节），并记得
`InheritedFlagSelector: cobramcp.NoFlags` 去掉 persistent flags。

**同名 flag 和位置参数冲突？** 位置参数名会做小写/下划线归一化后与 flag 名同层
出现，而 schema 属性的写入顺序是「本地 flag → 继承 flag → 位置参数」，因此同名时
**位置参数会覆盖 flag 的那份 schema**；更麻烦的是执行期还原命令行时，该键会同时被
当成 flag 与位置参数（既出 `--name value` 又追加一个位置值）。**务必避免重名**，
改名比调 selector 划算。

**想注册不是 Cobra 命令的工具？** 用 `srv.Transport()` 拿到底层 `transport/mcp`
服务端，直接 `RegisterHandler`。

**编辑器配置写坏了怎么回滚？** 用同目录的 `<名字>.backup.json` 覆盖回去，或
`my-cli mcp <editor> disable` 删掉条目（幂等，不存在也只告警）。

## 相关文档

- 设计论证：`docs/devel/zh-CN/Bald Cobra MCP 桥接设计.md`
- 传输层：`transport/mcp`（`Start` 同步绑定端口、`Endpoint()` 返回真实地址、
  `Done()` 感知对端断开）
- 日志契约：`docs/guide/zh-CN/Bald 日志使用.md`
- 健康检查与就绪：`docs/guide/zh-CN/Bald 健康检查.md`
