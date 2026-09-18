# Bald 消息代理设计：Body 装任意类型、Binder 负责落地，四个后端各自独立 module

> Author(s): bald 团队
>
> Last updated: 2026-09-17
>
> Discussion at: 源码 `broker/{broker,message,event,typed_handler,encoding,options,publish,subscriber}.go` 与四个后端 `broker/{kafka,rabbitmq,redis,rocketmq}/`；装配面 `bootstrap/broker.go`、`pkg/appkit/broker.go`；契约 `bconf/proto/bootstrap/v1/broker.proto`
>
> Status: Accepted（契约与四后端已落地、双装配路已通；**三个已知缺口已于 2026-09-18 全部修复**——默认 codec 惰性查表 + fail-fast、`Request` 从契约删除、未实现段 fail-fast，见「兼容性」与「实现与过渡」）

## 摘要

`broker` 是 bald 的消息代理契约包。它定义一件事：**消息怎么进出**。`Message.Body` 是 `any`——发布时装什么类型由业务决定，订阅时怎么落地成具体类型由 `Binder` 决定（`message.go:10`），`TypedHandler[T]` 泛型层让业务直接拿到 `*T`（`typed_handler.go:9`）。四个后端（kafka、rabbitmq、redis、rocketmq）各自住在独立 module 里，每个后端再带一个 `contract/` 子包对接 bconf 契约段。

我们做了三个承诺：

1. **业务只 import 契约 + 所选后端**。`broker` 根 module 只依赖 `encoding`；换后端只改构造那一行，`Publish`/`Subscribe` 调用点不动。
2. **类型化订阅是糖，不是底座**。底层管线只有一种 `Handler func(ctx, Event) error`；`TypedHandler[T]`、`TypedToEventHandler`、泛型 `Subscribe[T]` 是叠在上面的糖——不用糖的人零成本。
3. **配置驱动装配可选**。手写 `kafka.NewBroker(opts...)` 与 `appkit.FromBootstrap(cfg, WithBrokerRegistry(br))` 两条路并存，后者按 bconf 的 `broker` 段查表构建。

这个 module 是 `9ffe811`（2026-09-06，四后端随五注册表与 8 段停机链移植）落地的，移植时修掉了 tracer、统一了 Init→Connect、补了 `derefAnyPointer`。移植时留下三个缺口，**已于 2026-09-18 全部修复**：`DefaultCodec` 在包初始化时求值、恒为 nil（实际序列化静默走 gob 兜底）→ 改惰性查表 + fail-fast；`Request` 在全部五个后端实现里返回 `not implemented` → 从契约删除；bconf 声明了 13 个后端段、只实现 4 个，配了未实现段会被**静默忽略** → 改 fail-fast。

## 背景与动机

### 从 go-wind 移植，修了四件事，留了三个坑

`broker` 与 `transport/{kafka,rabbitmq,redis,rocketmq}` 四件套同批从 go-wind 移植（`9ffe811`，2026-09-06）。移植不是照抄，四处实质修改：

1. **tracer 全删**。go-wind 版把 OpenTelemetry tracer 织进发布/订阅管线；bald 的裁定是可观测走 `contrib/observability-otlp` 装配层，broker 契约不背 SDK。
2. **Init→Connect 统一**。契约装配（`contract.Provider`）一律先 `Init()` 再 `Connect()`，与 database/cache 域同一节奏。
3. **`derefAnyPointer` 先解一层**（`encoding.go:16`）。这是类型化订阅能工作的关键，见「设计」。
4. **rocketmq 的 `gogap/errors` 伪版本 replace**（`rocketmq/go.mod` 末行）。上游 SDK 的间接依赖没有 tag，只能钉伪版本。

留下的三个坑见「兼容性」——它们不是移植失误，是移植时已知、当时选择不动的历史形状。

### 痛点：消息体类型化，是消息代理的第一道坎

没有类型化层时，订阅者拿到的是字节：

