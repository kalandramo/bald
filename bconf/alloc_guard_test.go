package bconf

import "testing"

// alloc_guard_test.go 为 bconf 热路径的分配剖面守卫（批 6 收尾）：以
// testing.Benchmark() 直取 bench_test.go 既有基准定义（同包函数值直用，
// 不复制 fixture、不另起口径），在测试进程内断言关键分配不变量。分配
// 剖面只取决于代码与工具链版本——本地与 CI 同 go 1.27.1，确定性成立，
// 不经 CI 解析基准输出。
//
// 阈值出处：性能基准.md 存档 v1 实测值；headroom 原则——给到足以吸收
// 同版本内的缓存/布局噪声、又足以拦住分配回归的幅度（逐行注释写明）。
// 不加 testing.Short 守卫：单文件秒级，照常参与 -short 主循环。
// 修改基准口径（fixture/循环体）时，同步重测本文件阈值。

func TestAllocGuard_FindUnknownKeys_StrictScan(t *testing.T) {
	res := testing.Benchmark(BenchmarkFindUnknownKeys_StrictScan)
	// 实测 1,640 B/op / 95 allocs/op。阈值 8,192 B（×5.0）+ 200 allocs
	// （×2.1）：字节数受 map/切片增长的空间布局影响更大，headroom 放大；
	// 分配次数是代码路径的直接信号，收紧一档。
	guardAllocs(t, "FindUnknownKeys_StrictScan", res, 8_192, 200)
}

func TestAllocGuard_UnmarshalMap_FullContract(t *testing.T) {
	res := testing.Benchmark(BenchmarkUnmarshalMap_FullContract)
	// 实测 28,389 B/op / 808 allocs/op。阈值 49,152 B（=48KiB，×1.73）+
	// 1,200 allocs（×1.49）：15 段全树 coerce 是本包分配大头，分配回归会
	// 等比例放大，中位 headroom 足以拦截。
	guardAllocs(t, "UnmarshalMap_FullContract", res, 49_152, 1_200)
}

// guardAllocs 断言基准结果的字节数/次数上界（本文件守卫共用的最小断言器）。
func guardAllocs(t *testing.T, name string, res testing.BenchmarkResult, maxBytes, maxAllocs int64) {
	t.Helper()
	if res.AllocedBytesPerOp() > maxBytes {
		t.Errorf("%s AllocedBytesPerOp = %d > %d：分配回归（阈值出处见文件头注释）", name, res.AllocedBytesPerOp(), maxBytes)
	}
	if res.AllocsPerOp() > maxAllocs {
		t.Errorf("%s AllocsPerOp = %d > %d：分配回归（阈值出处见文件头注释）", name, res.AllocsPerOp(), maxAllocs)
	}
}
