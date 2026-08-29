# bald proto 配置契约设计文档

> 状态：**阶段 0、阶段 1、阶段 2 均已落地**（`pkg/options` 已废弃）。
> 最后更新：2026-08-29
> 包路径：
> - `api/proto/bald/config/v1/`（Protobuf 契约源文件）
> - `pkg/conf/gen/go/bald/config/v1`（生成代码，勿手改）
> - `pkg/conf`（契约层：默认值、校验、BindFlags flag 绑定、ResolveTLS）
> - `pkg/config`（加载器：四源合并 + 热更新；`Unmarshal` 为两者桥接点）

---

## 1. 背景与问题

改造前，bald 的配置契约是 **Go struct + `mapstructure` tag**：配置文件里的键名靠字符串与
结构体 tag 匹配，`v.Unmarshal(&opts)` 在键名写错、类型不符、嵌套层级变化时**静默失败**
——返回 nil，字段保持零值，服务照常启动但用的不是你以为的配置。

本次改造过程中，这个机制暴露了两个真实缺陷：

| # | 缺陷 | 现象 | 根因 |
|---|---|---|---|
| 1 | 业务 flag 不生效 | `--http.addr` 压不过本地文件 | flag 注册在全局 flagset，未进 viper override 层；且 flag 名带应用名前缀（`--bald-demo.http.addr`），键路径与文件对不上 |
| 2 | 配置文件 `http.tls` 段被忽略 | 配了 TLS 却不启用 HTTPS | `SecureServingOptions` 用 `mapstructure:",squash"` 内嵌 `TLSOptions`，viper 期望的键是 `http.enabled`，而配置文件写的是 `http.tls.enabled` |

两个都不是逻辑 bug，而是**契约缺失**：配置该长什么样，没有任何一处能机器校验。

### 1.1 横向比对

| 项目 | 配置契约 | 加载器 | 消费方式 |
|---|---|---|---|
| bald（改造前） | Go struct + `mapstructure` | viper | 反射 `v.Unmarshal(&opts)` |
| **go-lulu** | **Protobuf**（`bootstrap/conf/proto`） | `protojson` | 强类型 `cfg.GetXxx()` |
| **go-wind-toolkit** | **Protobuf**（生成代码消费 `conf/v1`） | kratos-bootstrap | 强类型 `&conf.AppInfo{}` |
| onexstack | Go struct + `mapstructure` | viper | 反射 |

同仓库的 go-lulu 与 go-wind-toolkit 都已用 proto 作配置契约（YAML 只是载体），
只有 bald 停留在结构体 tag。这是引入的直接动因。

---

## 2. 设计目标与边界

**做什么**：让 Protobuf 成为 bald **框架级配置**的契约（schema 单一事实源）。

**不做什么**（同样重要）：
- **不动加载器**：viper 继续负责 flag / env / 本地文件 / 远程的多源合并与热更新。
  它是 bald 相对 go-lulu 更细的部分（四源优先级、watch 互不污染、多环境），不能丢。
- **不约束业务配置**：业务配置常需要动态 key、灰度、细粒度热更新，绑死 proto 会丧失
  灵活性。业务配置继续用 viper 的 `AllSettings()` 动态能力，`config.Unmarshal` 对
  proto 未声明的段一律忽略（`DiscardUnknown`）。

范围：框架级配置 = `app` / `http` / `grpc` / `log`。

---

## 3. 落地路线（渐进，已完成前两阶段）

| 阶段 | 内容 | 状态 |
|---|---|---|
| 0 | 修复业务 flag 未进 viper override 层（`appkit.Bind`） | ✅ 完成 |
| 1 | proto 只做 schema，加载器不动；`pkg/conf` 提供默认值/校验/适配 | ✅ 完成 |
| 2 | 框架侧全量切 proto（`server` 层直接消费 proto，去掉 `pkg/options` 中间层） | ✅ 完成 |

阶段 2（P1 阶段 2，2026-08-29 落地）关键改动：
- 删除 `pkg/options` 整层（secure/grpc/insecure/tls/helper）；`server` 的
  `NewHTTPServer`/`NewGRPCServerWithRegister`/`NewGatewayServer` 改为直接接受
  `confv1.Http`/`confv1.Grpc` 消息。
- flag 绑定从 `options.AddFlags` 下沉到 `pkg/conf`：`conf.BindFlags(fs, msg, prefix)`
  遍历 proto 字段描述符生成带前缀 flag（替代 `opt.AddFlags`），`appkit.Bind` 对
  `proto.Message` 类型直接调用它。
