package web

import (
	"net/http"

	"google.golang.org/protobuf/encoding/protojson"
)

// ProtoJSONOption 定制 BindProtoRequest / WriteProtoResponse 使用的 protojson
// 行为与 HTTP 输出。以 variadic 参数传入，不传时保持引入 Option 之前的默认语义。
type ProtoJSONOption func(*protoJSONConfig)

// protoJSONConfig 聚合 protojson 的解码/编码选项与 HTTP 输出参数。
type protoJSONConfig struct {
	unmarshal   protojson.UnmarshalOptions
	marshal     protojson.MarshalOptions
	statusCode  int
	contentType string
}

// defaultProtoJSONConfig 逐字复刻引入 Option 之前的写死行为：状态码 200、
// Content-Type 为 "application/json; charset=utf-8"、两套 protojson 选项均为零值。
func defaultProtoJSONConfig() protoJSONConfig {
	return protoJSONConfig{
		statusCode:  http.StatusOK,
		contentType: "application/json; charset=utf-8",
	}
}

// WithProtoJSONOptions 直接替换 protojson 的解码/编码选项。
func WithProtoJSONOptions(u protojson.UnmarshalOptions, m protojson.MarshalOptions) ProtoJSONOption {
	return func(cfg *protoJSONConfig) {
		cfg.unmarshal = u
		cfg.marshal = m
	}
}

// WithDiscardUnknown 控制解码时是否忽略未知字段（默认关闭，遇到未知字段报错）。
func WithDiscardUnknown(enable bool) ProtoJSONOption {
	return func(cfg *protoJSONConfig) {
		cfg.unmarshal.DiscardUnknown = enable
	}
}

// WithUseProtoNames 控制编码时是否输出 proto 字段名（默认输出 JSON camelCase 名）。
func WithUseProtoNames(enable bool) ProtoJSONOption {
	return func(cfg *protoJSONConfig) {
		cfg.marshal.UseProtoNames = enable
	}
}

// WithStatusCode 覆盖成功响应的 HTTP 状态码；code <= 0 时忽略。
func WithStatusCode(code int) ProtoJSONOption {
	return func(cfg *protoJSONConfig) {
		if code > 0 {
			cfg.statusCode = code
		}
	}
}

// WithContentType 覆盖成功响应的 Content-Type；空字符串时忽略。
func WithContentType(ct string) ProtoJSONOption {
	return func(cfg *protoJSONConfig) {
		if ct != "" {
			cfg.contentType = ct
		}
	}
}

// newProtoJSONConfig 依次应用 Option 得到最终配置；nil Option 直接跳过。
func newProtoJSONConfig(opts []ProtoJSONOption) protoJSONConfig {
	cfg := defaultProtoJSONConfig()
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	return cfg
}
