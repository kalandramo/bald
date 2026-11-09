// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

// unmarshal_deep_test.go 补齐 unmarshal.go 里「不依赖外部 infra、纯靠描述符 + map
// 即可驱动」的分支：lookupField/foldPath/lookupKeyFold 的折叠下钻、coerceValue 的
// repeated/map/message 容器分支，以及 UnmarshalMap 的各错误出口。容器字段全部从
// 真实描述符树里动态取（不硬编码字段名），契约改名时测试随描述符自动跟随。

package bconf

import (
	"strings"
	"sync"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	storev1 "github.com/kalandramo/bald/bconf/gen/go/bald/store/v1"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// containerFds 记录描述符树中首个 repeated-scalar / map<scalar> / 非 Duration 子消息字段。
type containerFds struct {
	list protoreflect.FieldDescriptor
	kv   protoreflect.FieldDescriptor
	sub  protoreflect.FieldDescriptor
}

func findContainers(m proto.Message) containerFds {
	var res containerFds
	seen := make(map[protoreflect.MessageDescriptor]bool)
	var walk func(md protoreflect.MessageDescriptor)
	walk = func(md protoreflect.MessageDescriptor) {
		if seen[md] {
			return
		}
		seen[md] = true
		fields := md.Fields()
		for i := 0; i < fields.Len(); i++ {
			fd := fields.Get(i)
			switch {
			case fd.IsMap():
				if res.kv == nil && fd.MapValue().Kind() != protoreflect.MessageKind {
					res.kv = fd
				}
			case fd.IsList():
				if res.list == nil && fd.Kind() != protoreflect.MessageKind {
					res.list = fd
				}
			case fd.Kind() == protoreflect.MessageKind:
				if strings.HasPrefix(string(fd.Message().FullName()), "google.protobuf.") {
					continue
				}
				if res.sub == nil {
					res.sub = fd
				}
				walk(fd.Message())
			}
		}
	}
	walk(m.ProtoReflect().Descriptor())
	return res
}

// TestLookupFieldFold 覆盖折叠匹配的三种结局：大小写/连字符命中、嵌套链完整命中后
// 搬移+回删、中间段非 map 失败、首段查无失败。
func TestLookupFieldFold(t *testing.T) {
	// 大小写 + 连字符折叠命中（ReplaceAll 后 EqualFold）。
	if v, ok := lookupField(map[string]any{"Foo-Bar": 1}, "foo_bar"); !ok || v != 1 {
		t.Errorf("lookupField 折叠大小写/连字符 = (%v, %v), want (1,true)", v, ok)
	}

	// 两段嵌套完整命中：server_addrs → {server:{addrs:...}}，命中后搬平级并回删原链。
	m := map[string]any{"server": map[string]any{"addrs": "x:1"}}
	if v, ok := lookupField(m, "server_addrs"); !ok || v != "x:1" {
		t.Fatalf("lookupField 两段折叠 = (%v, %v), want (\"x:1\",true)", v, ok)
	}
	if m["server_addrs"] != "x:1" {
		t.Errorf("折叠命中后应把值搬到平级键 server_addrs: %v", m)
	}
	if _, present := m["server"]; present {
		t.Errorf("折叠命中后应回删原嵌套链 server 段: %v", m)
	}

	// 中间段存在但不是 map → 折叠失败。
	if v, ok := lookupField(map[string]any{"server": 5}, "server_addrs"); ok || v != nil {
		t.Errorf("折叠中间段非 map = (%v, %v), want (nil,false)", v, ok)
	}

	// 首段查无 → 折叠失败。
	if v, ok := lookupField(map[string]any{"other": 1}, "server_addrs"); ok || v != nil {
		t.Errorf("折叠首段查无 = (%v, %v), want (nil,false)", v, ok)
	}
}

// TestLookupKeyFold 单层折叠：先精确后忽略大小写与 -/_ 差异。
func TestLookupKeyFold(t *testing.T) {
	if k, v, ok := lookupKeyFold(map[string]any{"a_b": 1}, "a_b"); !ok || k != "a_b" || v != 1 {
		t.Errorf("lookupKeyFold 精确 = (%q, %v, %v)", k, v, ok)
	}
	if k, v, ok := lookupKeyFold(map[string]any{"A-B": 2}, "a_b"); !ok || k != "A-B" || v != 2 {
		t.Errorf("lookupKeyFold 折叠命中 = (%q, %v, %v)", k, v, ok)
	}
	if _, _, ok := lookupKeyFold(map[string]any{"x": 1}, "y"); ok {
		t.Error("lookupKeyFold 未命中应返回 false")
	}
}

// TestCoerceValueContainers 用真实容器字段描述符驱动 coerceValue 的 list/map/message
// 三形态，并覆盖各自的报错分支。
func TestCoerceValueContainers(t *testing.T) {
	c := findContainers(NewBootstrap())
	if c.list == nil || c.kv == nil || c.sub == nil {
		t.Fatalf("契约树应含 repeated-scalar / map<scalar> / 子消息字段: %+v", c)
	}

	// list：元素强转失败 → 带下标的错误。
	if _, err := coerceValue(c.list, []any{map[string]any{"nested": 1}}); err == nil {
		t.Error("coerceValue list←非法元素 应报错")
	}

	// map：raw 非 map[string]any → 报错。
	if _, err := coerceValue(c.kv, "not-a-map"); err == nil {
		t.Error("coerceValue map←非 map 应报错")
	}
	// map：某个 value 强转失败 → 带键名的错误。
	if _, err := coerceValue(c.kv, map[string]any{"k": map[string]any{"x": 1}}); err == nil {
		t.Error("coerceValue map←非法 value 应报错")
	}

	// message：raw 非 map → 报错。
	if _, err := coerceValue(c.sub, 12345); err == nil {
		t.Error("coerceValue message←非 map 应报错")
	}
	// message：递归子字段强转失败 → 透传子错误。
	subScalar := firstScalarField(c.sub.Message())
	if subScalar == nil {
		t.Fatal("子消息应含标量字段用于递归报错测试")
	}
	if _, err := coerceValue(c.sub, map[string]any{subScalar.JSONName(): map[string]any{}}); err == nil {
		t.Error("coerceValue message←子字段非法 应报错")
	}
}

// firstScalarField 返回消息描述符里首个标量（非 repeated/map/message）字段。
func firstScalarField(md protoreflect.MessageDescriptor) protoreflect.FieldDescriptor {
	fields := md.Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if fd.IsList() || fd.IsMap() {
			continue
		}
		if fd.Kind() == protoreflect.MessageKind || fd.Kind() == protoreflect.GroupKind {
			continue
		}
		return fd
	}
	return nil
}