- TLS Smart Mode 从 `options.TLSOptions` 迁为 `conf.ResolveTLS(cfg *confv1.Tls) (*tls.Config, error)`。
- 校验统一为 `conf.Validate(cfg)`；`log.Options` 仍保留（非 pkg/options，独立在 pkg/log）。

> 全栈 proto 化前提：框架配置、业务配置、API 接口、请求参数、前后端类型均由 proto 生成，
> proto 是全仓唯一契约与类型真相源。onexstack 仅为借鉴实现，无需对其 `IOptions` 对齐。

---

## 4. 契约定义

### 4.1 文件组织

```
api/proto/bald/config/v1/
├── config.proto      # 顶层 Bootstrap
├── app.proto         # App 元数据
├── server.proto      # Http / Grpc / Tls
└── log.proto         # Logger
api/proto/{buf.yaml, buf.gen.yaml}
pkg/conf/gen/go/bald/config/v1/*.pb.go   # 生成产物
```

生成（仅需 `buf` 与 `protoc-gen-go`，无外部依赖，无需联网）：

```bash
cd api/proto && buf generate
```

### 4.2 顶层结构

```proto
message Bootstrap {
  App    app    = 1;
  Http   http   = 2;
  Grpc   grpc   = 3;
  Logger logger = 4 [json_name = "log"];   // 字段名与配置键解耦
}
```

**消息形状严格镜像配置文件结构**，这是键路径四源一致的前提：

| 源 | 写法 | 键路径 |
|---|---|---|
| 配置文件 | `http: { addr: ":8080" }` | `http.addr` |
| 环境变量 | `BALD_DEMO_HTTP_ADDR` | `http.addr` |
| 命令行 flag | `--http.addr` | `http.addr` |
| proto 字段 | `Bootstrap.http.addr` | `http.addr` |

`json_name = "log"` 说明：proto 字段叫 `logger`（Go 侧 `GetLogger()` 语义清晰），
配置键是 `log`（沿用既有配置文件）。两者由 `json_name` 建立映射，不必迁就配置文件改名。

### 4.3 类型选择

超时用 `google.protobuf.Duration`，配置文件写法不变（`"10s"`）。
TLS 用显式子消息 `Http.tls`（键路径 `http.tls.*`），**不参与字段平铺** ——
这正是缺陷 2 的修正。

---

## 5. 桥接实现：`config.Unmarshal`

`pkg/config/proto.go` 是 viper 与 proto 的桥接点。流程：

```
v.AllSettings()  →  类型规范化  →  json.Marshal  →  protojson.Unmarshal  →  proto.Merge
```

### 5.1 为什么必须做类型规范化

`protojson` 是**严格模式**，而 viper 里的值类型取决于它来自哪里：

| 来源 | `timeout` 的实际类型 | protojson 期望 |
|---|---|---|
| 本地 yaml | `string "10s"` | `"10s"` ✅ |
| env（`AutomaticEnv`） | `string "10s"` | `"10s"` ✅ |
| flag（`DurationVar`） | `time.Duration` / 纳秒整数 | `"10s"` ❌ |

直接反序列化会报错。`coerceScalar` 按字段描述符逐个转换：

- 字符串 ↔ 数字 / bool 互转（env 场景一切皆字符串）
- `Duration` 统一格式化为十进制秒 + `s`
- 枚举接受值名与编号

⚠️ **不能用 `time.Duration.String()`**：它把 90s 格式化成 `"1m30s"`，
而 protojson 的 Duration 只接受以 `s` 结尾的十进制秒数。必须用
`strconv.FormatFloat(d.Seconds(), 'f', -1, 64) + "s"` → `"90s"`。

### 5.2 为什么用 `proto.Merge` 而不是直接 Unmarshal

`protojson.Unmarshal(data, msg)` 是**替换**语义：它先重置整个 `msg`，
把 `conf.NewBootstrap()` 填入的默认值全部清掉。

改为「解析到空消息 → `proto.Merge`」，语义才是「配置覆盖默认值」：

```go
parsed := msg.ProtoReflect().New().Interface()
protojson.Unmarshal(data, parsed)   // 解析到空消息
clearPresentLists(msg, parsed)      // 见下
proto.Merge(msg, parsed)            // 合并，未覆盖的字段保留默认值
```

⚠️ **`proto.Merge` 对 repeated 字段是 append，不是替换**。若不处理：

```
默认值 OutputPaths = ["stdout"]  +  配置值 ["stdout"]  →  ["stdout", "stdout"]
```

实际表现就是**日志向 stdout 写两遍**。`clearPresentLists` 在合并前把
「配置中显式出现过的 list 字段」在 dst 侧清空，使其表现为替换。
标量字段无此问题（后值覆盖前值）。

