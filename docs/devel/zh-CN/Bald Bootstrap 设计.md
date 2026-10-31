# Bald Bootstrap 设计

> Title: bootstrap——契约段到组件的纯构造层 + 配置装载内核
>
> Author(s): bald 团队
>
> Last updated: 2026-10-30
>
> Status: Accepted（对应实现 `bootstrap` module）

## 摘要

`bootstrap` 只回答一个问题：**给定契约的某一段，怎么造出对应的组件**。

它提供三类东西：

| 类别 | 内容 |
|---|---|
| 契约段 → 组件构造器 | 九个配置源工厂（`ConfigProvider`）、协议服务器工厂（`ServerProvider`）、日志 Options 映射、gRPC health 叠加器 |
| 配置装载内核 | `bootstrap/config` 子包的命名层模型（`Layer` / `Load` / `Store`），负责层优先级、热更新与快照隔离 |
| 契约 → 后端 Option 的薄适配 | 让 `bconfig/*`、`log/bslog` 等后端包保持**零契约依赖** |

**装配编排不在这里**：段的消费顺序、阶段、依赖与停机序由 `pkg/appkit` 的声明式引擎负责（见《[Bald 声明式装配设计](./Bald%20声明式装配设计.md)》与 guide《[Provider 接入](../../guide/zh-CN/Provider%20接入%20-%20contract.AppProvider%20与段%20Provider.md)》）。`bootstrap` 不 import `appkit`，也没有全局注册表：业务自己实现的 config 段 Provider 按需组合本包的工厂。

设计原则与全仓一致：**显式构造，不用 `init()` + blank import**——调用方在代码里选择组件，依赖图全程可见。

## 根包：契约段到组件

### ConfigProvider 与九个配置源

```go
type ConfigProvider func(ctx context.Context, cfg *bootstrapv1.BootstrapConfig) (*config.Layer, func(), error)
```

返回值语义是整个模块最细的契约：

- **layer 为 nil** 表示「该源未在契约中配置」，调用方跳过（非错误）；
- **layer.Reader 为 nil** 是装配错误；
- **Watch=true 但 Reader 不实现 `bconfig.ValueWatcher`** 是装配错误；
- **cleanup** 释放 provider 持有的资源（含 reader 本身），可为 nil。

「nil = 跳过」让「注册能力」与「启用能力」分离：代码声明支持哪些源，契约决定启用哪些。把「用户没配 nacos」当错误，等于强迫每个应用配置全部九个源。

九个工厂同构（`env.go`/`file.go`/`http.go`/`apollo.go`/`consul.go`/`etcd.go`/`nacos.go`/`vault.go`/`kubernetes.go`）：

```go
func XxxProvider() ConfigProvider {
	return func(_ context.Context, cfg *bootstrapv1.BootstrapConfig) (*config.Layer, func(), error) {
		c := cfg.GetConfig().GetXxx()
		if c == nil {
			return nil, nil, nil // 未配置该源，跳过
		}
		src, err := xxx.New(opts...) // 契约字段 → 源 Option 逐项映射
		if err != nil {
			return nil, nil, fmt.Errorf("bootstrap: build xxx source: %w", err)
		}
		return &config.Layer{Reader: src, Name: "xxx", Watch: true}, cleanup, nil
	}
}
```

**适配层的存在理由**：provider 知道契约字段与后端 Option 两边的形状，所以 `bconfig/nacos` 等源包无需 import bconf——源层保持零契约依赖。没有这层，九个源包都要依赖 proto 生成物，依赖图立刻变坏。

三个正交语义轴：

| 源 | Watch | 层格式 | cleanup |
|---|---|---|---|
| env | false（静态，无 ValueWatcher） | 回退 yaml | nil |
| file | 契约 `watch` 字段 | 契约 format > 扩展名推断 > yaml | 关闭文件源 |
| apollo | true（原生推送） | yaml 回退 | nil（agollo 无关闭接口） |
| consul | true（watch plan） | yaml 回退 | nil |
| etcd | true（原生 watch） | yaml 回退 | 关闭 client |
| nacos | true（ListenConfig） | 契约 format 透传 | nil |
| vault | true（轮询模拟，缺省 30s） | yaml 回退 | nil |
| kubernetes | true（ConfigMap watch） | yaml 回退 | nil |
| http | true（ETag 条件轮询，缺省 30s） | 契约 format | 关闭 idle 连接 |

