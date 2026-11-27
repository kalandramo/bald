package codegen

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/reflect/protoreflect"

	appspecv1 "github.com/kalandramo/bald/bconf/gen/go/bald/appspec/v1"
)

// gen_schema.go 实现 DX-4：从 AppSpec 的 protobuf 描述符派生 JSON Schema，
// 给 IDE 补全与编辑期校验用。真相源仍是 proto——schema 是它的只读投影，
// 不复制字段清单（复制即双源漂移，违反 J2）。Gate1/Gate2 仍负责语义校验，
// schema 只管「拼写/类型/层级」这一层，二者互补不互相替代。

type schemaOptions struct {
	OutFile string // --out：写文件；缺省打到 stdout（不命名 Out——会遮蔽 ioStreams.Out writer）
	Indent  bool   // --indent：缩进输出（默认 true，便于人读与入库）
	Force   bool   // --force：覆盖已存在的 schema 文件
	ioStreams
}

func genSchemaCmd(streams ioStreams) *cobra.Command {
	o := &schemaOptions{ioStreams: streams, Indent: true}
	cmd := &cobra.Command{
		Use:   "schema",
		Short: "Derive AppSpec JSON Schema from the proto descriptor (read-only, DX-4)",
		Long: `从 AppSpec 的 protobuf 描述符派生 JSON Schema 草稿 2020-12。

字段集合、类型与嵌套层级由 protoreflect 现场读取，不维护第二份字段清单。
输出用于 IDE 补全与编辑期校验；语义约束（能力依赖、域声明三选一、name 形状）
由 gen app 的 Gate1/Gate2 负责，schema 不重复表达这些规则。`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return o.run() },
	}
	cmd.Flags().StringVar(&o.OutFile, "out", "", "write schema to file (default: stdout)")
	cmd.Flags().BoolVar(&o.Indent, "indent", true, "indent JSON output")
	cmd.Flags().BoolVar(&o.Force, "force", false, "overwrite an existing schema file")
	return cmd
}

func (o *schemaOptions) run() error {
	doc := schemaForMessage((&appspecv1.AppSpec{}).ProtoReflect().Descriptor(), map[string]bool{}, 0)
	doc["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	doc["title"] = "bald AppSpec"
	doc["description"] = "bald 应用装配声明；由 proto 描述符派生（bald gen schema），不要手改本文件"

	var raw []byte
	var err error
	if o.Indent {
		raw, err = json.MarshalIndent(doc, "", "  ")
	} else {
		raw, err = json.Marshal(doc)
	}
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if o.OutFile != "" {
		if err := writeGuarded(o.OutFile, raw, owned, o.Force, o.ioStreams); err != nil {
			return fmt.Errorf("write %s: %w", o.OutFile, err)
		}
		return nil
	}
	_, err = o.Out.Write(raw)
	return err
}

// schemaForMessage 把一条 proto message 描述符递归折算成 JSON Schema object。
// 键用 json_name（protojson 的输入形态）；encoding/json 对 map 键排序，
// 输出字节稳定。stack 是当前递归路径上的消息名——proto 允许自引用，遇到
// 环时退化为通用 object（不产生悬空 $ref，也不无限展开）。
func schemaForMessage(md protoreflect.MessageDescriptor, stack map[string]bool, depth int) map[string]any {
	key := string(md.FullName())
	if stack[key] {
		return map[string]any{"type": "object"}
	}
	stack[key] = true
	defer delete(stack, key)
	props := map[string]any{}
	fields := md.Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		props[fd.JSONName()] = schemaForField(fd, stack, depth)
	}
	// required：proto3 无 required 语义，语义必填（meta.name/module）由 Gate1
	// 校验——schema 里不标 required，避免与 protojson 的 presence 规则打架。
	return map[string]any{
		"type":                 "object",
		"properties":           props,
		"additionalProperties": false,
	}
}

func schemaForField(fd protoreflect.FieldDescriptor, stack map[string]bool, depth int) map[string]any {
	var s map[string]any
	switch {
	case fd.IsMap():
		s = map[string]any{"type": "object", "additionalProperties": schemaForValue(fd.MapValue(), stack, depth)}
	case fd.IsList():
		s = map[string]any{"type": "array", "items": schemaForValue(fd, stack, depth)}
	default:
		s = schemaForValue(fd, stack, depth)
	}
	return s
}

func schemaForValue(fd protoreflect.FieldDescriptor, stack map[string]bool, depth int) map[string]any {
	switch fd.Kind() {
	case protoreflect.MessageKind, protoreflect.GroupKind:
		md := fd.Message()
		full := string(md.FullName())
		if wellKnown := wellKnownSchema(full); wellKnown != nil {
			return wellKnown
		}
		if depth >= 32 { // 防御性上限：真实 AppSpec 树远浅于此
			return map[string]any{"type": "object"}
		}
		return schemaForMessage(md, stack, depth+1)
	case protoreflect.EnumKind:
		var names []string
		vals := fd.Enum().Values()
		for i := 0; i < vals.Len(); i++ {
			names = append(names, string(vals.Get(i).Name()))
		}
		return map[string]any{"type": "string", "enum": names}
	case protoreflect.StringKind, protoreflect.BytesKind:
		return map[string]any{"type": "string"}
	case protoreflect.BoolKind:
		return map[string]any{"type": "boolean"}
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Uint32Kind, protoreflect.Fixed32Kind,
		protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind,
		protoreflect.Uint64Kind, protoreflect.Fixed64Kind,
		protoreflect.FloatKind, protoreflect.DoubleKind:
		// protojson 把 64 位整数字符串化，但这里描述的是**输入**形态：
		// protojson 接受数字与字符串两种写法，故 number|string 并列。
		return map[string]any{"type": []string{"number", "string"}}
	default:
		return map[string]any{}
	}
}

// wellKnownSchema 处理 protojson 有特殊 JSON 表示的内建类型。
func wellKnownSchema(fullName string) map[string]any {
	switch fullName {
	case "google.protobuf.Timestamp":
		return map[string]any{"type": "string", "format": "date-time"}
	case "google.protobuf.Duration":
		return map[string]any{"type": "string", "pattern": `^-?[0-9]+(?:\.[0-9]+)?s$`}
	case "google.protobuf.Struct":
		return map[string]any{"type": "object"}
	case "google.protobuf.Value", "google.protobuf.Any":
		return map[string]any{}
	case "google.protobuf.Empty":
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return nil
}