### 5.3 已知限制（proto3 固有限制）

proto3 标量字段没有 presence，无法区分「未配置」与「显式配置为零值」。
因此若某字段默认值非零（如 `http.addr` 默认 `":443"`），配置文件里显式写空字符串
**不会**覆盖它。确实需要三态语义时，在 proto 中用 `optional` 关键字启用 field presence。

---

## 6. 使用方式

### 6.1 标准接入

```go
bootstrap := baldconf.NewBootstrap()
logOpts  := baldlog.NewOptions()

app := appkit.New(
    appkit.Name("bald-demo"),
    appkit.ConfigFile("configs/bald-demo.yaml"),
    appkit.WatchConfigFile(true),

    // 1. 业务 flag 接入 viper override 层（阶段 0 的修复）；
    //    框架级 proto 子消息直接 Bind，flag 改的是同一对象，server 直接消费。
    appkit.Bind("http", bootstrap.GetHttp()),
    appkit.Bind("grpc", bootstrap.GetGrpc()),
    appkit.Bind("", logOpts),      // log.Options 自带 --log.* 前缀

    appkit.Servers(grpcSrv, httpSrv),

    // 2. 按 proto 契约解析（阶段 1/2）
    appkit.BeforeStart(func(ctx context.Context) error {
        if err := baldconfig.Unmarshal(app.Viper(), bootstrap); err != nil {
            return fmt.Errorf("unmarshal config: %w", err)
        }
        if err := baldconf.Validate(bootstrap); err != nil {  // 一次返回所有问题
            return fmt.Errorf("invalid config: %w", err)
        }
        // server 已持有 bootstrap.GetHttp()/GetGrpc() 指针，无需回填。
        setLogger(baldconf.LogOptions(bootstrap.GetLogger()))
        return nil
    }),

    // 3. 热更新同样走 proto 契约（同一指针，server 直接读到新值）
    appkit.OnConfigChange(func(v *viper.Viper) {
        if err := baldconfig.Unmarshal(v, bootstrap); err != nil { return }
    }),
)
```

可运行示例见 [`_example/bald/main.go`](../../../_example/bald/main.go)。

### 6.2 迁移说明

`pkg/options` 已废弃（阶段 2）。server 层直接消费 `confv1.*` proto 消息，
不再有「options → proto」中间层。TLS Smart Mode 由 `conf.ResolveTLS` 提供，
flag 绑定由 `conf.BindFlags` 提供（替代 `options.AddFlags`）。

### 6.3 默认值

**必须先调 `conf.NewBootstrap()`，不要直接 `&confv1.Bootstrap{}`**。
proto3 没有字段级默认值表达，未出现在配置中的段会保持零值；
框架的语义是「未配置 → 用默认值」，默认值由 `NewBootstrap()`（底层 `cfg.Default()`
依据 proto 的 `(defaults.value)` 注解注入）提供，
`config.Unmarshal` 只覆盖配置中显式出现的值。

---

## 7. 日志初始化的两阶段

`--log.*` 的真实取值要等配置加载完才知道，但启动期又需要 Logger。因此分两阶段：

- **阶段 A**（main 开头）：用默认 Options 装一个 Logger，保证启动期有日志；
- **阶段 B**（`BeforeStart`）：按最终配置重建全局 Logger。

⚠️ 改造前 `logOpts.AddFlags(pflag.CommandLine)` 注册后从未 `Parse`，
`--log.level` 等**实际不生效**（注释宣称支持多源配置，但拿不到值）。
现在由 `appkit.Bind("", logOpts)` 接入统一解析。

---

## 8. 阶段 2（已完成，2026-08-29）

`server` 层直接消费 proto，`pkg/options` 已删除。

- `server.NewHTTPServer` / `NewGRPCServerWithRegister` / `NewGatewayServer` 接收
  `confv1.Http` / `confv1.Grpc`；
- `NewXxxOptions()` / `Validate()` 由 proto 生成（`protoc-gen-defaults` 生成 `Defaulter`，
  `protoc-gen-go-redact` 做敏感字段脱敏）；
- 由 proto 导出 JSON Schema 供配置中心做变更校验与灰度下发，见配置中心设计 9.4。

后续可选增强：由 proto 导出 JSON Schema 供配置中心结构化 diff / 审计 / 灰度
（当前 `OnChange` 仅透传 viper，无结构化 diff）。

---

## 9. 并发安全（server 层竞态修复）

事件驱动：`appkit.waitForEndpoints` 会以 10ms 间隔**轮询** `Endpoint()`，
因此 `Start`（errgroup goroutine）与 `Endpoint`（appkit 主 goroutine）、
`Stop`（停机 goroutine）三者天然并发。

