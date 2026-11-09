// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

// bindflags_setters_test.go 驱动 bindflags.go 的各标量/Duration flag.Value
// 写入器：BindFlags 注册后，经 pflag 表面对每个非隐藏 flag 依次调用 Type、
// String、Set（合法值 + 非法值）——一份遍历覆盖全部 setter 的三方法。
// float32 setter 的「成功回写」路径需真实 float32 proto 字段，本仓契约树上
// 不存在（bindScalarFlag 的 Float32Kind 分支恒不可达），故只覆盖其 Type/String
// 与 Set 解析失败分支。

package bconf

import (
	"testing"
	"time"

	"github.com/spf13/pflag"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/durationpb"

	storev1 "github.com/kalandramo/bald/bconf/gen/go/bald/store/v1"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// validByType 给每种 flag.Value.Type() 一个能解析成功的样例值。
var validByType = map[string]string{
	"string":   "hello",
	"bool":     "true",
	"int":      "7",
	"int64":    "7",
	"uint":     "7",
	"uint64":   "7",
	"float":    "1.5",
	"duration": "10s",
}

func TestBindFlagsSetterSurface(t *testing.T) {
	fs := pflag.NewFlagSet("app", pflag.ContinueOnError)
	// 各叶子 message 直接作为 BindFlags 根，绕开父 message Has() 门控（未设子树
	// 的标量字段不注册），保证每类 Kind 都出现。
	targets := []proto.Message{
		NewBootstrap(),
		&bootstrapv1.Backoff{},
		&bootstrapv1.Server_Http{},
		&bootstrapv1.Server_Http_Middleware_CircuitBreaker{}, // int64 (open_duration_ms)
		&bootstrapv1.Ai_Openai{},
		&storev1.PagingRequest{},
	}
	for i, msg := range targets {
		// 每个目标消息用独立前缀，避免不同 message 的同名标量字段撞 "flag redefined"。
		BindFlags(fs, msg, "t"+string(rune('0'+i)))
	}

	seen := map[string]bool{}
	fs.VisitAll(func(f *pflag.Flag) {
		typ := f.Value.Type() // 命中 bindXxxSetter.Type（含 guideSetter.Type）
		_ = f.Value.String()  // 命中 bindXxxSetter.String
		if f.Hidden {         // 指引 flag（repeated/map）：Type/String 已覆盖，Set 由 bindflags_test 覆盖
			return
		}
		seen[typ] = true

		valid, ok := validByType[typ]
		if !ok {
			t.Fatalf("未知 flag 类型 %q（flag %s）——需补充样例", typ, f.Name)
		}
		if err := f.Value.Set(valid); err != nil {
			t.Errorf("Set(%q) 合法值失败 flag=%s: %v", valid, f.Name, err)
		}

		if typ == "string" {
			return // string 解析恒成功，无错误分支
		}
		if err := f.Value.Set("~~bogus~~"); err == nil {
			t.Errorf("Set 非法值应报错 flag=%s type=%s", f.Name, typ)
		}
	})

	// 断言每类可达 Kind 都被驱动到（缺类 = 绑定消息选错，测试须显式失败）。
	for typ := range validByType {
		if !seen[typ] {
			t.Errorf("未驱动到类型 %q 的 setter（应补充含该 Kind 字段的绑定消息）", typ)
		}
	}
}

// TestDurationOfNonDuration 覆盖 durationOf 的兜底分支：字段值不是
// *durationpb.Duration 时返回零时长（bindDurationSetter.String 只走命中分支）。
func TestDurationOfNonDuration(t *testing.T) {
	m := (&storev1.PagingRequest{}).ProtoReflect()
	// 取任一「非 message」标量字段：其 Get().Interface() 必不是 *durationpb.Duration。
	var fd protoreflect.FieldDescriptor
	fields := m.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		if f := fields.Get(i); f.Kind() != protoreflect.MessageKind && !f.IsList() && !f.IsMap() {
			fd = f
			break
		}
	}
	if fd == nil {
		t.Fatal("PagingRequest 无标量字段可用于负例")
	}
	if got := durationOf(m, fd); got == nil || got.AsDuration() != 0 {
		t.Errorf("durationOf(非 duration 字段) = %v, want 零时长非 nil", got)
	}
}

// TestDurationOfRealValue 回归 bug #40：Duration 字段 Value.Interface() 装箱成
// protoreflect.Message，durationOf 曾直接断言 *durationpb.Duration 恒失败，导致
// bindDurationSetter.String 对任意已设时长都误报 "0s"。钉住真实值透传。
func TestDurationOfRealValue(t *testing.T) {
	b := &bootstrapv1.Backoff{Initial: durationpb.New(5 * time.Second)}
	m := b.ProtoReflect()
	fields := m.Descriptor().Fields()
	var fd protoreflect.FieldDescriptor
	for i := 0; i < fields.Len(); i++ {
		f := fields.Get(i)
		if f.Kind() == protoreflect.MessageKind && !f.IsList() && !f.IsMap() &&
			f.Message().FullName() == "google.protobuf.Duration" {
			fd = f
			break
		}
	}
	if fd == nil {
		t.Fatal("Backoff 无 Duration 字段")
	}
	if got := durationOf(m, fd).AsDuration(); got != 5*time.Second {
		t.Errorf("durationOf = %v, want 5s", got)
	}
	s := &bindDurationSetter{m, fd}
	if got := s.String(); got != "5s" {
		t.Errorf("bindDurationSetter.String() = %q, want \"5s\"", got)
	}
}
