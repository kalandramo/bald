package errors

// 本文件提供传输中立 Code（uint32，对齐 gRPC codes.Code）与 HTTP 状态码
// （纯 int）的双向映射。
//
// 设计决策——为何用 int 而非 http.StatusCode：错误核心包保持零 net/http 依赖，
// 与把 Code 声明为 uint32 而非 codes.Code 是同一分层哲学。HTTP 服务在传输边界
// 应用结果即可：
//
//	w.WriteHeader(errors.CodeToHTTP(wErr.Code))
//
// Code→HTTP 表遵循 grpc-httpjson-transcoding、Envoy 与 Google API HTTP/JSON
// transcoding 的规范映射，是权威的完整 17 项表，与 code.go 中的 Code 常量
// 一一对应。
//
// 反向 HTTP→Code 是多对一（如 HTTP 400 可能对应 InvalidArgument、
// FailedPrecondition 或 OutOfRange），故 HTTPToCode 为每个状态码返回语义上
// 最具代表性的单一 Code。

// codeToHTTP 是权威的 Code → HTTP 状态映射。
// 来源：grpc-httpjson-transcoding / Envoy / Google API transcoding。
var codeToHTTP = map[uint32]int{
	CodeOK:                 200, // OK
	CodeCanceled:           499, // Client Closed Request
	CodeUnknown:            500, // Internal Server Error
	CodeInvalidArgument:    400, // Bad Request
	CodeDeadlineExceeded:   504, // Gateway Timeout
	CodeNotFound:           404, // Not Found
	CodeAlreadyExists:      409, // Conflict
	CodePermissionDenied:   403, // Forbidden
	CodeResourceExhausted:  429, // Too Many Requests
	CodeFailedPrecondition: 400, // Bad Request
	CodeAborted:            409, // Conflict
	CodeOutOfRange:         400, // Bad Request
	CodeUnimplemented:      501, // Not Implemented
	CodeInternal:           500, // Internal Server Error
	CodeUnavailable:        503, // Service Unavailable
	CodeDataLoss:           500, // Internal Server Error
	CodeUnauthenticated:    401, // Unauthorized
}

// httpToCode 是反向 HTTP 状态 → Code 映射。因正向是多对一，每个 HTTP 状态
// 映射到语义上最具代表性的单一 Code（如 400 → InvalidArgument，尽管
// FailedPrecondition 与 OutOfRange 也映射到 400）。
var httpToCode = map[int]uint32{
	200: CodeOK,
	400: CodeInvalidArgument,
	401: CodeUnauthenticated,
	403: CodePermissionDenied,
	404: CodeNotFound,
	409: CodeAlreadyExists,
	429: CodeResourceExhausted,
	499: CodeCanceled,
	500: CodeInternal,
	501: CodeUnimplemented,
	503: CodeUnavailable,
	504: CodeDeadlineExceeded,
}

// CodeToHTTP 返回给定 Code 对应的 HTTP 状态码。未知 Code 兜底 500（Internal
// Server Error），使畸形或未来定义的 Code 绝不会产生误导性的 2xx。
func CodeToHTTP(code uint32) int {
	if httpStatus, ok := codeToHTTP[code]; ok {
		return httpStatus
	}
	return 500
}

// HTTPToCode 返回给定 HTTP 状态码对应的 Code。因 Code→HTTP 是多对一，返回的
// Code 是该状态最具代表性的一个。未映射的 HTTP 状态（如 418、451）兜底
// CodeUnknown，调用方可据此判断不存在精确映射。
func HTTPToCode(httpStatus int) uint32 {
	if code, ok := httpToCode[httpStatus]; ok {
		return code
	}
	return CodeUnknown
}
