package p2c

import (
	"context"
	"fmt"
	"testing"

	"github.com/kalandramo/bald/selector"
)

// benchNodes 构造一组互异地址的候选节点。
func benchNodes(prefix string, n int) []selector.Node {
	nodes := make([]selector.Node, 0, n)
	for i := 0; i < n; i++ {
		nodes = append(nodes, selector.NewNode(fmt.Sprintf("%s-%d:8080", prefix, i), "grpc", "svc", 100, nil))
	}
	return nodes
}

// BenchmarkP2CSelectSteady 是生产稳态：候选集固定，map 大小 == 候选数，
// prune 应是廉价空操作。此路径此前每次 Select 无条件拿写锁——所有并发
// 选取与 node() 的读锁快路径互相串行化。RunParallel 度量锁竞争下的
// ns/op 与 allocs。
func BenchmarkP2CSelectSteady(b *testing.B) {
	sel := New()
	nodes := benchNodes("steady", 8)
	ctx := context.Background()

	// 预热：把全部节点状态灌进 map，进入稳态。
	for i := 0; i < len(nodes); i++ {
		_, done, err := sel.Select(ctx, nodes)
		if err != nil {
			b.Fatal(err)
		}
		done(ctx, selector.DoneInfo{})
	}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, done, err := sel.Select(ctx, nodes)
			if err != nil {
				b.Fatal(err)
			}
			done(ctx, selector.DoneInfo{})
		}
	})
}

// BenchmarkP2CSelectChurn 是实例翻滚场景：两套互斥候选集交替，map 超编，
// prune 每次都进入真实清理分支（写锁是这里该付的成本，修复后行为不变）。
func BenchmarkP2CSelectChurn(b *testing.B) {
	sel := New()
	setA := benchNodes("seta", 8)
	setB := benchNodes("setb", 8)
	ctx := context.Background()

	// 预热：两套集合的状态都灌进 map，制造 map(16) > 候选(8) 的真实清理。
	for _, set := range [][]selector.Node{setA, setB} {
		for i := 0; i < 50; i++ {
			_, done, err := sel.Select(ctx, set)
			if err != nil {
				b.Fatal(err)
			}
			done(ctx, selector.DoneInfo{})
		}
	}

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			if i%2 == 0 {
				_, done, _ := sel.Select(ctx, setA)
				done(ctx, selector.DoneInfo{})
			} else {
				_, done, _ := sel.Select(ctx, setB)
				done(ctx, selector.DoneInfo{})
			}
			i++
		}
	})
}
