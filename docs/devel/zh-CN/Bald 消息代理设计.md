# Bald 消息代理设计：Body 装任意类型、Binder 负责落地，四个后端各自独立 module

> Author(s): bald 团队
>
> Last updated: 2026-10-30
>
> Discussion at: 源码 `broker/`（契约根 module）与 `broker/{kafka,rabbitmq,redis,rocketmq}/`（后端 module，各带 `contract/` 子包）；契约 `bconf/proto/bootstrap/v1/broker.proto`；装配面 `pkg/appkit`（`broker.<backend>` 段 Provider）
>
> Status: Accepted

## 摘要

`broker` 是 bald 的消息代理契约包。它定义一件事：**消息怎么进出**。`Message.Body` 是 `any`——发布时装什么类型由业务决定，订阅时怎么落地成具体类型由 `Binder` 决定；`TypedHandler[T]` 泛型层让业务直接拿到 `*T`。四个后端（kafka、rabbitmq、redis、rocketmq）各自住在独立 module 里，每个后端再带一个 `contract/` 子包对接 bconf 契约段。

我们做了三个承诺：

1. **业务只 import 契约 + 所选后端**。`broker` 根 module 只依赖 `encoding`；换后端只改构造那一行，`Publish`/`Subscribe` 调用点不动。
2. **类型化订阅是糖，不是底座**。底层管线只有一种 `Handler func(ctx, Event) error`；`TypedHandler[T]`、`TypedToEventHandler`、泛型 `Subscribe[T]` 是叠在上面的糖——不用糖的人零成本。
3. **配置驱动装配可选**。手写 `kafka.NewBroker(opts...)` 与「`broker.<backend>` 段 Provider」（如 `rediscontract.AppProvider()`）两条路并存，后者按 bconf 的 `broker` 段构建，产物落资源面。

## 背景与动机

### 四个后端的共同约定

`broker` 与 `transport/{kafka,rabbitmq,redis,rocketmq}` 四件套各自独立 module，遵守同一组约定：

1. **不织入 tracer**。可观测走 `contrib/observability-otlp` 装配层，broker 契约不背 SDK。
2. **Init→Connect 统一**。契约装配（`contract.Provider`）一律先 `Init()` 再 `Connect()`，与 database/cache 域同一节奏。
3. **`derefAnyPointer` 先解一层**。这是类型化订阅能工作的关键，见「设计」。
4. **rocketmq 的 SDK 间接依赖按伪版本钉住**（上游无 tag）。

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

三层写法对应三类用户：要裸字节的（透传/加密场景）、要具体类型但不用泛型的（存量风格）、要泛型糖的（新代码）。契约只保证第一层是底座，上面两层是糖。

### 一句话定性

**契约形状完整、四后端可用、两条装配路已通**：transport Server 包装适合消息驱动进程，段驱动 Provider 适合与其它能力同进程装配。

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

日常用到的只有 `Publish` 和 `Subscribe`。`Init`/`Connect`/`Disconnect` 是生命周期（装配层调），`Name`/`Options`/`Address` 是诊断面。

契约里**没有 `Request`**：请求-响应语义无法用四个后端共有的能力干净实现（rocketmq/rabbitmq 需要额外队列或回执 topic，redis pubsub 根本没有），留一个四后端全是 `not implemented` 的方法，等于邀请业务写出运行时才炸的调用。将来真需要时，redis stream（请求写 stream、回执走 reply topic）是天然入口，届时按需增量引入。

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

三个裁定值得展开：

**Headers 与 Metadata 分开**。Headers 随消息走线（kafka header、amqp property），值只能是 string；Metadata 只活在进程内（经 `ExtractContext`/`GetMetadataFromContext` 存取），值是 any。混用两者是常见错误——想跨进程序传的东西放 Metadata 里，订阅端永远读不到。

**Partition/Offset 是 Kafka 形状**。rabbitmq/redis 后端不填它们。契约选择「为最强的后端定形状、弱的后端留零值」而不是「抽象成 map」——零值字段比 stringly-typed 元数据便宜。

**`Msg` 装原始消息**。订阅端 `event.RawMessage()` 拿到 `kafkaGo.Message` 或 `amqp.Delivery`，需要后端特有能力（如 kafka 的精确 offset 提交）时不用断言 `Body`。

