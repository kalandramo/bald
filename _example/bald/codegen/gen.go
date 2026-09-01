// Package codegen 是 bald 的轻量代码生成脚手架（对照 osbuilder 的嵌入模板范式）。
//
// 提供三个子命令，演示「配置驱动 + 嵌入模板」生成骨架：
//   - gen proto  <name>  生成 api/proto/bald/<name>/v1/<name>.proto（含 PagingRequest 引用）
//   - gen store  <name>  生成 <name>.go 实体骨架（gorm tag + keyOf 提取函数）
//   - gen app    <name>  生成 cmd/<name>/main.go 应用装配骨架（P12：appkit 全原语 + bundle）
//
// 这是 P4 工程化的最小可用集：不一次性铺开完整脚手架，仅验证「框架可生成 starter 骨架」的能力。
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

option go_package = "github.com/kalandramo/bald/example/bald/api/gen/go/bald/{{.Name}}/v1;{{.Name}}v1";

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
	cmd := &cobra.Command{
		Use:   "proto <name>",
		Short: "生成 protobuf 服务骨架",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			name := args[0]
			if out == "" {
				out = filepath.Join("api", "proto", "bald", name, "v1")
			}
			if err := render(protoTmpl, map[string]string{"Name": name},
				filepath.Join(out, name+".proto")); err != nil {
				return err
			}
			println("generated proto:", filepath.Join(out, name+".proto"))
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "输出目录（默认 api/proto/bald/<name>/v1）")
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
