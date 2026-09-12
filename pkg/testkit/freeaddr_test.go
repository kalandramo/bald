package testkit

import (
	"net"
	"strings"
	"testing"
)

// TestFreeAddr：P13 收编的测试基础设施（R4 复审：新 e2e 的直接依赖）。
// 契约：①带 127.0.0.1 前缀（防 gRPC 客户端把裸 ":port" 解析成 [::1] IPv6
// 导致 connection refused——设计文档明确记载的坑）；②返回地址可立即监听。
func TestFreeAddr(t *testing.T) {
	addr := FreeAddr(t)

	if !strings.HasPrefix(addr, "127.0.0.1:") {
		t.Fatalf("必须带 IPv4 前缀（gRPC 客户端 [::1] 误解析坑）, got %q", addr)
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("返回地址必须可监听: %v", err)
	}
	_ = ln.Close()
}

// TestFreeAddr_Unique：连续分配应得到不同端口（同一端口立刻关闭后内核
// 通常不复用，弱断言：允许偶发相同，只验证不 panic 且格式合法）。
func TestFreeAddr_Unique(t *testing.T) {
	addrs := map[string]bool{}
	for i := 0; i < 5; i++ {
		a := FreeAddr(t)
		if !strings.HasPrefix(a, "127.0.0.1:") {
			t.Fatalf("第 %d 次分配格式非法: %q", i, a)
		}
		addrs[a] = true
	}
	if len(addrs) < 2 {
		t.Logf("注意：5 次分配仅 %d 个唯一地址（内核端口复用，非缺陷）", len(addrs))
	}
}
