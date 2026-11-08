// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

// Package semconv 持有 bald 的请求观测词表（OTel semconv v1.43.0）：一次请求
// 被同时记进两个目的地——trace 存储里的一行 span、指标序列里的一个点——两者
// 必须对「这次请求是什么」达成一致。因此属性构造只此一份（J2 重复税清偿）：
// span 面与指标面、gin 与 gRPC、pkg/metrics 与观测中间件全部经本包取词。
//
// 词表是 OpenTelemetry 语义约定的：请求用 OTel 原生消费者已知的名字描述
// （http.request.method、rpc.response.status_code），不自造名字——dashboard、
// 告警表达式、APM 自动识别都按约定拼写匹配，自造一个词就少一块生态。
//
// 为什么是裸 attribute.Key 常量而非 otel semconv 生成物：pkg 模块的 otel
// v1.44.0 不携带 semconv v1.43.0 生成包（v1.45.0 起才有），引入即连带全仓
// otel 升版与 go.sum 级联。键名/值域由本包常量钉住——同一份实现即同一份词表，
// otel 升版时可平移到生成物并删掉这些常量。
//
// 平台的失败分类（berrors.Reason，封闭小集）走 ErrorReasonKey 紧邻标准
// error.type：error.type 对 HTTP 是状态码、对 gRPC 是状态名——只说「这次 403」，
// 说不出「因租户欠费 403」。标准指标 + 平台归因，两者兼得。
package semconv

import (
	"strconv"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"google.golang.org/grpc/codes"

	"github.com/kalandramo/bald/berrors"
)

// ErrorReasonKey 承载平台自身的失败分类（berrors.Reason），紧邻语义约定的
// error.type。
const ErrorReasonKey = attribute.Key("bald.error.reason")

// v1.43.0 的键名（自持常量，理由见包文档）。
const (
	keyHTTPMethod    = attribute.Key("http.request.method")
	keyHTTPRoute     = attribute.Key("http.route")
	keyURLPath       = attribute.Key("url.path")
	keyURLScheme     = attribute.Key("url.scheme")
	keyHTTPStatus    = attribute.Key("http.response.status_code")
	keyProtocolVer   = attribute.Key("network.protocol.version")
	keyErrorType     = attribute.Key("error.type")
	keyServerAddress = attribute.Key("server.address")
	keyServerPort    = attribute.Key("server.port")
	keyRPCSystem     = attribute.Key("rpc.system.name")
	keyRPCMethod     = attribute.Key("rpc.method")
	keyRPCStatus     = attribute.Key("rpc.response.status_code")
)

// rpcSystemGRPC 是 rpc.system.name 的 gRPC 取值。小写——这是约定文档的拼写，
// 按 spec 大小写过滤的 dashboard 才找得到。
const rpcSystemGRPC = "grpc"

// HTTPSpanAttrs 把一次 HTTP 服务端请求渲染成 span 属性全集（含 url.path）。
//
// url.path 只在 span 面：span 是 trace 存储里的一行，带用户 ID 的 path 代价
// 一行；指标属性是标签，同一串 path 代价是每用户一条时间序列、永久。两者
// 都有用且只有一个是付得起的——有界的路由模板走 http.route（两面都有），
// 精确 path 只进 span。
//
// method/route/protocol/path 为空时省略对应属性；status>0 时记
// http.response.status_code，且仅当 status>=400 记 error.type（状态码字符串）——
// 200 上挂 error.type 会让「有 error.type」不再意味着「这次失败了」。
func HTTPSpanAttrs(method, route, path, protocol string, status int, err error) []attribute.KeyValue {
	attrs := make([]attribute.KeyValue, 0, 7)
	if method != "" {
		// 必须有界：约定把 http.request.method 的取值限定在闭集里，无法识别
		// 的动词报 _OTHER——扫描器的 "GARBAGE" 才不会变成一条新序列。
		attrs = append(attrs, keyHTTPMethod.String(knownHTTPMethod(method)))
	}
	if route != "" {
		attrs = append(attrs, keyHTTPRoute.String(route))
	}
	if protocol != "" {
		attrs = append(attrs, keyProtocolVer.String(protocol))
	}
	if path != "" {
		attrs = append(attrs, keyURLPath.String(path))
	}
	attrs = appendHTTPFailure(attrs, status)
	return appendReason(attrs, err)
}

// HTTPMetricAttrs 把一次 HTTP 服务端请求渲染成指标属性全集（无 url.path）。
//
// http.request.method 与 url.scheme 在此手动携带而非省略：约定的 typed
// instrument 会把必选属性作位置参数自动注入，bald 的指标 instrument 是裸
// histogram（pkg 模块未用 httpconv 生成物，见包文档），没有自动注入这一层。
func HTTPMetricAttrs(method, scheme, route, protocol string, status int, err error) []attribute.KeyValue {
	attrs := make([]attribute.KeyValue, 0, 6)
	if method != "" {
		attrs = append(attrs, keyHTTPMethod.String(knownHTTPMethod(method)))
	}
	if scheme != "" {
		attrs = append(attrs, keyURLScheme.String(scheme))
	}
	if route != "" {
		attrs = append(attrs, keyHTTPRoute.String(route))
	}
	if protocol != "" {
		attrs = append(attrs, keyProtocolVer.String(protocol))
	}
	attrs = appendHTTPFailure(attrs, status)
	return appendReason(attrs, err)
}