### 9.1 已修复的竞态

`go test -race` 曾稳定复现两组竞争，均位于 `pkg/server`：

| 竞争对象 | 写方 | 读方 |
|---|---|---|
| `GRPCServer.ln` / `HTTPServer.ln` 字段 | `Start` | `Endpoint()` |
| `ln` 指向的 listener 内部状态 | `listen()` 构造 | `ln.Addr()`（经 `Extract`） |
| `GRPCServer.readinessCancel` | `Start` | `Stop` |

修复方式：`sync.RWMutex` 保护（读多写少：`Start` 写一次，之后只读）。

⚠️ **关键实现细节**：`Endpoint()` 必须先取快照再解锁，不能在持锁期间调用
`Extract()` —— 后者内部会枚举网卡（`net.Interfaces()`），是相对耗时的系统调用，
持锁执行会阻塞 `Start` 的写入：

```go
s.mu.RLock()
ln := s.ln
s.mu.RUnlock()          // 先释放
if ln != nil { ... }    // 再做 Extract
```

### 9.2 appkit 的 loadConfig 顺序

原实现把 `loadConfig` 放在防重入 CAS **之前**（理由：配置失败不属于「运行中」状态，
不应占用 `running`/`done`）。但这导致两个并发 `Run` 同时执行 `loadConfig`，
并发写 `a.cfg.cfgFile`（pflag 绑定了该字段的地址）与 `a.cfg.v`。

已改为 **CAS 在前、loadConfig 在后**，并用「显式归还 `running` + 不注册 defer close」
等价保住原语义：

```go
if !a.running.CompareAndSwap(false, true) { return ErrAlreadyRunning }
if err := a.loadConfig(); err != nil {
    a.running.Store(false)   // 归还槽位，允许重试
    a.runErr.Store(err)
    return err               // 不 close done：此时 done 尚未"认领"
}
defer a.running.Store(false) // 以下 defer 必须在 loadConfig 成功之后注册
defer close(a.done)
```

配置失败时 `done` 保持打开（与原行为一致），`Err()` 可取到错误，且可再次 `Run` 重试。

### 9.3 回归测试

| 文件 | 覆盖 |
|---|---|
| `pkg/server/server_test.go` | `Start`/`Endpoint`/`Stop` 三者并发；`Stop` 与 `Start` 重叠；listen 失败不泄漏 goroutine |
| `pkg/appkit/appkit_test.go` | 配置失败可回收重入槽；4 路并发 `Run` 恰好 1 成功 + 3 `ErrAlreadyRunning` |

### 9.4 顺带修复

`GRPCServer.Start` 原本先启动 readiness 轮询 goroutine 再 `listen`，
若 `listen` 失败则该 goroutine 无人取消（调用方未必再调 `Stop`），构成 goroutine 泄漏。
已改为 **listen 成功后再启动**轮询。

---

## 10. 代价与权衡（诚实记录）

| 代价 | 说明 |
|---|---|
| 构建工具链 | 改 proto 需 `buf` + `protoc-gen-go`（仅改契约时需要，业务构建不依赖） |
| 依赖 | `google.golang.org/protobuf` 由 indirect 变为 direct 依赖 |
| 与 onexstack 关系 | onexstack 仅为借鉴实现，无需对其 `IOptions` 契约对齐；bald 全栈 proto 化，flag 注册由 `conf.BindFlags`（遍历 proto 描述符）统一处理 |
| 表达力 | proto 无法表达「任意 key」（可用 `map<string, google.protobuf.Value>`，但会丢类型信息）——故动态业务配置仍可用 viper 动态 key |
| 中间层 | 阶段 1 的 proto → options 转换层已在阶段 2 删除，`pkg/options` 整体废弃 |

---

## 11. 测试覆盖

| 文件 | 覆盖内容 |
|---|---|
| `pkg/config/proto_test.go` | 类型规范化（Duration / 字符串数字 / 枚举）、默认值保留、list 替换语义、未知段忽略、错误路径 |
| `pkg/conf/conf_test.go` | 默认值与 options 一致、端到端解析、TLS 键路径回归、校验、往返一致性 |
| `pkg/appkit/bind_test.go` | flag > env > 文件的完整优先级链、键路径对齐、Bind 非法入参、`Join` 幂等 |
| `pkg/server/server_test.go` | 并发安全（见 9.3） |
| `pkg/appkit/appkit_test.go` | 防重入与配置失败重试（见 9.3） |

全量 `go test -race ./...` 零竞争报告。
