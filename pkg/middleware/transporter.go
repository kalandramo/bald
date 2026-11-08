// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

// transporter.go 是协议无关的请求元数据载体（自 onexmesh transport.Transporter
// 移植，MIT 同 owner）：把 http.Header 与 grpc metadata.MD 抹平在一个 Header
// 接口后面，中间件只依赖 Transporter 接口，不认识具体协议。
//
// 与 bald/transport（模块）的关系：transport.Server 是生命周期契约（J6——协议
// 只承诺协议形状），本文件的 Transporter 是**中间件层的请求元数据载体**，由
// 三座桥在装配点注入 ctx——两者正交，不改变 J6 的承诺面。

package middleware

import (
	"context"
	"net/http"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
)

// Kind 标识一次请求的传输协议。
type Kind string

const (
	// KindGRPC 表示 gRPC 请求。
	KindGRPC Kind = "gRPC"
	// KindHTTP 表示 HTTP 请求。
	KindHTTP Kind = "HTTP"
)

// String 返回协议的字符串形态。
func (k Kind) String() string { return string(k) }

// Header 是请求/响应头的协议无关视图：http.Header 与 grpc metadata.MD 都满足，
// 中间件读写头时不需要知道传输协议。
type Header interface {
	// Get 返回 key 的第一个值，缺失返回 ""。
	Get(key string) string
	// Set 把 key 的值替换为单值。
	Set(key, value string)
	// Add 向 key 追加一个值。
	Add(key, value string)
	// Keys 返回全部已存在的头键。
	Keys() []string
	// Values 返回 key 的全部值。
	Values(key string) []string
}

// httpHeader 把 net/http.Header 适配成 Header。
type httpHeader struct{ http.Header }

// NewHTTPHeader 把 http.Header 包装成 Header。
func NewHTTPHeader(h http.Header) Header { return httpHeader{h} }

func (h httpHeader) Get(key string) string      { return h.Header.Get(key) }
func (h httpHeader) Set(key, value string)      { h.Header.Set(key, value) }
func (h httpHeader) Add(key, value string)      { h.Header.Add(key, value) }
func (h httpHeader) Values(key string) []string { return h.Header.Values(key) }

func (h httpHeader) Keys() []string {
	keys := make([]string, 0, len(h.Header))
	for k := range h.Header {
		keys = append(keys, k)
	}
	return keys
}

// metadataHeader 把 grpc metadata.MD 适配成 Header。
type metadataHeader struct{ metadata.MD }

// NewMetadataHeader 把 grpc metadata.MD 包装成 Header。
func NewMetadataHeader(md metadata.MD) Header { return metadataHeader{md} }

// Get 返回 key 的第一个值。与 metadata.MD.Get 不同：键缺失返回 "" 而非 panic。
func (h metadataHeader) Get(key string) string {
	if vs := h.MD[key]; len(vs) > 0 {
		return vs[0]
	}
	return ""
}

func (h metadataHeader) Set(key, value string)      { h.MD.Set(key, value) }
func (h metadataHeader) Add(key, value string)      { h.MD.Append(key, value) }
func (h metadataHeader) Values(key string) []string { return h.MD[key] }

func (h metadataHeader) Keys() []string {
	keys := make([]string, 0, len(h.MD))
	for k := range h.MD {
		keys = append(keys, k)
	}
	return keys
}

// Transporter 携带一次在途请求的协议无关元数据。它把 http.Header 与
// grpc metadata.MD 抹平在 Header 接口后面（kratos transport 抽象的同思路）。
type Transporter interface {
	// Kind 返回本次请求的传输协议。
	Kind() Kind
	// Operation 返回全限定操作名。gRPC 是完整方法名（如
	// "/pkg.Service/Method"）；HTTP 是 "METHOD /路由模板"（如 "POST
	// /v1/users/:userID"），无路由命中时是 "METHOD unmatched"。
	//
	// 它刻意取**路由模板**而非请求路径：这个字符串是指标标签与 span 名，
	// 带路径参数的 path（"/v1/users/usr-123"）是每个用户一条时间序列——
	// 单个标签就这样无界增长直到拖垮指标后端。原始 path 仍可用（Path），
	// 只进 span 不进指标。
	Operation() string
	// Endpoint 返回处理本请求的服务端地址。
	Endpoint() string
	// RequestHeader 返回入站请求头。
	RequestHeader() Header
	// ReplyHeader 返回出站响应头。
	ReplyHeader() Header

	// Method 返回 HTTP 动词；非 HTTP 时返回 ""。与 Route 一起构成 HTTP 语义
	// 约定要求的低基数对（http.request.method + http.route）。
	Method() string
	// Route 返回匹配到的路由模板（框架语法 "/v1/users/:userID"）；无路由
	// （gRPC）或未命中时返回 ""。它是 Operation 去掉动词后的形态。
	Route() string
	// Path 返回原始请求路径；非 HTTP 或未知时返回 ""。只进 span，永不进指标。
	Path() string
	// Protocol 返回 network.protocol.version 形态的协议版本（"1.1"、"2.0"）。
	Protocol() string

	// StatusCode 返回 HTTP 响应状态；非 HTTP 或尚未写出时返回 0。
	StatusCode() int
	// SetStatusCode 记录 HTTP 响应状态。桥在响应定型后、链回卷前调用一次，
	// 让链上每个中间件看到同一个终值。
	SetStatusCode(int)
	// GRPCCode 返回本次调用的 gRPC 状态：成功时与非 gRPC 时是 codes.OK。
	GRPCCode() codes.Code
	// SetGRPCCode 记录 gRPC 状态，时机约束与 SetStatusCode 相同。
	SetGRPCCode(codes.Code)
}

