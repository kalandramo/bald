# Apache RocketMQ

基于 Apache RocketMQ Go 客户端的消息服务器，实现了 `transport.Server` 接口。支持集群消费和广播消费两种模式。

## 核心特性

- 集群消费和广播消费模式
- 泛型自动反序列化（`RegisterSubscriber[T]`）
- NameServer / NameServer Domain
- ACL 认证（AccessKey / SecretKey）
- 自定义编解码
- 链路追踪


## 安装

```bash
go get github.com/tx7do/go-wind-plugins/transport/rocketmq
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

    "github.com/tx7do/go-wind-plugins/transport/rocketmq"
    "github.com/tx7do/go-wind-plugins/broker"
)

// MyMessage 示例消息。
type MyMessage struct {
    Key   string `json:"key"`
    Value string `json:"value"`
}

func main() {
    srv := rocketmq.NewServer(
        rocketmq.WithAddress([]string{"127.0.0.1"}),
        rocketmq.WithCodec("json"),
    )

    // 注册订阅者（泛型，自动反序列化）
_ = rocketmq.RegisterSubscriber(srv,
    ctx,
    "my-topic", "my-group",
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
# NameServer
docker run -d --name rmqnamesrv -p 9876:9876 \
    apache/rocketmq:latest sh mqnamesrv

# Broker
docker run -d --name rmqbroker -p 10911:10911 --link rmqnamesrv \
    -e "NAMESRV_ADDR=rmqnamesrv:9876" \
    apache/rocketmq:latest sh mqbroker
```

管理后台：

```shell
docker run -d --name rmqconsole -p 9800:8080 --link rmqnamesrv \
    -e "JAVA_OPTS=-Drocketmq.namesrv.addr=rmqnamesrv:9876" \
    styletang/rocketmq-console-ng:latest
```

访问 <http://localhost:9800>

## 配置选项

| 选项 | 类型 | 说明 |
|------|------|------|
| `WithNameServer(addrs)` | []string | NameServer 地址列表 |
| `WithNameServerDomain(uri)` | string | NameServer Domain |
| `WithCodec(c)` | string | 编解码器名称（默认 json） |
| `WithCredentials(ak, sk, token)` | string, string, string | ACL 认证凭据 |
| `WithNamespace(ns)` | string | 命名空间 |
| `WithInstanceName(name)` | string | 实例名称 |
| `WithGroupName(name)` | string | 消费组名称 |
| `WithRetryCount(n)` | int | 重试次数 |
| `WithEnableTrace()` | - | 启用消息轨迹 |
| `WithBrokerOptions(opts)` | ...broker.Option | 直接传递 broker 选项 |


## 参考资料

- [Apache RocketMQ 文档](https://rocketmq.apache.org/docs/)
- [RocketMQ Go 客户端](https://github.com/apache/rocketmq-clients/tree/master/go)

