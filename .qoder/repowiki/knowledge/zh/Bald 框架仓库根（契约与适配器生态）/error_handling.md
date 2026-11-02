## 1. 采用的系统/方法

仓库采用自研的 `berrors` 包作为**传输级错误模型**，配合 `berrors/httperr`、`berrors/grpcerr` 两个边界子包完成 HTTP/gRPC 状态码转换；在 gin 侧通过 `transport/web` 的 `WriteResponse`/`ErrorResponse` 统一输出，在 gRPC 侧通过 `pkg/middleware/grpc.ErrorInterceptor` 统一拦截。该方案的核心设计是：**业务层返回领域稳定的 Reason（如 `ORDER_NOT_FOUND`），框架负责把 Code/Reason/Message/Details 无损地跨 HTTP、gRPC、gateway 边界传递。**

## 2. 关键文件与包

- `berrors/code.go`：定义与 gRPC `codes.Code` 一一对应的 `Code*` 常量（0–16）及 `BadRequest/NotFound/AlreadyExists/PermissionDenied/ResourceExhausted/FailedPrecondition/Unauthenticated/Internal/Unimplemented/Unavailable/DeadlineExceeded` 便捷工厂。
- `berrors/errors.go`：核心类型 `Error`（字段 `Code uint32 / Reason string / Message string / Details map[string]string`），以及不可变 builder `WithMessage/WithDetails/WithCause/WithCode`、`FromError`、`StackTrace()`。
- `berrors/httperr/`：`CodeToHTTP`、`HTTPToCode`、`StatusCode`，把 `uint32` 代码映射为 `int` HTTP 状态码，不引入 `net/http` 依赖。
- `berrors/grpcerr/`：`ToStatus`、`FromStatus`，基于 `google.golang.org/grpc/status` 与 `errdetails.ErrorInfo` 做双向转换。
- `transport/web/response.go`：`StatusBody`/`ErrorInfoBody` 与 `WriteResponse`/`ErrorResponse`/`StatusOf`，面向 Gin 的统一 JSON 错误响应体，结构同 grpc-gateway 输出的 `google.rpc.Status`。
- `pkg/middleware/grpc/error.go`：`ErrorInterceptor`，gRPC 链最外层的错误收口拦截器。

## 3. 架构与约定

### 3.1 错误模型与不可变 builder

`Error` 通过 `New(code, reason)` 构造，`Is(target error)` **仅按 `Reason` 匹配**（忽略 `Code`），因此业务可以用同一 `Reason` 常量做断言，而不关心传输类别是否变化。所有 `With*` 方法先 `clone()`，对唯一引用字段 `Details map[string]string` 执行深拷贝，保证 sentinel 永远不可变——这是测试中反复回归的约束（见 `TestImmutableBuilder_DetailsNotShared`、`TestWithDetails_CopiesInputMap`、`TestClone_NilDetailsStaysNil`）。

每个 `Error` 支持 `WithCause(error)` + `Unwrap()` 接入标准库 `errors.Is` 链路；同时调用点自动捕获栈帧，`StackTrace()` 返回字符串。

### 3.2 零依赖的传输层抽象

`berrors` 包自身**不依赖 gRPC 或 net/http**：`Code` 用裸 `uint32` 表示，HTTP/gRPC 转换下沉到 `httperr`/`grpcerr` 子包。这使错误语义可以在纯 Go 模块间传播，而不在核心包中耦合具体传输协议。

### 3.3 跨协议一致性契约（决策⑧）

`transport/web` 的 `StatusBody`/`ErrorInfoBody` 与 `google.rpc.ErrorInfo` protojson 形状完全对齐：`Type` 固定为 `type.googleapis.com/google.rpc.ErrorInfo`，`Domain` 恒为空串，`Metadata` 对应 `Details`，`reason` 字段承载稳定标识。这样前端只需一次解析逻辑——读 `message` 展示给用户，程序化读 `details[0].reason` 做分支判断。

### 3.4 中间件集成方式

- **gRPC**：`ErrorInterceptor` 放在链**最外层**，把任意 `error` 经 `grpcerr.ToStatus` 转为 `status`，确保 `*berrors.Error` 的 `Code/Reason/Details` 不被丢进 `Unknown`。
- **HTTP (Gin)**：`WriteResponse`/`ErrorResponse` 优先尝试 `berrors.FromError`，命中则取 `Code/Message/Reason/Details`，否则兜底 `Internal` 并把原始 `Error()` 文本放入 `Message`。HTTP 状态码走 `httperr.CodeToHTTP`。

### 3.5 往返语义

- gRPC roundtrip：`berrors.NotFound(...).WithMessage(...).WithDetails(...)` → `grpcerr.ToStatus` → `grpcerr.FromStatus` → `berrors.FromError`，要求 `Message/Reason/Details` 全部还原且 `Is` 仍匹配（见 `grpcerr_test.go` 中的 `TestRoundTripPreservesMessage`、`TestPlainStatusKeepsReasonClean`）。
- HTTP mapping 对称性：正向表与反向表的映射必须可逆（至少对 499/200/401/403/404/429/501/503/504 等一对一码），测试 `TestHTTPMappingSymmetry` 强制校验。

## 4. 观察到的约定与约束

- **业务错误必须携带稳定的 `Reason`**（如 `ORDER_NOT_FOUND`），用于跨层 `errors.Is` 匹配；用户可读文案放在 `Message`，不要混入 `Reason`。
- **错误构造使用便捷工厂**（`NotFound`、`PermissionDenied` 等）而非直接 `New`，以保持 `Code` 与领域意图一致。
- **所有 `With*` 派生实例共享 sentinel 的 `Reason`**，但 `Message`/`Details`/`cause`/stack 各自独立。
- **`Details` 必须是深拷贝的 map**；nil `Details` 应保持 nil，避免无谓分配。
- **gRPC 错误处理必须通过 `ErrorInterceptor`**（文档明确要求放在拦截器链最外层），禁止让 handler 直接返回原生 `error` 绕过转换。
- **HTTP 响应统一经 `WriteResponse`/`ErrorResponse`**，禁止业务自行写 JSON 错误体，以保证 `StatusBody` 结构与 `grpc-gateway` 输出同形。
- **未知错误兜底策略**：无法识别为 `*berrors.Error` 的原生错误在 HTTP 下转为 `INTERNAL(500)`、在 gRPC 下原样透传（由底层 `status.Convert` 兜底）。
- **panic/recover**：未发现仓库级的 panic/recover 机制；`berrors` 本身也不 panic，错误通过返回值传播。
- **日志/可观测性**：错误栈通过 `StackTrace()` 暴露给上层 log 适配器，但 `berrors` 自身不直接写入日志，保持传输层与日志层解耦。

## 5. 置信度评估

证据来自 `berrors` 核心包、`httperr`/`grpcerr` 边界包、`transport/web` 与 `pkg/middleware/grpc` 的集成点，以及覆盖 roundtrip、不可变性、对称映射的多组单元测试，属于已确立的系统性实现，置信度为 **high**。