// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package option_test

import (
	"context"
	"testing"
	"time"

	"github.com/kalandramo/bald/broker"
	"github.com/kalandramo/bald/broker/redis/option"
)

// applyTwice 对同一 Options 连续施加两次同一 option：
// 第一次走「Context 为空 + CommonOptions 未建」的创建分支，
// 第二次走「已有 CommonOptions 就地更新」分支——两个子分支都覆盖。
func applyCommon(t *testing.T, set func(time.Duration) broker.Option, get func(*option.CommonOptions) time.Duration, d1, d2 time.Duration) {
	t.Helper()
	o := &broker.Options{}
	set(d1)(o)
	co := o.Context.Value(option.OptionsKey).(*option.CommonOptions)
	if get(co) != d1 {
		t.Fatalf("首次应用期望 %v，实际 %v", d1, get(co))
	}
	set(d2)(o)
	if got := o.Context.Value(option.OptionsKey).(*option.CommonOptions); got != co {
		t.Fatal("二次应用应复用同一 CommonOptions 实例")
	}
	if get(co) != d2 {
		t.Fatalf("二次应用期望更新为 %v，实际 %v", d2, get(co))
	}
}

func TestWithConnectTimeout(t *testing.T) {
	applyCommon(t, option.WithConnectTimeout, func(c *option.CommonOptions) time.Duration { return c.ConnectTimeout }, time.Second, 2*time.Second)
}

func TestWithReadTimeout(t *testing.T) {
	applyCommon(t, option.WithReadTimeout, func(c *option.CommonOptions) time.Duration { return c.ReadTimeout }, time.Second, 3*time.Second)
}

func TestWithWriteTimeout(t *testing.T) {
	applyCommon(t, option.WithWriteTimeout, func(c *option.CommonOptions) time.Duration { return c.WriteTimeout }, time.Second, 4*time.Second)
}

func TestWithIdleTimeout(t *testing.T) {
	applyCommon(t, option.WithIdleTimeout, func(c *option.CommonOptions) time.Duration { return c.IdleTimeout }, time.Second, 5*time.Second)
}

// applyTwiceInt 同 applyCommon，但 option 收 int（MaxIdle/MaxActive）。
func applyInt(t *testing.T, set func(int) broker.Option, get func(*option.CommonOptions) int, v1, v2 int) {
	t.Helper()
	o := &broker.Options{}
	set(v1)(o)
	co := o.Context.Value(option.OptionsKey).(*option.CommonOptions)
	if get(co) != v1 {
		t.Fatalf("首次期望 %d，实际 %d", v1, get(co))
	}
	set(v2)(o)
	if get(co) != v2 {
		t.Fatalf("二次期望 %d，实际 %d", v2, get(co))
	}
}

func TestWithMaxIdle(t *testing.T) {
	applyInt(t, option.WithMaxIdle, func(c *option.CommonOptions) int { return c.MaxIdle }, 1, 2)
}

func TestWithMaxActive(t *testing.T) {
	applyInt(t, option.WithMaxActive, func(c *option.CommonOptions) int { return c.MaxActive }, 3, 4)
}

// TestWithDefaultOptions 验证默认配置整体写入且各字段取默认常量。
func TestWithDefaultOptions(t *testing.T) {
	o := &broker.Options{}
	option.WithDefaultOptions()(o)
	co := o.Context.Value(option.OptionsKey).(*option.CommonOptions)
	if co.MaxIdle != option.DefaultMaxIdle || co.MaxActive != option.DefaultMaxActive {
		t.Fatalf("默认池参数不符: %+v", co)
	}
	if co.ConnectTimeout != option.DefaultConnectTimeout || co.ReadTimeout != option.DefaultReadTimeout ||
		co.WriteTimeout != option.DefaultWriteTimeout || co.IdleTimeout != option.DefaultIdleTimeout {
		t.Fatalf("默认超时参数不符: %+v", co)
	}
}

// TestStreamOptionsConstruct 验证五个 Stream 选项构造器返回非空闭包
// （选项体内语句即「委托 broker.*ContextWithValue」，调用即覆盖）。
func TestStreamOptionsConstruct(t *testing.T) {
	if option.WithStreamGroup("g") == nil ||
		option.WithStreamConsumer("c") == nil ||
		option.WithStreamBlockTime(time.Second) == nil ||
		option.WithStreamCount(1) == nil ||
		option.WithStreamMaxLen(2) == nil {
		t.Fatal("Stream 选项构造器返回 nil")
	}
}

// TestStreamOptionsApply 端到端把 Stream 选项应用到 Subscribe/PublishOptions 并读回。
func TestStreamOptionsApply(t *testing.T) {
	so := &broker.SubscribeOptions{Context: context.Background()}
	option.WithStreamGroup("grp")(so)
	if got, _ := so.Context.Value(option.StreamGroupKey{}).(string); got != "grp" {
		t.Fatalf("StreamGroup 读回 %q", got)
	}
	po := &broker.PublishOptions{Context: context.Background()}
	option.WithStreamMaxLen(9)(po)
	if got, _ := po.Context.Value(option.StreamMaxLenKey{}).(int64); got != 9 {
		t.Fatalf("StreamMaxLen 读回 %d", got)
	}
}

// TestLoggerHelpers 逐一调用五个日志便捷函数，确认在默认 nop 后端下不 panic。
func TestLoggerHelpers(t *testing.T) {
	option.LogDebug("a", 1)
	option.LogError("e", 2)
	option.LogInfof("i %d", 3)
	option.LogWarnf("w %d", 4)
	option.LogErrorf("ef %d", 5)
}