`Clone()` 是浅拷贝但 Headers/Metadata 深复制——发布路径先 `Clone` 再替换 Body 为字节，保证原始 Message 不被发送过程污染。

### Binder 与类型化订阅：三层糖

```go
type Binder func() any
func Creator[T any]() *T

type TypedHandler[T any] func(ctx context.Context, topic string, headers Headers, msg *T) error
func TypedToEventHandler[T any](h TypedHandler[T]) Handler
func Subscribe[T any](broker Broker, topic string, handler TypedHandler[T], opts ...SubscribeOption) (Subscriber, error)
```

`TypedToEventHandler` 的类型分派：Body 已经是 `*T` 就直接用；是 `T` 值就取地址；都不是就报「不支持的类型」。这依赖订阅端先经 Binder 把字节落地成 `*T`——两层配合，缺一不可。

### 编解码：derefAnyPointer 是类型化订阅的地基

订阅端落地类型的管线：

```go
if s.binder != nil {
	bm.Body = s.binder()                                            // Body = *Order（装在 any 里）
	if err = broker.Unmarshal(s.b.options.Codec, km.Value, &bm.Body); err != nil {  // 注意 &bm.Body：*any
		...
	}
}
```

`Unmarshal` 收到的是 `*any`，any 里装着 `*Order`。标准 JSON unmarshaler 对 `*any` 会用 `map[string]any` 顶掉原类型——类型化语义就丢了。`derefAnyPointer` 解这一层：发现 `*any` 里装着非 nil 指针，就把该指针交给 codec。**没有这个函数，整个类型化订阅在 JSON codec 下是坏的。**

`Marshal`/`Unmarshal` 的规则：codec 非 nil 用 codec；nil 时 `[]byte`/`string` 直通，其余类型 fail-fast（`errNoCodec`，文案对齐 `transport/tcp` 的「codec is nil (nothing registered)」）。默认 codec 由 `NewOptions()` 惰性查表取得——注册晚于 import 也能生效。**不静默回退 gob**：静默兜底会让「配了 JSON 心智、跑了 gob 序列化」的错配直到跨语言对接才暴露。

### 中间件：发布与订阅各一条链

```go
type SubscriberMiddleware func(Handler) Handler
type PublishMiddleware func(PublishHandler) PublishHandler
```

`ChainSubscriberMiddleware`/`ChainPublishMiddleware` 逆序包裹、跳过 nil。后端在 `Publish` 入口与 `Subscribe` 装配时各自挂链。Broker 级 Option（`WithSubscriberMiddlewares`）作用于全部订阅，Subscribe 级 Option（`WithSubscribeMiddlewares`）追加在单条订阅上。

`WrapLegacyPublishHandler` 是给旧签名 `func(ctx, topic, msg any, ...)` 的迁移桥：存量 PublishHandler 不用改签名就能挂进新链。

### SubscriberSyncMap：后端共享的订阅簿

四个后端都要记账「我有哪些订阅者、停机时怎么清」，契约直接提供线程安全的簿：`Remove`（移除并退订）、`RemoveOnly`（只移除）、`Clear`/`ForceClear`（批量退订/不退订）、`RemoveWithTimeout`/`ClearWithTimeout`（限时版，防退订卡死停机）、`Foreach`（快照遍历，不在锁内回调）。

限时版的存在理由：`Unsubscribe` 要关网络连接，可能卡住。停机路径（`Disconnect` → `subscribers.Clear()`）不能被一个坏连接拖死整个进程退出。

### 四后端形状对照

| | kafka | rabbitmq | redis | rocketmq |
|---|---|---|---|---|
| 构造 | `NewBroker(opts...)` | `NewBroker(opts...)` | `NewBroker(driverType, opts...)` | `NewBroker(opts...)` |
| SDK | segmentio/kafka-go | amqp091-go | redigo | rocketmq-client-go/v2 |
| 驱动 | 单一 | 单一 | **pubsub / stream 双驱动** | v2 客户端 |
| `Name()` | `"kafka"` | `"rabbitmq"` | 按驱动 | `"rocketmqV2"` |
| 测试 | 编译验证 | 编译验证 | 契约回环（miniredis） | 编译验证 |

