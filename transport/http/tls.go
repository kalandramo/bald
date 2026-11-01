package httpserver

import (
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// scheme 根据 TLS 配置返回 URL scheme（https / http）：TLS 段非 nil 即 https。
func scheme(cfg *bootstrapv1.Server_TLS) string {
	if cfg != nil {
		return "https"
	}
	return "http"
}
