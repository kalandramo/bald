package fieldmaskutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	appspecv1 "github.com/kalandramo/bald/bconf/gen/go/bald/appspec/v1"
	storev1 "github.com/kalandramo/bald/bconf/gen/go/bald/store/v1"
)

// TestNestedMask_Filter 覆盖标量、嵌套消息、重复消息与标量 map 的保留/清空。
func TestNestedMask_Filter(t *testing.T) {
	msg := &appspecv1.AppSpec{
		Meta: &appspecv1.AppMeta{Name: "svc", Module: "example.com/svc"},
		Components: []*appspecv1.ComponentSpec{
			{Kind: "heartbeat", Name: "hb", Params: map[string]string{"interval": "5s", "mode": "on"}},
			{Kind: "heartbeat", Name: "hb2"},
		},
		Capability: &appspecv1.CapabilitySpec{Provides: []string{"audit"}},
	}

	NestedMaskFromPaths([]string{"meta.name", "components.name", "components.params.interval"}).Filter(msg)

	want := &appspecv1.AppSpec{
		Meta: &appspecv1.AppMeta{Name: "svc"},
		Components: []*appspecv1.ComponentSpec{
			{Name: "hb", Params: map[string]string{"interval": "5s"}},
			{Name: "hb2"},
		},
	}
	assert.True(t, proto.Equal(want, msg), "got %v", msg)
}

// TestNestedMask_Prune 覆盖字段、整段消息与 map 键的剪除，其余字段保持原样。
func TestNestedMask_Prune(t *testing.T) {
	msg := &appspecv1.AppSpec{
		Meta: &appspecv1.AppMeta{Name: "svc", Module: "m"},
		Components: []*appspecv1.ComponentSpec{
			{Kind: "heartbeat", Name: "hb", Params: map[string]string{"interval": "5s", "mode": "on"}},
		},
		Capability: &appspecv1.CapabilitySpec{Provides: []string{"audit"}},
	}

	NestedMaskFromPaths([]string{"meta.name", "components.params.mode"}).Prune(msg)

	want := &appspecv1.AppSpec{
		Meta: &appspecv1.AppMeta{Module: "m"},
		Components: []*appspecv1.ComponentSpec{
			{Kind: "heartbeat", Name: "hb", Params: map[string]string{"interval": "5s"}},
		},
		Capability: &appspecv1.CapabilitySpec{Provides: []string{"audit"}},
	}
	assert.True(t, proto.Equal(want, msg), "got %v", msg)

	NestedMaskFromPaths([]string{"meta", "components"}).Prune(msg)
	assert.Nil(t, msg.Meta)
	assert.Empty(t, msg.Components)
	assert.Equal(t, []string{"audit"}, msg.GetCapability().GetProvides())
}

// TestNestedMask_Overwrite 覆盖标量、嵌套消息（含 dest 父消息为空时的初始化）、
// 重复消息（截断多余元素）与整段字段替换。
func TestNestedMask_Overwrite(t *testing.T) {
	src := &appspecv1.AppSpec{
		Meta:       &appspecv1.AppMeta{Name: "new", Module: "newmod"},
		Components: []*appspecv1.ComponentSpec{{Kind: "newkind", Name: "c1", Params: map[string]string{"a": "1"}}},
	}
	dest := &appspecv1.AppSpec{
		Meta:       &appspecv1.AppMeta{Name: "old", Module: "oldmod"},
		Components: []*appspecv1.ComponentSpec{{Kind: "oldkind", Name: "c1"}, {Kind: "x", Name: "c2"}},
		Capability: &appspecv1.CapabilitySpec{Provides: []string{"kept"}},
	}

	NestedMaskFromPaths([]string{"meta.name", "components.kind"}).Overwrite(src, dest)

	want := &appspecv1.AppSpec{
		Meta:       &appspecv1.AppMeta{Name: "new", Module: "oldmod"},
		Components: []*appspecv1.ComponentSpec{{Kind: "newkind", Name: "c1"}},
		Capability: &appspecv1.CapabilitySpec{Provides: []string{"kept"}},
	}
	assert.True(t, proto.Equal(want, dest), "got %v", dest)
}