// ActiveRequestAttrs 渲染 http.server.active_requests 的属性
// （url.scheme/server.address/server.port）。scheme 恒携带；addr/port 空时省略
// （0 = 未知）。
func ActiveRequestAttrs(scheme, serverAddr string, serverPort int) []attribute.KeyValue {
	attrs := make([]attribute.KeyValue, 0, 3)
	attrs = append(attrs, keyURLScheme.String(scheme))
	if serverAddr != "" {
		attrs = append(attrs, keyServerAddress.String(serverAddr))
	}
	if serverPort > 0 {
		attrs = append(attrs, keyServerPort.Int(serverPort))
	}
	return attrs
}

// RPCAttrs 把一次 gRPC 服务端调用渲染成属性集——span 面与指标面同一份：
// 裸 instrument 没有自动注入，rpc.system.name 本就两面都要带，无需分叉。
//
// 无 rpc.service 属性：约定以全限定 rpc.method（"/pkg.Service/Method"）承载
// 服务名，拆回来是同一事实的第二种非标准拼法。
func RPCAttrs(operation string, code codes.Code, err error) []attribute.KeyValue {
	attrs := make([]attribute.KeyValue, 0, 4)
	attrs = append(attrs, keyRPCSystem.String(rpcSystemGRPC))
	if operation != "" {
		attrs = append(attrs, keyRPCMethod.String(operation))
	}
	name := GRPCStatusCodeName(code)
	attrs = append(attrs, keyRPCStatus.String(name))
	if code != codes.OK {
		// gRPC 的 error.type 取状态名（约定如此）——失败类别与状态拼写一致。
		attrs = append(attrs, keyErrorType.String(name))
	}
	return appendReason(attrs, err)
}

// appendHTTPFailure 记录 HTTP 状态与失败类别：status>0 记
// http.response.status_code，仅 status>=400 记 error.type（状态码字符串）。
func appendHTTPFailure(attrs []attribute.KeyValue, status int) []attribute.KeyValue {
	if status <= 0 {
		return attrs
	}
	attrs = append(attrs, keyHTTPStatus.Int(status))
	if status >= 400 {
		attrs = append(attrs, keyErrorType.String(strconv.Itoa(status)))
	}
	return attrs
}

// appendReason 添加平台的失败分类，且仅当确有失败可分类。
//
// err != nil 的判断不与空串检查冗余：berrors 对未知错误与 nil 都返回空
// Reason，只查字符串会「碰巧正确」，且只在常量不变的前提下持续正确。
func appendReason(attrs []attribute.KeyValue, err error) []attribute.KeyValue {
	if err == nil {
		return attrs
	}
	if werr, ok := berrors.FromError(err); ok && werr.Reason != "" {
		attrs = append(attrs, ErrorReasonKey.String(werr.Reason))
	}
	return attrs
}

// knownHTTPMethods 是约定允许 http.request.method 在服务端取值的闭集。
// 其余——路由器凑巧匹配的动词、扫描器的垃圾——报 _OTHER。
var knownHTTPMethods = map[string]string{
	"GET":     "GET",
	"HEAD":    "HEAD",
	"POST":    "POST",
	"PUT":     "PUT",
	"DELETE":  "DELETE",
	"CONNECT": "CONNECT",
	"OPTIONS": "OPTIONS",
	"TRACE":   "TRACE",
	"PATCH":   "PATCH",
}

// knownHTTPMethod 把 method 有界化到约定的取值集。
func knownHTTPMethod(method string) string {
	if known, ok := knownHTTPMethods[method]; ok {
		return known
	}
	return "_OTHER"
}

// GRPCStatusCodeName 把 gRPC 码渲染成约定与 gRPC 线上协议的拼写——"NOT_FOUND"，
// 而非 Go codes 包的 "NotFound"。
//
// 两者只差大小写与分隔符，恰好够让一份按 spec 拼写的 dashboard 变量或告警
// 表达式什么都匹配不到。
//
// 分隔规则是「词边界」，不是「大写字母」：下划线插在小写字母后跟大写字母处。
// 只测大写字母会把两个单词码拆坏——OK 会变 O_K；这个错误在十六个普通
// CamelCase 码里不可见。护栏让未知码的 String()（"Code(17)"）以 "CODE(17)"
// 存活，而不是碎成控制字符。
func GRPCStatusCodeName(code codes.Code) string {
	name := code.String()

	var b strings.Builder
	b.Grow(len(name) + 2)
	for i := 0; i < len(name); i++ {
		ch := name[i]
		switch {
		case ch >= 'A' && ch <= 'Z':
			if i > 0 && name[i-1] >= 'a' && name[i-1] <= 'z' {
				b.WriteByte('_')
			}
			b.WriteByte(ch)
		case ch >= 'a' && ch <= 'z':
			b.WriteByte(ch - 'a' + 'A')
		default:
			b.WriteByte(ch)
		}
	}
	return b.String()
}