redis 的双驱动值得说明：Pub/Sub 是 fire-and-forget（掉线期间消息丢失），Stream 是持久化消费（有 ACK、有消费组）。`NewBroker(DriverTypeStream, ...)` 显式选择；契约装配默认走 pubsub——契约段形状（address/password/db）只够建连接，消费组与 ACK 策略属于能力层参数。

kafka 后端两个非显然的默认：**一 topic 一 writer**（默认开，writer 按 topic 缓存复用）与**写失败自愈**（缓存过的 writer 失败 → 关闭重建 → 重试 `retriesCount` 次）。订阅端有单条/批量两种消费循环、指数退避带抖动、致命错误分类：ctx 取消/订阅者已关/非临时 `net.Error` 退出，`io.EOF` 视为可重试——broker 重启与 rebalance 都走这条路。

### 装配：两条路

**路一：transport Server 包装**。`transport/{kafka,rabbitmq,redis,rocketmq}` 把后端 broker 包成 `transport.Server`（Start 里 Init→Connect），订阅参数经 `transport/subscribe.SubscribeOptionMap` 延迟声明、Start 时统一挂。适合「消息驱动型服务」——整个进程就是消费者。

**路二：`broker.<backend>` 段 Provider**。配置驱动，按段存在性装配：

```go
appkit.MustServe(ctx, std.Standard(),
	rediscontract.AppProvider(), // 消费 broker.redis 段
	...)

// Run 后取实例（资源键即段路径）：
b, ok := appkit.NewKey[broker.Broker]("broker.redis").Resource(app)
```

每个后端在 `contract/` 子包导出段驱动 Provider（当前 `broker/redis` 已具备 `AppProvider()`；kafka/rabbitmq/rocketmq 的既有 `contract.Provider` 可被用户 Provider 包装成同一形状）。**broker 是 optional 段集合**——多个 `broker.<backend>` 段可并存，「事件总线与任务队列分别落在不同 broker 上」是装配语义的一部分；段存在而无 Provider → L3 覆盖检查启动期报错，不静默跳过。

停机位次：broker 节点用 `PriorityBroker`，cleanup 在停机逆序回放时**先于** workflow/ai/storage/cache/database 释放——in-flight 消息先排干，服务器 drain 最先。broker 段不支持热更新（`ReloadRestart`，与 database 段同理）。

### contract 子包：实现包零契约依赖

每个后端 module 里有一个 `contract/` 包：只有它 import bconf，导出 `Type`（段名常量）与 `Provider`（`func(ctx, *bootstrapv1.Broker) (any, func(), error)`）。后端实现包（如 `kafka` 根包）不 import bconf——纯 SDK 封装。

