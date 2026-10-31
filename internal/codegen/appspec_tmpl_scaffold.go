package codegen

// appspec_tmpl_scaffold.go 是生成一次的脚手架模板（自定义接线、HTTP 装配
// 钩子、种子数据与 buf/task 流水线配置）。

// bootstrapCustomTmpl 是 internal/bootstrap/custom.go（生成一次，归用户维护）：
// 自定义接线扩展的唯一落点。生成物只调用钩子、从不覆盖本文件——这是
// 「AppSpec 表达不了的接线」与「生成物确定性」并存的方式（设计 §7.3）。
var bootstrapCustomTmpl = `// Package bootstrap 的自定义接线（bald gen app 生成一次，此后归你维护；
// 重生成跳过本文件）。
//
// 生成物 assemble.gen.go 只声明 AppSpec 能表达的东西；第三方 Provider、
// Registrar、拦截器链、探针数据源、Opaque 预算等生成器不认识的接线全部
// 收敛在本文件——装配的声明面仍然只有 Options() 一个入口。
package bootstrap

import "github.com/kalandramo/bald/pkg/appkit"

// customOptions 返回应用自定义接线，生成物在 Options() 末尾展开它：追加
// 在末尾 = 同名单值字段末者胜，因此可以覆盖生成的面/健康检查/拦截器默认值。
//
// 多角色树里 role 是当前角色名（接线按角色分叉），单角色树为空串。
func customOptions(role string) []appkit.Option {
	return nil
}

// variantOpts 是构建期变体的应用级接线表（键 = AppSpec.variants.name）。
// 变体实现随 //go:build 文件编入时，在带 tag 的文件里登记：
//
//	func init() { variantOpts["grpcgw"] = []appkit.Option{...} }
//
// 生成物 variant_<name>.gen.go 的 init 经 customVariantOpts 取出——变体
// 声明/实现对账（I12）仍由生成物负责，业务只在命中时提供接线。
var variantOpts = map[string][]appkit.Option{}

// customVariantOpts 返回变体 name 的自定义接线（未登记返回 nil）。
func customVariantOpts(name string) []appkit.Option { return variantOpts[name] }
`

// appCustomTmpl 是 cmd/<app>/app/custom.go（生成一次，归用户维护）：CLI 侧
// 自定义接线（业务子命令与默认 serve 并存）。
var appCustomTmpl = `// Package app 的 CLI 自定义接线（bald gen app 生成一次，此后归你维护；
// 重生成跳过本文件）。
package app

import "github.com/spf13/cobra"

// customSubcommands 返回本角色额外挂到根命令的业务子命令（与默认 serve
// 并存）。生成物只调用本函数，不覆盖本文件。
func customSubcommands() []*cobra.Command { return nil }
`

// apiserverTmpl 是 internal/<apiserver_dir>/apiserver.go（生成一次，仅当
// 声明 HTTP 面）：路由分组/中间件挂载点——业务唯一的 HTTP 装配钩子。
var apiserverTmpl = `// Package {{ .APIDir }} 是 HTTP 面的业务装配钩子（bald gen app 生成一次，
// 此后归你维护——重生成跳过本文件；中间件链是安全策略，归业务自持）。
//
// 域路由经 appkit.Domain.Routes 挂到本 handler 上（域清单见
// internal/bootstrap/domains.gen.go；域自我接线、路由自持）。
package {{ .APIDir }}

import (
	"github.com/gin-gonic/gin"
	"github.com/kalandramo/bald/pkg/middleware/bundle"
)

// NewRouter 组织业务 HTTP 路由（gin 引擎 + 横切链）。被
// internal/bootstrap/assemble.gen.go 的 WithHTTP 声明消费。
func NewRouter() *gin.Engine {
	router := gin.New()
	// 横切关注点链：bundle 一次构造、HTTP 产出中间件链，链序由框架固化。
	// 业务依赖（Authn/Authz/Audit/Metrics）经 bundle.New 选项注入，
	// 未注入的层自动省略（零开销）。
	router.Use(bundle.New(
{{- if and .Spec.Bundle .Spec.Bundle.Normalized}}
		bundle.Normalized(), // P9 归一化：审计 object/action 双传输同源
{{- end}}
	).Gin()...)
	// TODO: 业务全局中间件（Recovery/RequestID/Logging…）与根路由组。
	return router
}
`

