package errors

// Code 是传输级别的误差类别；取值与 gRPC codes.Code 一一对应。
// 用 uint32 而非直接依赖 codes.Code，使错误核心包保持零 gRPC 依赖。
// HTTP 状态码转换由本包 CodeToHTTP/HTTPToCode 提供（返回 int，无 net/http 依赖）；
// gRPC 转换由调用方在传输边界通过 pkg/errors/grpcerr 完成。
//
// 不要改动这些数值：它们与 gRPC 协议、http.go 中的映射表（见 CodeToHTTP）、
// 以及 errdetails.ErrorInfo 语义一一对应。
const (
	CodeOK                 uint32 = 0  // OK, 成功
	CodeCanceled           uint32 = 1  // CANCELED, 调用方取消
	CodeUnknown            uint32 = 2  // UNKNOWN, 未知错误
	CodeInvalidArgument    uint32 = 3  // INVALID_ARGUMENT, 参数非法
	CodeDeadlineExceeded   uint32 = 4  // DEADLINE_EXCEEDED, 超时
	CodeNotFound           uint32 = 5  // NOT_FOUND, 资源不存在
	CodeAlreadyExists      uint32 = 6  // ALREADY_EXISTS, 资源已存在
	CodePermissionDenied   uint32 = 7  // PERMISSION_DENIED, 无权限
	CodeResourceExhausted  uint32 = 8  // RESOURCE_EXHAUSTED, 资源耗尽（限流）
	CodeFailedPrecondition uint32 = 9  // FAILED_PRECONDITION, 前置条件不满足
	CodeAborted            uint32 = 10 // ABORTED, 事务中止（通常可重试）
	CodeOutOfRange         uint32 = 11 // OUT_OF_RANGE, 值越界
	CodeUnimplemented      uint32 = 12 // UNIMPLEMENTED, 未实现
	CodeInternal           uint32 = 13 // INTERNAL, 内部错误
	CodeUnavailable        uint32 = 14 // UNAVAILABLE, 服务不可用（可重试）
	CodeDataLoss           uint32 = 15 // DATA_LOSS, 数据丢失
	CodeUnauthenticated    uint32 = 16 // UNAUTHENTICATED, 未认证
)

// 以下便捷工厂覆盖最常用的传输类别。业务代码只需给出领域唯一的 Reason
// 即可一行构造标准 Error。需要动态变量或包装底层原生错误时用 With* 链上。

// BadRequest 构造参数非法错误（INVALID_ARGUMENT）。
func BadRequest(reason string) *Error { return New(CodeInvalidArgument, reason) }

// NotFound 构造资源不存在错误（NOT_FOUND）。
func NotFound(reason string) *Error { return New(CodeNotFound, reason) }

// AlreadyExists 构造资源已存在错误（ALREADY_EXISTS）。
func AlreadyExists(reason string) *Error { return New(CodeAlreadyExists, reason) }

// PermissionDenied 构造无权限错误（PERMISSION_DENIED）。
func PermissionDenied(reason string) *Error { return New(CodePermissionDenied, reason) }

// ResourceExhausted 构造资源耗尽/限流错误（RESOURCE_EXHAUSTED）。
func ResourceExhausted(reason string) *Error { return New(CodeResourceExhausted, reason) }

// FailedPrecondition 构造前置条件不满足错误（FAILED_PRECONDITION）。
func FailedPrecondition(reason string) *Error { return New(CodeFailedPrecondition, reason) }

// Unauthenticated 构造未认证错误（UNAUTHENTICATED）。
func Unauthenticated(reason string) *Error { return New(CodeUnauthenticated, reason) }

// Internal 构造内部错误（INTERNAL）。
func Internal(reason string) *Error { return New(CodeInternal, reason) }

// Unimplemented 构造未实现错误（UNIMPLEMENTED）。
func Unimplemented(reason string) *Error { return New(CodeUnimplemented, reason) }

// Unavailable 构造服务不可用错误（UNAVAILABLE）。
func Unavailable(reason string) *Error { return New(CodeUnavailable, reason) }

// DeadlineExceeded 构造超时错误（DEADLINE_EXCEEDED）。
func DeadlineExceeded(reason string) *Error { return New(CodeDeadlineExceeded, reason) }
