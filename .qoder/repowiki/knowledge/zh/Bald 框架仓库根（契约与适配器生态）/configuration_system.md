## 1. 总体方案

Bald 的配置系统由三层组成，职责清晰分离：

- **bconf**（契约层）：用 Protobuf `bootstrap/v1` 定义进程级 BootstrapConfig 的完整结构、默认值与启动前校验。这是「配置形状的唯一真相」。
- **bconfig**（KV 抽象层）：定义 Reader / ReadCloser / Watcher / ValueWatcher / Decoder 等接口，以及优先级回退组合器 FallbackReader；所有具体后端（file/env/etcd/consul/nacos/apollo/http/kubernetes/vault/fs）只实现这些接口。
- **bootstrap**（装配适配层）：把 bconf 契约段映射为各后端包的 Option，并通过 appkit 的 Segment Provider 模型把配置源、日志、HTTP/gRPC 服务器等组件注册到生命周期中。

应用侧通过 AppSpec（`appspec.json`）声明 roles/domains/variants/bootstrap 片段，再由代码生成器产出装配代码，最终在启动期加载 YAML/TOML/JSON/环境变量/远程配置中心等多源配置，合并后反序列化为 proto 消息并校验。热更新路径通过 KV 源的 WatchValue 通道触发重新装载与 Reconcile。

## 2. 关键文件

- `bconf/bootstrap.go` — NewBootstrap() 默认值 + Validate() 启动前校验（App.Id/Name、Server.Addr、Logger.Backends 非空及子段取值域）。
- `bconf/unmarshal.go` — UnmarshalMap()：map → coerce → protojson.Unmarshal → clearPresentLists → proto.Merge，实现「合并语义 + repeated 字段整体替换」；FindUnknownKeys() 检测未声明键。
- `bconf/gen/go/bootstrap/v1/*.proto` — 编译生成的 Go 结构体，是上层唯一依赖的类型。
- `bconfig/bconfig.go` — Reader/Closer/ReadCloser/Watcher/ValueWatcher/Decoder 接口定义。
- `bconfig/fallback.go` — FallbackReader：按优先级顺序 Load，聚合 Close，合并 ValueWatcher 变更并重新 Load 有效值。
- `bconfig/{file,env,etcd,consul,nacos,apollo,http,kubernetes,vault,fs}/` — 各 KV 后端实现，统一暴露 Reader/Watch 或 ValueWatcher。
- `bootstrap/config.go` — ConfigProvider 工厂函数类型：`func(ctx, cfg) (*config.Layer, cleanup, error)`，负责根据契约构造命名配置层。
- `bootstrap/{file,env,etcd,consul,nacos,apollo,http,kubernetes,vault}.go` — 各契约段的 ConfigProvider 实现，把 bootstrapv1.* 段映射为对应后端包 Option。
- `_example/bald/appspec.json` — 示例应用的 AppSpec 声明真相源，包含 bootstrap/logger/database/registry 片段及 variants。
- `_example/bald/configs/bald-demo-api.yaml` — 运行时配置，键名使用 snake_case（由 UnmarshalMap 的 coerceMessage 自动转 JSON name）。
- `_example/bald/internal/bootstrap/providers.go` — 示例覆盖 logger Provider，演示如何通过 appkit.Segment Provider 消费配置段。

## 3. 架构与约定

### 3.1 配置来源与合并顺序

- 多个 bconfig.Reader 通过 FallbackReader 串联，**先传入者优先级更高**（第一个返回非空且无错的数据即生效）。
- 支持两种变更通知模式：
  - `Watcher`：仅发信号，调用方自行 Load 回读。
  - `ValueWatcher`：推送新字节值，FallbackReader.WatchValue 会识别并合并多个子源的通道。
- 任意子源推送变更时，FallbackReader 会重新执行 Load（遵循优先级规则），将新的有效值转发出去——这是热更新的底层机制。
- 只能实现 Watcher 的后端需要在 provider 内部收到信号后 Load 再转推 ValueWatcher，否则会被 FallbackReader 静默忽略（见 Watcher 接口注释中的能力边界说明）。

