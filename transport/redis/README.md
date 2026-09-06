# Redis

基于 [redis/go-redis](https://github.com/redis/go-redis) 的 Redis 消息服务器，实现了 `transport.Server` 接口。支持 Pub/Sub 和 Stream 两种驱动模式。

## 核心特性

- Pub/Sub（实时广播）和 Stream（持久化消费组）两种模式
- 泛型自动反序列化（`RegisterSubscriber[T]`）
- 连接池配置
- TLS 加密连接
- 自定义编解码


## 安装

```bash
go get github.com/tx7do/go-wind-plugins/transport/redis
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

    "github.com/tx7do/go-wind-plugins/transport/redis"
    "github.com/tx7do/go-wind-plugins/broker"
)

// MyMessage 示例消息。
type MyMessage struct {
    Key   string `json:"key"`
    Value string `json:"value"`
}

func main() {
    srv := redis.NewServer(
        redis.WithAddress([]string{"127.0.0.1"}),
        redis.WithCodec("json"),
    )

    // 注册订阅者（泛型，自动反序列化）
_ = redis.RegisterSubscriber(srv,
    "my-channel",
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
docker run -itd --name redis \
    -p 6379:6379 -e ALLOW_EMPTY_PASSWORD=yes \
    bitnami/redis:latest
```

管理工具：[RedisInsight](https://redis.io/insight/)、[Another Redis Desktop Manager](https://github.com/qishibo/AnotherRedisDesktopManager)

## 配置选项

| 选项 | 类型 | 说明 |
|------|------|------|
| `WithAddress(addr)` | string | Redis 服务器地址 |
| `WithCodec(c)` | string | 编解码器名称（默认 json） |
| `WithDriverType(t)` | redis.DriverType | 驱动类型（PubSub / Stream） |
| `WithConnectTimeout(d)` | time.Duration | 连接超时时间 |
| `WithReadTimeout(d)` | time.Duration | 读取超时时间 |
| `WithWriteTimeout(d)` | time.Duration | 写入超时时间 |
| `WithIdleTimeout(d)` | time.Duration | 空闲连接超时时间 |
| `WithMaxIdle(n)` | int | 最大空闲连接数 |
| `WithMaxActive(n)` | int | 最大活动连接数 |
| `WithBrokerOptions(opts)` | ...broker.Option | 直接传递 broker 选项 |


## 参考资料

- [Redis 文档](https://redis.io/documentation)
- [Redis Pub/Sub](https://redis.io/docs/manual/pubsub/)
- [Redis Streams](https://redis.io/docs/data-types/streams/)

