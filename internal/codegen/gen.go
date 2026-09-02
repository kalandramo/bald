// Package codegen 是 bald CLI（cmd/bald）的轻量代码生成脚手架（对照 osbuilder 的嵌入模板范式）。
//
// 提供三个子命令，演示「配置驱动 + 嵌入模板」生成骨架：
//   - bald gen proto  <name>  生成 api/proto/bald/<name>/v1/<name>.proto（含 PagingRequest 引用）
//   - bald gen store  <name>  生成 <name>.go 实体骨架（gorm tag + keyOf 提取函数）
//   - bald gen app    <name>  生成 cmd/<name>/main.go 应用装配骨架（P12：appkit 全原语 + bundle）
//
// 工具链归属（2026-09-02）：自 _example/bald 示例模块提升为核心 cmd/bald 子命令
// （P12 落地记录标注的「工具链归属」评估落地）。生成物以 _example/bald 消费者模块为
// 编译/运行上下文做端到端验证（见 app_test.go），核心 go.mod 不引入 gin/grpc。
package codegen

import (
	"bytes"
	"go/format"
	"os"
	"path/filepath"
	"text/template"

	"github.com/spf13/cobra"
)

// protoTmpl 是 protobuf 服务骨架模板（Package 用 bald.<name>.v1）。
var protoTmpl = `syntax = "proto3";

package bald.{{.Name}}.v1;

import "google/protobuf/empty.proto";
import "bald/store/v1/store.proto";

{{- if .GoPackage }}
option go_package = "{{.GoPackage}}";
{{- end }}

// {{.Name | title}}Service 示例服务。
service {{.Name | title}}Service {
  rpc List({{.Name | title}}ListRequest) returns ({{.Name | title}}ListResponse);
  rpc Get({{.Name | title}}GetRequest) returns ({{.Name | title}});
}

message {{.Name | title}} {
  string id = 1;
  string name = 2;
}

message {{.Name | title}}ListRequest {
  bald.store.v1.PagingRequest paging = 1;
}

message {{.Name | title}}ListResponse {
  repeated {{.Name | title}} items = 1;
  bald.store.v1.PaginationResponseMeta meta = 2;
}

message {{.Name | title}}GetRequest {
  string id = 1;
}
`

// entityTmpl 是实体骨架模板（含 gorm tag + keyOf 函数）。
var entityTmpl = `package {{.Pkg}}

// {{.Name | title}} 是 {{.Name}} 业务实体，同时充当 GORM 模型。
type {{.Name | title}} struct {
	ID   string ` + "`gorm:\"primaryKey\" json:\"id\"`" + `
	Name string ` + "`json:\"name\"`" + `
}

// KeyOf 提取主键，供存储 Provider 唯一定位实体。
func KeyOf{{.Name | title}}(u *{{.Name | title}}) string { return u.ID }
`

func NewCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gen",
		Short: "轻量代码生成脚手架（proto / store / app 骨架）",
	}
	cmd.AddCommand(genProtoCmd(), genStoreCmd(), genAppCmd())
	return cmd
}

func genProtoCmd() *cobra.Command {
	var out string
	var goPackage string
	cmd := &cobra.Command{
		Use:   "proto <name>",
		Short: "生成 protobuf 服务骨架",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			name := args[0]
			if out == "" {
				out = filepath.Join("api", "proto", "bald", name, "v1")
			}
			if err := render(protoTmpl, map[string]string{"Name": name, "GoPackage": goPackage},
				filepath.Join(out, name+".proto")); err != nil {
				return err
			}
			println("generated proto:", filepath.Join(out, name+".proto"))
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "输出目录（默认 api/proto/bald/<name>/v1）")
	cmd.Flags().StringVar(&goPackage, "go-package", "",
		"proto 的 go_package option（默认不写；bald 生态经 buf managed mode 补充）")
	return cmd
}

func genStoreCmd() *cobra.Command {
	var out string
	var pkg string
	cmd := &cobra.Command{
		Use:   "store <name>",
		Short: "生成实体骨架（gorm tag + keyOf）",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			name := args[0]
			if out == "" {
				out = "."
			}
			if pkg == "" {
				pkg = "main"
			}
			// 实体 .go 还需 gofmt 格式化，确保产出可编译。
			var buf bytes.Buffer
			t := template.Must(template.New("entity").Funcs(template.FuncMap{
				"title": title,
			}).Parse(entityTmpl))
			if err := t.Execute(&buf, map[string]string{"Name": name, "Pkg": pkg}); err != nil {
				return err
			}
			formatted, err := format.Source(buf.Bytes())
			if err != nil {
				return err
			}
			if err := os.MkdirAll(out, 0o755); err != nil {
				return err
			}
			path := filepath.Join(out, name+".go")
			if err := os.WriteFile(path, formatted, 0o644); err != nil {
				return err
			}
			println("generated store entity:", path)
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "输出目录（默认当前目录）")
	cmd.Flags().StringVar(&pkg, "out-pkg", "", "生成文件的 package（默认 main）")
	return cmd
}

func render(tmpl string, data map[string]string, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	t := template.Must(template.New("t").Funcs(template.FuncMap{"title": title}).Parse(tmpl))
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

func title(s string) string {
	if s == "" {
		return s
	}
	b := []byte(s)
	if b[0] >= 'a' && b[0] <= 'z' {
		b[0] -= 'a' - 'A'
	}
	return string(b)
}
