package codegen

// appspec_tmpl_scaffold.go 是生成一次的脚手架模板（自定义接线、HTTP 装配
// 钩子、种子数据与 buf/task 流水线配置）。

// bootstrapCustomTmpl 是 internal/bootstrap/custom.go（生成一次，归用户维护）：
// 自定义接线扩展的唯一落点。生成物只调用钩子、从不覆盖本文件——这是
// 「AppSpec 表达不了的接线」与「生成物确定性」并存的方式（设计 §7.3）。
//
// gRPC 面的契约链钩子（newGRPCChain）也落在这里：与 HTTP 面的 apiserver.NewRouter
// 对称——链序是安全策略、归业务自持，但链的参数来自契约 server.grpc.middleware 段。
var bootstrapCustomTmpl = `// Package bootstrap 的自定义接线（bald gen app 生成一次，此后归你维护；
// 重生成跳过本文件）。
//
// 生成物 assemble.gen.go 只声明 AppSpec 能表达的东西；第三方 Provider、
// Registrar、拦截器链、探针数据源、Opaque 预算等生成器不认识的接线全部
// 收敛在本文件——装配的声明面仍然只有 Options() 一个入口。
package bootstrap

import (
{{- if .HasGRPC}}
	"google.golang.org/grpc"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/pkg/middleware/bundle"
{{- end}}

	"github.com/kalandramo/bald/pkg/appkit"
)

// customOptions 返回应用自定义接线，生成物在 Options() 末尾展开它：追加
// 在末尾 = 同名单值字段末者胜，因此可以覆盖生成的面/健康检查/拦截器默认值。
//
// 多角色树里 role 是当前角色名（接线按角色分叉），单角色树为空串。
func customOptions(role string) []appkit.Option {
{{- if .MultiRole}}
	switch role {
{{- range .Roles}}
{{- if .HasGRPC}}
	case {{ q .Name }}:
		// 本角色有 gRPC 面：拦截器链按契约段构造（newGRPCChain）。
		return []appkit.Option{appkit.WithGRPCOptionsContract(newGRPCChain)}
{{- end}}
{{- end}}
	}
{{- else if .HasGRPC}}
	// gRPC 拦截器链按契约段构造（newGRPCChain）。
	return []appkit.Option{appkit.WithGRPCOptionsContract(newGRPCChain)}
{{- end}}
	return nil
}
{{- if .HasGRPC}}

// newGRPCChain 按契约 server.grpc.middleware 段构造 gRPC 服务端选项链（由
// customOptions 的 WithGRPCOptionsContract 在契约装载后求值一次）。
//
// 链序由 bundle 固化（Error 最外层——*berrors.Error 不实现 GRPCStatus()，
// 少了它错误码在 gRPC 边界全丢成 Unknown），rate_limit/timeout 两段的参数
// 来自配置文件。返回的 close 释放桥构造的资源（限流器桶），框架在停机与启动
// 失败两条路径回放它。
func newGRPCChain(mw *bootstrapv1.Server_Grpc_Middleware) ([]grpc.ServerOption, func() error, error) {
	// 契约段 → bundle Option：段的翻译在桥里做，本函数只决定链的姿态。
	// mw 为 nil（未配置该段）时零 Option，链回到 bundle 的既有默认。
	opts, closeChain, err := bundle.FromGRPCMiddleware(mw)
	if err != nil {
		return nil, nil, err
	}
	// 认证姿态与 HTTP 面同理由业务在这一行宣布（契约里没有 authn 段）。
	chain := bundle.New(append([]bundle.Option{
		bundle.Public(), // TODO: 接入认证后删除本行，改为 bundle.Authn(<authenticator>)
	}, opts...)...).GRPCChain()
	// TODO: 业务自有拦截器（Defaulter/Validator…）在这里追加——再给一条
	// grpc.ChainUnaryInterceptor 即可，它在 bundle 链的内侧靠近 handler。
	return chain, closeChain, nil
}
{{- end}}

// variantOpts 是构建期变体的应用级接线表（键 = AppSpec.variants.name）。
// 变体实现随 //go:build 文件编入时，在带 tag 的文件里登记：
//
//	func init() { variantOpts["grpcgw"] = []appkit.Option{...} }
//
// 生成物 variant_<name>.gen.go 的 init 经 customVariantOpts 取出——变体
// 声明/实现对账仍由生成物负责，业务只在命中时提供接线。
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
//
// 签名是 appkit.HTTPContractFunc：本函数在契约装载之后求值一次，入参就是
// server.http.middleware 段——配置面与链之间的那次调用在这里，固定发生。
var apiserverTmpl = `// Package {{ .APIDir }} 是 HTTP 面的业务装配钩子（bald gen app 生成一次，
// 此后归你维护——重生成跳过本文件；中间件链是安全策略，归业务自持）。
//
// 域路由经 appkit.Domain.Routes 挂到本 handler 上（域清单见
// internal/bootstrap/domains.gen.go；域自我接线、路由自持）。
//
// 本函数由 internal/bootstrap/assemble.gen.go 的 appkit.WithHTTPContract 消费：
// 求值点在契约装载之后、域接线与服务器构造之前，因此入参拿到的是装载后的
// server.http.middleware 段——配置文件里写的限流/超时/CORS/日志跳过路径经
// bundle.FromMiddleware 真的进链，不存在「写了不生效」的中间件段。
package {{ .APIDir }}