// transporter 是 Transporter 的默认实现。
type transporter struct {
	kind        Kind
	operation   string
	endpoint    string
	reqHeader   Header
	replyHeader Header

	method     string
	route      string
	path       string
	protocol   string
	statusCode int
	grpcCode   codes.Code
}

// transporterPool 跨请求复用实例（对象池模式），热请求路径省一次堆分配。
// 实例可复用的前提：桥在请求链同步完成后 Release；Transporter 不得比请求活得久。
var transporterPool = sync.Pool{
	New: func() any { return &transporter{} },
}

// NewTransporter 从部件组装 Transporter（底层结构取自对象池）。走热请求路径
// 的调用方在链读完后应 defer ReleaseTransporter(t)。
//
// gRPC 与 HTTP 对一次请求的声明形状不同，所以协议特有的半边经 opts 传入，
// 而不是再加两个位置参数让两个调用方之一永远留空。
func NewTransporter(kind Kind, operation, endpoint string, reqHeader, replyHeader Header, opts ...TransportOption) Transporter {
	t := transporterPool.Get().(*transporter)
	t.kind = kind
	t.operation = operation
	t.endpoint = endpoint
	t.reqHeader = reqHeader
	t.replyHeader = replyHeader
	for _, opt := range opts {
		opt(t)
	}
	return t
}

// TransportOption 填入 Transporter 除通用部分外的协议特有请求元数据。
type TransportOption func(*transporter)

// WithHTTPRequest 记录只有 HTTP 桥知道的量：动词、路由器匹配到的路由模板、
// 原始路径与协议版本。route 传 "" 有语义——未命中请求（404、扫描器）以此
// 归入有界的 "METHOD unmatched" operation，而不是按各自的 path 计数。
func WithHTTPRequest(method, route, path, protocol string) TransportOption {
	return func(t *transporter) {
		t.method = method
		t.route = route
		t.path = path
		t.protocol = protocol
	}
}

// ReleaseTransporter 把经 NewTransporter 取出的 Transporter 归还对象池，清空
// 字段避免池化实例保留请求级引用。只能在 Transporter 的全部读者（例如从 ctx
// 取它的中间件）结束后调用。
//
// 与上游（onexmesh transport.go）的一处刻意分歧：grpcCode **必须复位**。
// 上游注释称「codes.Code 零值即 codes.OK，无需复位」——但零值只在构造时成立；
// 上一请求 SetGRPCCode(codes.Internal) 后归还，复用实例会把 Internal 带进
// 下一请求。本仓测试钉住此回归（transporter_test.go TestTransporterPoolReset）。
func ReleaseTransporter(t Transporter) {
	if tp, ok := t.(*transporter); ok {
		tp.kind = ""
		tp.operation = ""
		tp.endpoint = ""
		tp.reqHeader = nil
		tp.replyHeader = nil
		tp.method = ""
		tp.route = ""
		tp.path = ""
		tp.protocol = ""
		tp.statusCode = 0
		tp.grpcCode = codes.OK // 复位：池化实例必须从「没有失败」起步
		transporterPool.Put(tp)
	}
}

func (t *transporter) Kind() Kind            { return t.kind }
func (t *transporter) Operation() string     { return t.operation }
func (t *transporter) Endpoint() string      { return t.endpoint }
func (t *transporter) RequestHeader() Header { return t.reqHeader }
func (t *transporter) ReplyHeader() Header   { return t.replyHeader }

func (t *transporter) Method() string   { return t.method }
func (t *transporter) Route() string    { return t.route }
func (t *transporter) Path() string     { return t.path }
func (t *transporter) Protocol() string { return t.protocol }

func (t *transporter) StatusCode() int { return t.statusCode }

// GRPCCode 不需要「设过没有」标志：codes.OK 是零值，没人报告过结果的实例
// 读出来就是成功——正是一个从未失败的请求应有的读数。
func (t *transporter) GRPCCode() codes.Code { return t.grpcCode }

func (t *transporter) SetStatusCode(code int)      { t.statusCode = code }
func (t *transporter) SetGRPCCode(code codes.Code) { t.grpcCode = code }

// ctxKey 是把 Transporter 放进 context 的键。
type ctxKey struct{}

// NewServerContext 返回携带 tr 的新 context。
func NewServerContext(ctx context.Context, tr Transporter) context.Context {
	return context.WithValue(ctx, ctxKey{}, tr)
}

// FromServerContext 取出经 NewServerContext 放入的 Transporter；不存在时 ok 为 false。
func FromServerContext(ctx context.Context) (Transporter, bool) {
	tr, ok := ctx.Value(ctxKey{}).(Transporter)
	return tr, ok
}