vault/http 的轮询模拟 watch 也是正经的 `ValueWatcher` 实现，只是推送由轮询驱动——没有「特例走特殊通道」。

### 日志：只有契约映射，没有构造

`LogOptions(b *bootstrapv1.Logger_Backend) *bslog.Options`（`log.go`）把契约的单后端声明映射为 bslog 选项：多输出优先（`output_paths` 非空优先，否则单值 `output_path`）、轮转零值回退默认（100MB / 7 份 / 30 天 / gzip）。后端实例由 `appkit.StandardLoggerProvider` 构造（见《[AppKit 日志装配设计](./AppKit%20日志装配设计.md)》）。

### 协议服务器：能力声明在代码，参数在契约

```go
type ServerProvider func(ctx context.Context, cfg *bootstrapv1.Server) (transport.Server, func(), error)

bootstrap.GrpcServerProvider(
	bootstrap.WithGRPCRegister(registerGRPCService),
	bootstrap.WithGRPCUnary(grpcOpts...),
	bootstrap.WithGRPCHealth(h, interval),
)
bootstrap.HttpServerProvider(
	bootstrap.WithHTTPHandler(handler),
	bootstrap.WithGatewayRegister(gatewayRegister),
)
```

分工：**地址 / TLS / 超时 / driver 来自契约，路由 / 拦截器 / service 注册 / 探针来自代码**。契约里配了 grpc 段但代码没有 `WithGRPC`，服务器缺失是明确的配置错误，不会静默启动一个空端口。

**gateway 是模式不是服务器**：`HttpServerProvider` 按「注入了 `WithGatewayRegister` 且契约 `server.http.driver` 为 `grpc-gateway`（或留空）」分支——走 `gateway.NewGatewayServer`（REST→gRPC 转码）或纯 `httpserver.NewHTTPServer`。同一个 `server.http` 段只跑一个面。driver 为其他值或两个声明同时留空时构造期报错（显式 > 隐式）。

`GrpcServerProvider` 还负责 reflection 的契约注册（`server.grpc.reflection=true` 时框架注册，业务勿重复注册）。

### health：就绪推送叠加器

`NewGRPCHealthServer(srv, h, interval)` 在 `GRPCServer` 之上叠加「就绪状态推送」：后台轮询 `health.Health` 并 `SetServingStatus`（`""` = 整体服务），使 K8s gRPC 探针与 HTTP `/readyz` 同源。

分层理由：gRPC health 是拉模型，必须由服务端推状态，但「判断业务就绪」不属于协议实现。标准健康服务注册留在 `transport/grpc`，推送留在装配层——详见《[Bald 健康检查设计](./Bald%20健康检查设计.md)》§探针与就绪的归属。

`interval <= 0` 回退 2s；`Serve` 返回即取消轮询，不残留 goroutine。

## 配置装载内核（`bootstrap/config`）

### 命名层模型

`config.Load(Options{...})` 把多层文档合并成一棵树，`Store` 持有结果并提供快照与热更新。

```
加载优先级（高 → 低）：
flag > env > 本地文件 > 契约源层（Layers 首元素最高）> 远程桥（基准）
```

- **远程桥**（`Options.Remote`，远程源桥便捷入口）垫底为基准层；
- **契约源层**（`Options.Layers`）在本地文件之下、远程桥之上；列表首元素优先级最高，合并时从尾向头叠加；
- **本地文件**由 `discoverLocalFile` 按规则查找（显式 `ConfigFile` > `Name-Env.yaml/json` > `Name.yaml/json`，搜 `.` / `./configs` / `$HOME/.config/Name`），缺失不报错（允许纯远程/纯 flag 应用）；
- **env 与 flag** 是静态层。

`Options.Name` 决定 env 前缀（`NAME_`）与多环境文件名发现。appkit 路径下它来自 **`ConfigNamespace`** 而非服务身份 `app.name`：后者可能来自配置文件/env，而 env 前缀又由它派生——构成自指，会让 `NAME_*` 覆盖静默失效。

### 四条行为承诺