```go
// 没有 Binder：业务自己 unmarshal
sub, _ := b.Subscribe("orders", func(ctx context.Context, evt broker.Event) error {
	var o Order
	return json.Unmarshal(evt.Message().Body.([]byte), &o) // 每个业务重复这行
}, nil)
```

有 Binder 时，broker 在管线里替你落地：

```go
// 有 Binder：broker 落地成 *Order，handler 直接用
sub, _ := b.Subscribe("orders", handler, func() any { return new(Order) })
```

泛型糖再进一步，连 Binder 都不用手写：

```go
sub, _ := broker.Subscribe(b, "orders",
	func(ctx context.Context, topic string, headers broker.Headers, o *Order) error {
		return process(ctx, o) // 直接拿到 *Order
	})
```

三层写法对应三类用户：要裸字节的（透传/加密场景）、要具体类型但不用泛型的（go-wind 存量风格）、要泛型糖的（新代码）。契约只保证第一层是底座，上面两层是糖。

### 一句话定性

**契约形状完整、四后端可用、双装配路已通；移植遗留的三个缺口（默认序列化静默降级 gob、Request 空头支票、契约段超集与实现脱节）已于 2026-09-18 修复。**

## 设计

### 契约：八个方法，两个核心

```go
type Broker interface {
	Name() string
	Options() Options
	Address() string
	Init(...Option) error
	Connect() error
	Disconnect() error
	Publish(ctx context.Context, topic string, msg *Message, opts ...PublishOption) error
	Subscribe(topic string, handler Handler, binder Binder, opts ...SubscribeOption) (Subscriber, error)
}
```

（`broker.go:9-33`）

日常用到的只有 `Publish` 和 `Subscribe`。`Init`/`Connect`/`Disconnect` 是生命周期（装配层调），`Name`/`Options`/`Address` 是诊断面。**`Request` 已从契约删除**（原为五后端全部 `not implemented` 的空头支票，零业务调用方时删除是最便宜的破坏窗口），详见「实现与过渡」修复记录。

### Message：Body 是 any，Key/Partition/Offset 是 Kafka 形状的字段

```go
type Message struct {
	ID        string
	Headers   Headers          // map[string]string，随消息传输
	Body      any              // 消息体：发布时装什么，订阅时由 Binder 落地
	Key       string           // Kafka Key / RocketMQ Key / RabbitMQ RoutingKey
	Metadata  Metadata         // map[string]any，进程内元数据，不随消息传输
	Partition int
	Offset    int64
	Msg       any              // 原始消息（kafka.Message、amqp.Delivery…）
}
```

（`message.go:25-48`）

三个裁定值得展开：

**Headers 与 Metadata 分开**。Headers 随消息走线（kafka header、amqp property），值只能是 string；Metadata 只活在进程内（`ExtractContext`/`GetMetadataFromContext`，`message.go:182-195`），值是 any。混用两者是常见错误——想跨进程序传的东西放 Metadata 里，订阅端永远读不到。

**Partition/Offset 是 Kafka 形状**。rabbitmq/redis 后端不填它们。契约选择「为最强的后端定形状、弱的后端留零值」而不是「抽象成 map」——零值字段比 stringly-typed 元数据便宜。

**`Msg` 装原始消息**。订阅端 `event.RawMessage()` 拿到 `kafkaGo.Message` 或 `amqp.Delivery`，需要后端特有能力（如 kafka 的精确 offset 提交）时不用断言 `Body`。

`Clone()` 是浅拷贝但 Headers/Metadata 深复制（`message.go:146-157`）——发布路径 `internalPublish` 先 `Clone` 再替换 Body 为字节（`kafka.go:353-354`），保证原始 Message 不被发送过程污染。

### Binder 与类型化订阅：三层糖

```go
type Binder func() any                          // message.go:10
func Creator[T any]() *T                        // message.go:13

type TypedHandler[T any] func(ctx context.Context, topic string, headers Headers, msg *T) error  // typed_handler.go:9
func TypedToEventHandler[T any](h TypedHandler[T]) Handler                                        // typed_handler.go:12
func Subscribe[T any](broker Broker, topic string, handler TypedHandler[T], opts ...SubscribeOption) (Subscriber, error) // broker.go:39
```

