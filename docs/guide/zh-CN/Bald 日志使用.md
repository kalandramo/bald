# Bald 日志使用

面向使用者的操作手册：默认行为是什么、接其他后端要做哪几步、报错了怎么修。
设计论证（为什么段 Provider 装配、为什么远端独立 module）见
`docs/devel/zh-CN/Bald 日志设计.md` 与 `AppKit 日志装配设计.md`，本文只讲操作。

## 心智模型：一句话版

**日志经 logger 段 Provider 装配：`std.Standard()` 预置默认 Provider（仅 `slog`
项，零代码零配置）；其余五个后端 = 引一个依赖 + 自声明一个 logger Provider
（Build 里调 contract 包现成部件）+ `backends` 里加一项。段路径末者胜——
后声明的 Provider 整体覆盖 std 默认，装饰器也归 Provider 自持。**

| | 默认路径（std 内置 Provider） | 自声明 logger Provider |
| --- | --- | --- |
| 可用后端 | 仅 `slog` 项 | Build 分派里写谁就有什么 |
| 依赖增量 | 严格为零 | 用哪个后端引哪个 module |
| 配了不支持的 type | fail-fast 教学报错 | 由你的 Build 决定（建议同样 fail-fast） |

## 0. 什么都不做：默认行为

`main.go` 不写任何日志代码（logger 段零声明时引擎回退同一内置 Provider）：

```go
appkit.MustServe(ctx, /* 你的业务 Option */) // 默认 slog，stdout + info 起步
```

调整行为只需 yaml（`backends` 至少一项；项内字段缺失时回退默认）：

```yaml
logger:
  backends:
    - type: slog
      slog:
        level: info                     # debug | info | warn | error
        format: json                    # json | console
        output_paths: ["stdout", "/var/log/myapp/app.log"]  # 多输出复制分流
        rotate:                         # 仅对文件目标生效；零值字段回退默认
          enabled: true                 # 默认 100MB / 7 份 / 30 天 / gzip
          max_size: 100
          max_backups: 7
          max_age: 30
          compress: true
```

## 1. 启用其他后端：自声明 logger Provider

以 Loki 为例（其余后端替换 module 与分派分支即可）：

**步骤 1：引依赖**（远端后端是独立 module，用的人付依赖代价）：

```bash
go get github.com/kalandramo/bald/log/loki
```

**步骤 2：自声明 logger Provider**（三方法实现 `appkit.Provider`；`Provider`
即 `Option`，可直接作 `MustServe` 变参项）：

```go
type loggerProvider struct{}

func (loggerProvider) Segments() []string { return []string{"logger"} }
func (loggerProvider) Apply(d *appkit.Decl) { d.Providers = append(d.Providers, loggerProvider{}) }

func (loggerProvider) Build(ctx context.Context, s appkit.BuildScope) (any, appkit.Cleanup, error) {
    l := s.Config.GetLogger()
    if l == nil { // 阶段 A（契约装载前）：引导 logger，保证启动日志可见
        return bslog.New(baldbootstrap.LogOptions(nil)), nil, nil
    }
    loggers := make([]log.Logger, 0, len(l.GetBackends()))
    var cleanups []func()
    for _, b := range l.GetBackends() {
        switch b.GetType() {
        case "slog":
            lg, c := bslog.NewWithCleanup(baldbootstrap.LogOptions(b))
            loggers, cleanups = append(loggers, lg), append(cleanups, c)
        case "loki": // contract 包现成部件：契约段 → 后端构造
            lg, c, err := lokicontract.Provider(ctx, b)
            if err != nil {
                return nil, nil, err
            }
            loggers, cleanups = append(loggers, lg), append(cleanups, c)
        default:
            return nil, nil, fmt.Errorf("logger: unsupported backend type %q", b.GetType())
        }
    }
    out := loggers[0]
    if len(loggers) > 1 {
        out = log.NewMultiLogger(loggers...) // 多项广播
    }
    merged := func() { for _, c := range cleanups { c() } }
    return log.NewFilterLogger(out, l.GetFilterKeys()...), // filter_keys 出口统一生效
        appkit.Cleanup(func(context.Context) error { merged(); return nil }), nil
}
```

```go
appkit.MustServe(ctx, std.Standard(), loggerProvider{}, ...) // 末者胜：整体覆盖默认
```

`log/{loki,sentry,charm,aliyun,tencent}` 各有一个 `contract.Provider(ctx, b)`
——「契约段 → 后端构造」的现成部件，在你的 Build 分派里按 type 调用即可。
没有 `init()` 自注册——blank import 不会接线，分派必须显式写在你的代码里。
热更重建复用同一 Build（`l == nil` 引导分支照旧要写）；脱敏/固定属性等装饰器
也在此自持（`bslog.Option`）——完整范本（含 `bslog.WithFilter` 掩码 +
`WithAttrs` service.name，行为测试 providers_test.go）见 `_example/bald/providers.go`。

**步骤 3：yaml 声明**（backends 列表加一项，可替换默认 slog 项或并存）：

```yaml
logger:
  backends:
    - type: loki
      loki:
        endpoint: http://loki:3100/loki/api/v1/push
        labels: { app: myapp }
```

三步完成后：项内 `type` 即契约开关，Provider 按名分派构造；热更新
（`WatchConfigFile(true)`）下改 yaml 即重建后端，改坏只记错不杀进程。

