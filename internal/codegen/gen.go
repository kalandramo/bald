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
	"fmt"
	"go/format"
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

// NewCommand 组装 gen 中间节点：仅挂载叶子子命令，不承载领域逻辑。
func NewCommand(streams IOStreams) *cobra.Command {
	cmd := &cobra.Command{
		Use:              "gen [command]",
		Short:            "轻量代码生成脚手架（proto / store / app 骨架）",
		TraverseChildren: true,
		SilenceUsage:     true,
		RunE:             func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(genProtoCmd(streams), genStoreCmd(streams), genAppCmd(streams))
	return cmd
}

// protoOptions 是 gen proto 叶子命令的领域配置（flag 绑定字段，Complete 补默认值）。
// 注意：输出目录字段不可命名 Out——会遮蔽内嵌 IOStreams.Out (io.Writer)。
type protoOptions struct {
	Name      string // 位置参数：proto 服务名
	OutDir    string // --out
	GoPackage string // --go-package
	Force     bool   // --force：覆盖已存在文件（默认拒绝，防冲掉手改生成物）

	IOStreams
}

// Complete 回填位置参数与派生默认值（不做校验、不产生副作用）。
func (o *protoOptions) Complete(args []string) error {
	if len(args) > 0 {
		o.Name = args[0]
	}
	if o.OutDir == "" {
		o.OutDir = filepath.Join("api", "proto", "bald", o.Name, "v1")
	}
	return nil
}

// Validate 校验必填项（不产生副作用）。
func (o *protoOptions) Validate() error {
	if o.Name == "" {
		return fmt.Errorf("the proto name is required")
	}
	return nil
}

// Run 纯执行：输入已全部就绪，不再读取任何 flag。
func (o *protoOptions) Run() error {
	content, err := renderTmpl("proto", protoTmpl, map[string]string{"Name": o.Name, "GoPackage": o.GoPackage})
	if err != nil {
		return err
	}
	if err := writeFileGuarded(filepath.Join(o.OutDir, o.Name+".proto"), content, o.Force, o.IOStreams); err != nil {
		return err
	}
	// A2：buf 生态联动——bald 仓库的 proto 生成统一走 buf（api/proto/buf.gen.yaml，
	// `task proto` 触发），生成物需纳入 buf 模块后运行 `buf generate` 才有 Go 代码。
	fmt.Fprintln(o.Out, "next: add the file to your buf module, then run `buf generate` (bald repo: `task proto`) to produce Go code")
	return nil
}

func genProtoCmd(streams IOStreams) *cobra.Command {
	o := &protoOptions{IOStreams: streams}
	cmd := &cobra.Command{
		Use:          "proto <name>",
		Short:        "生成 protobuf 服务骨架",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(_ *cobra.Command, args []string) error {
			if err := o.Complete(args); err != nil {
				return err
			}
			if err := o.Validate(); err != nil {
				return err
			}
			return o.Run()
		},
	}
	cmd.Flags().StringVar(&o.OutDir, "out", "", "输出目录（默认 api/proto/bald/<name>/v1）")
	cmd.Flags().StringVar(&o.GoPackage, "go-package", "",
		"proto 的 go_package option（默认不写；bald 生态经 buf managed mode 补充）")
	cmd.Flags().BoolVar(&o.Force, "force", false, "覆盖已存在的目标文件（默认拒绝）")
	return cmd
}

// storeOptions 是 gen store 叶子命令的领域配置。
type storeOptions struct {
	Name   string // 位置参数：实体名
	OutDir string // --out（不可命名 Out，避免遮蔽 IOStreams.Out）
	Pkg    string // --out-pkg
	Force  bool   // --force

	IOStreams
}

func (o *storeOptions) Complete(args []string) error {
	if len(args) > 0 {
		o.Name = args[0]
	}
	if o.OutDir == "" {
		o.OutDir = "."
	}
	if o.Pkg == "" {
		o.Pkg = "main"
	}
	return nil
}

func (o *storeOptions) Validate() error {
	if o.Name == "" {
		return fmt.Errorf("the entity name is required")
	}
	return nil
}

// Run 纯执行：渲染 + gofmt（实体 .go 必须可编译）+ 防覆盖落盘。
func (o *storeOptions) Run() error {
	raw, err := renderTmpl("entity", entityTmpl, map[string]string{"Name": o.Name, "Pkg": o.Pkg})
	if err != nil {
		return err
	}
	formatted, err := format.Source(raw)
	if err != nil {
		return err
	}
	return writeFileGuarded(filepath.Join(o.OutDir, o.Name+".go"), formatted, o.Force, o.IOStreams)
}

func genStoreCmd(streams IOStreams) *cobra.Command {
	o := &storeOptions{IOStreams: streams}
	cmd := &cobra.Command{
		Use:          "store <name>",
		Short:        "生成实体骨架（gorm tag + keyOf）",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(_ *cobra.Command, args []string) error {
			if err := o.Complete(args); err != nil {
				return err
			}
			if err := o.Validate(); err != nil {
				return err
			}
			return o.Run()
		},
	}
	cmd.Flags().StringVar(&o.OutDir, "out", "", "输出目录（默认当前目录）")
	cmd.Flags().StringVar(&o.Pkg, "out-pkg", "", "生成文件的 package（默认 main）")
	cmd.Flags().BoolVar(&o.Force, "force", false, "覆盖已存在的目标文件（默认拒绝）")
	return cmd
}

// renderTmpl 渲染模板并返回字节（落盘统一走 writeFileGuarded，不再内联写文件）。
func renderTmpl(name, tmpl string, data any) ([]byte, error) {
	t := template.Must(template.New(name).Funcs(template.FuncMap{"title": title}).Parse(tmpl))
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
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
