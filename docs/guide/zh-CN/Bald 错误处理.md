# Bald 错误处理

面向使用者的操作手册：怎么构造错误、怎么匹配错误、错误怎么出 HTTP/gRPC 边界。
设计论证（为什么零依赖核心 + 对等桥接子包、决策①~⑨）见
`docs/devel/zh-CN/Bald 错误模型设计.md`，本文只讲用法。

## 心智模型：一句话版

**错误的一生只有三段，前两段占业务代码的绝大多数、且只需要根包：**

```
产生（源头）──→ 匹配（消费）──→ 投影（边界）
   根包             根包            grpcerr / httperr
```

biz 构造、补偿/降级分支的匹配、DAL 包装、CLI 与测试断言——全部只
`import berrors`；投影段已由框架收口（HTTP 侧 `web.ErrorResponse`、gRPC 侧
`ErrorInterceptor`），**业务代码几乎不需要碰桥接子包**。下游 bald-admin
后端 25 个文件 import 根包、0 处 import 桥接子包，即此结构的实证。

## 0. 引依赖

berrors 是独立 module（`github.com/kalandramo/bald/berrors`，v0.1.0），根包
零第三方依赖——import 它只拉标准库：

```bash
go get github.com/kalandramo/bald/berrors
```

grpcerr/httperr 与根包同属一个 module，无需单独 go get；**只有写传输边界
收口代码时才 import 它们**（见第 4、5 节——多数项目一次都不用写）。

## 1. 构造错误：工厂优先

11 个工厂覆盖最常用的传输类别，一行构造标准错误：

```go
import "github.com/kalandramo/bald/berrors"

// biz 层最常见形态：工厂 + WithMessage
return nil, berrors.BadRequest("user/missing_required_fields").
    WithMessage("user.Create: id, username and password are required")
```

| 工厂 | Code | 语义 |
| --- | --- | --- |
| `BadRequest` | 3 | 参数非法（400） |
| `NotFound` | 5 | 资源不存在（404） |
| `AlreadyExists` | 6 | 资源已存在（409） |
| `PermissionDenied` | 7 | 无权限（403） |
| `ResourceExhausted` | 8 | 限流/资源耗尽（429） |
| `FailedPrecondition` | 9 | 前置条件不满足（400） |
| `Unauthenticated` | 16 | 未认证（401） |
| `Internal` | 13 | 内部错误（500） |
| `Unimplemented` | 12 | 未实现（501） |
| `Unavailable` | 14 | 服务不可用，可重试（503） |
| `DeadlineExceeded` | 4 | 超时（504） |

工厂之外的场景用 `New(code, reason)` + 17 个 `Code*` 常量；特殊业务需要覆盖
传输类别时链上 `WithCode`（逃生舱，慎用）。

**Reason 命名规范**：领域稳定标识，`域/蛇形标识`（如 `user/invalid_username`）。
它是不变契约——永不本地化、永不改动，`Is` 按它匹配，前端按它做程序化分支。

### With* 链：按需附加信息

```go
berrors.NotFound("order/not_found").
    WithMessage("订单 %s 不存在", orderID).      // 用户可见文案（Sprintf 格式化）
    WithDetails(map[string]string{"id": orderID}) // i18n 模板变量，前端做本地化
```

- `WithMessage`：给最终用户看的完整一句话。
- `WithDetails`：给前端 i18n 的模板变量，与 Message 并存不冲突（两个消费方两种载体）。
- `WithCause`：包装底层原生错误（如 DAL 的 sql 错误），保留 error 链可拆。
- `WithCode`：覆盖传输类别的逃生舱。

### sentinel：包级变量 + 派生安全

所有 `With*` 返回**新实例**、绝不修改接收者，所以包级 sentinel 是安全的：

```go
var ErrOrderNotFound = berrors.NotFound("ORDER_NOT_FOUND")

// 每次派生都是新实例，ErrOrderNotFound 本体永不被污染
return ErrOrderNotFound.WithDetails(map[string]string{"id": id})
```

## 2. 匹配错误：只认 Reason

`Is` 按 `Reason` 相等匹配，Code/Message/Details/cause 全部忽略——Code 是易变
传输分类（同一失败在不同服务可能不同），Reason 才是稳定契约：

```go
if berrors.Is(err, ErrOrderNotFound) {
    // 走补偿/降级分支
}
```

从任意错误链提取 `*Error`（补偿逻辑、日志收集常用）：

```go
if werr, ok := berrors.FromError(err); ok {
    log.Printf("reason=%s code=%d stack=%s", werr.Reason, werr.Code, werr.StackTrace())
}
```