`TypedToEventHandler` 的类型分派（`typed_handler.go:25-32`）：Body 已经是 `*T` 就直接用；是 `T` 值就取地址；都不是就报错。这依赖订阅端先经 Binder 把字节落地成 `*T`——两层配合，缺一不可。

### 编解码：derefAnyPointer 是类型化订阅的地基

订阅端落地类型的实际代码（`kafka/subscriber.go:281-287`）：

```go
if s.binder != nil {
	bm.Body = s.binder()                                            // Body = *Order（装在 any 里）
	if err = broker.Unmarshal(s.b.options.Codec, km.Value, &bm.Body); err != nil {  // 注意 &bm.Body：*any
		...
	}
}
```

`Unmarshal` 收到的是 `*any`，any 里装着 `*Order`。标准 JSON unmarshaler 对 `*any` 会用 `map[string]any` 顶掉原类型——类型化语义就丢了。`derefAnyPointer`（`encoding.go:16-30`）解这一层：发现 `*any` 里装着非 nil 指针，就把该指针交给 codec。**没有这个函数，整个类型化订阅在 JSON codec 下是坏的。**

`Marshal`/`Unmarshal` 的兜底链（`encoding.go:33-95`）：codec 非 nil 用 codec；nil 则 `[]byte`/`string` 直通、其余 gob。这个兜底是缺口的温床，见「兼容性」第 1 条。

### 中间件：发布与订阅各一条链

```go
type SubscriberMiddleware func(Handler) Handler             // event.go:28
type PublishMiddleware func(PublishHandler) PublishHandler  // publish.go:9
```

`ChainSubscriberMiddleware`/`ChainPublishMiddleware` 逆序包裹、跳过 nil（`event.go:31-42`、`publish.go:12-23`）。后端在 `Publish` 入口与 `Subscribe` 装配时各自挂链（`kafka.go:336-344`、`kafka.go:629-631`）。Broker 级 Option（`WithSubscriberMiddlewares`）作用于全部订阅，Subscribe 级 Option（`WithSubscribeMiddlewares`）追加在单条订阅上。

`WrapLegacyPublishHandler`（`publish.go:26-38`）是给旧签名 `func(ctx, topic, msg any, ...)` 的迁移桥——go-wind 存量代码的 PublishHandler 不用改签名就能挂进新链。

### SubscriberSyncMap：后端共享的订阅簿

四个后端都要记账「我有哪些订阅者、停机时怎么清」，契约直接提供线程安全的簿（`subscriber.go:25-219`）：`Remove`（移除并退订）、`RemoveOnly`（只移除）、`Clear`/`ForceClear`（批量退订/不退订）、`RemoveWithTimeout`/`ClearWithTimeout`（限时版，防退订卡死停机）、`Foreach`（快照遍历，不在锁内回调）。

限时版的存在理由：`Unsubscribe` 要关网络连接，可能卡住。停机路径（`Disconnect` → `subscribers.Clear()`）不能被一个坏连接拖死整个进程退出。

### 四后端形状对照

| | kafka | rabbitmq | redis | rocketmq |
|---|---|---|---|---|
| 构造 | `NewBroker(opts...)` | `NewBroker(opts...)` | `NewBroker(driverType, opts...)` | `NewBroker(opts...)` |
| SDK | segmentio/kafka-go v0.4.49 | amqp091-go v1.10.0 | redigo v1.9.2 | rocketmq-client-go/v2 v2.1.2 |
| 驱动 | 单一 | 单一 | **pubsub / stream 双驱动** | v2 客户端 |
| `Name()` | `"kafka"` | `"rabbitmq"` | 按驱动 | `"rocketmqV2"` |
| `Request` | —（已从契约删除） | — | — | — |
| 测试 | 0 | 0 | contract 3（miniredis 回环） | 0 |

