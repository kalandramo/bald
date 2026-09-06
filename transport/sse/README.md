# Server-Sent Events (SSE)

Server-Sent Events（SSE）是一种基于 HTTP 协议的服务器推送机制。客户端打开一个长连接，服务器通过简单的 `field: value\n` 文本格式持续推送事件。

本模块实现了 `go-wind/transport.Server` 接口，支持多命名流、订阅者管理、事件重放（Last-Event-ID）和可选鉴权。由于 SSE 基于 HTTP，服务器包装了标准 `http.Server`，支持与 `transport/http` 相同的中间件类型，可以与 `go-wind` 应用框架无缝集成。

## 核心特性

- **多命名流**：通过 `StreamID` 管理多个独立的事件流
- **自动重放**：基于 Last-Event-ID 的断线重连事件回放
- **自动建流**：客户端订阅不存在的流时可自动创建
- **事件广播**：支持向所有流广播事件
- **HTTP 中间件**：兼容 `transport/http` 中间件（recovery / logging / request-id 等）
- **鉴权支持**：内置 Token 提取和鉴权拦截
- **自定义编解码**：基于 `encoding.Codec` 支持 JSON / Proto / MsgPack 等多种格式
- **CORS 支持**：可配置 `Access-Control-Allow-Origin`
- **阻塞式生命周期**：`Start` 阻塞直到 context 取消，兼容 `go-wind` App

## SSE vs WebSocket

| 维度   | SSE         | WebSocket |
|------|-------------|-----------|
| 协议   | HTTP        | 独立协议      |
| 通信方向 | 单向（服务器→客户端） | 全双工       |
| 断线重连 | 浏览器内置       | 需自行实现     |
| 复杂度  | 轻量、简单       | 相对复杂      |
| 数据格式 | UTF-8 文本    | 支持二进制     |

## 安装

```bash
go get github.com/tx7do/go-wind-plugins/transport/sse
```

## 快速开始

```go
package main

import (
    "context"
    "log"
    "time"

    sse "github.com/tx7do/go-wind-plugins/transport/sse"
)

func main() {
    srv := sse.NewServer(":8080",
        sse.WithPath("/events"),
        sse.WithCodec("json"),
    )

    // 创建命名流
    srv.CreateStream("notifications")

    // 定时推送事件
    go func() {
        for {
            time.Sleep(5 * time.Second)
            srv.PublishData(context.Background(), "notifications", map[string]any{
                "message": "hello",
                "time":    time.Now().Format(time.RFC3339),
            })
        }
    }()

    // 启动服务器（阻塞）
    ctx := context.Background()
    if err := srv.Start(ctx); err != nil {
        panic(err)
    }
}
```

## 配置选项

| 选项                            | 说明            | 默认值       |
|-------------------------------|---------------|-----------|
| `WithPath(path)`              | SSE 路由路径      | `/events` |
| `WithCodec(name)`             | 编解码器名称        | `json`    |
| `WithStreamIdKey(key)`        | 流 ID 查询参数键名   | `stream`  |
| `WithBufferSize(n)`           | 每个流事件通道缓冲大小   | `1024`    |
| `WithAutoStream(enable)`      | 自动创建流         | `false`   |
| `WithAutoReplay(enable)`      | 事件重放          | `true`    |
| `WithEncodeBase64(enable)`    | Base64 编码事件数据 | `false`   |
| `WithSplitData(enable)`       | 按换行符分割事件数据    | `false`   |
| `WithEventTTL(d)`             | 事件有效期         | `0`（不过期）  |
| `WithTLS(cert, key)`          | TLS 证书文件路径    | -         |
| `WithTLSConfig(cfg)`          | TLS 配置        | -         |
| `WithHeaders(h)`              | 额外 HTTP 响应头   | -         |
| `WithCORSAllowOrigin(origin)` | CORS 允许来源     | `*`       |
| `WithMiddleware(mw...)`       | HTTP 中间件      | -         |

## 流管理

```go
// 创建流
srv.CreateStream("notifications")

// 移除流
srv.RemoveStream("notifications")

// 获取流
stream := srv.GetStream("notifications")

// 查询流数量
count := srv.StreamCount()
```

## 事件发布

```go
// 发布原始 Event
srv.Publish(ctx, "notifications", &sse.Event{
    Data: []byte("hello"),
})

// 发布任意数据（自动编码）
srv.PublishData(ctx, "notifications", myPayload)

// 发布带事件名的数据
srv.PublishDataWithEventName(ctx, "notifications", "alert", myPayload)

// 发布带完整元数据的数据
srv.PublishDataWithMeta(ctx, "notifications", myPayload,
    sse.WithEventID("msg-1"),
    sse.WithEventName("alert"),
    sse.WithEventRetry("5000"),
    sse.WithEventComment("system alert"),
)

// 向所有流广播
srv.NotifyData(ctx, myPayload)
srv.NotifyDataWithEventName(ctx, "broadcast", myPayload)
```

## 鉴权与 Token

SSE 服务器支持在建立流连接前进行 Token 提取和鉴权拦截。

### 默认 Token 提取规则

如果未自定义提取器，默认按以下顺序提取：

1. `Authorization`（支持 `Bearer <token>`）
2. `X-Token`
3. Query 参数 `token`

### 鉴权示例

```go
srv := sse.NewServer(":8080",
    sse.WithPath("/events"),
    sse.WithAuthorizeFunc(func(r *http.Request, token string) error {
        if token == "" {
            return sse.ErrUnauthorized // 401
        }
        if token != "ok-token" {
            return sse.ErrForbidden // 403
        }
        return nil
    }),
)
```

### 自定义 Token 提取

```go
srv := sse.NewServer(":8080",
    sse.WithTokenHeader("X-Auth-Token"),
    sse.WithAuthorizeFunc(func(r *http.Request, token string) error {
        if token == "" {
            return sse.ErrUnauthorized
        }
        return nil
    }),
)
```

### 订阅回调中读取 Token

```go
srv := sse.NewServer(":8080",
    sse.WithSubscriberFunction(func(streamID sse.StreamID, sub *sse.Subscriber) {
        auth := sub.Authorization()  // 原始 Authorization
        token := sub.Token("")       // Bearer / X-Token / query token 回退
        log.Printf("subscriber joined: stream=%s, token=%s", streamID, token)
    }),
)
```

## JS 客户端示例

```js
const source = new EventSource("http://localhost:8080/events?stream=notifications")

source.onmessage = (event) => {
    console.log("message:", JSON.parse(event.data))
}

source.addEventListener("alert", (event) => {
    console.log("alert:", JSON.parse(event.data))
})

source.onerror = () => {
    console.log("connection lost, browser will auto-reconnect")
}
```

## SSE 协议格式

```
Content-Type: text/event-stream
Cache-Control: no-cache
Connection: keep-alive

data: hello\n\n

event: alert\n
data: {"level": "warning"}\n\n

id: msg-1\n
data: important message\n\n

retry: 5000\n
data: retry interval updated\n\n
```

## 参考资料

- [Server-Sent Events - Wikipedia](https://en.wikipedia.org/wiki/Server-sent_events)
- [Server-Sent Events 教程](https://www.ruanyifeng.com/blog/2017/05/server-sent_events.html)
- [HTML Spec - Server-Sent Events](https://html.spec.whatwg.org/multipage/server-sent-events.html)