标准库桥接：包内重导出 `Is/As/Unwrap/Join`，与标准库 `errors` 可同文件共存
（无需别名 import）。注意 `New` 不重导出（签名冲突），需要标准库 New 时别名
import 标准库。

## 3. 错误出 HTTP：gin 面（通常零代码）

gin handler 里返回 `*Error`，`web.WriteResponse` / `web.ErrorResponse` 自动
完成投影——状态码经 `httperr.CodeToHTTP`，响应体是 google.rpc.Status JSON
（决策⑧，与 grpc-gateway 转码输出同形）：

```go
func (h *Handler) GetUser(c *gin.Context) {
    data, err := h.biz.GetUser(c, id)
    web.WriteResponse(c, data, err) // err 非 nil 时自动写错误响应体
}
```

响应体形状（前端只写一次解析：展示读 `message`，程序化读 `details[0].reason`）：

```json
{
  "code": 5,
  "message": "订单 42 不存在",
  "details": [{
    "@type": "type.googleapis.com/google.rpc.ErrorInfo",
    "reason": "order/not_found",
    "domain": "",
    "metadata": { "id": "42" }
  }]
}
```

手写出口的中间件（如 authn/authz）需要状态码时才 import `httperr`：

```go
c.AbortWithStatusJSON(httperr.StatusCode(werr), web.StatusOf(err))
```

17 码映射要点：`NotFound→404`、`ResourceExhausted→429`、`Unavailable→503`、
`Canceled→499`（nginx 约定）、`Unauthenticated→401`；未知码兜底 500，绝不产生
误导性 2xx。

## 4. 错误出 gRPC：拦截器一行接线

gRPC 面同样是框架收口——`ErrorInterceptor` 放拦截器链**最外层**（最先注册），
内层（校验、认证）的错误才能全部被转换：

```go
grpc.ChainUnaryInterceptor(
    grpcmw.ErrorInterceptor(),      // ← 最外层，收口所有错误
    grpcmw.RequestIDInterceptor(),
    grpcmw.ValidatorInterceptor(v),
)
```

不放它的后果：gRPC 框架的 `status.Convert` 会把 `*berrors.Error` 当普通
error，只剩 `Error()` 文本进 `Unknown`，Code/Reason/Details 全丢。

**客户端还原**：跨服务调用收到错误时，`grpcerr.FromStatus` 从
`errdetails.ErrorInfo` 恢复完整语义（`_example/bald` e2e 同款写法）：

```go
restored := grpcerr.FromStatus(status.Convert(err))
if berr, ok := berrors.FromError(restored); ok {
    // Reason/Details 完整保留，可按 ErrXxx 匹配
    if berrors.Is(berr, ErrOrderNotFound) { /* 降级 */ }
}
```

Kratos 互通是天然的：两边都产/认 `ErrorInfo`，无需引 Kratos 包。

## 5. 三包 API 速查

| 包 | import 时机 | API |
| --- | --- | --- |
| `berrors`（根包） | 随处 | `New`、11 工厂、`WithMessage/Details/Cause/Code`、`Is`、`FromError`、`As/Unwrap/Join`（桥接）、`StackTrace` |
| `httperr` | 手写 HTTP 出口时 | `CodeToHTTP(code)`、`HTTPToCode(status)`、`StatusCode(e)` |
| `grpcerr` | 写 gRPC 拦截器/客户端时 | `ToStatus(err)`、`FromStatus(st)` |

## 6. FAQ

**为什么 `err.Error()` 里看不到 Message？** `Error()` 是日志取向
（`code: 5, reason: order/not_found`），Message 给用户看（字段直接取）——
混入会污染日志且无法结构化消费。

**为什么 `Is` 匹配不上？** 检查两边 Reason 是否逐字相等（大小写敏感）；
`Is` 不比 Code——`NotFound("x")` 与 `Internal("x")` 互相匹配。

**栈是什么时候捕获的？** 构造时（`New` 即捕获失败点调用栈），无需等
`With*`；`With*` 上的捕获是幂等保护，不覆盖构造点栈。`StackTrace()` 供日志
收集器消费。

**`WithDetails` 传入的 map 会被复用吗？** 会——`clone` 对 Details 浅拷贝、
`WithDetails` 整体替换 map。不要在派生后再就地修改传入的 map。

**i18n 怎么做？** `Message` 写默认语言完整句子；`Details` 放模板变量，
前端按 `reason` 选词条、用 `metadata` 填变量。两者都写不冲突。
