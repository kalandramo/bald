# TCP Socket Server

TCP（传输控制协议）是一种面向连接的、可靠的、基于字节流的传输层通信协议。

本模块提供了一个基于 TCP 的 Socket 服务器，实现了 `go-wind/transport.Server` 接口，支持会话管理、消息类型路由和广播，可以与 `go-wind` 应用框架无缝集成。

## 核心特性

- **会话管理**：每个客户端连接对应一个 Session，包含唯一的 SessionID
- **消息类型路由**：通过 `RegisterMessageHandler` 注册不同消息类型的处理器
- **自定义编解码**：基于 `encoding.Codec` 支持 JSON / Proto / MsgPack 等多种格式
- **广播 / 定向发送**：支持向所有客户端广播或向指定 Session 发送消息
- **阻塞式生命周期**：`Start` 阻塞直到 context 取消，兼容 `go-wind` App

## 安装

```bash
go get github.com/tx7do/go-wind-plugins/transport/tcp
```

## 快速开始

```go
package main

import (
    "context"
    "log"

    tcp "github.com/tx7do/go-wind-plugins/transport/tcp"
)

type ChatMessage struct {
    Sender  string `json:"sender"`
    Message string `json:"message"`
}

func main() {
    srv := tcp.NewServer(
        tcp.WithAddress(":9090"),
        tcp.WithCodec("json"),
    )

    // 注册消息处理器
    tcp.RegisterServerMessageHandler(srv, 1, func(sid tcp.SessionID, msg *ChatMessage) error {
        log.Printf("[%s] %s: %s", sid, msg.Sender, msg.Message)
        srv.Broadcast(1, *msg) // 广播给所有客户端
        return nil
    })

    // 启动服务器（阻塞）
    ctx := context.Background()
    if err := srv.Start(ctx); err != nil {
        panic(err)
    }
}
```

## 配置选项

| 选项 | 说明 | 默认值 |
|------|------|--------|
| `WithAddress(addr)` | 监听地址 | `:0` |
| `WithTimeout(d)` | 读写超时 | `1s` |
| `WithCodec(name)` | 编解码器名称 | `json` |
| `WithSocketConnectHandler(fn)` | 连接 / 断开回调 | - |
| `WithSocketRawDataHandler(fn)` | 原始数据处理回调 | - |
| `WithMessageMarshaler(fn)` | 自定义封包函数 | - |
| `WithMessageUnmarshaler(fn)` | 自定义拆包函数 | - |

## 客户端

```go
cli := tcp.NewClient(
    tcp.WithEndpoint("127.0.0.1:9090"),
    tcp.WithClientCodec("json"),
)
defer cli.Disconnect()

tcp.RegisterClientMessageHandler(cli, 1, func(msg *ChatMessage) error {
    log.Printf("received: %+v", msg)
    return nil
})

if err := cli.Connect(); err != nil {
    panic(err)
}

_ = cli.SendMessage(1, &ChatMessage{Sender: "alice", Message: "hello"})
```

## 协议格式

TCP 模块使用自定义的 `NetPacket` 应用层协议包格式：

```
+-------------------+-------------------+
| Type (uint32, 4B) | Payload (变长)     |
+-------------------+-------------------+
```

- `Type`：消息类型标识，用于路由到对应的 handler
- `Payload`：消息体，由 `encoding.Codec` 进行序列化/反序列化

字节序默认为小端序，可通过 `WithBigEndian()` 切换为大端序。

## 参考资料

- [TCP/IP 协议](https://zh.wikipedia.org/wiki/TCP/IP协议族)
