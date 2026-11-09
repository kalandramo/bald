package codegen

import (
	"strings"
	"testing"

	appspecv1 "github.com/kalandramo/bald/bconf/gen/go/bald/appspec/v1"
)

// 本文件补齐 Gate1（validateAppSpec）剩余未覆盖的拒绝分支：域/服务/角色/
// 变体组件/变体补丁的形状校验，以及 projectServer 的 nil 输入。全部纯逻辑，
// 从 guardFixture 变异构造，无需文件系统。

// TestGate1_DomainNameRequired 域名为空的拒绝（appspec_validate.go:129）。
func TestGate1_DomainNameRequired(t *testing.T) {
	as := guardFixture()
	as.Domains[0].Name = ""
	if msg := joinErrs(validateAppSpec(as)); !strings.Contains(msg, "domains[0].name is required") {
		t.Errorf("empty domain name not rejected: %s", msg)
	}
}

// TestGate1_ServiceSurroundingWhitespace 服务名首尾空白的拒绝（appspec_validate.go:162）。
func TestGate1_ServiceSurroundingWhitespace(t *testing.T) {
	as := guardFixture()
	as.Domains[0].Service = ""
	as.Domains[0].Services = []string{" greet.v1.GreetService "}
	if msg := joinErrs(validateAppSpec(as)); !strings.Contains(msg, "surrounding whitespace") {
		t.Errorf("whitespace service not rejected: %s", msg)
	}
}

// TestGate1_RoleNameRequired 角色名为空的拒绝（appspec_validate.go:201）。角色
// 声明了域 greet 使并集仍等于全集，只剩空名一条错。
func TestGate1_RoleNameRequired(t *testing.T) {
	as := guardFixture()
	as.Meta.Roles = []*appspecv1.RoleSpec{{Name: "", Domains: []string{"greet"}}}
	if msg := joinErrs(validateAppSpec(as)); !strings.Contains(msg, "meta.roles[0].name 不能为空") {
		t.Errorf("empty role name not rejected: %s", msg)
	}
}

// TestGate1_VariantComponentKindWhitelist 变体组件 kind 白名单（appspec_validate.go:294）。
func TestGate1_VariantComponentKindWhitelist(t *testing.T) {
	as := guardFixture()
	as.Variants = []*appspecv1.Variant{{
		Name: "v1", BuildTags: []string{"v1"},
		Effects: &appspecv1.VariantEffect{Components: []*appspecv1.ComponentSpec{{Kind: "db", Name: "v1.db"}}},
	}}
	if msg := joinErrs(validateAppSpec(as)); !strings.Contains(msg, "is not supported") {
		t.Errorf("variant unknown kind not rejected: %s", msg)
	}
}

// TestGate1_VariantComponentNameRequired 变体组件名为空的拒绝（appspec_validate.go:299）。
func TestGate1_VariantComponentNameRequired(t *testing.T) {
	as := guardFixture()
	as.Variants = []*appspecv1.Variant{{
		Name: "v1", BuildTags: []string{"v1"},
		Effects: &appspecv1.VariantEffect{Components: []*appspecv1.ComponentSpec{{Kind: "heartbeat", Name: ""}}},
	}}
	if msg := joinErrs(validateAppSpec(as)); !strings.Contains(msg, "name is required (component") {
		t.Errorf("variant component empty name not rejected: %s", msg)
	}
}

// TestGate1_VariantComponentDuplicatesGlobal 变体组件名与全局组件同名（appspec_validate.go:301）。
func TestGate1_VariantComponentDuplicatesGlobal(t *testing.T) {
	as := guardFixture() // 全局组件 demo.hb
	as.Variants = []*appspecv1.Variant{{
		Name: "v1", BuildTags: []string{"v1"},
		Effects: &appspecv1.VariantEffect{Components: []*appspecv1.ComponentSpec{{Kind: "heartbeat", Name: "demo.hb"}}},
	}}
	if msg := joinErrs(validateAppSpec(as)); !strings.Contains(msg, "duplicates a components[].name") {
		t.Errorf("variant component dup global not rejected: %s", msg)
	}
}

// TestGate1_VariantComponentDuplicatesAcrossVariants 变体组件名跨变体重复
// （appspec_validate.go:304）。
func TestGate1_VariantComponentDuplicatesAcrossVariants(t *testing.T) {
	as := guardFixture()
	as.Variants = []*appspecv1.Variant{
		{Name: "v1", BuildTags: []string{"v1"}, Effects: &appspecv1.VariantEffect{Components: []*appspecv1.ComponentSpec{{Kind: "heartbeat", Name: "shared"}}}},
		{Name: "v2", BuildTags: []string{"v2"}, Effects: &appspecv1.VariantEffect{Components: []*appspecv1.ComponentSpec{{Kind: "heartbeat", Name: "shared"}}}},
	}
	if msg := joinErrs(validateAppSpec(as)); !strings.Contains(msg, "duplicates variants[0]") {
		t.Errorf("variant component dup across variants not rejected: %s", msg)
	}
}

// TestGate1_VariantPatchPathShape 补丁路径三态拒绝：空、首尾空白、多行
// （appspec_validate.go:317/319/321）。
func TestGate1_VariantPatchPathShape(t *testing.T) {
	cases := []struct{ name, path, want string }{
		{"empty", "", ".path is required"},
		{"whitespace", " app.env ", "surrounding whitespace"},
		{"multiline", "app\nenv", "single-line field path"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			as := guardFixture()
			as.Variants = []*appspecv1.Variant{{
				Name: "v1", BuildTags: []string{"v1"},
				Effects: &appspecv1.VariantEffect{Patches: []*appspecv1.VariantPatch{{Path: c.path, Value: "x"}}},
			}}
			if msg := joinErrs(validateAppSpec(as)); !strings.Contains(msg, c.want) {
				t.Errorf("patch path %q: want %q, got: %s", c.path, c.want, msg)
			}
		})
	}
}

// TestProjectServer_NilSpec 未声明传输面 ⇒ nil（appspec_validate.go:339）。
func TestProjectServer_NilSpec(t *testing.T) {
	if out := projectServer(nil, nil); out != nil {
		t.Errorf("projectServer(nil) = %+v, want nil", out)
	}
}

// 结构性不可达分支（如实留空）：appspec_validate.go:497/498 是 checkFieldConsumers
// 遍历非空 repeated 本地消息的分支；但 walk 的入口恒以空的 &appspecv1.AppSpec{}
// （appspec_validate.go:518）起遍历——守卫管的是「声明面本身」而非某个实例，
// 递归下钻的每个消息也恒为空（空列表走 494 的空元素下钻），故 list.Len()>0
// 永不成立。这是防御性死码，非覆盖率缺口。
