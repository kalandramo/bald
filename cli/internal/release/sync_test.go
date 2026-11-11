package release

import (
	"strings"
	"testing"
)


// DX-14：孤儿归位守卫——归位目标的 To 版本所属模块不在 plan.Changes 里
// （计划被裁剪）即拒绝，杜绝把 go.mod 推到没人打 tag 的版本。
func TestSyncGuardOrphanBumps(t *testing.T) {
	plan := &Plan{
		Changes: []Change{{
			Module: "github.com/kalandramo/bald/bconf", Next: "v0.26.0", Tag: "bconf/v0.26.0",
			Requires: []RequireUpdate{
				{Module: "github.com/kalandramo/bald/bconf", From: "v0.25.0", To: "v0.26.0"}, // 目标 bconf 在 Changes 里 → 合法
			},
		}},
	}
	if err := guardOrphanBumps(plan); err != nil {
		t.Fatalf("closed-circuit plan should pass: %v", err)
	}

	// 裁剪形态：cli 的归位目标 cobramcp v0.3.5 不在 Changes——正是
	// cli/v0.7.0 坏 tag 的形态。
	trimmed := &Plan{
		Changes: []Change{{
			Module: "github.com/kalandramo/bald/cli", Next: "v0.7.0", Tag: "cli/v0.7.0",
			Requires: []RequireUpdate{
				{Module: "github.com/kalandramo/bald/cobramcp", From: "v0.3.4", To: "v0.3.5"},
			},
		}},
	}
	err := guardOrphanBumps(trimmed)
	if err == nil {
		t.Fatal("trimmed plan must be rejected")
	}
	if !strings.Contains(err.Error(), "cobramcp") || !strings.Contains(err.Error(), "v0.3.5") {
		t.Fatalf("error should name orphan module+version: %v", err)
	}
}
