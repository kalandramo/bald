package bconf

// fold_invariant_test.go 把折叠匹配依赖的描述符形状前提从散文变成闸门
// （unmarshal.go 的 lookupCanonical/foldPathRead 依赖这两条，破坏即歧义）：
//
//   1. 同层不同字段的折叠键互不重合：foldKey（小写、'-' 折成 '_'）作用于
//      proto 名与 JSON 名两形态，跨字段重合会让一次查找命中别的字段的键。
//   2. 下划线字段名不得是同层其它字段名的严格段前缀：server_addrs 与
//      server（或 server_addrs_port）同层共存时，折叠下钻会与后者的子链
//      互相歧义（这就是「a_b 标量与 a 子消息不得同层共存」的完全体）。
//
// 契约形状一旦破坏，本测试先红——而不是等配置装载在生产里走出歧义分支。

import (
	"strings"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// foldKey 返回键的折叠形：'-' 折成 '_' 后统一小写。折叠匹配
// （忽略大小写与 '-'/'_' 差异）在纯 ASCII 键上等价于折叠形相等比较。
func foldKey(s string) string {
	return strings.ToLower(strings.ReplaceAll(s, "-", "_"))
}

func TestContractFoldShapeInvariant(t *testing.T) {
	root := (&bootstrapv1.BootstrapConfig{}).ProtoReflect().Descriptor()
	visited := map[protoreflect.MessageDescriptor]bool{}
	var walk func(md protoreflect.MessageDescriptor, path string)
	walk = func(md protoreflect.MessageDescriptor, path string) {
		if md == nil || visited[md] {
			return
		}
		visited[md] = true

		fields := md.Fields()
		for i := 0; i < fields.Len(); i++ {
			for j := i + 1; j < fields.Len(); j++ {
				fi, fj := fields.Get(i), fields.Get(j)
				// 前提 1：折叠键（proto 名 / JSON 名两形态）跨字段不重合。
				for _, xi := range []string{string(fi.Name()), fi.JSONName()} {
					for _, xj := range []string{string(fj.Name()), fj.JSONName()} {
						if foldKey(xi) == foldKey(xj) {
							t.Errorf("%s: 字段 %q 与 %q 折叠键重合（%q vs %q），折叠匹配将歧义",
								path, fi.Name(), fj.Name(), xi, xj)
						}
					}
				}
				// 前提 2：下划线段序列不得互为严格前缀。
				si, sj := strings.Split(string(fi.Name()), "_"), strings.Split(string(fj.Name()), "_")
				if segsStrictPrefix(si, sj) || segsStrictPrefix(sj, si) {
					t.Errorf("%s: 字段 %q 与 %q 段序列互为严格前缀，折叠下钻将歧义",
						path, fi.Name(), fj.Name())
				}
			}
		}

		for i := 0; i < fields.Len(); i++ {
			fd := fields.Get(i)
			if fd.IsMap() {
				if v := fd.MapValue(); v.Kind() == protoreflect.MessageKind {
					walk(v.Message(), path+"."+string(fd.Name())+"{}")
				}
				continue
			}
			if fd.Kind() == protoreflect.MessageKind {
				walk(fd.Message(), path+"."+string(fd.Name()))
			}
		}
	}
	walk(root, "BootstrapConfig")
}

// segsStrictPrefix 判定 a 是否是 b 的严格段前缀（长度必须严格小于）。
func segsStrictPrefix(a, b []string) bool {
	if len(a) >= len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
