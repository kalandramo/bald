// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package balancer

import (
	"testing"

	"google.golang.org/grpc/attributes"
	"google.golang.org/grpc/balancer"
	"google.golang.org/grpc/resolver"

	"github.com/kalandramo/bald/selector"
)

// 测试边界说明：grpc v1.83 用 internal.EnforceSubConnEmbedding 把
// balancer.SubConn 做成外部不可实现（实现方必须内嵌 grpc 内部脚手架），
// 因此 pickerBuilder.Build 的端到端行为无法在外部包伪 SubConn 单测——
// 该面由集成验证覆盖（真 grpc.Dial + 服务端）。本文件只测外部可测面：
// 策略注册语义、builder 的策略回退逻辑与契约常量。上游 onexmesh 的
// balancer 本来就零测试，这里比上游多测了可测的部分。

func TestRegisterDefaultStrategies(t *testing.T) {
	// 独立注册表语义在 selector 包已被钉死；这里验证默认策略注册函数的
	// 四个名字都就位（RegisterDefaultStrategies 内部走 MustRegister，
	// 重复注册 panic——所以本测试进程只调用一次）。
	RegisterDefaultStrategies()
	for _, name := range []string{"round_robin", "random", "weighted", "p2c"} {
		if !selector.Registered(name) {
			t.Errorf("strategy %q not registered after RegisterDefaultStrategies", name)
		}
	}
}

// TestNewSelectorFallsBackToRoundRobin 未注册策略回退 round_robin（同包白盒）。
func TestNewSelectorFallsBackToRoundRobin(t *testing.T) {
	RegisterDefaultStrategies()
	if s := newSelector("nonexistent"); s == nil {
		t.Fatal("newSelector returned nil for unknown strategy")
	}
	if s := newSelector("p2c"); s == nil {
		t.Fatal("newSelector returned nil for registered strategy")
	}
}

func TestContractConstants(t *testing.T) {
	if Name != "bald_selector" {
		t.Errorf("Name = %q", Name)
	}
	for k, want := range map[string]string{
		StrategyKey: "bald.selector.strategy",
		ServiceKey:  "bald.selector.service",
		WeightKey:   "bald.selector.weight",
	} {
		if k != want {
			t.Errorf("attribute key = %q, want %q", k, want)
		}
	}
}

// TestAttrsOfWeight 权重从 Metadata 透传时是字符串，直构属性表时可给 int；
// 非正/不可解析视为缺失（交回默认 100）。
func TestAttrsOfWeight(t *testing.T) {
	strAddr := resolver.Address{Attributes: attributes.New(WeightKey, "250")}
	if na := attrsOf(strAddr); !na.hasWeight || na.weight != 250 {
		t.Errorf("字符串权重 250: got %+v", na)
	}
	intAddr := resolver.Address{Attributes: attributes.New(WeightKey, 7)}
	if na := attrsOf(intAddr); !na.hasWeight || na.weight != 7 {
		t.Errorf("整型权重 7: got %+v", na)
	}
	bad := resolver.Address{Attributes: attributes.New(WeightKey, "abc")}
	if na := attrsOf(bad); na.hasWeight {
		t.Errorf("不可解析权重应判缺失: got %+v", na)
	}
	neg := resolver.Address{Attributes: attributes.New(WeightKey, "-5")}
	if na := attrsOf(neg); na.hasWeight {
		t.Errorf("负权重应判缺失: got %+v", na)
	}
}

// TestAttrsOfStrategyService 策略/服务名按字符串读；缺属性即空串（调用方回退）。
func TestAttrsOfStrategyService(t *testing.T) {
	addr := resolver.Address{Attributes: attributes.New(StrategyKey, "p2c").
		WithValue(ServiceKey, "user")}
	na := attrsOf(addr)
	if na.strategy != "p2c" || na.service != "user" {
		t.Errorf("got %+v, want strategy=p2c service=user", na)
	}
	if na := (attrsOf(resolver.Address{})); na.strategy != "" || na.service != "" || na.hasWeight {
		t.Errorf("nil 属性应全默认: got %+v", na)
	}
}

// TestActivateIdempotent Activate 多次调用不 panic（Register 重复注册会 panic，
// once 收敛为一次）；调用后默认策略与 builder 就位。
func TestActivateIdempotent(t *testing.T) {
	Activate()
	Activate() // 第二次必须不 panic
	if !selector.Registered("weighted") {
		t.Error("Activate 后 weighted 策略应已注册")
	}
	if balancer.Get(Name) == nil {
		t.Errorf("Activate 后 %q builder 应已注册进 grpc", Name)
	}
}

// TestDialOption 产出非 nil 拨号选项，并顺带完成注册。
func TestDialOption(t *testing.T) {
	if DialOption() == nil {
		t.Fatal("DialOption 不应返回 nil")
	}
	if !selector.Registered("round_robin") {
		t.Error("DialOption 应经 Activate 注册默认策略")
	}
}

func TestDefaultServiceConfig(t *testing.T) {
	const want = `{"loadBalancingConfig":[{"bald_selector":{}}]}`
	if DefaultServiceConfig != want {
		t.Errorf("DefaultServiceConfig = %s, want %s", DefaultServiceConfig, want)
	}
}