redis 的双驱动（`redis/redis.go:11-20`）值得说明：Pub/Sub 是 fire-and-forget（掉线期间消息丢失），Stream 是持久化消费（有 ACK、有消费组）。`NewBroker(DriverTypeStream, ...)` 显式选择；契约装配默认走 pubsub（`redis/contract/contract.go:47`）。

kafka 后端两个非显然的默认：**一 topic 一 writer**（`kafka.go:110-114`，默认开，writer 按 topic 缓存复用）与**写失败自愈**（缓存过的 writer 失败 → 关闭重建 → 重试 `retriesCount` 次，`kafka.go:420-443`）。订阅端有单条/批量两种消费循环（`kafka/subscriber.go:140-146`）、指数退避带抖动（`backoffSleep`，`kafka/subscriber.go:312-331`）、致命错误分类（`isFatalError`：ctx 取消/订阅者已关/非临时 net.Error 退出，io.EOF 视为可重试——broker 重启与 rebalance 都走这条路，`kafka/subscriber.go:334-357`）。

### 装配：两条路

**路一：transport Server 包装**。`transport/{kafka,rabbitmq,redis,rocketmq}` 把后端 broker 包成 `transport.Server`（Start 里 Init→Connect，`transport/kafka/server.go:60-77`），订阅参数经 `transport/subscribe.SubscribeOptionMap` 延迟声明、Start 时统一挂。适合「消息驱动型服务」——整个进程就是消费者。

**路二：bootstrap.BrokerRegistry + appkit**。配置驱动：

```go
br := bootstrap.NewBrokerRegistry()
br.MustRegister(kafkacontract.Type, kafkacontract.Provider)      // "kafka"
br.MustRegister(rabbitmqcontract.Type, rabbitmqcontract.Provider)
app, _ := appkit.FromBootstrap(cfg, appkit.WithBrokerRegistry(br), ...)

// Run 后取实例：
b := app.Broker(kafkacontract.Type).(broker.Broker)
```

`BrokerRegistry.Build`（`bootstrap/broker.go:86-130`）按 `brokerSections` 枚举顺序（proto 字段序：kafka→rabbitmq→redis→rocketmq）逐段查表构建，段存在但未注册 fail-fast，构建失败逆序回滚已建 cleanup。**broker 是 optional 段集合**——四段可并存，「事件总线与任务队列分别落在不同 broker 上」是装配语义的一部分（`bootstrap/broker.go:23-25`）。

停机位次：broker 的 cleanup 注册在 workflow-clients 之后，停机逆序回放时**先于** workflow/ai/storage/cache/database 关闭（`bootstrap/broker.go:83-85`）——in-flight 消息先排干，服务器 drain 最先。broker 段不支持热更新（与 database 段同理）。

### contract 子包：实现零契约依赖

每个后端 module 里有一个 `contract/` 包（如 `kafka/contract/contract.go`）：只它 import bconf，导出 `Type`（段名常量）与 `Provider`（`func(ctx, *bootstrapv1.Broker) (any, func(), error)`）。后端实现包（`kafka` 根包）不 import bconf——纯 SDK 封装。

```go
// kafka/contract/contract.go:30-61（节选）
func Provider(ctx context.Context, cfg *bootstrapv1.Broker) (any, func(), error) {
	sec := cfg.GetKafka()
	if sec == nil {
		return nil, nil, fmt.Errorf("broker: type=%q but kafka section is missing", Type)
	}
	// brokers → WithAddress；auth_type → plain/scram-sha256/scram-sha512
	b := kafka.NewBroker(opts...)
	b.Init(); b.Connect()
	return b, func() { _ = b.Disconnect() }, nil
}
```

## 理由与取舍

### 我们没把 Message 做成泛型 `Message[T]`

泛型消息体意味着每个 topic 一个静态类型、Broker 接口带类型参数、中间件链全部泛型化——一套订阅管线服务所有类型的结构优势就没了。我们选的是**运行时类型发现**：Body 是 any，Binder 在订阅端落地，`TypedHandler[T]` 在编译期钉住 handler 签名。