import (
	"net/http"

	"github.com/gin-gonic/gin"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/pkg/middleware/bundle"
)

// NewRouter 按契约段组织业务 HTTP 路由（gin 引擎 + 横切链）。
//
// 返回的 close 释放桥构造的资源（限流器桶）——框架把它挂成停机 Effect，启动
// 失败与优雅停机两条路径都会回放；err 非 nil 即启动失败（显式配了非法段值，
// 不降级成「没有链」）。
func NewRouter(mw *bootstrapv1.Server_Http_Middleware) (http.Handler, func() error, error) {
	// 契约段 → bundle Option：七段的翻译在桥里做，本函数只决定链的姿态。
	// mw 为 nil（未配置该段）时零 Option，链回到 bundle 的既有默认。
	opts, closeChain, err := bundle.FromMiddleware(mw)
	if err != nil {
		return nil, nil, err
	}
	router := gin.New()
	// 横切关注点链：bundle 一次构造、HTTP 产出中间件链，链序由框架固化。
	// 业务依赖（Authn/Authz/Audit/Metrics）经 bundle.New 选项注入，
	// 未注入的层自动省略（零开销）。
	//
	// 认证姿态必须显式决定：要么注入 bundle.Authn(...)，要么保留下面的
	// bundle.Public()（= 本服务有意公开、链中无认证层）。两者都不给时
	// bundle.Verify() 报错、出链时告警——「忘了配认证」不该静默变成
	// 一个无认证服务器。契约里没有 authn 段，所以这个姿态只能由业务在这一行
	// 宣布，框架不代替它决定。
	// bundle.Normalized 已裁撤（方案 S 门面收敛）：归一化默认开启、Raw() 显式
	// opt-out——「双传输同源」是缺省正确行为。契约位 bundle.normalized=true
	// 的消费落点即本注释（声明已读，不再产生 Option 行）。
{{- if and .Spec.Bundle .Spec.Bundle.Normalized}}
		// bundle.normalized=true → 归一化默认开启（bundle.Normalized 已裁撤，无 Option 行）
{{- end}}
	router.Use(bundle.New(append([]bundle.Option{
		bundle.Public(), // TODO: 接入认证后删除本行，改为 bundle.Authn(<authenticator>)
	}, opts...)...).Gin()...)
	// TODO: 业务全局中间件（Recovery/RequestID/Logging…）与根路由组。
	return router, closeChain, nil
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
// 每个 service 一个 object = Service 段去 Service 后缀转小写
// （DefaultGRPCObject）；动作 = **六元组**——REST 动词（get/post/put/delete）+
// gRPC 动作（list/write）双面对齐。教训：只写四条 REST 动词，补 gRPC 面时
// List(→list)/Create(→write) 因缺策略行 403。viewer 只读
// （get/list）；http_only 域无 proto service，不派生策略行。
// gen domain 产物 authz/alias.gen.go 的 DefaultActions 是本表的对账面。只有
// 自定义策略才需要登记在本文件。
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
{{- if $d.HttpOnly }}
	// {{ $d.Name }}: http_only domain (REST surface is owned by the domain package; no proto service, so no policy rows are derived)
{{- else }}
{{- range $svc := services $d }}
	// {{ $d.Name }}（{{ $svc }} → object {{ grpcObject $svc }}）
{{- range $a := list "get" "list" "write" "delete" "post" "put" }}
	{Role: "admin", Object: {{ grpcObject $svc | q }}, Action: {{ $a | q }}},
{{- end }}
{{- range $a := list "get" "list" }}
	{Role: "viewer", Object: {{ grpcObject $svc | q }}, Action: {{ $a | q }}},
{{- end }}
{{- end }}
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
