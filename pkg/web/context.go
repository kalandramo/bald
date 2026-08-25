package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
)

type ctxKeyRequestID struct{}
type ctxKeyPathVars struct{}

// withRequestID 把请求 ID 注入 ctx。
func withRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKeyRequestID{}, id)
}

// RequestIDFromContext 从 ctx 取出请求 ID，缺失时返回空串。
func RequestIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyRequestID{}).(string); ok {
		return v
	}
	return ""
}

// withPathVars 把 pattern 中的通配符变量名注入 ctx，供 URI 绑定器枚举。
func withPathVars(ctx context.Context, vars []string) context.Context {
	return context.WithValue(ctx, ctxKeyPathVars{}, vars)
}

// pathVarsFromContext 取出 pattern 变量名列表。
func pathVarsFromContext(ctx context.Context) []string {
	if v, ok := ctx.Value(ctxKeyPathVars{}).([]string); ok {
		return v
	}
	return nil
}

// newID 生成一个随机 16 字节 hex 字符串作为请求 ID。
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}

// extractPathVars 从 ServeMux pattern 中提取通配符变量名，如
// "/v1/users/{id}" → ["id"]。标准库不暴露已注册 pattern，故在此解析。
func extractPathVars(pattern string) []string {
	var vars []string
	inBrace := false
	start := -1
	for i, c := range pattern {
		switch c {
		case '{':
			inBrace = true
			start = i + 1
		case '}':
			if inBrace && start >= 0 {
				name := pattern[start:i]
				if idx := strings.IndexByte(name, ':'); idx >= 0 {
					name = name[:idx] // 剥离 "id:int" 这类类型约束
				}
				vars = append(vars, name)
			}
			inBrace = false
			start = -1
		}
	}
	return vars
}
