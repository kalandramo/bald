// Package codegen 是 bald CLI（cmd/bald）的轻量代码生成脚手架（对照 osbuilder 的嵌入模板范式）。
//
// 提供四个子命令，演示「配置驱动 + 嵌入模板」生成骨架：
//   - bald gen proto   <name>  生成 api/proto/<name>/v1/<name>.proto（CRUD 六件套：
//                              google.api.http 注解 + buf.validate 规则 + FilterExpr）
//   - bald gen store   <name>  生成 <name>.go 实体骨架（gorm tag + keyOf 提取函数）
//   - bald gen app     <name>  生成 cmd/<name>/main.go 应用装配骨架（appkit 全原语 + bundle）
//   - bald gen domain  --spec  按 AppSpec.domains 重写域驱动生成物（批 3 W3b，
//                              见 gen_domain.go——清单×2 + 逐域 handler + 授权别名）
//
// 工具链归属（2026-09-02）：自 _example/bald 示例模块提升为核心 cmd/bald 子命令
// （落地记录标注的「工具链归属」评估落地）。生成物以 _example/bald 消费者模块为
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

// protoTmpl 是 protobuf 服务骨架模板（裁决 7 的 CRUD 六件套：service +
// List/Get/Create/Update/Delete，其上叠 google.api.http 注解（REST 映射）、
// buf.validate 规则（字段校验）与 bald.store.v1.FilterExpr（结构化过滤），
// 装配层评估 §4.6）。package 用 <name>.v1（与 AppSpec.domains[].proto_package
// 的声明口径一致，gen domain 的 service 校验以此对账）；REST 资源段默认
// <name>s，可经 --plural 覆盖（bald-admin 口径：/v1/org-units）。
//
// 单模板源纪律：本模板是全仓唯一的 proto 模板——gen app 不生成 proto（只落
// buf 扇出配置），--spec 与 CRUD 模式无双轨。注解依赖（googleapis/
// protovalidate/bald store）经 buf 模块 deps 解析（对照 _example/bald/proto
// 的 buf.yaml），Go 侧 genproto 由业务 module 按需 require（官方包提供，
// buf managed mode 的 except 口径）。
var protoTmpl = `syntax = "proto3";

package {{.Name}}.v1;

import "google/api/annotations.proto";
import "google/protobuf/empty.proto";
import "buf/validate/validate.proto";
import "bald/store/v1/store.proto";

{{- if .GoPackage }}
option go_package = "{{.GoPackage}}";
{{- end }}

// {{.Name | title}}Service 是 {{.Name}} 域的 CRUD 六件套（bald gen proto 生成）。
// google.api.http 注解驱动 REST 面与网关转码（同一个 RPC 同时暴露 gRPC 与
// REST/JSON，业务不写两遍）；授权策略与路由表由 gen domain 从本契约派生。
service {{.Name | title}}Service {
  // List 分页查询（paging 三态分页 + filter_expr 结构化过滤，服务端按白名单
  // 翻译为查询——bald.store.v1 安全规约）。
  rpc List({{.Name | title}}ListRequest) returns ({{.Name | title}}ListResponse) {
    option (google.api.http) = {
      get: "/v1/{{.Plural}}"
    };
  }

  // Get 按 id 取单条。
  rpc Get({{.Name | title}}GetRequest) returns ({{.Name | title}}) {
    option (google.api.http) = {
      get: "/v1/{{.Plural}}/{id}"
    };
  }

  // Create 新建（body = 整个资源；AIP-133）。
  rpc Create({{.Name | title}}CreateRequest) returns ({{.Name | title}}) {
    option (google.api.http) = {
      post: "/v1/{{.Plural}}"
      body: "*"
    };
  }

  // Update 更新（body = 整个资源；AIP-134）。
  rpc Update({{.Name | title}}UpdateRequest) returns ({{.Name | title}}) {
    option (google.api.http) = {
      put: "/v1/{{.Plural}}/{id}"
      body: "*"
    };
  }

  // Delete 删除。
  rpc Delete({{.Name | title}}DeleteRequest) returns (google.protobuf.Empty) {
    option (google.api.http) = {
      delete: "/v1/{{.Plural}}/{id}"
    };
  }
}

// {{.Name | title}} 是 {{.Name}} 业务资源。buf.validate 注解是契约的一部分：
// 前后端、gRPC/REST 多入口共享同一份规则，由 protovalidate 运行时读取执行
// （无需代码生成插件）。注解表达不了的复杂逻辑（查库、外部状态比对）才
// 手写 Go——同一规则禁止两处各写一遍。
message {{.Name | title}} {
  // id 资源标识（Create 时留空由服务端分配）。
  string id = 1 [(buf.validate.field).string = {min_len: 1, max_len: 63}];

  // name 展示名。
  string name = 2 [(buf.validate.field).string = {min_len: 1, max_len: 128}];
}

message {{.Name | title}}ListRequest {
  // paging 分页参数（页码/偏移/游标三态共存，服务端按优先级择一）。
  bald.store.v1.PagingRequest paging = 1;

  // filter_expr 结构化过滤表达式（AND/OR 布尔代数，可嵌套；优先于字符串 DSL，
  // 服务端须严格校验与参数化——bald.store.v1 安全规约 4）。
  bald.store.v1.FilterExpr filter_expr = 2;
}

message {{.Name | title}}ListResponse {
  repeated {{.Name | title}} items = 1;
  bald.store.v1.PaginationResponseMeta meta = 2;
}

message {{.Name | title}}GetRequest {
  string id = 1 [(buf.validate.field).string = {min_len: 1}];
}

message {{.Name | title}}CreateRequest {
  // {{.Name}} 待建资源（id 留空由服务端分配）。
  {{.Name | title}} {{.Name}} = 1;
}

message {{.Name | title}}UpdateRequest {
  // id 目标资源标识（REST 路径变量）。
  string id = 1 [(buf.validate.field).string = {min_len: 1}];

  // {{.Name}} 更新后的资源。
  {{.Name | title}} {{.Name}} = 2;
}

message {{.Name | title}}DeleteRequest {
  string id = 1 [(buf.validate.field).string = {min_len: 1}];
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
		Short:            "轻量代码生成脚手架（proto / store / app / domain）",
		TraverseChildren: true,
		SilenceUsage:     true,
		RunE:             func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(genProtoCmd(streams), genStoreCmd(streams), genAppCmd(streams), genDomainCmd(streams))
	// cobra 的 "Error: ..." 打到 ErrOrStderr——与 IOStreams 解耦；接上后错误
	// 随 streams.ErrOut 走（生产 = os.Stderr 行为不变；测试注入弃流即不把
	// 预期负例错误漏进 CI 日志）。
	cmd.SetErr(streams.ErrOut)
	return cmd
}

// protoOptions 是 gen proto 叶子命令的领域配置（flag 绑定字段，Complete 补默认值）。
// 注意：输出目录字段不可命名 Out——会遮蔽内嵌 IOStreams.Out (io.Writer)。
type protoOptions struct {
	Name      string // 位置参数：proto 服务名
	Plural    string // --plural：REST 资源段（默认 name+s）
	OutDir    string // --out
	GoPackage string // --go-package
	Module    string // --module：业务 module 路径（裁决 7 落地消费——go_package 缺省派生源）
	Force     bool   // --force：覆盖已存在文件（默认拒绝，防冲掉手改生成物）

	IOStreams
}

// Complete 回填位置参数与派生默认值（不做校验、不产生副作用）。--module
// 落地消费（裁决 7 / D2 Q6）：未显式给 --go-package 时由 module 派生
// <module>/api/gen/go/<name>/v1;<name>v1——与 gen app 目录树的 buf managed
// mode 前缀同源；buf managed 会在生成期覆盖它，此处声明保 proto 自身的
// 可读性与非 buf 工具链的正确性。
func (o *protoOptions) Complete(args []string) error {
	if len(args) > 0 {
		o.Name = args[0]
	}
	if o.Plural == "" {
		o.Plural = o.Name + "s"
	}
	if o.OutDir == "" {
		o.OutDir = filepath.Join("api", "proto", o.Name, "v1")
	}
	if o.GoPackage == "" && o.Module != "" {
		o.GoPackage = o.Module + "/api/gen/go/" + o.Name + "/v1;" + o.Name + "v1"
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
	content, err := renderTmpl("proto", protoTmpl, map[string]string{
		"Name": o.Name, "Plural": o.Plural, "GoPackage": o.GoPackage,
	})
	if err != nil {
		return err
	}
	if err := writeGuarded(filepath.Join(o.OutDir, o.Name+".proto"), content, Owned, o.Force, o.IOStreams); err != nil {
		return err
	}
	// A2：buf 生态联动——注解依赖（googleapis / bufbuild/protovalidate / bald
	// store）经 buf 模块 deps 解析（对照 _example/bald/proto/buf.yaml），生成
	// 物需纳入 buf 模块后运行 buf generate 才有 Go 代码。
	fmt.Fprintln(o.Out, "next: add the file to your buf module (deps: buf.build/googleapis/googleapis, buf.build/bufbuild/protovalidate and your bald store module), then run `buf generate` to produce Go code")
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
	cmd.Flags().StringVar(&o.OutDir, "out", "", "输出目录（默认 api/proto/<name>/v1）")
	cmd.Flags().StringVar(&o.Plural, "plural", "", "REST 资源段（google.api.http 路径用；默认 <name>s）")
	cmd.Flags().StringVar(&o.GoPackage, "go-package", "",
		"proto 的 go_package option（默认由 --module 派生；bald 生态经 buf managed mode 补充）")
	cmd.Flags().StringVar(&o.Module, "module", "", "业务 module 路径（go_package 缺省派生源：<module>/api/gen/go/<name>/v1）")
	cmd.Flags().BoolVar(&o.Force, "force", false, "覆盖已存在的目标文件（默认拒绝）")
	cmd.SetErr(streams.ErrOut)
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
	return writeGuarded(filepath.Join(o.OutDir, o.Name+".go"), formatted, Owned, o.Force, o.IOStreams)
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
	cmd.SetErr(streams.ErrOut)
	return cmd
}

// renderTmpl 渲染模板并返回字节（落盘统一走 writeGuarded，不再内联写文件）。
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