func TestNestedMask_OverwriteInitializesNilParent(t *testing.T) {
	src := &appspecv1.AppSpec{Meta: &appspecv1.AppMeta{Name: "svc", Module: "ignored"}}
	dest := &appspecv1.AppSpec{}

	NestedMaskFromPaths([]string{"meta.name"}).Overwrite(src, dest)

	require.NotNil(t, dest.Meta)
	assert.Equal(t, "svc", dest.Meta.GetName())
	assert.Empty(t, dest.Meta.GetModule())
}

func TestNestedMask_OverwriteEmptySourceClearsDest(t *testing.T) {
	src := &appspecv1.AppSpec{Meta: &appspecv1.AppMeta{}}
	dest := &appspecv1.AppSpec{Meta: &appspecv1.AppMeta{Name: "old", Module: "m"}}

	NestedMaskFromPaths([]string{"meta.name"}).Overwrite(src, dest)

	assert.Empty(t, dest.Meta.GetName())
	assert.Equal(t, "m", dest.Meta.GetModule())
}

func TestNestedMask_OverwriteWholeFields(t *testing.T) {
	t.Run("message and repeated field", func(t *testing.T) {
		src := &appspecv1.AppSpec{
			Meta:       &appspecv1.AppMeta{Name: "svc"},
			Components: []*appspecv1.ComponentSpec{{Name: "a"}},
		}
		dest := &appspecv1.AppSpec{
			Meta:       &appspecv1.AppMeta{Name: "old"},
			Components: []*appspecv1.ComponentSpec{{Name: "b"}, {Name: "c"}},
		}

		NestedMaskFromPaths([]string{"meta", "components"}).Overwrite(src, dest)

		require.NotNil(t, dest.Meta)
		assert.Equal(t, "svc", dest.Meta.GetName())
		require.Len(t, dest.Components, 1)
		assert.Equal(t, "a", dest.Components[0].GetName())
	})

	t.Run("scalar map", func(t *testing.T) {
		src := &appspecv1.AppSpec{
			Components: []*appspecv1.ComponentSpec{{Name: "a", Params: map[string]string{"a": "1"}}},
		}
		dest := &appspecv1.AppSpec{
			Components: []*appspecv1.ComponentSpec{{Name: "b", Params: map[string]string{"b": "2"}}},
		}

		NestedMaskFromPaths([]string{"components.params"}).Overwrite(src, dest)

		require.Len(t, dest.Components, 1)
		assert.Equal(t, "b", dest.Components[0].GetName())
		assert.Equal(t, map[string]string{"a": "1"}, dest.Components[0].GetParams())
	})
}

// TestNestedMask_Validate 覆盖合法嵌套路径与四类非法路径的错误定位。
func TestNestedMask_Validate(t *testing.T) {
	model := &appspecv1.AppSpec{}

	for _, paths := range [][]string{
		{"meta.name"},
		{"components.params"},
		{"components.name"},
		{"capability.requires.component"},
	} {
		assert.NoError(t, NestedMaskFromPaths(paths).Validate(model), "paths %v", paths)
	}

	tests := []struct {
		paths []string
		want  string
	}{
		{[]string{"nope"}, "unknown path: 'nope'"},
		{[]string{"meta.nope"}, "unknown path: 'meta.nope'"},
		{[]string{"capability.provides.foo"}, "'capability.provides': list element isn't message kind"},
		{[]string{"components.params.k.v"}, "'components.params': map value isn't message kind"},
		{[]string{"meta.name.x"}, "'meta.name': can't get nested fields"},
	}
	for _, tt := range tests {
		assert.ErrorContains(t, NestedMaskFromPaths(tt.paths).Validate(model), tt.want, "paths %v", tt.paths)
	}
}

func TestNestedMask_EmptyMaskIsNoop(t *testing.T) {
	msg := &appspecv1.AppSpec{
		Meta:       &appspecv1.AppMeta{Name: "svc", Module: "m"},
		Components: []*appspecv1.ComponentSpec{{Kind: "heartbeat", Name: "hb"}},
	}
	orig := proto.Clone(msg)

	empty := NestedMask{}
	empty.Filter(msg)
	empty.Prune(msg)
	empty.Overwrite(&appspecv1.AppSpec{}, msg)
	assert.NoError(t, empty.Validate(msg))

	assert.True(t, proto.Equal(orig, msg), "got %v", msg)
}

