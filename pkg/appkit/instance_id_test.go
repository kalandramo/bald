package appkit

import (
	"strings"
	"testing"
)

// TestDefaultInstanceID_Unique 默认实例 ID 须全局唯一：hostname 前缀 + 随机后缀，
// 同机多实例 / 同进程多 AppKit 不冲突（多实例注册覆盖的架构债修复）。
func TestDefaultInstanceID_Unique(t *testing.T) {
	host := hostname()
	seen := make(map[string]bool)
	for i := 0; i < 64; i++ {
		id := defaultInstanceID()
		if !strings.HasPrefix(id, host+"-") {
			t.Errorf("instance id %q must have hostname prefix %q", id, host)
		}
		if seen[id] {
			t.Fatalf("duplicate default instance id %q", id)
		}
		seen[id] = true
	}
}

// TestInstanceID_SuffixLength 随机后缀取 uuid 前 8 字符（host + "-" + 8）。
func TestInstanceID_SuffixLength(t *testing.T) {
	id := defaultInstanceID()
	host := hostname() + "-"
	if len(id) != len(host)+8 {
		t.Fatalf("instance id %q: want hostname+8 chars, got %d (host+%d)", id, len(id), len(id)-len(host))
	}
}

// TestIDOption_OverridesDefault 显式 ID Option 覆盖随机默认（K8s 固定 pod 名等场景）。
func TestIDOption_OverridesDefault(t *testing.T) {
	a := New(ID("fixed-instance"))
	if a.id != "fixed-instance" {
		t.Fatalf("ID option not applied: got %q", a.id)
	}
	other := New()
	if other.id == a.id {
		t.Fatalf("default instance id %q must differ from explicit ID", other.id)
	}
}
