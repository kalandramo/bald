package appkit

import (
	"context"
	"testing"
)

// CleanupFrom 是 25 份 contract 包 adaptCleanup 复制的收拢点（ADR-1）：
// nil 透传无资源；非 nil 包装为 Cleanup 并真正调用原闭包、错误原样上抛。
func TestCleanupFrom(t *testing.T) {
	if CleanupFrom(nil) != nil {
		t.Fatal("CleanupFrom(nil) should pass through nil")
	}
	called := false
	cl := CleanupFrom(func() { called = true })
	if cl == nil {
		t.Fatal("CleanupFrom(non-nil) returned nil")
	}
	if err := cl(context.Background()); err != nil || !called {
		t.Fatalf("cl = %v called=%v", err, called)
	}
	// 入参是无返回值闭包——契约工厂的失败路径走 Build 返回值，不走 cleanup。
}
