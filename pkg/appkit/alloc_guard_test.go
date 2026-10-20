package appkit

import "testing"

// alloc_guard_test.go 为 appkit 资源面/装配期的分配剖面守卫（批 6 收尾）：
// 以 testing.Benchmark() 直取 bench_test.go 既有基准定义（同包函数值直用），
// 在测试进程内断言分配不变量——本地与 CI 同 go 1.27.1，确定性成立。
//
// 阈值出处：性能基准.md 存档 v1 实测值（headroom 依据逐行注释）。
// 不加 testing.Short 守卫：单文件秒级，照常参与 -short 主循环。
// 修改基准口径时，同步重测本文件阈值。

// TestAllocGuard_Resource_ZeroAlloc 守护 Resource 查询零分配强不变量：
// 整产物段精确命中（Lookup，resource.go:41 map 直查）与子段精确命中
// （SubSegment，直查 + 类型断言）存档 v1 实测均恰为 0 B/op / 0 allocs/op。
// Run 期取 registry/audit/数据库产物走的就是这两条路径——任何逐请求分配
// （拷贝、包装、日志）都应在此失败，故不设 headroom。
func TestAllocGuard_Resource_ZeroAlloc(t *testing.T) {
	for _, tc := range []struct {
		name string
		fn   func(*testing.B)
	}{
		{"Resource_Lookup", BenchmarkResource_Lookup},
		{"Resource_SubSegment", BenchmarkResource_SubSegment},
	} {
		res := testing.Benchmark(tc.fn)
		if res.AllocsPerOp() != 0 || res.AllocedBytesPerOp() != 0 {
			t.Errorf("%s AllocsPerOp = %d, AllocedBytesPerOp = %d：零分配不变量被破坏（实测恰零，无 headroom）",
				tc.name, res.AllocsPerOp(), res.AllocedBytesPerOp())
		}
	}
}

// TestAllocGuard_DeclApply_OptionFanout 守护装配期 Option 扇出分配：25 个
// 混合 Option（标量/追加切片/单槽覆盖）逐个 Apply 到 Decl 拷贝。存档 v1
// 实测 16 allocs/op，阈值 32（×2.0）。仅守 allocs：字节数随追加型切片的
// 空间布局漂移更敏感，路径单一、次数是更稳的信号。
func TestAllocGuard_DeclApply_OptionFanout(t *testing.T) {
	res := testing.Benchmark(BenchmarkDeclApply_OptionFanout)
	if res.AllocsPerOp() > 32 {
		t.Errorf("DeclApply_OptionFanout AllocsPerOp = %d > 32：分配回归（阈值出处见文件头注释）", res.AllocsPerOp())
	}
}