## 2. 五后端速查

| 后端 | type | module（`github.com/kalandramo/bald/`…） | 字段 | 停机行为 |
| --- | --- | --- | --- | --- |
| Grafana Loki | `loki` | `log/loki` | **endpoint 必填**；labels（map）；batch_size；flush_interval（毫秒） | 冲刷批量缓冲 |
| Sentry | `sentry` | `log/sentry` | **dsn 必填**；environment；release；server_name | Flush 事件队列 |
| Charmbracelet | `charm` | `log/charm` | level（默认 info）；format（text/json，默认 text）；output_path（默认 stderr） | 文件输出时关文件 |
| 腾讯云 CLS | `tencent` | `log/tencent` | endpoint；topic_id；access_key；access_secret | 冲刷并关闭 producer |
| 阿里云 SLS | `aliyun` | `log/aliyun` | endpoint；project；logstore；access_key；access_secret；security_token | 冲刷并关闭 producer |

yaml 项示例（各后端段名与 type 同名，逐个替换即可）：

```yaml
logger:                            # Sentry：错误追踪
  backends:
    - type: sentry
      sentry:
        dsn: https://xxx@sentry.example.com/1
        environment: production
        release: myapp@1.2.3
---
logger:                            # Charm：开发终端美化输出
  backends:
    - type: charm
      charm: { level: debug, format: text, output_path: stderr }
---
logger:                            # 阿里云 SLS（腾讯云 CLS 同形）
  backends:
    - type: aliyun
      aliyun:
        endpoint: cn-hangzhou.log.aliyuncs.com
        project: my-project
        logstore: app-log
        access_key: ...            # 云凭据建议经环境变量注入，不落明文 yaml
        access_secret: ...
```

## 3. 多后端广播：本地 + 远程双写

`backends` 是清单：多项并存时逐项构造、按构造序广播（每条日志复制分流到全部
后端），每项 level/format 独立：

```yaml
logger:
  filter_keys: ["password", "access_token"]   # 顶层全局脱敏，全部后端生效
  backends:
    - type: slog                             # 本地：stdout 全量排障
      slog: { level: debug, format: console, output_path: stdout }
    - type: loki                             # 远程：level 独立过滤
      loki: { endpoint: http://loki:3100/loki/api/v1/push, labels: { app: myapp } }
```

含非 slog 项时**必须**自声明 logger Provider（默认路径只接受 slog 项，其余报错定位到项）。
任一项构造失败 fail-fast（错误定位到项，启动即停，不静默降级）。

## 4. 报错解读：fail-fast 及修复

**默认路径配了非 slog 项**（没自声明 Provider）：

```text
appkit: logger.backends[0] type "loki": default path supports only "slog";
  remote/custom backends need a logger provider:
    appkit.MustServe(ctx, std.Standard(), myLoggerProvider()) // Segments()=["logger"]
```

修复：照错误里的样子自声明 logger Provider，就是第 1 节的步骤 2。绝不会静默降级为 slog。

**backends 空清单**（段存在但一项没写）：

```text
appkit: logger.backends is empty (declare at least one backend, e.g. type "slog")
```

修复：至少声明一项（通常是把默认 slog 项写回去）。

**Provider Build 内构造失败**（yaml 配错，错误定位到失败项，如 loki 缺 endpoint）：

```text
log/loki/contract: endpoint is required
```

修复：按报错补 yaml 字段（自声明 Provider 可自带自己的报错前缀）。启动期
装配 fail-fast；热更新路径改坏则试装载/校验失败直接丢弃本次变更、重建失败
仅记错误日志——两种情况旧 logger 都继续服役，进程不退。

## 5. 自定义后端

后端构造函数的签名与内置 contract 部件完全相同：

```go
func(ctx context.Context, b *bootstrapv1.Logger_Backend) (log.Logger, func(), error)
```

把它放进自声明 Provider 的 Build 分派里，`type: mylog` 项即契约可达：

```go
case "mylog":
    l := mylog.New()                         // 你的实现，实现 log.Logger 六方法接口
    loggers = append(loggers, l)
    cleanups = append(cleanups, func() { _ = l.Close() }) // cleanup 可为 nil（引擎兜底为空函数）
```

段内配置可复用契约已有段（如 `b.GetSlog()`），也可以不读段纯代码构造。

## 6. 边界与注意事项

- **装饰器归 Provider 自持（`WithLogDecorators` 已取消导出）**：要脱敏/固定
  属性，在自声明 Provider 里把 `bslog.Option`（`bslog.WithFilter`、
  `bslog.WithAttrs`）传给 `bslog.New`/`bslog.NewWithCleanup`，阶段 A 引导
  分支同样传入——范本 `_example/bald/providers.go`。
- **`filter_keys` 是顶层字段**，与后端清单正交，对全部后端统一生效（出口统一包
  `NewFilterLogger`）。
- **云凭据（access_key/dsn 等）建议经环境变量注入**：配置源 flag > env > yaml
  的优先级天然支持凭据与代码仓库分离。
- 停机顺序：cleanup 按构造序正放；热切换后端时旧钩子同步兑现，带缓冲后端
  （loki 尾批）不丢日志。
- **未实现的后端不占契约**：zap/zerolog 等曾是预留段，现已从契约删除——
  实现一个新后端时随实现补契约段（PR 走「实现 + 契约」一体）。