### 3.2 从 map 到 Proto 的反序列化

UnmarshalMap 的固定流程：

1. 深拷贝入参 map（避免热更新回调中对同一快照多次反序列化互相破坏）。
2. coerceMessage：按 proto 字段描述符做类型规范化（snake_case→JSON name、字符串→Duration/数值等），因为 env/flag 来的值可能是 string 而非 protojson 接受的形态。
3. json.Marshal + protojson.Unmarshal 解析到空消息（DiscardUnknown=true，允许业务自持顶层段存在未知键）。
4. clearPresentLists：对配置中出现过的 repeated 字段清理 msg 中原有值（避免 proto.Merge 的 append 语义导致默认项与新项叠加）。
5. proto.Merge：把解析结果合并进带默认值的 msg（NewBootstrap 填充的默认值得以保留）。

### 3.3 未知键检测

FindUnknownKeys 以 proto 字段描述符为白名单扫描 map，递归检查嵌套 message/list/map。顶级段可通过 allowTop 参数放行（用于业务自持段，如 audit.fallback_tenant）。该函数不修改输入，也不改变 UnmarshalMap 行为，由调用方决定是否 fail-fast。

### 3.4 配置与装配解耦

bootstrap 包只承担「契约段 → 组件构造函数」的薄适配，装配编排（阶段、依赖、停机序）完全交给 pkg/appkit 的声明式引擎。ConfigProvider 返回值语义严格约定：layer=nil 表示未配置（跳过）、cleanup 释放资源、错误短路回滚。

### 3.5 示例应用装配模式

_example/bald 通过 AppSpec 声明 api/worker 两个角色，各自监听不同端口；logger/database/registry 等 bootstrap 片段在 AppSpec 中以 protobuf 等价结构书写，同时提供独立的 YAML 配置文件覆盖。自定义 Provider（如 newLoggerProvider）通过 appkit.Option 注册，重写标准 logger Provider 的行为（加脱敏、service.name attr）。

## 4. 约定与约束

- **契约即 schema**：所有框架级配置必须落在 `bootstrap/v1` 定义的 BootstrapConfig 树内；业务扩展放在 AppSpec 中声明的业务段下，并由 FindUnknownKeys 白名单放行。
- **默认值集中维护**：NewBootstrap() 是唯一默认值来源，Validate() 与 UnmarshalMap 的合并语义都依赖它；新增字段需在两处同步补齐默认值。
- **repeated 字段语义是「替换」**：clearPresentLists 保证配置中出现的列表整体覆盖默认列表，不追加；因此若只想改 logger.backends 中某一项的 format，必须显式写出整项。
- **缺省字段视为未启用**：Validate 对 nil 段直接跳过校验，子段空串多被放行（由装配层回退默认值）；只有明确非空的值才进入取值域校验。
- **proto3 标量字段的零值三态限制**：UnmarshalMap 注释明确指出，proto3 无法区分「未配置」和「显式写零值」，需要三态语义时应改用 optional 关键字。
- **Provider 显式构造**：bootstrap 包禁用 init()+blank import，所有组件通过显式调用选择，依赖图全程可见（见 package doc 的设计原则）。
- **KV 后端零契约依赖**：bconfig 接口不包含任何 bootstrapv1 引用，具体后端（file/env/etcd 等）只实现 Reader/Watcher 等抽象，契约映射集中在 bootstrap 适配层。
- **热更新要求**：能高效传递变更后数值的后端应实现 ValueWatcher；仅能发出信号的后端需自行在 provider 内部 Load 回读后再转推，不能依赖 FallbackReader 自动兼容 Watcher。
- **配置格式无关**：bconfig.Decoder 抽象使 YAML/JSON/TOML/XML 等格式由编码层提供，KV 源只输出原始字节，反序列化统一走 UnmarshalMap。
