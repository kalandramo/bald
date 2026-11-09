package testkit

import (
	"errors"
	"net"
	"strings"
	"testing"
)

// TestFreeAddr：测试基础设施（e2e 的直接依赖）。
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

// TestFreeAddr_ListenError：注入监听失败（模拟资源耗尽），FreeAddr 应 Fatalf。
// Fatalf 经 runtime.Goexit 终结协程，故用 fake-T + goroutine 回收（零值 testing.T
// 不支持 .Run，但 Helper/Fatalf 可用）。
func TestFreeAddr_ListenError(t *testing.T) {
	orig := listen
	listen = func(string, string) (net.Listener, error) {
		return nil, errors.New("EMFILE: too many open files")
	}
	t.Cleanup(func() { listen = orig })

	done := make(chan struct{})
	var ft testing.T
	go func() {
		defer close(done)
		FreeAddr(&ft)
	}()
	<-done
	if !ft.Failed() {
		t.Error("FreeAddr should Fatalf when listen fails")
	}
}
