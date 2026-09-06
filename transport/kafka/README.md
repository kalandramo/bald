# Kafka

基于 [segmentio/kafka-go](https://github.com/segmentio/kafka-go) 的 Kafka 消息队列服务器，实现了 `transport.Server` 接口。支持消费组、分区消费等 Kafka 核心特性。

## 核心特性

- 消费组（Consumer Group）模式
- 泛型自动反序列化（`RegisterSubscriber[T]`）
- PLAIN / SCRAM 认证
- TLS 加密连接
- 自定义编解码
- 链路追踪（OpenTelemetry）


## 安装

```bash
go get github.com/tx7do/go-wind-plugins/transport/kafka
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

    "github.com/tx7do/go-wind-plugins/transport/kafka"
    "github.com/tx7do/go-wind-plugins/broker"
)

// MyMessage 示例消息。
type MyMessage struct {
    Key   string `json:"key"`
    Value string `json:"value"`
}

func main() {
    srv := kafka.NewServer(
        kafka.WithAddress([]string{"127.0.0.1"}),
        kafka.WithCodec("json"),
    )

    // 注册订阅者（泛型，自动反序列化）
_ = kafka.RegisterSubscriber(srv,
    ctx,
    "my-topic", "my-group", false,
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
docker run -itd --name kafka-standalone \
    -p 9092:9092 -p 9093:9093 \
    -e KAFKA_ENABLE_KRAFT=yes \
    -e KAFKA_CFG_NODE_ID=1 \
    -e KAFKA_CFG_PROCESS_ROLES=broker,controller \
    -e KAFKA_CFG_CONTROLLER_LISTENER_NAMES=CONTROLLER \
    -e KAFKA_CFG_LISTENERS=PLAINTEXT://:9092,CONTROLLER://:9093 \
    -e KAFKA_CFG_CONTROLLER_QUORUM_VOTERS=1@127.0.0.1:9093 \
    -e KAFKA_CFG_LISTENER_SECURITY_PROTOCOL_MAP=CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT \
    -e KAFKA_ADVERTISED_LISTENERS=PLAINTEXT://127.0.0.1:9092 \
    -e ALLOW_PLAINTEXT_LISTENER=yes \
    bitnami/kafka:latest
```

管理工具：[Offset Explorer](https://www.kafkatool.com/download.html)

## 配置选项

| 选项 | 类型 | 说明 |
|------|------|------|
| `WithAddress(addrs)` | []string | Kafka Broker 地址列表 |
| `WithCodec(c)` | string | 编解码器名称（默认 json） |
| `WithTLSConfig(c)` | *tls.Config | TLS 配置 |
| `WithPlainMechanism(user, pass)` | string, string | PLAIN 认证 |
| `WithScramMechanism(algo, user, pass)` | string, string, string | SCRAM 认证 |
| `WithBrokerOptions(opts)` | ...broker.Option | 直接传递 broker 选项 |


## 参考资料

- [Apache Kafka](https://kafka.apache.org/)
- [segmentio/kafka-go](https://github.com/segmentio/kafka-go)

