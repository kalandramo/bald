package authzcasbin

import "testing"

// alloc_guard_test.go 为 authz-casbin 桥的分配剖面守卫（批 6 收尾）：以
// testing.Benchmark() 直取 bench_test.go 既有基准定义（同包函数值直用），
// 在测试进程内断言分配不变量——本地与 CI 同 go 1.27.1，确定性成立。
//
// 阈值出处：性能基准.md 存档 v1 实测值（headroom 依据逐行注释）。
// 不加 testing.Short 守卫：单文件秒级，照常参与 -short 主循环。
// 修改基准口径时，同步重测本文件阈值。

// TestAllocGuard_AliasAuthorizer_ZeroAlloc 守护别名装饰器三条 Enforce 路径
// 的零分配强不变量：Direct（内层一次 Enforce 直通放行）、Hit（内层拒绝后
// 反查等价键重试放行）、Miss（重试仍拒绝，fail-closed）。存档 v1 实测三条
// 均恰为 0 B/op / 0 allocs/op——候选切片仅在明确拒绝后才构造、reverse 索引
// 走读锁，热路径无逐请求堆分配。任何非零即回归：日志、ctx 包装、临时切片
// 都应在此失败，故不设 headroom（零就是零）。
func TestAllocGuard_AliasAuthorizer_ZeroAlloc(t *testing.T) {
	for _, tc := range []struct {
		name string
		fn   func(*testing.B)
	}{
		{"Direct", BenchmarkAliasAuthorizer_Direct},
		{"Hit", BenchmarkAliasAuthorizer_Hit},
		{"Miss", BenchmarkAliasAuthorizer_Miss},
	} {
		res := testing.Benchmark(tc.fn)
		if res.AllocsPerOp() != 0 || res.AllocedBytesPerOp() != 0 {
			t.Errorf("AliasAuthorizer_%s AllocsPerOp = %d, AllocedBytesPerOp = %d：零分配不变量被破坏（实测恰零，无 headroom）",
				tc.name, res.AllocsPerOp(), res.AllocedBytesPerOp())
		}
	}
}

// TestAllocGuard_NewRows_LoadPolicy 守护策略装载构造期分配：rowsToCSV 渲染
// 111 行 → casbin 模型解析 → 全量装载（gen 产物启动期入口，Reload 热重载
// 同一装载路径）。存档 v1 实测 2,579 allocs/op，阈值 3,000（×1.16）——
// 装载是启动期一次性成本，回归直接拖慢每个产物的冷启动，headroom 从紧。
// 仅守 allocs：字节数随 CSV 渲染缓冲与解析器内部布局漂移更敏感，次数是
// 更稳的信号。
func TestAllocGuard_NewRows_LoadPolicy(t *testing.T) {
	res := testing.Benchmark(BenchmarkNewRows_LoadPolicy)
	if res.AllocsPerOp() > 3_000 {
		t.Errorf("NewRows_LoadPolicy AllocsPerOp = %d > 3,000：分配回归（阈值出处见文件头注释）", res.AllocsPerOp())
	}
}
