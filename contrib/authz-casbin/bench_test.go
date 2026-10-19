package authzcasbin

import (
	"context"
	"fmt"
	"strconv"
	"testing"
)

// bench_test.go 为 authz-casbin 桥的基准套件（批 5 W3）。
//
// 口径纪律：
//   - 内层授权器一律用真实 casbin 引擎（NewRows 构造），不用桩——落证的是
//     「装饰器 + Enforce」全路径的逐请求分配；
//   - 策略行/绑定在计时外构造（NewRows_LoadPolicy 本身即度量构造，除外）；
//   - 热路径无 fmt/日志。

// benchPolicySet 构造 10 角色 × 10 对象 = 100 条 p 行 + 10 条 g 绑定的 RBAC
// 策略集，外加一条 role-3 对 greet 的授权（别名命中路径用）。规模对齐 gen
// 产物默认策略集量级。
func benchPolicySet() ([]PolicyRow, []Grant) {
	rows := make([]PolicyRow, 0, 101)
	for r := 0; r < 10; r++ {
		for o := 0; o < 10; o++ {
			rows = append(rows, PolicyRow{
				Role:   fmt.Sprintf("role-%d", r),
				Object: fmt.Sprintf("obj-%d-%d", r, o),
				Action: "get",
			})
		}
	}
	rows = append(rows, PolicyRow{Role: "role-3", Object: "greet", Action: "get"})
	grants := make([]Grant, 0, 10)
	for r := 0; r < 10; r++ {
		grants = append(grants, Grant{
			Subject: fmt.Sprintf("u-%d", r),
			Role:    fmt.Sprintf("role-%d", r),
		})
	}
	return rows, grants
}

// benchInner 构造真实 casbin 内层授权器（默认 RBAC 模型 + benchPolicySet）。
func benchInner(b *testing.B) *Authorizer {
	b.Helper()
	rows, grants := benchPolicySet()
	az, err := NewRows(rows, grants)
	if err != nil {
		b.Fatalf("NewRows: %v", err)
	}
	return az
}

// BenchmarkAliasAuthorizer_Direct 度量别名装饰器的直通放行路径：内层一次
// Enforce 即放行，装饰器在 allowed 分支直接透传（alias.go:86），不进入候选
// 构造——落证「Enforce 路径无逐请求分配」的装饰器侧：本路径装饰器自身
// 零分配（候选切片仅在明确拒绝后才构造），报告的 B/op 即内层引擎自身。
func BenchmarkAliasAuthorizer_Direct(b *testing.B) {
	az := NewAlias(benchInner(b), map[string]string{"greets": "greet"})
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		ok, err := az.Authorize(ctx, "u-3", "obj-3-0", "get")
		if err != nil || !ok {
			b.Fatalf("direct allow: got=%v err=%v", ok, err)
		}
	}
}

// BenchmarkAliasAuthorizer_Hit 度量别名命中路径：内层对 "greets" 明确拒绝后，
// 反查等价键 "greet"（reverse 索引，键序确定）重试并放行——两次 Enforce +
// 一次候选构造。装饰器侧仅 cands 切片与 reverse 快照读锁。
func BenchmarkAliasAuthorizer_Hit(b *testing.B) {
	az := NewAlias(benchInner(b), map[string]string{"greets": "greet"})
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		ok, err := az.Authorize(ctx, "u-3", "greets", "get")
		if err != nil || !ok {
			b.Fatalf("alias hit: got=%v err=%v", ok, err)
		}
	}
}

// BenchmarkAliasAuthorizer_Miss 度量别名未命中路径：内层拒绝 → 候选重试
// 仍拒绝 → 保持拒绝（fail-closed 不放宽）。两次 Enforce + 候选构造，验证
// 「重试不放大放行面」路径的成本与 Direct/Hit 同量级。
func BenchmarkAliasAuthorizer_Miss(b *testing.B) {
	az := NewAlias(benchInner(b), map[string]string{"unknown": "unknown-alias"})
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		ok, err := az.Authorize(ctx, "u-3", "unknown", "get")
		if err != nil || ok {
			b.Fatalf("alias miss: got=%v err=%v", ok, err)
		}
	}
}

// BenchmarkNewRows_LoadPolicy 度量装载构造期成本（policy.go NewRows）：
// rowsToCSV 渲染 101 p 行 + 10 g 行 → casbin 模型解析 → 策略全量装载。
// gen app 产物启动期的策略装载入口；Reload 热重载走同一 adapter 装载，
// 本基准即其单次成本。
func BenchmarkNewRows_LoadPolicy(b *testing.B) {
	rows, grants := benchPolicySet()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := NewRows(rows, grants); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkAuthorizer_NoReuse 度量**零重用**形态（批 6）：键空间 8,192 个
// subject 远超缓存容量 1,024——稳态下每次判定都是缓存未命中（评估 + 插入 +
// 逐出）。对照 HEAD 同名基准（无缓存，纯 Enforce）即得 miss 路径的边际成本；
// 与 Direct/Hit/Miss（重入形态，稳态全命中）互补，共同划定缓存收益区间。
// subject 键在计时外预构造（循环内无 fmt/拼接分配）。
func BenchmarkAuthorizer_NoReuse(b *testing.B) {
	az := benchInner(b)
	const numSubjects = 8192 // > defaultEnforceCacheCapacity(1024)
	subs := make([]string, numSubjects)
	for i := range subs {
		subs[i] = "u-no-" + strconv.Itoa(i)
	}
	ctx := context.Background()
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		ok, err := az.Authorize(ctx, subs[i%numSubjects], "unknown", "get")
		if err != nil || ok {
			b.Fatalf("no-reuse deny: got=%v err=%v", ok, err)
		}
		i++
	}
}
