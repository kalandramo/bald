// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package grpcdiscovery

import (
	"testing"

	"google.golang.org/grpc/attributes"
	"google.golang.org/grpc/resolver"

	"github.com/kalandramo/bald/registry"
)

func instance(id string, meta map[string]string, eps ...string) *registry.ServiceInstance {
	return &registry.ServiceInstance{ID: id, Name: "svc", Metadata: meta, Endpoints: eps}
}

// TestAddressesOfCarriesMetadata 实例 Metadata 逐键透传成字符串属性；非 gRPC
// 面端点出局、grpc:// 前缀剥掉。
func TestAddressesOfCarriesMetadata(t *testing.T) {
	in := instance("i1", map[string]string{
		"bald.selector.strategy": "weighted",
		"bald.selector.weight":   "250",
	}, "grpc://10.0.0.1:9000", "http://10.0.0.1:8080")

	addrs := addressesOf([]*registry.ServiceInstance{in})
	if len(addrs) != 1 {
		t.Fatalf("只应留 gRPC 面端点，得到 %d 条: %+v", len(addrs), addrs)
	}
	a := addrs[0]
	if a.Addr != "10.0.0.1:9000" {
		t.Errorf("Addr = %q, want 剥前缀后的 10.0.0.1:9000", a.Addr)
	}
	if got, _ := a.Attributes.Value("bald.selector.strategy").(string); got != "weighted" {
		t.Errorf("strategy 属性 = %q, want weighted", got)
	}
	if got, _ := a.Attributes.Value("bald.selector.weight").(string); got != "250" {
		t.Errorf("weight 属性 = %q, want 字符串 250", got)
	}
}

// TestAttributesOfNilWhenEmpty 空 Metadata 返回 nil 属性表——保持无 metadata 实例
// 的历史字节行为（裸 resolver.Address）。
func TestAttributesOfNilWhenEmpty(t *testing.T) {
	if attributesOf(nil) != nil {
		t.Error("nil Metadata 应产出 nil 属性表")
	}
	if attributesOf(map[string]string{}) != nil {
		t.Error("空 Metadata 应产出 nil 属性表")
	}
	addrs := addressesOf([]*registry.ServiceInstance{instance("i", nil, "10.0.0.1:1")})
	if addrs[0].Attributes != nil {
		t.Errorf("无 Metadata 实例不应带属性表, got %v", addrs[0].Attributes)
	}
}

// TestAttributesOfDeterministic 同一 Metadata 多次构造产出相等属性表——addressKey
// 的稳定指纹依赖这点（map 迭代顺序不定，必须先排键）。
func TestAttributesOfDeterministic(t *testing.T) {
	meta := map[string]string{"c": "3", "a": "1", "b": "2"}
	x := attributesOf(meta)
	y := attributesOf(meta)
	if !x.Equal(y) {
		t.Errorf("同 Metadata 两次构造不等: %v vs %v", x, y)
	}
	if x.String() != y.String() {
		t.Errorf("同 Metadata 两次构造 String() 不同: %q vs %q", x.String(), y.String())
	}
	// 逐键可读回字符串值。
	for k, v := range meta {
		if got, _ := x.Value(k).(string); got != v {
			t.Errorf("属性 %q = %q, want %q", k, got, v)
		}
	}
}

// TestAddressKeyReflectsMetadata 只改属性不改地址的快照必须换指纹——否则一次真实
// 的重加权会被 publish 的判等吞掉，balancer 拿不到新权重。
func TestAddressKeyReflectsMetadata(t *testing.T) {
	lo := addressesOf([]*registry.ServiceInstance{instance("i", map[string]string{"w": "1"}, "10.0.0.1:1")})
	hi := addressesOf([]*registry.ServiceInstance{instance("i", map[string]string{"w": "9"}, "10.0.0.1:1")})
	if addressKey(lo) == addressKey(hi) {
		t.Fatal("权重变更应改变地址指纹")
	}
	// 相同快照（不同构造实例）指纹相同。
	lo2 := addressesOf([]*registry.ServiceInstance{instance("i2", map[string]string{"w": "1"}, "10.0.0.1:1")})
	if addressKey(lo) != addressKey(lo2) {
		t.Error("相同地址+相同 Metadata 应得同一指纹")
	}
}

// TestAddressKeyIgnoresOrder 后端换返回顺序不算变更（历史不变量，带属性后仍成立）。
func TestAddressKeyIgnoresOrder(t *testing.T) {
	a := []resolver.Address{
		{Addr: "10.0.0.2:1", Attributes: attributes.New("w", "2")},
		{Addr: "10.0.0.1:1", Attributes: attributes.New("w", "1")},
	}
	b := []resolver.Address{a[1], a[0]}
	if addressKey(a) != addressKey(b) {
		t.Error("地址集换序不应改变指纹")
	}
}