1. **快照隔离**：`Settings()` 返回深拷贝，已发布快照不受后续热更新影响；`deepMerge` 每层新建 map（copy-on-write），旧引用天然稳定。
2. **flag 只取显式传入**（`Changed == true`）：零值 flag 压过 env/文件是最常见的反直觉行为，这里不做。引导 flag（`--config`）不落树——它由装载器自己消费，且契约顶层有同名 message 字段。
3. **热更新全量重合并**：任一层 watch 触发 → decode → 更新该层缓存 → 全量 `remerge()`。层缓存仍在，结构上保证「上层覆盖不污染底层基准、底层更新不冲掉上层覆盖」。
4. **单层装配超时**（`DefaultLoadTimeout = 10s`）：远端配置中心不可达时快速失败而非阻塞启动；负值禁用（信任源自身超时，如 etcd dial timeout）。

watch 转发（`config.go`）：源关闭通道时退避 1s 重建监听，重连后主动拉一次全量弥补断连期间的变更；解码失败保留旧层缓存但仍触发 `OnChange`——坏状态可观测而非静默。没有 `OnChange` 回调时不建 watch goroutine。

### 职责分离的 Close

`Store.Close` 只停自己的转发 goroutine 并释放自建源（本地文件 watch）；契约源 reader 归 `ConfigProvider` 的 cleanup 释放。**谁构造，谁释放**——同一 reader 被关两次会在停机路径上变成难查的 use-after-close。

### 严格契约校验

`StrictContract` 开启后，`Load` 用 `bconf.FindUnknownKeys` 扫合并树，未知键即报错。这能捕获把 `server.http` 写成 `server.htpp` 这类笔误——此前会被 `DiscardUnknown` 静默丢弃（进程照常启动、行为却不对）。

框架无法区分「用户笔误」与「业务自持键」，故必须由调用方用 `AllowBusinessSections` 声明业务顶层段；契约段**内部**的业务键仍判未知——要放行应把键挪到独立顶层段，而不是让框架猜。

`DeferEnv` 用于两遍合并：第一遍不含 env 层，由文件层解析出配置名后，再经 `Store.MountEnv(name)` 挂载 env 层——同样是为消解「env 前缀依赖配置名、配置名可能来自配置文件」的自指。

## 理由与取舍

### 放弃 blank import + init() 自注册

被放弃方案：`import _ "x/apollo"` + `init()` 注册进全局 map。三处硬伤：依赖不透明（读代码看不出装了什么）、装配顺序由 Go 导入图决定（无法表达层优先级）、与全仓显式装配哲学冲突（`appkit` 的 Provider/Option 全是显式声明）。显式工厂的代价是调用方多写几行，换来依赖图写在代码里、每个测试自有实例。

### 配置侧保留远程源桥，注册中心侧直连

配置侧保留桥（`source.go`）：既有配置源后端生态（etcd/consul/nacos/apollo）成熟，重写没有增量价值。注册中心侧走 `registry/<backend>` 直连，因为 naming 语义（健康检查、实例订阅）与 config 语义差异大，桥接层反而碍事。这个不对称是务实取舍。

### 为什么不把装配也收进来

装配需要知道「段的阶段、依赖、停机序、覆盖检查」，还要持有 `AppKit` 生命周期。把两者塞进一个模块会同时依赖运行期设施与构造期工厂，且让业务无法只取「构造函数」而不背整套引擎。当前分层：`bootstrap` 是纯函数与内核，`appkit` 是编排。

## 实现与验证

- `go test ./...`（bootstrap module）：配置源 provider 矩阵（Unconfigured / ParameterFail / ContractMapping 三类用例，http/vault 另有 httptest 端到端）、config 内核（优先级链、快照隔离、双向热更新、flag 显式语义、坏配置保留旧值、远程桥多 KV 合并）、服务器装配（多选、gateway 模式分支、reflection 开关）、gRPC health 叠加器。
- `_example/bald` 经 appkit 消费本包的配置源工厂与服务器 Provider，e2e 与生产共用同一构造函数。

### 与其他文档的分工

| 文档 | 视角 |
|---|---|
| [Bald 配置系统设计](./Bald%20配置系统设计.md) | 配置系统三环总览；本文是其 bootstrap 环的模块全貌 |
| [Bald 配置源层设计](./Bald%20配置源层设计.md) | `bconfig` 源层专文（本文只消费其 Reader/ValueWatcher） |
| [Bald 配置契约设计](./Bald%20配置契约设计.md) | `bconf` 契约层专文 |
| [Bald 声明式装配设计](./Bald%20声明式装配设计.md) | 段的消费、阶段与停机序（本文不负责） |
| [Bald 健康检查设计](./Bald%20健康检查设计.md) §探针与就绪的归属 | health 域装配的全展开 |
