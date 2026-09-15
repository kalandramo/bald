# Bald 日志使用

面向使用者的操作手册：默认行为是什么、启用其他后端要做哪几步、报错了怎么修。
设计论证（为什么两级工厂、为什么远端独立 module）见
`docs/devel/zh-CN/Bald 日志设计.md` 与 `AppKit 日志装配设计.md`，本文只讲操作。

## 心智模型：一句话版

**契约只有一个多值配置项 `backends`：默认预置一项 slog（零代码零配置）；
其余五个后端 = 引一个依赖 + 三行注册 + `backends` 里加一项。**

| | 默认路径（零 Option） | 注册表路径（`WithLogRegistry`） |
| --- | --- | --- |
| 可用后端 | 仅 `slog` 项 | 注册了什么就有什么 |
| 依赖增量 | 严格为零 | 用哪个后端引哪个 module |
| 配了不支持的 type | fail-fast 教学报错 | fail-fast 列出已注册名单 |

## 0. 什么都不做：默认行为

`main.go` 不写任何日志代码：

```go
app := appkit.FromBootstrap(cfg) // 默认 slog，stdout + info 起步
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

## 1. 启用其他后端：通用三步

以 Loki 为例（其余后端替换 module 与 type 即可）：

**步骤 1：引依赖**（远端后端是独立 module，用的人付依赖代价）：

```bash
go get github.com/kalandramo/bald/log/loki
```

**步骤 2：main.go 三行注册**：

```go
import (
    "github.com/kalandramo/bald/bootstrap"
    lokicontract "github.com/kalandramo/bald/log/loki/contract"
    "github.com/kalandramo/bald/pkg/appkit"
)

reg := bootstrap.NewLogRegistry()
reg.MustRegister(lokicontract.Type, lokicontract.Provider) // "loki"
app := appkit.FromBootstrap(cfg, appkit.WithLogRegistry(reg))
```

用哪个注册哪个，可同时注册多个（`MustRegister` 逐个调用）。
没有 `init()` 自注册——blank import 不会接线，注册必须显式出现在你的代码里。

**步骤 3：yaml 声明**（backends 列表加一项，可替换默认 slog 项或并存）：

```yaml
logger:
  backends:
    - type: loki
      loki:
        endpoint: http://loki:3100/loki/api/v1/push
        labels: { app: myapp }
```

三步完成后：项内 `type` 即契约开关，装配层按名查表构造；热更新
（`WithWatchConfig(true)`）下改 yaml 即重建后端，改坏只记错不杀进程。

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

含非 slog 项时**必须**走注册表路径（默认路径只接受 slog 项，其余报错定位到项）。
任一项构造失败 fail-fast 并回滚已构造项（带缓冲后端不泄漏）。

## 4. 报错解读：三类 fail-fast 及修复

**默认路径配了远端后端项**（没走步骤 2）：

```text
appkit: logger.backends[0] type "loki": default path supports only "slog";
  remote/custom backends need explicit registration:
    reg := bootstrap.NewLogRegistry()
    reg.MustRegister(lokicontract.Type, lokicontract.Provider) // log/loki/contract
    appkit.FromBootstrap(cfg, appkit.WithLogRegistry(reg))
```

修复：照错误里的三行做，就是第 1 节的步骤 2。绝不会静默降级为 slog。

**注册了但 type 拼错 / 忘注册**（走了步骤 2 但名单没覆盖）：

```text
bootstrap: backends[0]: bootstrap: log provider "loki" not registered (registered: [sentry tencent])
```

修复：对照括号里的已注册名单，补 `MustRegister` 或改 `type` 拼写。

**backends 空清单**（段存在但一项没写）：

```text
appkit: logger.backends is empty (declare at least one backend, e.g. type "slog")
```

修复：至少声明一项（通常是把默认 slog 项写回去）。

## 5. 自定义后端

provider 就是一个函数，签名与内置后端完全相同：

```go
func(ctx context.Context, b *bootstrapv1.Logger_Backend) (log.Logger, func(), error)
```

注册后 `type: mylog` 项即契约可达：

```go
reg := bootstrap.NewLogRegistry()
reg.MustRegister("mylog", func(ctx context.Context, b *bootstrapv1.Logger_Backend) (log.Logger, func(), error) {
    l := mylog.New()                       // 你的实现，实现 log.Logger 六方法接口
    return l, func() { _ = l.Close() }, nil // cleanup 恒非 nil（返回 nil 会被兜底为空函数）
})
app := appkit.FromBootstrap(cfg, appkit.WithLogRegistry(reg))
```

段内配置可复用契约已有段（如 `b.GetSlog()`），也可以不读段纯代码构造。

## 6. 边界与注意事项

- **业务装饰器（`WithLogDecorators`）只对默认路径的 slog 项生效**（deco 是
  `bslog.Option`）。注册表路径要装饰器，注册一个 deco-aware 的 slog provider：
  `reg.MustRegister("slog", decoAwareProvider)`，并 yaml 用 `type: slog` 项。
- **`filter_keys` 是顶层字段**，与后端清单正交，对全部后端统一生效（出口统一包
  `NewFilterLogger`）。
- **云凭据（access_key/dsn 等）建议经环境变量注入**：配置源 flag > env > yaml
  的优先级天然支持凭据与代码仓库分离。
- 停机顺序：cleanup 按构造序正放；热切换后端时旧钩子同步兑现，带缓冲后端
  （loki 尾批）不丢日志。
- **未实现的后端不占契约**：zap/zerolog 等曾是预留段，现已从契约删除——
  实现一个新后端时随实现补契约段（PR 走「实现 + 契约」一体）。
