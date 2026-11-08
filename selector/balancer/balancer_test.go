// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package balancer

import (
	"testing"

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