代价要如实说：类型错误从编译期推迟到运行时（`TypedToEventHandler` 的 default 分支报错，`typed_handler.go:30-32`）。但消息本就来自外部——线上的字节不会因为编译期类型检查而变对。真正的类型安全边界在 Binder 与 codec 的配合上，`derefAnyPointer` 补的就是这条边。

### 我们没在核心 Options 里开后端特有字段

kafka 的 batch/linger、rabbitmq 的 prefetch、rocketmq 的 nameServer——这些全走 `Options.Context` + 类型化 key（`kafka/options.go` 的 `batchSizeKey{}` 等）。核心 `Options`（`options.go:20-43`）只有八个通用字段。

好处：加后端不动契约。代价：**不可发现**——`WithBatchSize` 之类的 Option 散在后端包里，读核心 Options 看不到 kafka 有哪些旋钮。这个代价我们认，因为另一面更贵：核心 Options 变成所有后端字段的并集，每加一个后端都是全仓编译。

### 我们没让 encoding 的注册语义迁就 broker

`encoding` 包的纪律是显式注册（`encoding.MustRegister(jsoncodec.New())`），不用 init() 自注册。broker 的 `options.go:8-9` 还留着两个 blank import——**它们是死的**（json/proto 包没有 init 副作用），`DefaultCodec = encoding.GetCodec("json")`（`options.go:14`）在包初始化时求值、此时注册表必然为空。

我们有两个修法可选（见「开放问题」第 1 条）：惰性求值（`NewOptions` 时才查表）或 fail-fast（codec nil 且 Body 非字节时报错，学 `transport/tcp` 的「codec is nil (nothing registered)」）。**不修的选项不存在**——现状是「配了 JSON 心智、跑了 gob 序列化」的静默错配，redis 契约回环测试能过纯粹因为 gob 自回环。

### 我们删掉了 Request，而不是让它继续当空头支票（2026-09-18 更新）

`Request` 原在五个后端实现里全部返回 `errors.New("not implemented")`（`kafka.go`、`rabbitmq.go`、`redis/{pubsub,stream}/redis.go`、`rocketmq/v2/rocketmq.go` 五处）。移植时留着它是形状保全——go-wind 有这个方法、调用方签名依赖它。但**契约里躺着一个没人实现的方法，比没有这个方法更糟**：它邀请业务写 `Request` 调用，运行时才炸。零业务调用方时删除是最便宜的破坏窗口，故 2026-09-18 从契约、五后端实现与 `request_options.go` 一并删除。若将来需要请求-响应语义，redis stream（请求写 stream、回复走回执 topic）是天然候选，届时按需重新引入。

### 我们没把 13 个契约段都实现

bconf 的 `Broker` 消息声明了 13 个后端段（`broker.proto:138-150`）：kafka、rabbitmq、redis、nats、mqtt、pulsar、azuresb、gcpubsub、nsq、rocketmq、sqs、stomp、activemq。实现的只有 4 个，`brokerSections`（`bootstrap/broker.go:70-75`）也只枚举 4 个。

bconf 是超集契约（先声明、后实现），这个方向本身没问题。**问题在静默**：配了 `broker.nats` 的应用不会报错——`brokerSections` 里没有 nats，Build 直接跳过。用户以为消息代理接上了，实际什么都没发生。正确形状是「段存在但无对应实现 → fail-fast」，与「段存在但未注册 Provider → fail-fast」对齐。

### 我们让 kafka 的日志走 nil ctx 的包级函数

`kafka/logger.go:12-41` 的 `LogInfof` 系列全部 `log.Info(nil, ...)`——ctx 传 nil。这不是疏忽：订阅循环里没有请求 ctx，消息处理用 `context.Background()` 隔离（`kafka/subscriber.go:271-272`）。`LogFatal` 被降级为 `log.Error`（`logger.go:24-26`）——库代码不 `os.Exit`，致命性交给调用方。代价：这些日志没有 trace 关联。要关联，得把消息 Headers 里的 trace context 显式接进 ctx——那是可观测装配层的事，不是 broker 契约的事。

