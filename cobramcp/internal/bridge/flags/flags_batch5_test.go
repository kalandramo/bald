package flags

import (
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// fakeVal 只用于驱动 AddFlagToSchema 的 Type() 路由：switch 只看类型串，不触真实语义。
type fakeVal struct {
	t, def string
}

func (v fakeVal) String() string   { return v.def }
func (v fakeVal) Set(string) error { return nil }
func (v fakeVal) Type() string     { return v.t }

func newSchema() *jsonschema.Schema {
	return &jsonschema.Schema{Properties: map[string]*jsonschema.Schema{}}
}

func TestIsFlagRequired(t *testing.T) {
	cases := []struct {
		name string
		ann  map[string][]string
		want bool
	}{
		{"nil", nil, false},
		{"required-true", map[string][]string{cobra.BashCompOneRequiredFlag: {"true"}}, true},
		{"required-false", map[string][]string{cobra.BashCompOneRequiredFlag: {"false"}}, false},
		{"required-empty", map[string][]string{cobra.BashCompOneRequiredFlag: {}}, false},
		{"other-annotation", map[string][]string{"something": {"true"}}, false},
	}
	for _, c := range cases {
		f := &pflag.Flag{Name: "x", Annotations: c.ann}
		if got := isFlagRequired(f); got != c.want {
			t.Errorf("%s: isFlagRequired=%v want %v", c.name, got, c.want)
		}
	}
}

func TestAddFlagToSchema_Types(t *testing.T) {
	cases := []struct {
		typ  string
		want string // 期望 flagSchema.Type
	}{
		{"bool", "boolean"},
		{"int", "integer"},
		{"int64", "integer"},
		{"uint", "integer"},
		{"count", "integer"},
		{"float32", "number"},
		{"float64", "number"},
		{"stringSlice", "array"},
		{"stringArray", "array"},
		{"intSlice", "array"},
		{"int64Slice", "array"},
		{"uintSlice", "array"},
		{"float64Slice", "array"},
		{"boolSlice", "array"},
		{"stringToString", "object"},
		{"stringToInt", "object"},
		{"stringToInt64", "object"},
		{"duration", "string"},
		{"ip", "string"},
		{"ipMask", "string"},
		{"ipNet", "string"},
		{"bytesHex", "string"},
		{"bytesBase64", "string"},
		{"totallyUnknown", "string"}, // default 分支
	}
	for _, c := range cases {
		s := newSchema()
		AddFlagToSchema(s, &pflag.Flag{Name: "f", Usage: "u", Value: fakeVal{t: c.typ}})
		got := s.Properties["f"]
		if got.Type != c.want {
			t.Errorf("type %q → schema.Type=%q want %q", c.typ, got.Type, c.want)
		}
	}
}

func TestAddFlagToSchema_StringAnnotationBranches(t *testing.T) {
	// 1) Annotations 为 nil → string（else 分支）。
	s := newSchema()
	AddFlagToSchema(s, &pflag.Flag{Name: "a", Value: fakeVal{t: "string"}})
	if s.Properties["a"].Type != "string" {
		t.Error("无标注的 string flag 应为 string")
	}

	// 2) jsonschema 标注存在但值为空数组 → warn 后仍 string。
	s = newSchema()
	AddFlagToSchema(s, &pflag.Flag{
		Name:        "b",
		Value:       fakeVal{t: "string"},
		Annotations: map[string][]string{"jsonschema": {}},
	})
	if s.Properties["b"].Type != "string" {
		t.Error("空 jsonschema 标注应降级为 string")
	}

	// 3) jsonschema 标注非法 JSON → error 后仍 string。
	s = newSchema()
	AddFlagToSchema(s, &pflag.Flag{
		Name:        "c",
		Value:       fakeVal{t: "string"},
		Annotations: map[string][]string{"jsonschema": {"{not-json"}},
	})
	if s.Properties["c"].Type != "string" {
		t.Error("非法 jsonschema 标注应降级为 string")
	}

	// 4) jsonschema 标注合法 → 整体替换为该 schema（此处声明 integer）。
	s = newSchema()
	AddFlagToSchema(s, &pflag.Flag{
		Name:        "d",
		Value:       fakeVal{t: "string"},
		Annotations: map[string][]string{"jsonschema": {`{"type":"integer","enum":[1,2]}`}},
	})
	d := s.Properties["d"]
	if d.Type != "integer" || len(d.Enum) == 0 {
		t.Errorf("合法 jsonschema 标注应替换为声明的整数枚举，得到 type=%q enum=%v", d.Type, d.Enum)
	}

	// 5) 有标注但无 jsonschema 键（如 required 标注）→ Debug 分支，仍 string。
	s = newSchema()
	AddFlagToSchema(s, &pflag.Flag{
		Name:        "e",
		Value:       fakeVal{t: "string"},
		Annotations: map[string][]string{cobra.BashCompOneRequiredFlag: {"true"}},
	})
	if s.Properties["e"].Type != "string" {
		t.Error("非 jsonschema 标注的 string flag 应为 string")
	}
	// 该 flag 同时应被登记进 required 列表（覆盖 isFlagRequired 命中 + Required 追加）。
	found := false
	for _, r := range s.Required {
		if r == "e" {
			found = true
		}
	}
	if !found {
		t.Error("required flag 应进入 schema.Required")
	}
}

func TestAddFlagToSchema_ObjectDefault(t *testing.T) {
	// stringToString 且有默认值 → setDefaultFromFlag 走 object 分支 → parseObject 解析。
	s := newSchema()
	AddFlagToSchema(s, &pflag.Flag{
		Name:     "m",
		Usage:    "map",
		Value:    fakeVal{t: "stringToString", def: "[a=1]"},
		DefValue: "[a=1]",
	})
	if s.Properties["m"].Default == nil {
		t.Error("object 默认值应被解析并写入 schema.Default")
	}
}

func TestAddFlagToSchema_MalformedObjectNoDefault(t *testing.T) {
	// 回归 #39：全非法的 object 默认不得装箱成类型化 nil map 泄漏过 obj!=nil，
	// 否则会给 object 型 schema 写出 `default: null`（与其声明类型自相矛盾）。
	s := newSchema()
	AddFlagToSchema(s, &pflag.Flag{
		Name:     "m",
		Usage:    "map",
		Value:    fakeVal{t: "stringToInt", def: "[a=xyz]"},
		DefValue: "[a=xyz]",
	})
	if d := s.Properties["m"].Default; d != nil {
		t.Errorf("非法 object 默认不应落账, 得到 %s", string(d))
	}
}

func TestParseObject(t *testing.T) {
	intProp := &jsonschema.Schema{Type: "integer"}
	strProp := &jsonschema.Schema{Type: "string"}

	if got := parseObject("[]", intProp); got != nil {
		t.Errorf("空数组应返回 nil, got %v", got)
	}
	if got := parseObject("notarray", intProp); got != nil {
		t.Errorf("缺方括号应返回 nil, got %v", got)
	}
	if got := parseObject("[a=1,b=2]", intProp); got == nil {
		t.Error("整数对象默认应解析成功")
	}
	if got := parseObject("[a=x]", strProp); got == nil {
		t.Error("字符串对象默认应解析成功")
	}
	// 字符串对象全非法（无 '='）→ 折叠为 nil（覆盖 string 空结果分支）。
	if got := parseObject("[foo,bar]", strProp); got != nil {
		t.Errorf("全非法字符串对象应折叠为 nil, got %#v", got)
	}
	// 未知条目类型 → warn 分支返回 nil。
	if got := parseObject("[a=1]", &jsonschema.Schema{Type: "boolean"}); got != nil {
		t.Errorf("不支持的条目类型应返回 nil, got %v", got)
	}
	// parseIntObj 非法整数：全部跳过 → 空 map。parseObject 现将其折叠为未导出 nil 接口
	// （修复类型化 nil map 泄漏：否则 setDefaultFromFlag 会给 object 写出 default:null）。
	if got := parseObject("[a=xyz]", intProp); got != nil {
		t.Errorf("非法整数默认应折叠为 nil, got %#v", got)
	}
	// 混合有效/无效：有至少一项有效才返回非 nil map。
	if got := parseObject("[a=1,b=bad]", intProp); got == nil {
		t.Error("部分有效应返回非 nil map")
	}
	// parseStringObj 空片段跳过（覆盖 TrimSpace 空 key 分支）。
	if got := parseObject("[a=,b=x]", strProp); got == nil {
		t.Error("字符串对象应解析出至少一项")
	} else if m, ok := got.(map[string]string); ok && m["a"] != "" {
		t.Errorf("空字符串值应保留空串: %v", m)
	}
}

func TestParseIntObj_MixedValidInvalid(t *testing.T) {
	// 混合有效/无效：有效项保留，无效项跳过——结果非 nil（部分成功）。
	got := parseIntObj(strings.Split("a=1,b=bad", ","))
	if got == nil {
		t.Fatal("部分有效应返回非 nil map")
	}
	if got["a"] != 1 {
		t.Errorf("有效项 a 应=1, got %d", got["a"])
	}
	if _, ok := got["b"]; ok {
		t.Error("无效项 b 应被跳过")
	}
}
