// Package testkit 收编跨项目复用的测试工具（P13，见 docs/devel/zh-CN/架构优化路线.md）。
//
// 此前 freeAddr 在 pkg/server/server_test.go、_example/bald、examples/go-bald-admin
// 等处各自复制，收编于此供新 e2e 直接 import。
package testkit

import (
	"net"
	"testing"
)

// FreeAddr 分配一个当前空闲的 TCP 端口，返回 "127.0.0.1:port"。
//
// 用途：「先拿地址、再启动监听」的 e2e 场景。典型坑是 GatewayServer 的后端
// gRPC 地址不能用 ":0"（转码转发时无从得知真实端口），必须用确定端口。
//
// 做法：监听 :0 拿到内核分配的端口后立刻关闭，再用该端口。这不是 100% 可靠
// （两次调用间端口可能被抢占），但对测试足够，且比硬编码端口安全得多。
//
// 带 127.0.0.1 是刻意的：裸 ":port" 会被 gRPC 客户端解析成 [::1]:port（IPv6），
// 与监听在 IPv4 的服务不匹配，导致 "connection refused"。
func FreeAddr(tb testing.TB) string {
	tb.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatalf("testkit.FreeAddr: listen 127.0.0.1:0: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}