## 兼容性

### 纯新增，零破坏

`broker` 是新 module（`9ffe811`，2026-09-06）。消费方只有同批移植的 `transport/{kafka,rabbitmq,redis,rocketmq}` 四件套与 `bootstrap`/`appkit` 装配层。`_example` 零引用。

### 但三件事必须说清

> **2026-09-18 修复**：三条缺口全部关闭，详见「实现与过渡」的修复记录。以下保留原描述作为问题记录。

1. **`DefaultCodec` 恒为 nil，实际序列化是 gob**。`options.go:8-9` 的两个 blank import 自 encoding 改显式注册后就是死代码；`options.go:14` 的包级 var 在 init 期求值，注册表必然为空（已用探针测试实证：`DefaultCodec is nil at init time`）。后果链：`NewOptions()` 的 `Codec` 是 nil → `Marshal`/`Unmarshal` 走 gob 兜底 → **配了「JSON 默认」心智的应用实际跑 gob**。`WithCodec("json")`（`options.go:117-124`）在未注册时也静默返回 nil，同样落 gob。跨语言消费（对端用真 JSON 解）必然炸。**未修**——修法见「开放问题」第 1 条。
2. **`Request` 五处 `not implemented`**。契约方法、零实现。调用它运行时报错，编译期无任何提示。**未修**——实现或删除，见「开放问题」第 3 条。
3. **未实现的契约段静默忽略**。bconf 13 段、实现 4 段；`brokerSections` 只枚举 4 段，配 `broker.nats`/`broker.mqtt` 等九段中的任何一个，Build 无声跳过。**未修**——应与「段存在但未注册」同样 fail-fast，见「开放问题」第 2 条。

### 迁移策略：不强制迁移

没有存量调用方。要落地的是**接入**：业务经 `WithBrokerRegistry` 装配后用 `app.Broker(typ)` 取实例。接入前建议先修缺口 1——否则第一个跨语言消费者会把 gob 字节当 JSON 解。

## 实现与过渡

### 已落地

`broker/` 根 module（8 个契约文件）+ 四后端 module（各含 `contract/` 子包），引入于 `9ffe811`（2026-09-06）。

测试 26 个：根 module 23 个（message 12：Creator×4、Headers/Metadata 操作与拷贝独立性、Clone、BodyBytes、ctx 注入提取、AckSuccess；subscriber 4：SyncMap 基础操作/限时移除/清理族/Foreach；typed_handler 4：nil 防御×2、指针与值分派、不支持类型；event 2：中间件链序与跳 nil；publish 1：发布中间件链序）+ `redis/contract` 3 个（dial URL 组装、miniredis pubsub 回环、段缺失报错）。**kafka/rabbitmq/rocketmq 后端 0 个**——它们需要真实 broker，测试成本与 sentinel 同款。

### 2026-09-18 修复：三条缺口关闭

审查（天权）核验后逐条修复，均带回归测试：

| 缺口 | 修复 | 验证 |
|---|---|---|
| 1. codec 静默降级 gob | `broker/encoding.go` 的 `Marshal`/`Unmarshal` 在 codec==nil 且非 `[]byte`/`string` 时 fail-fast（新增 `errNoCodec`，文案对齐 `transport/tcp`）；`options.go` 删死 blank import 与恒 nil 的包级 `DefaultCodec`，改 `NewOptions()` 惰性查表 `defaultCodecName`；`WithCodec` 注释写明未注册即 fail-fast | `broker/encoding_test.go` 新增 5 个（fail-fast×2、透传×2、惰性查表×1） |
| 2. `Request` 空头支票 | 契约删 `Request` 方法 + 五后端实现 + `request_options.go`（零业务调用方，编译期可见的窗口） | 五 module build/vet 全绿，全仓 `RequestOption` 零残留 |
| 3. 未实现段静默跳过 | `brokerSections` 扩为全 13 段（含 `implemented` 标志），未实现段被配置即 fail-fast | `bootstrap/broker_test.go` 新增 2 个（9 段逐一 fail-fast 子测试 + 已实现段回滚） |