// TestNestedMask_MapOfMessages 用动态消息覆盖真实 proto 里不存在的
// map<string, message> 分支（Filter/Prune/Overwrite 的按 key 下钻）。
func TestNestedMask_MapOfMessages(t *testing.T) {
	// src 与 dest 必须共享同一份描述符，动态消息的字段校验按描述符指针归属。
	md := holderDescriptor(t)
	newHolder := func(items map[string][2]string) *dynamicpb.Message {
		return buildHolder(t, md, items)
	}

	t.Run("filter", func(t *testing.T) {
		msg := newHolder(map[string][2]string{
			"a": {"1", "n1"},
			"b": {"2", "n2"},
		})

		NestedMaskFromPaths([]string{"items.a.id"}).Filter(msg)

		assert.Equal(t, map[string][2]string{"a": {"1", ""}}, holderItems(msg))
	})

	t.Run("filter keeps whole entry for empty submask", func(t *testing.T) {
		msg := newHolder(map[string][2]string{
			"a": {"1", "n1"},
			"b": {"2", "n2"},
		})

		NestedMaskFromPaths([]string{"items.a"}).Filter(msg)

		assert.Equal(t, map[string][2]string{"a": {"1", "n1"}}, holderItems(msg))
	})

	t.Run("prune", func(t *testing.T) {
		msg := newHolder(map[string][2]string{
			"a": {"1", "n1"},
			"b": {"2", "n2"},
		})

		NestedMaskFromPaths([]string{"items.a.id", "items.b"}).Prune(msg)

		assert.Equal(t, map[string][2]string{"a": {"", "n1"}}, holderItems(msg))
	})

	t.Run("overwrite", func(t *testing.T) {
		src := newHolder(map[string][2]string{"a": {"1", "n1"}})
		dest := newHolder(map[string][2]string{
			"a": {"old", "oldnote"},
			"b": {"keep", "keepnote"},
		})

		NestedMaskFromPaths([]string{"items.a.id"}).Overwrite(src, dest)

		// 覆写按 src 的 key 遍历：dest 独有的 "b" 不在 mask 里，但也不会被清除
		// （与 Filter/Prune 遍历 dest 语义不同）。
		assert.Equal(t, map[string][2]string{
			"a": {"1", ""},
			"b": {"keep", "keepnote"},
		}, holderItems(dest))
	})

	t.Run("overwrite into unset map", func(t *testing.T) {
		src := newHolder(map[string][2]string{"a": {"1", "n1"}})
		dest := newHolder(nil)

		NestedMaskFromPaths([]string{"items.a.id"}).Overwrite(src, dest)

		assert.Equal(t, map[string][2]string{"a": {"1", ""}}, holderItems(dest))
	})

	t.Run("validate", func(t *testing.T) {
		model := newHolder(nil)

		// Validate 把 map 后的路径当作「元素消息的字段名」，不理解 map key；
		// 按 key 下钻的路径（items.a.id）只有 Filter/Prune/Overwrite 支持。
		assert.NoError(t, NestedMaskFromPaths([]string{"items.id"}).Validate(model))
		assert.ErrorContains(t,
			NestedMaskFromPaths([]string{"items.nope"}).Validate(model),
			"unknown path: 'items.nope'")
		assert.ErrorContains(t,
			NestedMaskFromPaths([]string{"items.id.x"}).Validate(model),
			"can't get nested fields")
	})
}

// TestFieldMaskWrappers 覆盖 fieldmaskutil.go 的便捷包装。
func TestFieldMaskWrappers(t *testing.T) {
	msg := &appspecv1.AppSpec{
		Meta:       &appspecv1.AppMeta{Name: "svc", Module: "m"},
		Capability: &appspecv1.CapabilitySpec{Provides: []string{"audit"}},
	}

	Filter(msg, []string{"meta.name"})
	assert.Equal(t, "svc", msg.GetMeta().GetName())
	assert.Empty(t, msg.GetMeta().GetModule())
	assert.Nil(t, msg.Capability)

	Prune(msg, []string{"meta.name"})
	assert.Empty(t, msg.GetMeta().GetName())

	Overwrite(&appspecv1.AppSpec{Meta: &appspecv1.AppMeta{Name: "new"}}, msg, []string{"meta.name"})
	assert.Equal(t, "new", msg.GetMeta().GetName())

	assert.NoError(t, Validate(msg, []string{"meta.name"}))
	assert.Error(t, Validate(msg, []string{"nope"}))
}

