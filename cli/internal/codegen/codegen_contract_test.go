package codegen

import (
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	appspecv1 "github.com/kalandramo/bald/bconf/gen/go/bald/appspec/v1"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// 合成一个含全部标量类别（string/bytes/enum/map/repeated/duration/timestamp/
// 嵌套消息）的 proto 描述符，用 dynamicpb 驱动 messageToMap/scalarToConfig 的
// 每条派生分支——真实 BootstrapConfig 不含 bytes/enum 等字段，那些分支只能靠
// 合成描述符到达。Duration/Timestamp 依赖经 GlobalFiles 解析（well-known 已注册）。
func contractTestMessageDesc(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	opt := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum()
	rep := descriptorpb.FieldDescriptorProto_LABEL_REPEATED.Enum()
	str := descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum()
	byt := descriptorpb.FieldDescriptorProto_TYPE_BYTES.Enum()
	msg := descriptorpb.FieldDescriptorProto_TYPE_MESSAGE.Enum()
	enum := descriptorpb.FieldDescriptorProto_TYPE_ENUM.Enum()
	field := func(name string, num int32, typ *descriptorpb.FieldDescriptorProto_Type, label *descriptorpb.FieldDescriptorProto_Label, typeName string) *descriptorpb.FieldDescriptorProto {
		f := &descriptorpb.FieldDescriptorProto{Name: proto.String(name), Number: proto.Int32(num), Type: typ, Label: label}
		if typeName != "" {
			f.TypeName = proto.String(typeName)
		}
		return f
	}
	entry := func(name, valType string) *descriptorpb.DescriptorProto {
		vt := str
		if valType == "bytes" {
			vt = byt
		}
		return &descriptorpb.DescriptorProto{
			Name:    proto.String(name),
			Options: &descriptorpb.MessageOptions{MapEntry: proto.Bool(true)},
			Field: []*descriptorpb.FieldDescriptorProto{
				field("key", 1, str, opt, ""),
				field("value", 2, vt, opt, ""),
			},
		}
	}
	fd := &descriptorpb.FileDescriptorProto{
		Name:       proto.String("contract_test.proto"),
		Package:    proto.String("ctest"),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"google/protobuf/duration.proto", "google/protobuf/timestamp.proto"},
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: proto.String("M"),
				Field: []*descriptorpb.FieldDescriptorProto{
					field("s", 1, str, opt, ""),
					field("b", 2, byt, opt, ""),
					field("e", 3, enum, opt, ".ctest.E"),
					field("mp", 4, msg, rep, ".ctest.M.MpEntry"),
					field("lst", 5, str, rep, ""),
					field("blst", 6, byt, rep, ""),
					field("dur", 7, msg, opt, ".google.protobuf.Duration"),
					field("ts", 8, msg, opt, ".google.protobuf.Timestamp"),
					field("child", 9, msg, opt, ".ctest.Child"),
					field("children", 10, msg, rep, ".ctest.Child"),
					field("mb", 11, msg, rep, ".ctest.M.MbEntry"),
				},
				NestedType: []*descriptorpb.DescriptorProto{
					entry("MpEntry", "string"),
					entry("MbEntry", "bytes"),
				},
			},
			{
				Name:  proto.String("Child"),
				Field: []*descriptorpb.FieldDescriptorProto{field("name", 1, str, opt, "")},
			},
		},
		EnumType: []*descriptorpb.EnumDescriptorProto{
			{
				Name: proto.String("E"),
				Value: []*descriptorpb.EnumValueDescriptorProto{
					{Name: proto.String("ZERO"), Number: proto.Int32(0)},
					{Name: proto.String("ONE"), Number: proto.Int32(1)},
				},
			},
		},
	}
	file, err := protodesc.NewFile(fd, protoregistry.GlobalFiles)
	if err != nil {
		t.Fatalf("build descriptor: %v", err)
	}
	return file.Messages().ByName("M")
}

