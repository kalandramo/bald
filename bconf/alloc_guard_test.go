package bconf

import "testing"

// alloc_guard_test.go 为 bconf 热路径的分配剖面守卫：以
// testing.Benchmark() 直取 bench_test.go 既有基准定义（同包函数值直用，
// 不复制 fixture、不另起口径），在测试进程内断言关键分配不变量。
//
// 确定性口径分两层：非 race 下剖面只取决于代码与工具链版本（本地与 CI 同
// go 1.27.1，同 benchtime 逐轮漂移 ±0.03%）；-race 换用另一套分配器，同一份
// 代码的剖面整体上移（实测 UnmarshalMap +12% B/op、+32 allocs/op），所以阈值
// 按非 race 实测定、headroom 必须同时盖住 -race 那档，否则跑 -race 就是假报警。
//
// 阈值出处：逐行写明非 race / -race 两组实测值与倍数。改基准口径（fixture/
// 循环体）或规范化路径语义后，同机重测并同步本文件数字——只挪阈值不算重测。
// 不加 testing.Short 守卫：单文件秒级，照常参与 -short 主循环。

func TestAllocGuard_FindUnknownKeys_StrictScan(t *testing.T) {
	res := testing.Benchmark(BenchmarkFindUnknownKeys_StrictScan)
	// 非 race 实测 1,630–1,646 B/op / 95 allocs/op；-race 1,824 B / 95 allocs
	// （分配次数不变，只有字节数上移）。阈值 8,192 B（×5.0 非 race / ×4.5
	// -race）+ 200 allocs（×2.1）：字节数受 map/切片增长的空间布局影响更大，
	// headroom 放大；分配次数是代码路径的直接信号，收紧一档。
	guardAllocs(t, "FindUnknownKeys_StrictScan", res, 8_192, 200)
}

func TestAllocGuard_UnmarshalMap_FullContract(t *testing.T) {
	res := testing.Benchmark(BenchmarkUnmarshalMap_FullContract)
	// 非 race 实测 33,632 B/op / 751 allocs/op；-race 40,667 B / 784 allocs。
	// canonicalize 构建规范树（输入只读），marshal 缓冲池化后稳态零增长：
	// 深拷贝的容器副本只剩规范树本身，另有每节点 matched 位集与折叠命中的
	// chainUsed 小对象。阈值 46,080 B（=45KiB，×1.37 非 race / ×1.13 -race）
	// + 960 allocs（×1.28 / ×1.22）：15 段全树规范化仍是本包分配大头，回归
	// 等比例放大，headroom 收到两成多就够拦住；字节再往下收会被 -race 那档
	// （40,667）顶穿，那就成了跑 -race 的假报警。
	guardAllocs(t, "UnmarshalMap_FullContract", res, 46_080, 960)
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