// seedTmpl / seedPoliciesTmpl 是 internal/bootstrap/seed/ 的两个生成一次文件
// （演示域种子 + proto 派生的默认授权策略行）。
var seedTmpl = `// Package seed 承载业务种子数据（bald gen app 生成一次，此后归你维护；
// 重生成跳过本文件）。
//
// 演示域的种子起步集，对照 _example/bald 的 greet 域演示数据
// ——行形状对齐 bald.store.v1 通用 CRUD 面；保留字表演示「proto 注解表达
// 不了、需要外部状态的校验」的数据源（对照 greetv1 的 reservedNames）。
// 真实业务把 Items 换成你的域模型行，并在域 Wire / 业务 BeforeStart 里经
// 存储 Provider 落库（对照 contrib/store-gorm 范本）；无存储栈的演示域
// 保留内存语义即可。
package seed

// Item 是演示种子行（形状对齐 bald.store.v1 通用 CRUD 面：id/name/greet）。
type Item struct {
	ID    string
	Name  string
	Greet string
}

// Items 是演示域的种子行（_example/bald greet 域的等效演示数据：三条问候
// 配置——问候前缀可经域配置段 biz.greet.greeting 覆盖，见 greetv1.wire）。
var Items = []Item{
	{ID: "greet-001", Name: "bald", Greet: "hello"},
	{ID: "greet-002", Name: "world", Greet: "hi"},
	{ID: "greet-003", Name: "demo", Greet: "你好"},
}

// Reserved 是业务保留字种子（对照 greetv1 的 reservedNames：proto 注解表达
// 不了、需要外部状态的校验，其数据源从这里起步）。
var Reserved = map[string]struct{}{
	"root":   {},
	"admin":  {},
	"system": {},
}

// Seed 把种子行装载进业务存储的示范入口（真实实现：app.Resource
// ("database.sql") 取连接后落库；示例域无存储依赖，保留内存语义）。
//
// 调用点：域 Wire 或业务 BeforeStart（资源面就绪之后、server 构建之前）。
func Seed() error {
	// TODO: 读写真实存储（示例域无存储依赖——保留空实现即不污染装配）。
	return nil
}
`

var seedPoliciesTmpl = `// Package seed 承载授权策略行（bald gen app 生成一次，此后归你维护；
// 重生成跳过本文件）。
//
// 默认策略从 proto 的 service/method 派生（归一化口径与 pkg/authz 同源）：
// 每域 object = Service 段去 Service 后缀转小写（DefaultGRPCObject）；动作 =
// **六元组**——REST 动词（get/post/put/delete）+ gRPC 动作（list/write）双面
// 对齐。教训：只写四条 REST 动词，补 gRPC 面时 List(→list)/Create(→write)
// 因缺策略行 403。viewer 只读
// （get/list）；gen domain 产物 authz/alias.gen.go 的 DefaultActions 是本表
// 的对账面。只有自定义策略才需要登记在本文件。
//
// 消费入口（行为不在生成物里）：contrib/authz-casbin 的 NewRows——本表行
// 映射为 authzcasbin.PolicyRow 装载（见该包文档；角色绑定 g 行是运行期
// 业务数据，由业务侧提供）。
package seed

// PolicyRow 一条默认授权策略行（role/object/action）。
type PolicyRow struct {
	Role   string
	Object string
	Action string
}

// Rows 是默认授权策略行（AppSpec.domains 声明序；object 由域 service 派生）。
var Rows = []PolicyRow{
{{- range $d := .Spec.Domains }}
	// {{ $d.Name }}（{{ $d.Service }} → object {{ grpcObject $d.Service }}）
{{- range $a := list "get" "list" "write" "delete" "post" "put" }}
	{Role: "admin", Object: {{ grpcObject $d.Service | q }}, Action: {{ $a | q }}},
{{- end }}
{{- range $a := list "get" "list" }}
	{Role: "viewer", Object: {{ grpcObject $d.Service | q }}, Action: {{ $a | q }}},
{{- end }}
{{- end }}
}
`

// bufYamlTmpl / bufGenYamlTmpl 是 api/ 的 buf 扇出配置（生成物）。
var bufYamlTmpl = `# 生成一次（bald gen app 播种；此后归你维护）：模块名/依赖模块/lint 是树的
# 属性，不是生成器的推导结果。--check 只判存在性。
version: v2
modules:
  - path: proto
    name: buf.build/kalandramo/{{ .AppName }}
    lint:
      use:
        - STANDARD
`

var bufGenYamlTmpl = `# 生成一次（bald gen app 播种；此后归你维护）：插件集合与输出路径是树的属性
# （grpc / grpc-gateway / 校验器等插件按需加）。--check 只判存在性。
version: v2
managed:
  enabled: true
  override:
    - file_option: go_package_prefix
      value: {{ .Module }}/api/gen/go
plugins:
  - remote: buf.build/protocolbuffers/go
    out: gen/go
    opt: paths=source_relative
`

// taskfileTmpl 是 taskfile.yml（生成物）。
var taskfileTmpl = `# 目标目录树的构建入口（bald gen app 生成）。
version: "3"

tasks:
  proto:
    desc: 生成 protobuf 代码（插件与输出见 api/buf.gen.yaml）
    dir: api
    cmds:
      - buf generate
{{- if .MultiRole}}
{{- range .Roles}}
  run-{{ .Name }}:
    desc: 本地运行角色 {{ .Name }}（读 {{ .ConfigFile }}）
    cmds:
      - go run ./cmd/{{ .CmdName }}
{{- end}}
{{- else}}
  run:
    desc: 本地运行（默认读 {{ .ConfigFile }}）
    cmds:
      - go run ./cmd/{{ .CmdName }}
{{- end}}
  verify:
    desc: 构建 + vet + 测试（layering 不变量含在内）
    cmds:
      - go build ./...
      - go vet ./...
      - go test ./...
`