func TestContract_ScalarToConfig(t *testing.T) {
	md := contractTestMessageDesc(t)
	fd := func(n string) protoreflect.FieldDescriptor { return md.Fields().ByName(protoreflect.Name(n)) }

	// bytes → base64 字符串（protojson 与 bconf 装载器的 bytes 口径）；
	// inline PEM 因此能进契约 YAML。
	if got, err := scalarToConfig(fd("b"), protoreflect.ValueOfBytes([]byte("x"))); err != nil || got != "eA==" {
		t.Errorf("bytes: got=%v err=%v, want eA==", got, err)
	}
	// 枚举按名序列化。
	if got, err := scalarToConfig(fd("e"), protoreflect.ValueOfEnum(1)); err != nil || got != "ONE" {
		t.Errorf("enum: got=%v err=%v, want ONE", got, err)
	}
	// Duration → protojson 字面量 "10s"。
	if got, err := scalarToConfig(fd("dur"), protoreflect.ValueOfMessage(durationpb.New(10*time.Second).ProtoReflect())); err != nil || got != "10s" {
		t.Errorf("duration: got=%v err=%v, want 10s", got, err)
	}
	// Timestamp → RFC3339。
	ts := timestamppb.New(time.Unix(1700000000, 0).UTC())
	got, err := scalarToConfig(fd("ts"), protoreflect.ValueOfMessage(ts.ProtoReflect()))
	if err != nil || got != ts.AsTime().Format(time.RFC3339Nano) {
		t.Errorf("timestamp: got=%v err=%v, want %v", got, err, ts.AsTime().Format(time.RFC3339Nano))
	}
	// 无效消息值 → nil,nil（空段语义）。
	if got, err := scalarToConfig(fd("child"), protoreflect.ValueOfMessage(&dynamicpb.Message{})); err != nil || got != nil {
		t.Errorf("invalid message: got=%v err=%v, want nil,nil", got, err)
	}
	// 普通标量原样透传。
	if got, err := scalarToConfig(fd("s"), protoreflect.ValueOfString("hi")); err != nil || got != "hi" {
		t.Errorf("string: got=%v err=%v, want hi", got, err)
	}
}

func TestContract_MessageToMap(t *testing.T) {
	md := contractTestMessageDesc(t)
	m := dynamicpb.NewMessage(md)
	f := func(n string) protoreflect.FieldDescriptor { return md.Fields().ByName(protoreflect.Name(n)) }

	m.Set(f("s"), protoreflect.ValueOfString("hi"))
	m.Set(f("e"), protoreflect.ValueOfEnum(1))
	m.Mutable(f("mp")).Map().Set(protoreflect.ValueOfString("k").MapKey(), protoreflect.ValueOfString("v"))
	m.Mutable(f("lst")).List().Append(protoreflect.ValueOfString("a"))
	m.Mutable(f("lst")).List().Append(protoreflect.ValueOfString("b"))
	child := m.Mutable(f("child")).Message()
	child.Set(child.Descriptor().Fields().ByName("name"), protoreflect.ValueOfString("x"))
	kid := m.Mutable(f("children")).List().NewElement()
	kid.Message().Set(kid.Message().Descriptor().Fields().ByName("name"), protoreflect.ValueOfString("y"))
	m.Mutable(f("children")).List().Append(kid)

	tree, err := messageToMap(m.ProtoReflect())
	if err != nil {
		t.Fatalf("messageToMap: %v", err)
	}
	got := tree.(map[string]any)
	if got["s"] != "hi" || got["e"] != "ONE" {
		t.Errorf("scalars: %v", got)
	}
	if mp, ok := got["mp"].(map[string]any); !ok || mp["k"] != "v" {
		t.Errorf("map field: %v", got["mp"])
	}
	if lst, ok := got["lst"].([]any); !ok || len(lst) != 2 || lst[0] != "a" {
		t.Errorf("list field: %v", got["lst"])
	}
	if ch, ok := got["child"].(map[string]any); !ok || ch["name"] != "x" {
		t.Errorf("nested message: %v", got["child"])
	}
	if kids, ok := got["children"].([]any); !ok || len(kids) != 1 {
		t.Errorf("message list: %v", got["children"])
	}
}

