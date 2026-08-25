package web

import (
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/kalandramo/bald/pkg/log"
)

// Recovery 在 handler panic 时恢复，记录堆栈并返回 500 明文。
// 注意：panic 后无法再安全地写结构化业务错误，故按 HTTP 层兜底处理。
// 若下游 handler 已写出部分响应（statusWriter.status != 0），则不再二次写头，
// 避免破坏已发出的响应；否则补 Content-Type 让客户端正确解析 500 正文。
func Recovery() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			defer func() {
				if rec := recover(); rec != nil {
					log.GetLogger().Error(r.Context(), "http handler panicked",
						"panic", rec, "stack", string(debug.Stack()))
					if sw.status == http.StatusOK {
						w.Header().Set("Content-Type", "text/plain; charset=utf-8")
						w.WriteHeader(http.StatusInternalServerError)
						_, _ = w.Write([]byte("internal server error"))
					}
				}
			}()
			next.ServeHTTP(sw, r)
		})
	}
}

// RequestID 从请求头 X-Request-ID 提取（缺失时生成），注入 ctx，并在响应头
// X-Request-ID 回写，便于链路追踪。header 可省略，默认 "X-Request-ID"。
func RequestID(header ...string) Middleware {
	h := "X-Request-ID"
	if len(header) > 0 && header[0] != "" {
		h = header[0]
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(h)
			if id == "" {
				id = newID()
			}
			ctx := withRequestID(r.Context(), id)
			w.Header().Set(h, id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// CORSConfig 配置跨域中间件。
type CORSConfig struct {
	// AllowOrigin 允许的来源，默认 "*"。
	AllowOrigin string
	// AllowMethods 允许的 METHODS，默认简单集。
	AllowMethods []string
	// AllowHeaders 允许的请求头，默认 Content-Type, Authorization。
	AllowHeaders []string
	// MaxAge 预检缓存秒数，默认 86400。
	MaxAge int
}

// DefaultCORS 返回常见默认 CORS 配置。
func DefaultCORS() CORSConfig {
	return CORSConfig{
		AllowOrigin:  "*",
		AllowMethods: []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowHeaders: []string{"Content-Type", "Authorization"},
		MaxAge:       86400,
	}
}

// CORS 返回跨域中间件。对 OPTIONS 预检直接 204 应答。
func CORS(cfg CORSConfig) Middleware {
	if cfg.AllowOrigin == "" {
		cfg = DefaultCORS()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", cfg.AllowOrigin)
			h.Set("Access-Control-Allow-Methods", strings.Join(cfg.AllowMethods, ", "))
			h.Set("Access-Control-Allow-Headers", strings.Join(cfg.AllowHeaders, ", "))
			if cfg.MaxAge > 0 {
				h.Set("Access-Control-Max-Age", strconv.Itoa(cfg.MaxAge))
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// Secure 加固响应头（HSTS、禁止嗅探、XSS 防护、frame 隔离）。
func Secure() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("X-XSS-Protection", "1; mode=block")
			if r.TLS != nil {
				h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
			}
			next.ServeHTTP(w, r)
		})
	}
}

// Logging 记录每个请求的方法、路径、状态码与耗时（通过 pkg/log）。
func Logging() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)
			log.GetLogger().Info(r.Context(), "http request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", sw.status,
				"duration", time.Since(start).String(),
			)
		})
	}
}

// statusWriter 透传 ResponseWriter 并记录状态码。
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (sw *statusWriter) WriteHeader(code int) {
	sw.status = code
	sw.ResponseWriter.WriteHeader(code)
}
