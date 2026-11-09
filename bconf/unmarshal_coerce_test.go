// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

// unmarshal_coerce_test.go 逐分支覆盖 env/flag→proto 类型强转的纯函数：它们只吃
// raw（任意 Go 值）与 protoreflect.Kind，无需真实字段描述符，直接穷举各类型分支。

package bconf

import (
	"math"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	storev1 "github.com/kalandramo/bald/bconf/gen/go/bald/store/v1"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// scalarKinds 递归遍历 message 的描述符树（不看实例，nil 子树照样能取到类型），
// 为每个标量 Kind 记下第一个真实字段描述符。跳过 repeated/map（其 fd 走的是
// coerceMessage 的元素分支，不是这里的标量强转），跳过 google.protobuf.* well-known
// 以免 Duration 的 seconds/nanos 污染 Kind 表。
func scalarKinds(m proto.Message) map[protoreflect.Kind]protoreflect.FieldDescriptor {
	out := make(map[protoreflect.Kind]protoreflect.FieldDescriptor)
	var walk func(md protoreflect.MessageDescriptor)
	walk = func(md protoreflect.MessageDescriptor) {
		fields := md.Fields()
		for i := 0; i < fields.Len(); i++ {
			fd := fields.Get(i)
			if fd.IsList() || fd.IsMap() {
				continue
			}
			if fd.Kind() == protoreflect.MessageKind || fd.Kind() == protoreflect.GroupKind {
				if strings.HasPrefix(string(fd.Message().FullName()), "google.protobuf.") {
					continue
				}
				walk(fd.Message())
				continue
			}
			if _, ok := out[fd.Kind()]; !ok {
				out[fd.Kind()] = fd
			}
		}
	}
	walk(m.ProtoReflect().Descriptor())
	return out
}

func TestAnyToInt64(t *testing.T) {
	positives := []any{
		int(1), int8(2), int16(3), int32(4), int64(5),
		uint(6), uint8(7), uint16(8), uint32(9), uint64(10),
	}
	want := []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	for i, v := range positives {
		n, err := anyToInt64(v)
		if err != nil || n != want[i] {
			t.Errorf("anyToInt64(%T=%v) = (%d, %v), want %d", v, v, n, err, want[i])
		}
	}
	if _, err := anyToInt64("x"); err == nil {
		t.Error("anyToInt64(string) 应报错")
	}
	if _, err := anyToInt64(1.5); err == nil {
		t.Error("anyToInt64(float64) 应报错")
	}
}

func TestToFloat(t *testing.T) {
	if got := toFloat(float32(1.5)); got != 1.5 {
		t.Errorf("toFloat(float32) = %v", got)
	}
	if got := toFloat(float64(2.5)); got != 2.5 {
		t.Errorf("toFloat(float64) = %v", got)
	}
	if got := toFloat("nope"); got != 0 {
		t.Errorf("toFloat(非 float) = %v, want 0", got)
	}
}

func TestToSlice(t *testing.T) {
	if got, ok := toSlice([]any{1, "a"}); !ok || len(got) != 2 {
		t.Errorf("toSlice([]any) = (%v, %v)", got, ok)
	}
	got, ok := toSlice([]string{"a", "b"})
	if !ok || len(got) != 2 || got[0] != "a" {
		t.Errorf("toSlice([]string) = (%v, %v)", got, ok)
	}
	got, ok = toSlice([]int{1, 2, 3})
	if !ok || len(got) != 3 || got[2] != 3 {
		t.Errorf("toSlice([]int) = (%v, %v)", got, ok)
	}
	if got, ok := toSlice(42); ok || got != nil {
		t.Errorf("toSlice(非切片) = (%v, %v), want (nil,false)", got, ok)
	}
}

func TestCoerceDuration(t *testing.T) {
	// 字符串：合法重排成秒串，非法报错。
	if v, err := coerceDuration("1500ms"); err != nil || v != "1.5s" {
		t.Errorf(`coerceDuration("1500ms") = (%v, %v), want "1.5s"`, v, err)
	}
	if v, err := coerceDuration("1m30s"); err != nil || v != "90s" {
		t.Errorf(`coerceDuration("1m30s") = (%v, %v), want "90s"`, v, err)
	}
	if _, err := coerceDuration("bogus"); err == nil {
		t.Error("coerceDuration(非法串) 应报错")
	}
	// time.Duration 直接串化。
	if v, err := coerceDuration(2 * time.Second); err != nil || v != "2s" {
		t.Errorf("coerceDuration(Duration) = (%v, %v)", v, err)
	}
	// 纳秒整数（flag 常见）。
	if v, err := coerceDuration(int64(3_000_000_000)); err != nil || v != "3s" {
		t.Errorf("coerceDuration(nanos int) = (%v, %v)", v, err)
	}
	// 浮点纳秒。
	if v, err := coerceDuration(float64(4_000_000_000)); err != nil || v != "4s" {
		t.Errorf("coerceDuration(nanos float) = (%v, %v)", v, err)
	}
	if _, err := coerceDuration(true); err == nil {
		t.Error("coerceDuration(bool) 应报错")
	}
}

func TestCoerceInt(t *testing.T) {
	// 整数家族 → int64（64 位与 32 位各走一遍）。
	if v, err := coerceInt(int32(7), protoreflect.Int32Kind); err != nil || v != int64(7) {
		t.Errorf("coerceInt(int32, Int32) = (%v, %v)", v, err)
	}
	if v, err := coerceInt(uint(9), protoreflect.Int64Kind); err != nil || v != int64(9) {
		t.Errorf("coerceInt(uint, Int64) = (%v, %v)", v, err)
	}
	// 32 位溢出拒绝。
	if _, err := coerceInt(int64(math.MaxInt32)+1, protoreflect.Int32Kind); err == nil {
		t.Error("coerceInt 超 int32 应报错")
	}
	// 整值 float 放行，非整值拒绝，越界拒绝。
	if v, err := coerceInt(float64(5), protoreflect.Int64Kind); err != nil || v != int64(5) {
		t.Errorf("coerceInt(float=5) = (%v, %v)", v, err)
	}
	if _, err := coerceInt(1.5, protoreflect.Int64Kind); err == nil {
		t.Error("coerceInt 非整 float 应报错")
	}
	if _, err := coerceInt(float64(1<<63), protoreflect.Int64Kind); err == nil {
		t.Error("coerceInt float 超 int64 应报错")
	}
	// 字符串整数 / 非法（ParseInt 不 trim，两侧空白即失败）。
	if v, err := coerceInt("42", protoreflect.Int64Kind); err != nil || v != int64(42) {
		t.Errorf("coerceInt(\"42\") = (%v, %v)", v, err)
	}
	if _, err := coerceInt(" 42 ", protoreflect.Int64Kind); err == nil {
		t.Error("coerceInt 带空白串 应报错（不 trim）")
	}
	if _, err := coerceInt("x", protoreflect.Int32Kind); err == nil {
		t.Error("coerceInt 非法串 应报错")
	}
	// time.Duration → 纳秒整数。
	if v, err := coerceInt(time.Second, protoreflect.Int64Kind); err != nil || v != int64(time.Second) {
		t.Errorf("coerceInt(Duration) = (%v, %v)", v, err)
	}
	if _, err := coerceInt(true, protoreflect.Int64Kind); err == nil {
		t.Error("coerceInt(bool) 应报错")
	}
}

func TestCoerceUint(t *testing.T) {
	// uint 家族原样。
	if v, err := coerceUint(uint32(3)); err != nil || v != uint32(3) {
		t.Errorf("coerceUint(uint32) = (%v, %v)", v, err)
	}
	// 非负 int 家族 → uint64。
	if v, err := coerceUint(int(4)); err != nil || v != uint64(4) {
		t.Errorf("coerceUint(int=4) = (%v, %v)", v, err)
	}
	if _, err := coerceUint(int(-1)); err == nil {
		t.Error("coerceUint(负 int) 应报错")
	}
	// 字符串。
	if v, err := coerceUint("12"); err != nil || v != uint64(12) {
		t.Errorf("coerceUint(\"12\") = (%v, %v)", v, err)
	}
	if _, err := coerceUint("x"); err == nil {
		t.Error("coerceUint 非法串 应报错")
	}
	// float：整值非负放行；非整/负/越界拒绝。
	if v, err := coerceUint(float64(8)); err != nil || v != uint64(8) {
		t.Errorf("coerceUint(float=8) = (%v, %v)", v, err)
	}
	if _, err := coerceUint(1.5); err == nil {
		t.Error("coerceUint 非整 float 应报错")
	}
	if _, err := coerceUint(float64(-1)); err == nil {
		t.Error("coerceUint 负 float 应报错")
	}
	if _, err := coerceUint(float64(1 << 64)); err == nil {
		t.Error("coerceUint float 超 uint64 应报错")
	}
	if _, err := coerceUint(true); err == nil {
		t.Error("coerceUint(bool) 应报错")
	}
}

// TestCoerceScalarBranches 用真实字段描述符驱动 coerceScalar 的类型分派：
// 只补「异型可强转」与「不可强转报错」的分支（同型正常路径已被 UnmarshalMap
// 端到端测过）。raw 形态模拟 env/flag 覆写——值类型与 proto 字段类型常常不匹配。
func TestCoerceScalarBranches(t *testing.T) {
	kinds := scalarKinds(NewBootstrap())
	for _, extra := range []proto.Message{&bootstrapv1.Backoff{}, &storev1.PagingRequest{}} {
		for k, fd := range scalarKinds(extra) {
			if _, ok := kinds[k]; !ok {
				kinds[k] = fd
			}
		}
	}

	if fd := kinds[protoreflect.StringKind]; fd != nil {
		if v, err := coerceScalar(fd, true); err != nil || v != "true" {
			t.Errorf("string←bool = (%v, %v)", v, err)
		}
		if v, err := coerceScalar(fd, 42); err != nil || v != "42" {
			t.Errorf("string←int = (%v, %v)", v, err)
		}
		if v, err := coerceScalar(fd, 1.5); err != nil || v != "1.5" {
			t.Errorf("string←float = (%v, %v)", v, err)
		}
		if _, err := coerceScalar(fd, []string{"x"}); err == nil {
			t.Error("string←切片 应报错")
		}
	} else {
		t.Fatal("契约树应含 string 字段")
	}

	if fd := kinds[protoreflect.BoolKind]; fd != nil {
		if _, err := coerceScalar(fd, "nope"); err == nil {
			t.Error("bool←非法串 应报错")
		}
		if _, err := coerceScalar(fd, 1); err == nil {
			t.Error("bool←int 应报错")
		}
	} else {
		t.Fatal("契约树应含 bool 字段")
	}

	if fd := kinds[protoreflect.Uint32Kind]; fd != nil {
		if v, err := coerceScalar(fd, int64(5)); err != nil || v != uint64(5) {
			t.Errorf("uint←int = (%v, %v)", v, err)
		}
	}

	if fd := kinds[protoreflect.DoubleKind]; fd != nil {
		if v, err := coerceScalar(fd, 7); err != nil || v != float64(7) {
			t.Errorf("double←int = (%v, %v)", v, err)
		}
		if _, err := coerceScalar(fd, "bad"); err == nil {
			t.Error("double←非法串 应报错")
		}
		if _, err := coerceScalar(fd, []int{1}); err == nil {
			t.Error("double←切片 应报错")
		}
	} else {
		t.Fatal("契约树应含 double 字段")
	}

	// bytes（server.tls.cert_pem）：protojson 期望 base64 串，其它形态报错。
	if fd := kinds[protoreflect.BytesKind]; fd != nil {
		if v, err := coerceScalar(fd, "aGVsbG8="); err != nil || v != "aGVsbG8=" {
			t.Errorf("bytes←串 = (%v, %v)", v, err)
		}
		if _, err := coerceScalar(fd, 1); err == nil {
			t.Error("bytes←int 应报错")
		}
	} else {
		t.Fatal("契约树应含 bytes 字段（server.tls.cert_pem）")
	}
}

// TestCoerceEnum 用真实 enum 字段描述符穷举 coerceEnum 的四种输入形态：
// 合法枚举名、命中编号的数字串、非法名（带候选列表）、整型编号、越界编号、异型。
func TestCoerceEnum(t *testing.T) {
	fd := scalarKinds(&bootstrapv1.Backoff{})[protoreflect.EnumKind]
	if fd == nil {
		t.Fatal("Backoff 应含 enum 字段（curve/jitter）")
	}
	values := fd.Enum().Values()
	firstName := string(values.Get(0).Name())
	firstNum := int(values.Get(0).Number())

	// 合法枚举名原样返回。
	if v, err := coerceEnum(fd, firstName); err != nil || v != firstName {
		t.Errorf("enum←名 %q = (%v, %v)", firstName, v, err)
	}
	// 数字串命中编号 → int64。
	if v, err := coerceEnum(fd, "0"); err != nil {
		t.Errorf(`enum←"0" = (%v, %v)`, v, err)
	} else if n, ok := v.(int64); !ok || n != 0 {
		t.Errorf(`enum←"0" = %v (%T), want int64(0)`, v, v)
	}
	// 非法名 → 报错并列出候选。
	err := func() error {
		_, e := coerceEnum(fd, "__not_a_value__")
		return e
	}()
	if err == nil {
		t.Fatal("enum←非法名 应报错")
	}
	if !strings.Contains(err.Error(), firstName) {
		t.Errorf("错误应列出合法候选 %q: %v", firstName, err)
	}
	// 整型编号命中。
	if v, err := coerceEnum(fd, firstNum); err != nil {
		t.Errorf("enum←int(%d) = (%v, %v)", firstNum, v, err)
	}
	// 越界编号 → 报错。
	if _, err := coerceEnum(fd, 999_999); err == nil {
		t.Error("enum←越界编号 应报错")
	}
	// 异型（float）→ 默认分支报错。
	if _, err := coerceEnum(fd, 1.5); err == nil {
		t.Error("enum←float 应报错")
	}
}