func TestContract_MessageToMap_Bytes(t *testing.T) {
	md := contractTestMessageDesc(t)
	f := func(n string) protoreflect.FieldDescriptor { return md.Fields().ByName(protoreflect.Name(n)) }

	// 三种 bytes 形状（标量 / repeated / map 值）都投影成 base64 字符串：
	// 装载器只认 string，生成器写出其他形态就是一份跑不起来的配置。
	mb := dynamicpb.NewMessage(md)
	mb.Set(f("b"), protoreflect.ValueOfBytes([]byte("x")))
	tree, err := messageToMap(mb.ProtoReflect())
	if err != nil || tree.(map[string]any)["b"] != "eA==" {
		t.Errorf("singular bytes: tree=%v err=%v, want eA==", tree, err)
	}

	ml := dynamicpb.NewMessage(md)
	ml.Mutable(f("blst")).List().Append(protoreflect.ValueOfBytes([]byte("x")))
	tree, err = messageToMap(ml.ProtoReflect())
	if lst, ok := tree.(map[string]any)["blst"].([]any); err != nil || !ok || len(lst) != 1 || lst[0] != "eA==" {
		t.Errorf("list bytes: tree=%v err=%v, want [eA==]", tree, err)
	}

	mm := dynamicpb.NewMessage(md)
	mm.Mutable(f("mb")).Map().Set(protoreflect.ValueOfString("k").MapKey(), protoreflect.ValueOfBytes([]byte("x")))
	tree, err = messageToMap(mm.ProtoReflect())
	if mp, ok := tree.(map[string]any)["mb"].(map[string]any); err != nil || !ok || mp["k"] != "eA==" {
		t.Errorf("map bytes value: tree=%v err=%v, want k=eA==", tree, err)
	}
}

func TestContract_ContractTree_AppName(t *testing.T) {
	// meta.name 优先。
	as := &appspecv1.AppSpec{Meta: &appspecv1.AppMeta{Name: "from-meta"}}
	tree, err := contractTree(as, &bootstrapv1.BootstrapConfig{})
	if err != nil || tree["app"].(map[string]any)["name"] != "from-meta" {
		t.Errorf("meta name: tree=%v err=%v", tree, err)
	}
	// meta.name 空 → 回退 bs.app.name。
	as2 := &appspecv1.AppSpec{Meta: &appspecv1.AppMeta{}}
	bs2 := &bootstrapv1.BootstrapConfig{App: &bootstrapv1.App{Name: "from-bs"}}
	tree2, err := contractTree(as2, bs2)
	if err != nil || tree2["app"].(map[string]any)["name"] != "from-bs" {
		t.Errorf("bs app name fallback: tree=%v err=%v", tree2, err)
	}
}

// TestContract_Gate2_RealMapField 验活：真实 BootstrapConfig 含 map<string,string>
// 字段（metrics.otlp.headers 等），声明后经完整 Gate2 必须正常派生而非 panic。
// 回归 messageToMap 误把 map entry 描述符当值描述符（标量值走 v.Message() 崩溃）。
func TestContract_Gate2_RealMapField(t *testing.T) {
	as := &appspecv1.AppSpec{Meta: &appspecv1.AppMeta{Name: "map-demo", Module: "example.com/map-demo"}}
	bs := &bootstrapv1.BootstrapConfig{
		Metrics: &bootstrapv1.Metrics{
			Otlp: &bootstrapv1.Metrics_Otlp{Headers: map[string]string{"authorization": "Bearer tok"}},
		},
	}
	out, err := contractGate(as, bs)
	if err != nil {
		t.Fatalf("Gate2 with real map<string,string> field must not panic/fail: %v", err)
	}
	if !strings.Contains(string(out), "authorization") || !strings.Contains(string(out), "Bearer tok") {
		t.Errorf("generated config must carry the map entries, got:\n%s", out)
	}
}
