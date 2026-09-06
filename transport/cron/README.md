# Cron Server

基于 [robfig/cron/v3](https://github.com/robfig/cron) 的定时任务服务器，实现了 `go-wind/transport.Server` 接口，支持秒级 cron 表达式和描述符语法，可以与 `go-wind` 应用框架无缝集成。

## 核心特性

- **秒级表达式**：支持 6 位 cron 表达式（秒 分 时 日 月 周）
- **描述符语法**：支持 `@every`、`@daily`、`@hourly` 等便捷描述符
- **动态任务管理**：运行时动态添加、移除单个或全部任务
- **优雅关闭**：等待正在运行的任务执行完毕后再退出
- **时区支持**：可自定义时区
- **阻塞式生命周期**：`Start` 阻塞直到 context 取消，兼容 `go-wind` App

## 安装

```bash
go get github.com/tx7do/go-wind-plugins/transport/cron
```

## 快速开始

```go
package main

import (
    "context"
    "log"

    cron "github.com/tx7do/go-wind-plugins/transport/cron"
)

func main() {
    srv := cron.NewServer()

    // 在 Start 前注册任务（推荐）
    // 每 10 秒执行一次
    srv.NewTimerJob("*/10 * * * * *", func() {
        log.Println("task run every 10 seconds")
    })

    // 每分钟执行一次
    srv.NewTimerJob("0 */1 * * * *", func() {
        log.Println("task run every minute")
    })

    // 描述符写法
    srv.NewTimerJob("@every 5s", func() {
        log.Println("task run every 5 seconds")
    })

    // 启动服务器（阻塞）
    ctx := context.Background()
    if err := srv.Start(ctx); err != nil {
        panic(err)
    }
}
```

## 配置选项

| 选项                               | 说明               | 默认值     |
|----------------------------------|------------------|---------|
| `WithGracefullyShutdown(enable)` | 是否等待运行中任务完成后再退出  | `true`  |
| `WithLocation(loc)`              | 设置时区             | 系统本地时区  |
| `WithSeconds(enable)`            | 是否使用秒级表达式（默认已启用） | `true`  |
| `WithLogger(logger)`             | 自定义 cron 日志记录器   | 标准库 log |

## 任务管理

```go
// 添加任务
entryID, err := srv.NewTimerJob("0 0 12 * * *", func() {
    // 每天中午 12 点执行
    log.Println("noon task")
})

// 移除单个任务
srv.RemoveTimerJob(entryID)

// 移除所有任务
srv.RemoveAllJobs()

// 获取当前任务数量
count := srv.GetJobCount()

// 获取所有任务条目（含下次执行时间等信息）
entries := srv.GetEntries()
```

## Cron 表达式

支持两种写法：

### 秒级表达式（6 位）

```text
# 秒 分 时 日 月 周
*/10 * * * * *       → 每 10 秒
0   */1 * * * *      → 每分钟
0   0  12 * * *      → 每天 12:00
0   0  12 * * 1-5    → 工作日 12:00
```

### 描述符

```text
@every 5s    → 每 5 秒
@hourly      → 每小时
@daily       → 每天 0 点
@midnight    → 每天 0 点（同 @daily）
@weekly      → 每周一 0 点
@monthly     → 每月 1 日 0 点
```

## 生命周期说明

1. `Start(ctx)`：启动 cron 调度器，阻塞等待 ctx 取消
2. `Stop(ctx)`：优雅停止调度器，等待运行中任务执行完毕
3. 应用退出时通过取消 context 触发关闭

## 适用场景

- 后台定时数据统计、清理
- 定时同步、对账、消息推送
- 定时任务统一管理
- 微服务内部轻量定时任务

## 参考资料

- [robfig/cron](https://github.com/robfig/cron)
- [Cron 表达式语法](https://en.wikipedia.org/wiki/Cron)
