package codegen

import (
	"errors"
	"fmt"
	"go/build/constraint"
	"regexp"
	"slices"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	appspecv1 "github.com/kalandramo/bald/bconf/gen/go/bald/appspec/v1"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// appspec_validate.go 是 Gate1（生成期 schema 门）：
// 在任何文件落盘之前校验 AppSpec 声明——kind 白名单 / name 唯一 /
// requires⊆provides / app.name 只声明一处。不校验则这些违例要到运行期才炸
// （unknown component kind / unresolved capabilities），或根本无人发现。
// 契约段的唯一真相源是 bootstrap（旧快捷方式字段已移除，无投影对账）。

// componentKinds 是生成器已实现组件工厂的 kind 白名单（Gate1）。白名单外
// 的 kind 生成期即报错——否则 db/redis 等要到运行期才炸 unknown component kind。
var componentKinds = map[string]bool{
	"heartbeat": true,
}

// supportedComponentKinds 是白名单的排序展示形态：全局组件与变体组件共用
// 同一条错误信息，避免两处措辞漂移。
func supportedComponentKinds() string {
	kinds := make([]string, 0, len(componentKinds))
	for k := range componentKinds {
		kinds = append(kinds, k)
	}
	slices.Sort(kinds)
	return strings.Join(kinds, ", ")
}

// variantNameRe 限定变体名：它同时是 ambient 诊断的 Subject 后缀与生成物
// 里的字符串字面量，限定为小写标识符形状，避免诊断/文件口径随命名漂移。
var variantNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// nameShapeRe 限定直接拼进文件系统路径的名字（AR-11）：meta.name 驱动
// cmd/<name>/ 与 configs/<name>.yaml，roles[].name 驱动 cmd/<name>-<role>/，
// domains[].name 是域目录/包名。只判非空时 `../../escaped` 这类值会让 gen app
// 写出 --out，且 manifest 对 .. 路径永不回收——任意目录写入面（J7 fail-fast）。
var nameShapeRe = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// depFieldRe 匹配域 Deps 结构体字段名：导出 Go 标识符（大写开头，驼峰/下划线）。
var depFieldRe = regexp.MustCompile(`^[A-Z][A-Za-z0-9_]*$`)

// depImportAliasRe 匹配 deps 依赖项的 import 别名：Go 标识符（小写开头的
// 包名习惯，但只判标识符形状——下划线开头的别名虽可用，不做额外放行）。
var depImportAliasRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// variantTagRe 限定单个 build tag 的形状（Go 构建约束允许字母、数字、下划线
// 与点）。逗号、空格与 ! / && / || 都是表达式语法：生成物按「列表包含该项」
// 判定命中，这类写法永远不命中且不报错——必须在生成期拒绝。
var variantTagRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.]*$`)

// gatewayDriver 是角色传输面 gateway 的契约投影值：同端口单面语义
// （server.http.driver = "grpc-gateway"，生成物以 transport/gateway/contract 的
// HTTPProvider 接管 server.http 段承载；独立端口拓扑直接声明 bootstrap.server.gateway）。
const gatewayDriver = "grpc-gateway"

// domainServices 返回域声明的有效 gRPC service 全名：services[] 优先，退化为
// 单数 service；http_only 域与未声明任何 service 的域返回 nil。声明形态的
// 合法性由 Gate1 保证（三态互斥），生成器只按此口径消费。
func domainServices(d *appspecv1.DomainSpec) []string {
	if d.GetHttpOnly() {
		return nil
	}
	if ss := d.GetServices(); len(ss) > 0 {
		return ss
	}
	if s := d.GetService(); s != "" {
		return []string{s}
	}
	return nil
}

// serviceShortName 返回 service 全名的最后一段（greet.v1.GreetService →
// GreetService）：绑定的 Go 接口按它命名。
func serviceShortName(full string) string {
	if i := strings.LastIndexByte(full, '.'); i >= 0 {
		return full[i+1:]
	}
	return full
}

// validateAppSpec 校验 AppSpec 声明（Gate1），返回全部违例（聚合报错，
// 每条含字段路径与非法值）。在 contractBootstrap 之前调用。
func validateAppSpec(as *appspecv1.AppSpec) []error {
	var errs []error
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	if as.GetMeta().GetName() == "" {
		add("meta.name is required (drives cmd/<name>/ and configs/<name>.yaml)")
	} else if !nameShapeRe.MatchString(as.GetMeta().GetName()) {
		add("meta.name %q is not a valid name (must match ^[a-z][a-z0-9-]*$; it is joined into filesystem paths like cmd/<name>/)", as.GetMeta().GetName())
	}
	if as.GetMeta().GetModule() == "" {
		add("meta.module is required (generated files import <module>/internal/...)")
	}

	// ---- components：kind 白名单 + name 唯一 ----
	seenComp := map[string]int{}
	for i, c := range as.GetComponents() {
		if !componentKinds[c.GetKind()] {
			add("components[%d].kind %q is not supported (field components.kind; whitelist: %s)",
				i, c.GetKind(), supportedComponentKinds())
		}
		if prev, dup := seenComp[c.GetName()]; dup {
			add("components[%d].name %q duplicates components[%d].name (field components.name must be unique)", i, c.GetName(), prev)
		} else {
			seenComp[c.GetName()] = i
		}
	}

	// ---- capability：requires[].caps ⊆ provides（S1 的生成期镜像）----
	provides := map[string]bool{}
	for _, p := range as.GetCapability().GetProvides() {
		provides[p] = true
	}
	for i, r := range as.GetCapability().GetRequires() {
		for _, cap := range r.GetCaps() {
			if !provides[cap] {
				add("capability.requires[%d] (%s) requires capability %q which is not in capability.provides "+
					"(field capability.requires[].caps must be a subset of capability.provides)", i, r.GetComponent(), cap)
			}
		}
	}

	// ---- domains：name 非空且唯一 + 声明形态三选一 + go_package 必填 + 别名不冲突 ----
	seenDomain := map[string]int{}
	seenAlias := map[string]int{}
	// seenDepImport 记 path → 别名（跨域全局）：domains.gen.go 是一个文件，同一
	// path 只能 import 一次，故别名必须全场一致。
	seenDepImport := map[string]string{}
	for i, d := range as.GetDomains() {
		if d.GetName() == "" {
			add("domains[%d].name is required (field domains.name)", i)
			continue
		}
		if !nameShapeRe.MatchString(d.GetName()) {
			add("domains[%d].name %q is not a valid name (must match ^[a-z][a-z0-9-]*$; it is joined into domain directory paths)", i, d.GetName())
			continue
		}
		if prev, dup := seenDomain[d.GetName()]; dup {
			add("domains[%d].name %q duplicates domains[%d].name (field domains.name must be unique)", i, d.GetName(), prev)
		} else {
			seenDomain[d.GetName()] = i
		}
		// 声明形态三选一：service（单数）| services（复数）| http_only。
		// http_only 没有 proto 契约可派生，因此不能同时声明 proto 字段；
		// 非 http_only 域必须给出至少一个 service（否则 gen domain 无从派生）。
		svcs := d.GetServices()
		switch {
		case d.GetHttpOnly():
			if d.GetService() != "" || len(svcs) > 0 || d.GetProtoPackage() != "" {
				add("domains[%d] (%s): http_only domains must not declare service, services or proto_package "+
					"(a pure-HTTP domain has no proto contract to derive from)", i, d.GetName())
			}
		case d.GetService() != "" && len(svcs) > 0:
			add("domains[%d] (%s): service and services are mutually exclusive "+
				"(declare the singular service, the services list, or http_only)", i, d.GetName())
		case d.GetService() == "" && len(svcs) == 0:
			add("domains[%d] (%s): declare service or services, or set http_only for a domain without a proto service", i, d.GetName())
		}
		// services 逐项形状 + 域内唯一；短名也须唯一——绑定的 Go 接口按短名命名。
		seenSvc := map[string]int{}
		seenShort := map[string]string{}
		for j, s := range svcs {
			switch {
			case strings.TrimSpace(s) == "":
				add("domains[%d].services[%d] is empty", i, j)
				continue
			case strings.TrimSpace(s) != s:
				add("domains[%d].services[%d] %q has surrounding whitespace", i, j, s)
				continue
			}
			if prev, dup := seenSvc[s]; dup {
				add("domains[%d].services[%d] %q duplicates domains[%d].services[%d]", i, j, s, i, prev)
			} else {
				seenSvc[s] = j
			}
			short := serviceShortName(s)
			if prev, dup := seenShort[short]; dup {
				add("domains[%d].services[%d] %q and %q share the short name %q (bindings name their Go interface after it; short names must be unique within a domain)",
					i, j, prev, s, short)
			} else {
				seenShort[short] = s
			}
		}
		if d.GetGoPackage() == "" {
			add("domains[%d] (%s): go_package is required (the import path of the package exporting its appkit.Domain value)", i, d.GetName())
			continue
		}
		alias := domainAlias(d.GetGoPackage())
		if prev, dup := seenAlias[alias]; dup {
			add("domains[%d] (%s): import alias %q collides with domains[%d] (%s) -- distinct package base names are required",
				i, d.GetName(), alias, prev, as.GetDomains()[prev].GetName())
		} else {
			seenAlias[alias] = i
		}
		// deps：每项装配依赖的字段名（合法标识符 + 域内唯一）、稳定 id、闭包
		// 表达式与 import 清单都必须齐备，否则生成的 Setup 接线不可编译。
		seenDepField := map[string]int{}
		for j, dep := range d.GetDeps() {
			f := strings.TrimSpace(dep.GetField())
			if f == "" {
				add("domains[%d].deps[%d].field is required (the Deps struct field name to wire)", i, j)
			} else if !depFieldRe.MatchString(f) {
				add("domains[%d].deps[%d].field %q is not a valid exported Go identifier (field domains.deps.field)", i, j, f)
			}
			if prev, dup := seenDepField[f]; dup && f != "" {
				add("domains[%d].deps[%d].field %q duplicates domains[%d].deps[%d].field (field domains.deps.field must be unique within a domain)", i, j, f, i, prev)
			} else if f != "" {
				seenDepField[f] = j
			}
			if strings.TrimSpace(dep.GetId()) == "" {
				add("domains[%d].deps[%d].id is required (a stable dependency id, e.g. store.Tenant or authn)", i, j)
			}
			if strings.TrimSpace(dep.GetExpr()) == "" {
				add("domains[%d].deps[%d].expr is required (the Go closure expression, e.g. func() *store.Store[T] { return src.Store().T })", i, j)
			}
			for k, imp := range dep.GetImports() {
				path := strings.TrimSpace(imp.GetPath())
				alias := strings.TrimSpace(imp.GetAlias())
				if path == "" {
					add("domains[%d].deps[%d].imports[%d].path is required (the import path referenced by expr)", i, j, k)
					continue
				}
				if strings.ContainsAny(path, " \t\"'") {
					add("domains[%d].deps[%d].imports[%d].path %q contains whitespace or quotes (put the alias in imports[].alias, not in path)", i, j, k, path)
				}
				if alias != "" && !depImportAliasRe.MatchString(alias) {
					add("domains[%d].deps[%d].imports[%d].alias %q is not a valid Go identifier", i, j, k, alias)
				}
				// 同一 path 在一个生成文件的 import 块里只能出现一次，别名必须
				// 处处一致——不同域给不同别名会产出不可编译的重复 import。
				if prev, dup := seenDepImport[path]; dup {
					if prev != alias {
						add("domains[%d].deps[%d].imports[%d]: import %q declared with alias %q conflicts with earlier alias %q "+
							"(one import block cannot import the same path twice under different names)", i, j, k, path, alias, prev)
					}
				} else {
					seenDepImport[path] = alias
				}
			}
		}
		// grpc_biz_package / grpc_biz_type 必须成对：只给一半说明骨架接线不可
		// 编译（生成器不猜另一半），生成期即报。
		if (d.GetGrpcBizPackage() == "") != (d.GetGrpcBizType() == "") {
			add("domains[%d] (%s): grpc_biz_package and grpc_biz_type must be set together "+
				"(the skeleton needs both the biz import path and its type name)", i, d.GetName())
		}
	}

	// ---- roles：为空 = 单角色（零迁移，不校验不报错）；非空才进三道检查 ----
	// 1) name 非空且唯一；2) 每个 domains 元素必须是已声明的域；3) 各角色
	// domains 并集恰好等于 domains 全集（缺域 = 有域无人服务，多域 = 引用了
	// 不存在的域，两者都进聚合错误并列出具体域名）。
	if roles := as.GetMeta().GetRoles(); len(roles) > 0 {
		seenRole := map[string]int{}
		union := map[string]bool{}
		for i, r := range roles {
			name := r.GetName()
			if name == "" {
				add("meta.roles[%d].name 不能为空（字段 meta.roles.name：api | runner，决定 cmd/<meta.name>-<role>/ 目录名）", i)
			} else if !nameShapeRe.MatchString(name) {
				add("meta.roles[%d].name %q 不是合法名字（须匹配 ^[a-z][a-z0-9-]*$；它直接拼进 cmd/<meta.name>-<role>/ 路径）", i, name)
			} else if prev, dup := seenRole[name]; dup {
				add("meta.roles[%d].name %q 与 meta.roles[%d].name 重复（字段 meta.roles.name 必须全应用唯一）", i, name, prev)
			} else {
				seenRole[name] = i
			}
			for _, d := range r.GetDomains() {
				if _, ok := seenDomain[d]; !ok {
					add("meta.roles[%d] (%s) 的 domains %q 未在 domains[].name 中声明（字段 meta.roles.domains 必须引用已声明的域）", i, name, d)
				}
				union[d] = true
			}
		}
		missing := make([]string, 0, len(as.GetDomains()))
		for _, d := range as.GetDomains() {
			if n := d.GetName(); n != "" && !union[n] {
				missing = append(missing, n)
			}
		}
		extra := make([]string, 0)
		for n := range union {
			if _, ok := seenDomain[n]; !ok {
				extra = append(extra, n)
			}
		}
		slices.Sort(missing)
		slices.Sort(extra)
		if len(missing) > 0 || len(extra) > 0 {
			add("meta.roles 各角色 domains 的并集必须等于 domains 全集：缺失=%v 多余=%v "+
				"（字段 meta.roles.domains 的并集 vs domains[].name——缺域则该域无人服务，多域则引用了不存在的域）",
				missing, extra)
		}
	}

	// ---- variants：build tag 分叉进声明 ----
	globalComp := map[string]bool{}
	for _, c := range as.GetComponents() {
		globalComp[c.GetName()] = true
	}
	variantComp := map[string]string{} // 组件名 -> 首次声明位置（跨变体唯一）
	seenVariant := map[string]bool{}
	for i, vr := range as.GetVariants() {
		name := vr.GetName()
		switch {
		case name == "":
			add("variants[%d].name is required (plan origin \"variant:<name>\" and variant_<name>.gen.go are derived from it)", i)
		case !variantNameRe.MatchString(name):
			add("variants[%d].name %q is invalid (must match %s; it names variant_<name>.gen.go and the plan origin)", i, name, variantNameRe)
		case seenVariant[name]:
			add("variants[%d].name %q is declared twice (variant names must be unique)", i, name)
		default:
			seenVariant[name] = true
		}
		tags := vr.GetBuildTags()
		when := strings.TrimSpace(vr.GetWhen())
		switch {
		case when != "" && len(tags) > 0:
			add("variants[%d] declares both when and build_tags (they are mutually exclusive; when is the canonical form, build_tags is &&-sugar)", i)
		case when == "" && len(tags) == 0:
			add("variants[%d] needs a condition: declare when (a go/build/constraint expression) or build_tags (all must match)", i)
		case when != "":
			if strings.ContainsAny(vr.GetWhen(), "\r\n") {
				add("variants[%d].when must be a single-line go/build/constraint expression", i)
			} else if _, err := constraint.Parse("//go:build " + when); err != nil {
				add("variants[%d].when %q is not a valid go/build/constraint expression: %v", i, when, err)
			}
		}
		seenTag := map[string]bool{}
		for j, t := range tags {
			switch {
			case strings.TrimSpace(t) == "":
				add("variants[%d].build_tags[%d] is empty", i, j)
				continue
			case strings.TrimSpace(t) != t:
				add("variants[%d].build_tags[%d] %q has surrounding whitespace", i, j, t)
				continue
			case !variantTagRe.MatchString(t):
				// build_tags 是「每个条目一个 tag，全部命中」的迁移糖：带逗号
				// 或布尔语法会被 && 连接后语义含糊，写表达式请用 when——两者
				// 在 Gate1 互斥，不能让同一条件有两种拼法。
				add("variants[%d].build_tags[%d] %q is not a single build tag (use when for expressions such as \"a,b\", \"!a\" or \"a&&b\")", i, j, t)
				continue
			}
			if seenTag[t] {
				add("variants[%d].build_tags[%d] %q is duplicated", i, j, t)
			}
			seenTag[t] = true
		}
		// effects.components：kind 白名单与全局组件同一张表；name 跨全局与
		// 所有变体唯一（component.<name> 是计划节点身份，重复即两条节点）。
		for j, c := range vr.GetEffects().GetComponents() {
			field := fmt.Sprintf("variants[%d].effects.components[%d]", i, j)
			if !componentKinds[c.GetKind()] {
				add("%s.kind %q is not supported (whitelist: %s)", field, c.GetKind(), supportedComponentKinds())
			}
			name := c.GetName()
			switch {
			case name == "":
				add("%s.name is required (component.<name> is a plan node identity)", field)
			case globalComp[name]:
				add("%s.name %q duplicates a components[].name (component names are plan node identities across the app and its variants)", field, name)
			default:
				if prev, dup := variantComp[name]; dup {
					add("%s.name %q duplicates %s.name (component names are plan node identities across variants)", field, name, prev)
				} else {
					variantComp[name] = field
				}
			}
		}
		// effects.patches：路径形状生成期校验；字段存在性与「覆盖非空值须给
		// reason」由 appkit 对默认契约（bconf 预填）在计划期校验——AppSpec 的
		// bootstrap 段是部分声明，缺段不代表运行期缺字段。
		for j, p := range vr.GetEffects().GetPatches() {
			field := fmt.Sprintf("variants[%d].effects.patches[%d]", i, j)
			switch {
			case strings.TrimSpace(p.GetPath()) == "":
				add("%s.path is required (a bootstrap contract field path such as app.env)", field)
			case strings.TrimSpace(p.GetPath()) != p.GetPath():
				add("%s.path %q has surrounding whitespace", field, p.GetPath())
			case strings.ContainsAny(p.GetPath(), "\r\n"):
				add("%s.path must be a single-line field path", field)
			}
		}
	}

	// ---- app.name 双处声明：meta.name 与 bootstrap.app.name 只能有一处 ----
	if an := as.GetBootstrap().GetApp().GetName(); an != "" && as.GetMeta().GetName() != "" && an != as.GetMeta().GetName() {
		add("bootstrap.app.name=%q but meta.name=%q (contract app.name defaults to meta.name; declare it in at most one place)",
			an, as.GetMeta().GetName())
	}
	return errs
}

// projectServer 把角色传输面（RoleSpec.server）投影为该角色的契约 server
// 段。未声明任何传输面时返回 nil：段缺席即「该角色不开传输面」，段覆盖
// 检查无从要求覆盖。
func projectServer(sv *appspecv1.ServerSpec, global *bootstrapv1.Server) *bootstrapv1.Server {
	if sv == nil {
		return nil
	}
	var out *bootstrapv1.Server
	ensure := func() *bootstrapv1.Server {
		if out == nil {
			out = &bootstrapv1.Server{}
		}
		return out
	}
	if sv.GetHttp() || sv.GetHttpAddr() != "" || sv.GetGateway() {
		driver := ""
		if sv.GetGateway() {
			driver = gatewayDriver
		}
		out = &bootstrapv1.Server{Http: &bootstrapv1.Server_Http{Addr: sv.GetHttpAddr(), Driver: driver}}
	}
	if sv.GetGrpc() || sv.GetGrpcAddr() != "" {
		ensure().Grpc = &bootstrapv1.Server_Grpc{Addr: sv.GetGrpcAddr()}
	}
	// cron/asynq 面：ServerSpec 给开关，段内容继承全局（AppSpec 不复制 bconf
	// 字段——redis_address/location 等只在全局 bootstrap.server 声明一处）。
	// 开关开但全局无该段时投影为**非空空段**：cron/asynq 的契约 Provider 见
	// nil 段即跳过该面，空 message 段才表示「用实现默认起这一面」。
	if sv.GetCron() {
		ensure().Cron = &bootstrapv1.Server_Cron{}
		if gc := global.GetCron(); gc != nil {
			ensure().Cron = proto.Clone(gc).(*bootstrapv1.Server_Cron)
		}
	}
	if sv.GetAsynq() {
		ensure().Asynq = &bootstrapv1.Server_Asynq{}
		if ga := global.GetAsynq(); ga != nil {
			ensure().Asynq = proto.Clone(ga).(*bootstrapv1.Server_Asynq)
		}
	}
	if sv.GetMcp() {
		ensure().Mcp = &bootstrapv1.Server_Mcp{}
		if gm := global.GetMcp(); gm != nil {
			ensure().Mcp = proto.Clone(gm).(*bootstrapv1.Server_Mcp)
		}
	}
	return out
}

// contractBootstrap 返回契约段唯一真相源（AppSpec.bootstrap）。未声明时
// 返回空配置：Gate2 仍能派生 app.name 等默认键，段存在性语义不受影响。
func contractBootstrap(as *appspecv1.AppSpec) *bootstrapv1.BootstrapConfig {
	if bs := as.GetBootstrap(); bs != nil {
		return bs
	}
	return &bootstrapv1.BootstrapConfig{}
}

// ---------------------------------------------------------------------------
// 字段消费机器守卫（声明字段必须有消费者）
// ---------------------------------------------------------------------------

// fieldConsumers 是「声明字段 → 生成物标记」表：key = proto 字段全名，
// value = 消费者必须在生成物文本中留下的标记。遍历 proto 描述符逐字段校验，
// ServerSpec.gateway 型死声明不可能上线（新增字段忘接消费者 = 测试红）。
var fieldConsumers = map[string]string{
	"bald.appspec.v1.AppSpec.meta.name":                     "demoapp", // 标记按 guardFixture 取值断言（同包测试夹具）
	"bald.appspec.v1.AppSpec.meta.module":                   "demo.example/app",
	"bald.appspec.v1.AppSpec.meta.desc":                     "demo desc",
	"bald.appspec.v1.AppSpec.components.kind":               "heartbeat",
	"bald.appspec.v1.AppSpec.components.name":               "demo.hb",
	"bald.appspec.v1.AppSpec.components.config_prefix":      "demo.hb",
	"bald.appspec.v1.AppSpec.components.params":             "30s",
	"bald.appspec.v1.AppSpec.capability.provides":           "mech.Provides(",
	"bald.appspec.v1.AppSpec.capability.requires.component": "audit.store",
	"bald.appspec.v1.AppSpec.capability.requires.caps":      "db",
	"bald.appspec.v1.AppSpec.layout.apiserver_dir":          "internal/apiserver",
	"bald.appspec.v1.AppSpec.layout.strict_contract":        "appkit.StrictContract(",
	"bald.appspec.v1.AppSpec.bundle.normalized":             "bundle.normalized=true → 归一化默认开启",
	"bald.appspec.v1.AppSpec.domains.name":                  "greet",
	"bald.appspec.v1.AppSpec.domains.proto_package":         "greet.v1",
	"bald.appspec.v1.AppSpec.domains.service":               "greet.v1.GreetService",
	"bald.appspec.v1.AppSpec.domains.services":              "greet.v1.GreetAdminService",
	"bald.appspec.v1.AppSpec.domains.go_package":            "demo.example/app/internal/apiserver/greetv1",
	// http_only 的消费是 seed 策略的显式跳过注释（负向分支也必须可从生成物
	// 读到——否则「域没有策略行」会被误读为生成器漏接）。
	"bald.appspec.v1.AppSpec.domains.http_only": "http_only domain",
	// variants.gen.go 是 gofmt 后的 VariantDecl 字面量：name 的标记带上
	// 字段名与对齐空格，避免与 when 的 `"grpcgw"` 标记混淆（两个字段
	// 各留一个独立标记，任一字段漏接消费者都能单独报红）。build_tags 是
	// when 的 &&-迁移糖，消费 = 生成期归一（builderConsumed），不落独立标记。
	"bald.appspec.v1.AppSpec.variants.name":        "Name:   \"grpcgw\",",
	"bald.appspec.v1.AppSpec.variants.when":        `When:   "linux && amd64",`,
	"bald.appspec.v1.AppSpec.variants.description": "gateway wiring behind the grpcgw tag",
	// variants.effects.*：增量声明在 variants.gen.go 落 VariantEffect 字面量，
	// 组件字段另在 variant_<name>.gen.go 的 buildComponent 调用里出现——守卫
	// 只看拼接文本，同一标记即可。标记按 variantFixture 取值断言。
	"bald.appspec.v1.AppSpec.variants.effects.components.kind":          "heartbeat",
	"bald.appspec.v1.AppSpec.variants.effects.components.name":          "demo.gw",
	"bald.appspec.v1.AppSpec.variants.effects.components.config_prefix": "demo.gw",
	"bald.appspec.v1.AppSpec.variants.effects.components.params":        "15s",
	"bald.appspec.v1.AppSpec.variants.effects.patches.path":             "app.env",
	"bald.appspec.v1.AppSpec.variants.effects.patches.value":            "prod",
	"bald.appspec.v1.AppSpec.variants.effects.patches.reason":           "production builds are tagged grpcgw",
}

// builderConsumed 是「由树构建器直接消费（不落生成物文本）」的字段集合：
// bootstrap 段的消费 = Gate2 契约门 + 配置反射发射；layout.root 的消费 =
// 产出根定位。仍须登记——漏登即死声明（与 fieldConsumers 同一守卫）。
var builderConsumed = map[string]bool{
	"bald.appspec.v1.AppSpec.bootstrap":   true,
	"bald.appspec.v1.AppSpec.layout.root": true,
	// meta.roles：消费 = Gate1 角色校验（本文件 roles 块：name 唯一 +
	// domains 引用 + 并集覆盖）+ 树构建器按角色切分 cmd/<app>-<role>/ 与
	// configs/<app>-<role>.yaml（不落可断言的单一标记，故与 layout.root
	// 同类登记）。
	"bald.appspec.v1.AppSpec.meta.roles":                  true,
	"bald.appspec.v1.AppSpec.meta.roles.name":             true,
	"bald.appspec.v1.AppSpec.meta.roles.domains":          true,
	"bald.appspec.v1.AppSpec.meta.roles.server":           true,
	"bald.appspec.v1.AppSpec.meta.roles.server.http":      true,
	"bald.appspec.v1.AppSpec.meta.roles.server.grpc":      true,
	"bald.appspec.v1.AppSpec.meta.roles.server.gateway":   true,
	"bald.appspec.v1.AppSpec.meta.roles.server.http_addr": true,
	"bald.appspec.v1.AppSpec.meta.roles.server.grpc_addr": true,
	// cron/asynq 面：消费 = projectServer 投影 server.cron/server.asynq 段
	// （进每角色契约 YAML）+ buildRoles 的 HasCron/HasAsynq 驱动 assemble
	// 模板的 provider 接线；与 http/grpc 同属树构建器直接消费，不落文本标记。
	"bald.appspec.v1.AppSpec.meta.roles.server.cron":  true,
	"bald.appspec.v1.AppSpec.meta.roles.server.asynq": true,
	// mcp 面：消费同 cron/asynq（projectServer 投影 + HasMcp 驱动接线）。
	"bald.appspec.v1.AppSpec.meta.roles.server.mcp": true,
	// variants.build_tags：消费 = buildVariants 归一为 when（&& 连接），不落
	// 独立文本标记——与 when 共用同一生成形态。
	"bald.appspec.v1.AppSpec.variants.build_tags": true,
	// domains.deps.*：消费 = buildDomainDeps 汇总（HasDomainDeps / import 去重）
	// + 模板条件渲染 setupDeclaredDomainDeps。是**条件性**消费——只有域声明
	// 了 deps 才落文本，故不适合固定标记（守卫夹具默认无 deps）；正向覆盖由
	// TestTree_DomainDepsGolden 钉住（声明 deps 时生成物含逐项 Setup 接线）。
	"bald.appspec.v1.AppSpec.domains.deps.field": true,
	"bald.appspec.v1.AppSpec.domains.deps.id":    true,
	"bald.appspec.v1.AppSpec.domains.deps.expr":  true,
	// imports.path / imports.alias：消费同 deps.*（buildDomainDeps 汇总成
	// import 块的一行，path 定序、alias 定渲染形态）。条件性，同上。
	"bald.appspec.v1.AppSpec.domains.deps.imports.path":  true,
	"bald.appspec.v1.AppSpec.domains.deps.imports.alias": true,
	// domains.grpc_biz_*：消费 = emitGRPCSkeletons（仅 --grpc-skeleton 且二
	// 者齐备时落骨架文本，条件性；正向覆盖由 TestGenDomain_GRPCSkeleton 钉住）。
	"bald.appspec.v1.AppSpec.domains.grpc_biz_package": true,
	"bald.appspec.v1.AppSpec.domains.grpc_biz_type":    true,
}

// appspecLocal 报告消息是否属于 appspec 本地声明面（守卫只遍历 AppSpec
// 根上的本地消息；bootstrap 子树是 bconf 契约自己的对账域——其每个字段的
// 消费由 bconf 的契约测试与段覆盖检查守卫，不在本表重复登记）。
func appspecLocal(name protoreflect.FullName) bool {
	return strings.HasPrefix(string(name), "bald.appspec.v1.")
}

// checkFieldConsumers 是机器守卫本体：遍历 AppSpec 描述符（含 appspec 本地
// 嵌套消息），要求每个叶子字段（标量/list/map）都在 fieldConsumers（标记须
// 出现在 rendered 生成物拼接文本）或 builderConsumed 登记。返回全部违例。
func checkFieldConsumers(rendered string) []error {
	var errs []error
	var walk func(path string, msg protoreflect.Message)
	walk = func(path string, msg protoreflect.Message) {
		fields := msg.Descriptor().Fields()
		for i := 0; i < fields.Len(); i++ {
			fd := fields.Get(i)
			full := path + "." + string(fd.Name())
			localMsg := fd.Kind() == protoreflect.MessageKind && !fd.IsMap() &&
				fd.Message() != nil && appspecLocal(fd.Message().FullName()) &&
				!isWellKnown(fd.Message().FullName())
			if localMsg {
				// 本地消息 / 消息列表：下钻（容器字段的消费 = 子树消费；
				// 静态遍历与实例是否置位无关——守卫管的是声明面本身）。
				if fd.IsList() {
					list := msg.Get(fd).List()
					if list.Len() == 0 {
						// 空列表也必须按元素类型下钻一次：守卫管的是声明面，
						// 与实例是否置位无关。否则重复消息的子字段
						// （domains/components/capability.requires/roles）会落在
						// 盲区——登记写错、字段漏接消费者都不会报红。
						walk(full, dynamicpb.NewMessage(fd.Message()).ProtoReflect())
						continue
					}
					for j := 0; j < list.Len(); j++ {
						walk(full, list.Get(j).Message())
					}
				} else {
					walk(full, msg.Get(fd).Message())
				}
				continue
			}
			marker, hasMarker := fieldConsumers[full]
			switch {
			case hasMarker && marker != "" && !strings.Contains(rendered, marker):
				errs = append(errs, fmt.Errorf("field %s: consumer marker %q not found in generated output (consumer must emit it)", full, marker))
			case hasMarker:
				// 标记命中。
			case builderConsumed[full]:
				// 树构建器直接消费（Gate2 契约门 / 产出根定位）。
			default:
				errs = append(errs, fmt.Errorf("field %s is declared but has no consumer (register it in fieldConsumers or builderConsumed)", full))
			}
		}
	}
	walk("bald.appspec.v1.AppSpec", (&appspecv1.AppSpec{}).ProtoReflect())
	return errs
}

// isWellKnown 报告是否 google.protobuf well-known 类型（守卫不视其为可下钻
// 的本地消息，也不要求其内部字段登记）。
func isWellKnown(name protoreflect.FullName) bool {
	return strings.HasPrefix(string(name), "google.protobuf.")
}

// guardErrs 聚合违例为单个错误（无违例返回 nil）。
func guardErrs(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	return errors.Join(errs...)
}
