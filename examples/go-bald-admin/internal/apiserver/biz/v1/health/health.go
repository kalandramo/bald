// Package health 是 M0 最小业务域，演示 biz 层（use case）分层：
// 业务函数与传输层解耦，gin handler 与后续 gRPC handler 共用同一份 biz。
package health

import "context"

// Info 返回服务基本信息（M0 占位，后续从配置/注册表读取）。
func Info(_ context.Context) map[string]string {
	return map[string]string{
		"service": "go-bald-admin",
		"version": "v0.1.0",
		"status":  "ok",
	}
}

// Ping 健康检查业务函数。
func Ping(_ context.Context) string {
	return "pong"
}
