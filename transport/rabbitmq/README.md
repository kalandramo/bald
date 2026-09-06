# RabbitMQ

基于 [rabbitmq/amqp](https://github.com/rabbitmq/amqp-go) 的 RabbitMQ 消息服务器，实现了 `transport.Server` 接口。支持 Exchange 路由和 Queue 消费模型。

## 核心特性

- Exchange 路由（Direct / Fanout / Topic）
- 泛型自动反序列化（`RegisterSubscriber[T]`）
- TLS 加密连接
- 自定义编解码
- 链路追踪（OpenTelemetry）


## 安装

```bash
go get github.com/tx7do/go-wind-plugins/transport/rabbitmq
```

## 快速开始

```go
package main

import (
    "context"
    "log"
    "os"
    "os/signal"
    "syscall"

    "github.com/tx7do/go-wind-plugins/transport/rabbitmq"
    "github.com/tx7do/go-wind-plugins/broker"
)

// MyMessage 示例消息。
type MyMessage struct {
    Key   string `json:"key"`
    Value string `json:"value"`
}

func main() {
    srv := rabbitmq.NewServer(
        rabbitmq.WithAddress([]string{"127.0.0.1"}),
        rabbitmq.WithCodec("json"),
    )

    // 注册订阅者（泛型，自动反序列化）
_ = rabbitmq.RegisterSubscriber(srv,
    context.Background(),
    "my-routing-key",
    func(ctx context.Context, topic string, headers broker.Headers, msg *MyMessage) error {
        log.Printf("received: %+v", msg)
        return nil
    },
)

    // 启动服务器（阻塞）
    ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
    defer stop()

    if err := srv.Start(ctx); err != nil {
        log.Fatal(err)
    }
}
```

## Docker 部署

```shell
docker run -itd --name rabbitmq \
    -p 5672:5672 -p 15672:15672 \
    bitnami/rabbitmq:latest
```

管理后台：<http://localhost:15672>（user / bitnami）

## 配置选项

| 选项 | 类型 | 说明 |
|------|------|------|
| `WithAddress(addrs)` | []string | RabbitMQ 地址列表 |
| `WithCodec(c)` | string | 编解码器名称（默认 json） |
| `WithTLSConfig(c)` | *tls.Config | TLS 配置 |
| `WithExchange(name, durable)` | string, bool | Exchange 名称和持久化 |
| `WithBrokerOptions(opts)` | ...broker.Option | 直接传递 broker 选项 |


## 参考资料

- [RabbitMQ 文档](https://www.rabbitmq.com/documentation.html)
- [AMQP 0-9-1 协议](https://www.rabbitmq.com/protocol.html)

