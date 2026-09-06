# Asynq

基于 [hibiken/asynq](https://github.com/hibiken/asynq) 的分布式异步任务队列服务器，实现了 `go-wind/transport.Server` 接口。使用 Redis 作为后端，集成消费者（Server）、生产者（Client）、定时调度器（Scheduler）和任务巡检器（Inspector）于一体。

## 核心特性

- **任务入队**：支持即时、延迟（`ProcessIn`）、定时（`ProcessAt`）入队
- **类型化处理器**：基于任务类型（task type）的分发，支持泛型自动反序列化
- **定时任务**：内置 cron 调度器，动态注册/移除定时任务
- **任务重试**：失败自动重试，可自定义重试延迟策略
- **结果等待**：`NewWaitResultTask` 同步等待任务执行完成
- **Redis 多模式**：单节点、Cluster、Sentinel 三种部署模式
- **组件可开关**：Server / Client / Scheduler 可独立启用/禁用
- **自定义编解码**：基于 `encoding.Codec` 支持 JSON / Proto / MsgPack 等
- **阻塞式生命周期**：`Start` 阻塞直到 context 取消，兼容 `go-wind` App

## 安装

```bash
go get github.com/tx7do/go-wind-plugins/transport/asynq
```

## 前置依赖

需要 Redis 服务。使用 Docker 快速启动：

```bash
# Redis
docker run -d --name redis -p 6379:6379 -e ALLOW_EMPTY_PASSWORD=yes bitnami/redis:latest

# Asynqmon（管理后台，可选）
docker run -d --name asynqmon -p 8080:8080 \
    hibiken/asynqmon:latest --redis-addr=host.docker.internal:6379
```

管理后台：http://localhost:8080

## 快速开始

```go
package main

import (
    "context"
    "fmt"
    "log"
    "os"
    "os/signal"
    "syscall"

    asynqServer "github.com/tx7do/go-wind-plugins/transport/asynq"
)

// EmailPayload 是邮件任务的载荷。
type EmailPayload struct {
    To      string `json:"to"`
    Subject string `json:"subject"`
    Body    string `json:"body"`
}

func main() {
    srv := asynqServer.NewServer(
        asynqServer.WithRedisAddress("127.0.0.1:6379"),
    )

    // 注册任务处理器（泛型，自动反序列化）
    asynqServer.RegisterSubscriber[EmailPayload](srv, "email:send",
        func(taskType string, msg *EmailPayload) error {
            fmt.Printf("sending email to %s: %s\n", msg.To, msg.Subject)
            return nil
        },
    )

    // 入队一个任务
    if err := srv.NewTask("email:send", &EmailPayload{
        To:      "user@example.com",
        Subject: "Welcome",
        Body:    "Hello!",
    }); err != nil {
        log.Fatal(err)
    }

    // 注册定时任务（每分钟执行）
    _, _ = srv.NewPeriodicTask("*/1 * * * *", "email:send", &EmailPayload{
        To:      "admin@example.com",
        Subject: "Hourly Report",
    })

    // 启动服务器（阻塞）
    ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
    defer stop()

    if err := srv.Start(ctx); err != nil {
        fmt.Fprintf(os.Stderr, "server error: %v\n", err)
        os.Exit(1)
    }
}
```

## Redis 连接配置

支持三种 Redis 部署模式：

### 单节点

```go
srv := asynqServer.NewServer(
    asynqServer.WithRedisAddress("127.0.0.1:6379"),
    asynqServer.WithRedisPassword("mypassword"),
    asynqServer.WithRedisDB(0),
)
```

### URI 连接

```go
// 单节点
srv := asynqServer.NewServer(
    asynqServer.WithRedisURI("redis://:password@127.0.0.1:6379/0"),
)

// 集群
srv := asynqServer.NewServer(
    asynqServer.WithRedisURI("redis+cluster://:password@node1:6379,node2:6379"),
)

// 哨兵
srv := asynqServer.NewServer(
    asynqServer.WithRedisURI("redis+sentinel://:password@sentinel1:26379/sentinel2:26379/mymaster/0"),
)
```

### 集群模式

```go
srv := asynqServer.NewServer(
    asynqServer.WithRedisType("cluster"),
    asynqServer.WithRedisAddresses([]string{"node1:6379", "node2:6379", "node3:6379"}),
)
```

### 哨兵模式

```go
srv := asynqServer.NewServer(
    asynqServer.WithRedisType("sentinel"),
    asynqServer.WithRedisAddresses([]string{"sentinel1:26379", "sentinel2:26379"}),
    asynqServer.WithMasterName(strPtr("mymaster")),
)
```

## 任务处理器注册

### 泛型注册（推荐）

```go
asynqServer.RegisterSubscriber[EmailPayload](srv, "email:send",
    func(taskType string, msg *EmailPayload) error {
        // msg 已自动反序列化
        return nil
    },
)
```

### 带 Context 的泛型注册

```go
asynqServer.RegisterSubscriberWithCtx[EmailPayload](srv, "email:send",
    func(ctx context.Context, taskType string, msg *EmailPayload) error {
        // 可以从 ctx 获取截止时间等信息
        return nil
    },
)
```

### 原始字节注册

```go
srv.RegisterSubscriber("raw:task",
    func(taskType string, payload asynqServer.MessagePayload) error {
        data := payload.([]byte)
        // 手动处理原始字节
        return nil
    },
    nil, // creator 为 nil，不进行反序列化
)
```

## 任务发布

### 即时任务

```go
srv.NewTask("email:send", &EmailPayload{To: "user@example.com"})
```

### 延迟任务

```go
srv.NewTask("email:send", payload, asynq.ProcessIn(10*time.Minute))
```

### 定时任务

```go
srv.NewTask("email:send", payload, asynq.ProcessAt(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)))
```

### 唯一任务（避免重复）

```go
srv.NewTask("email:send", payload,
    asynq.Unique(5*time.Minute), // 5 分钟内相同任务不重复入队
)
```

### 等待结果

```go
// 入队并同步等待任务完成（轮询模式，最长等待 5 分钟）
err := srv.NewWaitResultTask("email:send", payload)
```

## 定时任务管理

```go
// 注册定时任务
entryID, err := srv.NewPeriodicTask("*/5 * * * * *", "email:report", payload)

// 移除定时任务
srv.RemovePeriodicTask("email:report")

// 移除所有定时任务
srv.RemoveAllPeriodicTask()
```

## 配置选项

### Redis 连接

| 选项 | 说明 | 默认值 |
|------|------|--------|
| `WithRedisType(type)` | `single` / `cluster` / `sentinel` | `single` |
| `WithRedisURI(uri)` | URI 连接字符串 | - |
| `WithRedisAddress(addr)` | 单节点地址 | `127.0.0.1:6379` |
| `WithRedisAddresses(addrs)` | 多节点地址列表 | - |
| `WithRedisAuth(user, pass)` | 用户名 + 密码 | - |
| `WithRedisDB(db)` | 数据库编号 | `0` |
| `WithRedisPoolSize(size)` | 连接池大小 | - |
| `WithRedisConnOpt(opt)` | 直接设置 `asynq.RedisConnOpt` | - |

### 消费者服务器

| 选项 | 说明 | 默认值 |
|------|------|--------|
| `WithConcurrency(n)` | 并发工作协程数 | `20` |
| `WithQueues(queues)` | 队列及权重 | `default: 1` |
| `WithStrictPriority(val)` | 严格优先级模式 | `false` |
| `WithShutdownTimeout(d)` | 关闭超时时间 | - |
| `WithGracefullyShutdown(val)` | 优雅关闭 | `false` |
| `WithMiddleware(mw...)` | asynq 中间件 | - |
| `WithErrorHandler(fn)` | 错误处理器 | - |

### 定时调度器

| 选项 | 说明 | 默认值 |
|------|------|--------|
| `WithLocation(name)` | 时区 | 系统本地时区 |
| `WithPreEnqueueFunc(fn)` | 任务入队前回调 | - |
| `WithPostEnqueueFunc(fn)` | 任务入队后回调 | - |

### 组件开关

| 选项 | 说明 | 默认值 |
|------|------|--------|
| `WithServerEnabled(val)` | 启用/禁用消费者 | `true` |
| `WithClientEnabled(val)` | 启用/禁用生产者 | `true` |
| `WithSchedulerEnabled(val)` | 启用/禁用调度器 | `true` |
| `WithCodec(name)` | 编解码器名称 | `json` |

## 管理工具

### CLI 工具

```bash
go install github.com/hibiken/asynq/tools/asynq@latest

# 查看队列
asynq queue ls --redis-addr=localhost:6379

# 查看任务
asynq task ls --queue=default --redis-addr=localhost:6379
```

### Web UI（Asynqmon）

```bash
docker run -d -p 8080:8080 hibiken/asynqmon:latest \
    --redis-addr=host.docker.internal:6379
```

## 参考资料

- [hibiken/asynq](https://github.com/hibiken/asynq)
- [Asynq 官方文档](https://github.com/hibiken/asynq/wiki)