```go
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

代价要如实说：类型错误从编译期推迟到运行时（`TypedToEventHandler` 的 default 分支报错）。但消息本就来自外部——线上的字节不会因为编译期类型检查而变对。真正的类型安全边界在 Binder 与 codec 的配合上，`derefAnyPointer` 补的就是这条边。

### 我们没在核心 Options 里开后端特有字段

kafka 的 batch/linger、rabbitmq 的 prefetch、rocketmq 的 nameServer——这些全走 `Options.Context` + 类型化 key。核心 `Options` 只有八个通用字段。

好处：加后端不动契约。代价：**不可发现**——`WithBatchSize` 之类的 Option 散在后端包里，读核心 Options 看不到 kafka 有哪些旋钮。这个代价我们认，因为另一面更贵：核心 Options 变成所有后端字段的并集，每加一个后端都是全仓编译。

### 我们没让 encoding 的注册语义迁就 broker

`encoding` 包的纪律是显式注册（`encoding.MustRegister(jsoncodec.New())`），不用 init() 自注册——注册顺序可见、依赖图可审计。broker 顺这条纪律：不 import 具体 codec 包、不做包级 `GetCodec` 求值，默认 codec 在 `NewOptions()` 时才查表，缺失即 fail-fast（见「编解码」）。

### 我们没把 13 个契约段都实现

bconf 的 `Broker` 消息声明了 13 个后端段：kafka、rabbitmq、redis、nats、mqtt、pulsar、azuresb、gcpubsub、nsq、rocketmq、sqs、stomp、activemq；实现的是 4 个。bconf 是超集契约（先声明、后实现），这个方向本身没问题。

边界由装配层兜住：**段存在而无 Provider → L3 覆盖检查启动期报错**。配了 `broker.nats` 却没有对应 Provider 的应用不会跑起来，而不是「以为接上了、实际什么都没发生」。

### 我们让 kafka 的日志走 nil ctx 的包级函数

kafka 后端的 `LogInfof` 系列全部 `log.Info(nil, ...)`——ctx 传 nil。这不是疏忽：订阅循环里没有请求 ctx，消息处理用 `context.Background()` 隔离。`LogFatal` 被降级为 `log.Error`——库代码不 `os.Exit`，致命性交给调用方。代价：这些日志没有 trace 关联。要关联，得把消息 Headers 里的 trace context 显式接进 ctx——那是可观测装配层的事，不是 broker 契约的事。

## 兼容性

- **codec**：`NewOptions()` 惰性查表默认 codec；`Marshal`/`Unmarshal` 在 codec 缺失且 Body 不是 `[]byte`/`string` 时 fail-fast，不静默降级；`WithCodec` 查表失败同样报错。
- **契约方法**：契约只保留四后端都能实现的 8 个方法，不含 `Request`。将来需要请求-响应语义时按需增量引入。
- **未实现段**：bconf 超集段在无 Provider 时由 L3 覆盖检查启动期报错。

## 实现与验证

模块布局：`broker/` 根 module 只放契约（接口、Message/Headers、中间件、类型化糖、编码）；四个后端各自独立 module，`<backend>/contract/` 子包把 bconf 段映射为 Broker 实例——根 module 的依赖图不因增加后端而增长。

测试现状：根 module 为主（message 族：Creator、Headers/Metadata、Clone、BodyBytes、ctx 注入、Ack；subscriber 族：SyncMap 全部操作；typed_handler 族：nil 防御、指针/值分派、不支持类型；event 与 publish 中间件链序），`redis/contract` 用 miniredis 跑通契约回环（dial URL 组装、pubsub 回环、段缺失报错）。**kafka/rabbitmq/rocketmq 只有编译验证**——三者都需要真实 broker，成本取舍见「开放问题」第 2 条。

验证命令：

```bash
# 根 module 与 redis 后端
go test ./broker/... ./broker/redis/...
# 全量（含未测试后端，仅编译/静态检查）
task broker-verify
```

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

**`Queue` 字段在不同后端是什么？** kafka 是 GroupID（`WithSubscribeGroupID` 是别名）、rabbitmq 是 queue 名、rocketmq 是 consumer group。契约用 `Queue` 一个词装三种语义，代价是文档负担——这里如实记录。

**为什么 `Message.Key` 是 string？** 三家后端的 key/routing key 都是字符串语义（kafka 按 key 哈希分区、rabbitmq 按 routing key 路由）。要字节级 key 用 `Msg` 装原始消息。

**redis 契约为什么默认 pubsub 而不是 stream？** 契约段形状（address/password/db）只够建连接；stream 需要消费组、ACK 策略等额外参数，属于能力层——业务要持久化消费就显式 `redis.NewBroker(option.DriverTypeStream, ...)`。

**`SetDelay` 为什么是 metadata 而不是字段？** 延迟级别是 rocketmq 特有概念（18 个 level），放 metadata 避免契约背 rocketmq 形状——与 Partition/Offset 的处理相反，因为延迟没有跨后端的通用语义。

### 开放问题

1. **后端特有 Option 的可发现性要不要补？** `Options.Context` + 类型化 key 的代价是不可发现。候选：各后端导出一份「key 清单」文档注释，或 `Init` 里对未知 key 报 warning。不动也成立——kafka 的 Option 本身就是导出函数，godoc 可查。
2. **kafka/rabbitmq/rocketmq 的测试从哪起步？** redis 用 miniredis（进程内、零外部依赖）跑通了契约回环；kafka 没有等价的进程内实现，候选是 testcontainers（CI 需要 Docker）或 CI service container（GitHub Actions 的 kafka service）。rabbitmq/rocketmq 同理。裁定前，三后端维持「编译验证 + 契约层测试」的现状。
