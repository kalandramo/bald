package codegen

// gen_domain_proto_test.go 补 gen_domain_proto.go 的行级缺口：点式注解回退、
// 畸形 additional_bindings、扫描根跳过规则、service 与包派生的边界，以及
// domainValuePresent 的目录读取/文件筛选分支。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	appspecv1 "github.com/kalandramo/bald/bconf/gen/go/bald/appspec/v1"
)

// TestScanProto_DotAnnotation 点式注解（option (google.api.http).get = "..."）
// 是 braced 形态的等价写法，扫描必须同样识别出动词与路径。
func TestScanProto_DotAnnotation(t *testing.T) {
	src := `syntax = "proto3";

package greet.v1;

service GreetService {
  rpc Get(GetRequest) returns (GetResponse) {
    option (google.api.http).get = "/v1/greets/{id}";
  }
}
`
	f, err := scanProto("greet.proto", []byte(src))
	if err != nil {
		t.Fatalf("scanProto: %v", err)
	}
	m := f.Services[0].Methods[0]
	if m.Verb != "get" || m.Path != "/v1/greets/{id}" {
		t.Fatalf("dot annotation = %q %q, want get /v1/greets/{id}", m.Verb, m.Path)
	}
	if len(m.Rules) != 1 || m.Rules[0].Verb != "get" {
		t.Fatalf("dot annotation rules = %+v, want one get rule", m.Rules)
	}
}

// TestParseHTTPRules_MalformedAdditionalBindings 畸形注解块不能 panic：
// additional_bindings 缺 "{"、块未闭合时只保留已解析的主规则。
func TestParseHTTPRules_MalformedAdditionalBindings(t *testing.T) {
	cases := map[string]string{
		"missing brace":     `get: "/v1/a" additional_bindings get: "/v1/b"`,
		"unterminated body": `get: "/v1/a" additional_bindings { get: "/v1/b"`,
	}
	for name, block := range cases {
		t.Run(name, func(t *testing.T) {
			rules := parseHTTPRules(block)
			if len(rules) != 1 || rules[0].Path != "/v1/a" {
				t.Fatalf("rules = %+v, want only the primary rule", rules)
			}
		})
	}
}

// TestScanProtoRoots_SkipsGenAndNonProto gen/ 下的 pb 产物不是契约源，非 .proto
// 文件也不参与扫描；两者都被跳过。
func TestScanProtoRoots_SkipsGenAndNonProto(t *testing.T) {
	dir := t.TempDir()
	writeFile := func(rel, content string) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	writeFile("api/proto/greet/v1/greet.proto", "package greet.v1;\n")
	writeFile("api/proto/greet/v1/README.md", "not a proto")
	writeFile("api/proto/gen/ignored.proto", "package ignored.v1;\n")
	if err := os.Symlink(filepath.Join(dir, "absent.target"), filepath.Join(dir, "api", "proto", "dangling.proto")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	_, roots, err := scanProtoRoots(dir)
	if err == nil || !strings.Contains(err.Error(), "dangling.proto") {
		t.Fatalf("dangling .proto symlink must surface the read error, got: %v (roots %v)", err, roots)
	}

	// 去掉悬空链接后只剩真实契约：gen/ 与非 proto 文件都不入账。
	if err := os.Remove(filepath.Join(dir, "api", "proto", "dangling.proto")); err != nil {
		t.Fatalf("remove symlink: %v", err)
	}
	scan, roots, err := scanProtoRoots(dir)
	if err != nil {
		t.Fatalf("scanProtoRoots: %v", err)
	}
	if len(roots) != 1 || roots[0] != "api/proto" {
		t.Fatalf("roots = %v, want [api/proto]", roots)
	}
	if len(scan.Files) != 1 || scan.Files[0].Package != "greet.v1" {
		t.Fatalf("files = %+v, want only greet.proto", scan.Files)
	}
	if len(scan.Packages) != 1 || scan.Packages[0] != "greet.v1" {
		t.Fatalf("packages = %v, want [greet.v1]", scan.Packages)
	}
}

// TestScanProtoRoots_UnreadableDir 不可读子目录让扫描直接失败（半份结果不能
// 往下走）。root 下无权限差异，跳过。
func TestScanProtoRoots_UnreadableDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	locked := filepath.Join(dir, "api", "proto", "locked")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	if _, _, err := scanProtoRoots(dir); err == nil {
		t.Fatal("unreadable proto directory must fail the scan")
	}
}

// TestValidateDomainServices_ServiceRequired 非 http_only 域必须声明 service
// （http_only 域跳过该检查）。
func TestValidateDomainServices_ServiceRequired(t *testing.T) {
	as := &appspecv1.AppSpec{Domains: []*appspecv1.DomainSpec{
		{Name: "api"},
		{Name: "raw", HttpOnly: true},
	}}
	errs := validateDomainServices(as, &protoScan{}, nil)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "service is required") {
		t.Fatalf("errs = %v, want exactly the service-required error", errs)
	}
}

// TestValidateDomainServices_DerivesPackage proto_package 缺省时由 service 全名
// 反推包名（取最后一个 "." 之前）。无包名点分的 service 无法与扫描结果对齐，
// 报 not found 而不是静默通过。
func TestValidateDomainServices_DerivesPackage(t *testing.T) {
	scan := &protoScan{Files: []protoFile{
		{Path: "a.proto", Package: "greet.v1", Services: []protoService{{Package: "greet.v1", Name: "GreetService"}}},
		{Path: "solo.proto", Package: "Solo", Services: []protoService{{Package: "Solo", Name: "Solo"}}},
	}}
	as := &appspecv1.AppSpec{Domains: []*appspecv1.DomainSpec{
		{Name: "greet", Service: "greet.v1.GreetService"},
	}}
	if errs := validateDomainServices(as, scan, []string{"api/proto"}); len(errs) != 0 {
		t.Fatalf("derived package must match the scan, got: %v", errs)
	}

	as.Domains = append(as.Domains, &appspecv1.DomainSpec{Name: "solo", Service: "Solo"})
	errs := validateDomainServices(as, scan, []string{"api/proto"})
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), `service "Solo" not found`) {
		t.Fatalf("undotted service must be reported not-found, got: %v", errs)
	}
}

// TestDomainValuePresent_ErrorsAndEdges 目录读取失败上报；目录内非 .go / _test.go /
// 子目录一律跳过；有 .go 但未声明 Domain 视为「未声明」。
func TestDomainValuePresent_ErrorsAndEdges(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := domainValuePresent(filePath); err == nil {
		t.Fatal("a file path must surface the ReadDir error")
	}

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	for name, content := range map[string]string{
		"domain_test.go": "package x\n\nvar Domain = 1\n", // 测试文件不算声明
		"notes.txt":      "x",
		"other.go":       "package x\n\nvar Other = 1\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	present, err := domainValuePresent(dir)
	if err != nil {
		t.Fatalf("domainValuePresent: %v", err)
	}
	if present {
		t.Fatal("Domain declared only in a _test.go file must not count")
	}
}