// TestUnmarshalMapErrorPaths 逐条驱动 UnmarshalMap 的四个错误出口。
func TestUnmarshalMapErrorPaths(t *testing.T) {
	// 1) msg==nil。
	if err := UnmarshalMap(map[string]any{}, nil); err == nil ||
		!strings.Contains(err.Error(), "proto message is nil") {
		t.Errorf("UnmarshalMap(nil msg) 应报 nil 消息错: %v", err)
	}

	// 2) coerce 失败：Backoff.curve 给一个非法枚举名 → coerceEnum 错 → coerce 出口。
	if err := UnmarshalMap(map[string]any{"curve": "__not_a_curve__"}, &bootstrapv1.Backoff{}); err == nil ||
		!strings.Contains(err.Error(), "coerce") {
		t.Errorf("UnmarshalMap 非法枚举应报 coerce 错: %v", err)
	}

	// 3) json.Marshal 失败：契约外未知键挂一个 func（coerce 只遍历已知字段，原样
	//    留在树里），marshal 遇 func 必失败。
	if err := UnmarshalMap(map[string]any{"__weird__": func() {}}, &bootstrapv1.Backoff{}); err == nil ||
		!strings.Contains(err.Error(), "marshal settings") {
		t.Errorf("UnmarshalMap 不可序列化值应报 marshal 错: %v", err)
	}

	// 4) protojson 失败：PagingRequest.page 是 uint32，给一个 coerce 放行但超出
	//    uint32 值域的整数 → 规范化后仍是合法 JSON 数字，protojson 严格解析报越界。
	if err := UnmarshalMap(map[string]any{"page": 5_000_000_000}, &storev1.PagingRequest{}); err == nil ||
		!strings.Contains(err.Error(), "unmarshal into") {
		t.Errorf("UnmarshalMap uint32 越界应报 protojson 错: %v", err)
	}
}

// TestLookupFieldsConcurrentFirstBuild 用同一个「未入缓存」描述符并发首建，触发
// LoadOrStore 竞争分支（返回他人在建期间抢先入表的等价表）。竞争命中依赖调度，
// 但在 -race + 足够并发下稳定发生；本用例同时验证缓存复用结果始终等价。
func TestLookupFieldsConcurrentFirstBuild(t *testing.T) {
	md := (&bootstrapv1.Backoff{}).ProtoReflect().Descriptor()
	fieldLookupCache.Delete(md)

	const n = 64
	var wg sync.WaitGroup
	got := make([]*fieldLookup, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			got[i] = lookupFields(md)
		}(i)
	}
	close(start)
	wg.Wait()

	// 所有 goroutine 拿到的表必须等价（同名键映射到同字段）。
	want := got[0]
	for i := 1; i < n; i++ {
		if len(got[i].byName) != len(want.byName) {
			t.Fatalf("并发建的表长度不一致: %d vs %d", len(got[i].byName), len(want.byName))
		}
		for k, fd := range want.byName {
			if got[i].byName[k] != fd {
				t.Fatalf("键 %q 在并发建的表中映射不同字段", k)
			}
		}
	}
}