**修复中的连带发现**：`broker/{kafka,rabbitmq,redis,rocketmq}/contract` 的 `Provider` 均不注入 codec，而 bconf 的 broker 段没有 codec 字段——契约装配路径只能依赖 `NewOptions()` 的惰性默认。故 `broker/redis/contract/contract_test.go` 补 `TestMain` 显式注册 json（修复前该测试靠 gob 兜底通过，是缺口 1 的受害者而非缺陷）。

### 登记工作（2026-09-18 补齐）

`Taskfile.yml` 新增 `retry-verify` / `ratelimit-verify` / `broker-verify`（`ratelimit` 4 module、`broker` 5 module，逐 module `dir:` 验证），并挂进根 `verify` 的 deps；根 `README.md` 模块树登记三 module。CI 无需改动（`ci.yml` 用 `find . -name go.mod` 自动发现）。

### 修复顺序：先修静默错配，再谈扩展

> **2026-09-18 状态**：第 1–3 项已全部完成（见「实现与过渡」修复记录）；第 4 项（后端测试）仍开放。

1. ~~**`DefaultCodec`**（缺口 1）。两个候选：惰性求值（`NewOptions` 时查表，注册晚于 import 也能生效）或 fail-fast（codec nil 且 Body 非字节时报错）。倾向后者——静默兜底正是这个 bug 的温床，`transport/tcp` 已有同款错误文案先例。~~ **已修**：两者都做——`NewOptions` 惰性查表 + `Marshal`/`Unmarshal` fail-fast。
2. ~~**未实现段 fail-fast**（缺口 3）。`brokerSections` 补全 13 段的存在性检查，未实现段配置即报错。~~ **已修**。
3. ~~**`Request` 裁定**（缺口 2）。实现（redis stream 优先）或从契约删除。删除是破坏性变更，但当前零调用方，窗口就在现在。~~ **已删**。
4. **后端测试**。kafka/rabbitmq/rocketmq 从 0 起步；redis 的 miniredis 模式（contract 回环测试）是现成范本——kafka 可用 testcontainers 或 CI 里的真 Kafka，取舍见「开放问题」第 5 条。

## 附录

### 完整 API（根 module）

| 类别 | 导出符号 | 说明 |
|---|---|---|
| 接口 | `Broker`（8 方法） | 见「设计」 |
| 接口 | `Event{Topic, Message, RawMessage, Ack, Error}` | 订阅端事件视图 |
| 接口 | `Subscriber{Options, Topic, Unsubscribe(removeFromManager)}` | 退订句柄 |
| 函数 | `Handler func(ctx, Event) error` | 订阅底座 |
| 函数 | `Binder func() any` / `Creator[T]() *T` | 类型落地 |
| 函数 | `TypedHandler[T]` / `TypedToEventHandler[T]` / `Subscribe[T]` | 类型化三层糖 |
| 函数 | `PublishHandler` / `PublishMiddleware` / `SubscriberMiddleware` / 两个 `Chain*` | 中间件 |
| 函数 | `WrapLegacyPublishHandler` | 旧签名迁移桥 |
| 类型 | `Message`（+Set/With/Get/Copy/Merge/Clone/ctx 注入提取族）、`Headers`、`Metadata` | 消息结构 |
| 类型 | `SubscriberSyncMap`（Add/Remove/RemoveOnly/Clear/ForceClear/×WithTimeout/Foreach） | 订阅簿 |
| 函数 | `Marshal` / `Unmarshal` / `derefAnyPointer`（私有） | 编解码 + 指针解层 |
| Option | `Options`：`WithAddress`/`WithCodec`/`WithErrorHandler`/`WithEnableSecure`/`WithTLSConfig`/`WithOptionContext`/`With{Subscriber,Publish}Middlewares` | Broker 级 |
| Option | `PublishOptions`：`WithPublishContext`/`WithPublishTimeout`/`WithPublishAsync`/`WithPublishRetries`/`WithPublishRequiredAcks`/`WithPublishBodyCodec` | 发布级 |
| Option | `SubscribeOptions`：`DisableAutoAck`/`WithSubscribeQueueName`/`WithSubscribeGroupID`/`WithSubscribeConcurrency`/`WithSubscribeRetry`/`WithSubscribeMiddlewares` | 订阅级 |