func TestNormalizePaths(t *testing.T) {
	assert.Nil(t, NormalizePaths(nil))
	assert.Equal(t, []string{"id", "page_size"}, NormalizePaths([]string{"id_", "pageSize"}))

	assert.NotPanics(t, func() { NormalizeFieldMaskPaths(nil) })
	assert.NotPanics(t, func() { NormalizeFieldMaskPaths(&fieldmaskpb.FieldMask{}) })
}

func TestFieldMaskByFieldMask(t *testing.T) {
	msg := proto.Message(&storev1.PagingRequest{Token: strp("cursor"), OrderBy: strp("id")})

	fm, err := fieldmaskpb.New(&storev1.PagingRequest{}, "token")
	require.NoError(t, err)
	require.NoError(t, PruneByFieldMask(&msg, fm))
	assert.Empty(t, msg.(*storev1.PagingRequest).GetToken())
	assert.Equal(t, "id", msg.(*storev1.PagingRequest).GetOrderBy())

	// 同源覆写是自赋值，不应改变消息。
	require.NoError(t, OverwriteByFieldMask(&msg, fm))
	assert.Equal(t, "id", msg.(*storev1.PagingRequest).GetOrderBy())
}

// buildHolder 按给定描述符构造一个含 map<string, Item> 字段的动态消息，
// 用于覆盖真实 proto 中不存在的 map-of-message 分支。
func buildHolder(t *testing.T, md protoreflect.MessageDescriptor, items map[string][2]string) *dynamicpb.Message {
	t.Helper()
	msg := dynamicpb.NewMessage(md)
	if items == nil {
		return msg
	}

	fd := md.Fields().ByName("items")
	itemDesc := fd.MapValue().Message()
	xmap := msg.Mutable(fd).Map()
	for key, item := range items {
		val := dynamicpb.NewMessage(itemDesc)
		val.Set(itemDesc.Fields().ByName("id"), protoreflect.ValueOfString(item[0]))
		val.Set(itemDesc.Fields().ByName("note"), protoreflect.ValueOfString(item[1]))
		xmap.Set(protoreflect.ValueOfString(key).MapKey(), protoreflect.ValueOfMessage(val))
	}
	return msg
}

func holderItems(msg *dynamicpb.Message) map[string][2]string {
	md := msg.Descriptor()
	fd := md.Fields().ByName("items")
	out := map[string][2]string{}
	if !msg.Has(fd) {
		return out
	}
	msg.Get(fd).Map().Range(func(k protoreflect.MapKey, v protoreflect.Value) bool {
		item := v.Message()
		desc := item.Descriptor()
		out[k.String()] = [2]string{
			item.Get(desc.Fields().ByName("id")).String(),
			item.Get(desc.Fields().ByName("note")).String(),
		}
		return true
	})
	return out
}

func holderDescriptor(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()

	optional := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum()
	repeated := descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
	stringType := descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()
	messageType := descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum()

	file, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name:    proto.String("fieldmaskutil/holder.proto"),
		Package: proto.String("fieldmaskutil.test"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: proto.String("Item"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{Name: proto.String("id"), Number: proto.Int32(1), Label: optional, Type: stringType},
					{Name: proto.String("note"), Number: proto.Int32(2), Label: optional, Type: stringType},
				},
			},
			{
				Name: proto.String("Holder"),
				Field: []*descriptorpb.FieldDescriptorProto{
					{
						Name:     proto.String("items"),
						Number:   proto.Int32(1),
						Label:    repeated,
						Type:     messageType,
						TypeName: proto.String(".fieldmaskutil.test.Holder.ItemsEntry"),
					},
				},
				NestedType: []*descriptorpb.DescriptorProto{
					{
						Name: proto.String("ItemsEntry"),
						Field: []*descriptorpb.FieldDescriptorProto{
							{Name: proto.String("key"), Number: proto.Int32(1), Label: optional, Type: stringType},
							{
								Name:     proto.String("value"),
								Number:   proto.Int32(2),
								Label:    optional,
								Type:     messageType,
								TypeName: proto.String(".fieldmaskutil.test.Item"),
							},
						},
						Options: &descriptorpb.MessageOptions{MapEntry: proto.Bool(true)},
					},
				},
			},
		},
	}, nil)
	require.NoError(t, err)
	return file.Messages().ByName("Holder")
}