### FAQ

**为什么 `Subscribe` 的 binder 是参数而不是 Option？** 它与 handler 是一对：有 binder 才有类型化落地，两者总是一起决定。塞进 Option 会让「handler 要 `*T`、binder 忘了给」成为运行时才炸的配置错误。

**`AutoAck` 默认开，什么时候该关？** handler 处理失败时想控制重试/死信语义时关掉，在 handler 里显式 `event.Ack()`。kafka 后端的 Ack 是 commit offset——关掉 AutoAck 意味着处理失败的消息会在 rebalance 后重投。

**`Queue` 字段在不同后端是什么？** kafka 是 GroupID（`WithSubscribeGroupID` 是别名，`subscribe_options.go:103-106`）、rabbitmq 是 queue 名、rocketmq 是 consumer group。契约用 `Queue` 一个词装三种语义，代价是文档负担——这里如实记录。

**为什么 `Message.Key` 是 string？** 三家后端的 key/routing key 都是字符串语义（kafka 按 key 哈希分区、rabbitmq 按 routing key 路由）。要字节级 key 用 `Msg` 装原始消息。

**redis 契约为什么默认 pubsub 而不是 stream？** 契约段形状（address/password/db）只够建连接；stream 需要消费组、ACK 策略等额外参数，属于能力层——业务要持久化消费就显式 `redis.NewBroker(option.DriverTypeStream, ...)`（`redis/contract/contract.go:31-32` 注释已写明）。

**`SetDelay` 为什么是 metadata 而不是字段？** 延迟级别是 rocketmq 特有概念（18 个 level），放 metadata（`message.go:141-143`）避免契约背 rocketmq 形状——与 Partition/Offset 的处理相反，因为延迟没有跨后端的通用语义。

### 开放问题

1. **`DefaultCodec` 怎么修？** 惰性求值（`NewOptions` 时查表）兼容「main 里先注册再构造」的用法；fail-fast（codec nil 且 Body 非字节报错）把错配炸在第一次发布。倾向 fail-fast，但 `[]byte`/`string` 直通路径要保留（透传场景合法）。另需同步裁定：`WithCodec` 查表失败应报错还是静默 nil。
2. **未实现段要不要 fail-fast？** `brokerSections` 补全 13 段存在性检查即可，但会改变「配了 nats 的存量配置」的行为（从静默跳过变启动报错）。当前零存量调用方，窗口就在现在。
3. ~~**`Request` 实现还是删除？**~~ **已裁定并执行（2026-09-18）：删除**。契约 9 方法 → 8 方法；零调用方窗口已用掉。将来若需请求-响应语义，redis stream（请求写 stream、`ReplyTopic` 回执）是天然候选。
4. **后端特有 Option 的可发现性要不要补？** `Options.Context` + 类型化 key 的代价是不可发现。候选：各后端导出一份「key 清单」文档注释，或 `Init` 里对未知 key 报 warning。不动也成立——kafka 的 Option 本身就是导出函数，godoc 可查。
5. **kafka/rabbitmq/rocketmq 的测试从哪起步？** redis 用 miniredis（进程内、零外部依赖）跑通了 contract 回环；kafka 没有等价的进程内实现，候选是 testcontainers（CI 需要 Docker）或 CI service container（GitHub Actions 的 kafka service）。rabbitmq/rocketmq 同理。裁定前，三后端维持「编译验证 + 契约层测试」的现状。
