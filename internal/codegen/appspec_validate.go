package codegen

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	appspecv1 "github.com/kalandramo/bald/bconf/gen/go/bald/appspec/v1"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// appspec_validate.go 是 Gate1（生成期 schema 门，D2-target-codegen.md §3.5）：
// 在任何文件落盘之前校验 AppSpec 声明——kind 白名单 / name 唯一 /
// requires⊆provides / 快捷方式与 bootstrap 投影一致。此前这些违例要到
// 运行期才炸（unknown component kind / unresolved capabilities），或根本
// 无人校验（双真相源静默分叉）。

// componentKinds 是生成器已实现组件工厂的 kind 白名单（Gate1）。白名单外
// 的 kind 生成期即报错——此前 db/redis 等到运行期才炸 unknown component kind。
var componentKinds = map[string]bool{
	"heartbeat": true,
}

// variantNameRe 限定变体名：它同时是 ambient 诊断的 Subject 后缀与生成物
// 里的字符串字面量，限定为小写标识符形状，避免诊断/文件口径随命名漂移。
var variantNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// gatewayDriver 是「gateway 快捷方式」的契约投影值：同端口单面语义
// （server.http.driver = "grpc-gateway"，与 WithGatewayRegister 的既有
// 约定一致；独立端口拓扑用契约段 server.gateway，不经快捷方式）。
const gatewayDriver = "grpc-gateway"

// validateAppSpec 校验 AppSpec 声明（Gate1），返回全部违例（聚合报错，
// 每条含字段路径与非法值）。在 normalizeBootstrap 之前调用。
func validateAppSpec(as *appspecv1.AppSpec) []error {
	var errs []error
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	if as.GetMeta().GetName() == "" {
		add("meta.name is required (drives cmd/<name>/ and configs/<name>.yaml)")
	}
	if as.GetMeta().GetModule() == "" {
		add("meta.module is required (generated files import <module>/internal/...)")
	}

	// ---- components：kind 白名单 + name 唯一 ----
	seenComp := map[string]int{}
	for i, c := range as.GetComponents() {
		if !componentKinds[c.GetKind()] {
			kindList := make([]string, 0, len(componentKinds))
			for k := range componentKinds {
				kindList = append(kindList, k)
			}
			slices.Sort(kindList)
			add("components[%d].kind %q is not supported (field components.kind; whitelist: %s)",
				i, c.GetKind(), strings.Join(kindList, ", "))
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

	// ---- domains：name 非空且唯一 + go_package 必填 + import 别名不冲突 ----
	seenDomain := map[string]int{}
	seenAlias := map[string]int{}
	for i, d := range as.GetDomains() {
		if d.GetName() == "" {
			add("domains[%d].name is required (field domains.name)", i)
			continue
		}
		if prev, dup := seenDomain[d.GetName()]; dup {
			add("domains[%d].name %q duplicates domains[%d].name (field domains.name must be unique)", i, d.GetName(), prev)
		} else {
			seenDomain[d.GetName()] = i
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

	// ---- variants：build tag 分叉进声明（Q7）----
	seenVariant := map[string]bool{}
	for i, vr := range as.GetVariants() {
		name := vr.GetName()
		switch {
		case name == "":
			add("variants[%d].name is required (drives the ambient Subject \"variant:<name>\")", i)
		case !variantNameRe.MatchString(name):
			add("variants[%d].name %q is invalid (must match %s; used as the ambient Subject suffix)", i, name, variantNameRe)
		case seenVariant[name]:
			add("variants[%d].name %q is declared twice (variant names must be unique)", i, name)
		default:
			seenVariant[name] = true
		}
		tags := vr.GetBuildTags()
		if len(tags) == 0 {
			add("variants[%d].build_tags is empty (a variant without tags would always match)", i)
		}
		seenTag := map[string]bool{}
		for j, t := range tags {
			if strings.TrimSpace(t) == "" {
				add("variants[%d].build_tags[%d] is empty", i, j)
				continue
			}
			if strings.TrimSpace(t) != t {
				add("variants[%d].build_tags[%d] %q has surrounding whitespace", i, j, t)
			}
			if seenTag[t] {
				add("variants[%d].build_tags[%d] %q is duplicated", i, j, t)
			}
			seenTag[t] = true
		}
	}

	// ---- 快捷方式 ⇄ bootstrap 投影一致（bootstrap 非空时；消灭双真相源）----
	bs := as.GetBootstrap()
	if bootstrapEmpty(bs) {
		return errs // bootstrap 为空：快捷方式由 normalizeBootstrap 归一化为投影
	}
	sv := as.GetServer()
	if sv != nil {
		if sv.GetHttp() && bs.GetServer().GetHttp() == nil {
			add("server.http=true but bootstrap.server.http is absent (shortcut must agree with the bootstrap projection)")
		}
		if a := sv.GetHttpAddr(); a != "" && bs.GetServer().GetHttp().GetAddr() != a {
			add("server.http_addr=%q but bootstrap.server.http.addr=%q (fields server.http_addr vs bootstrap.server.http.addr must agree)",
				a, bs.GetServer().GetHttp().GetAddr())
		}
		if sv.GetGrpc() && bs.GetServer().GetGrpc() == nil {
			add("server.grpc=true but bootstrap.server.grpc is absent (shortcut must agree with the bootstrap projection)")
		}
		if a := sv.GetGrpcAddr(); a != "" && bs.GetServer().GetGrpc().GetAddr() != a {
			add("server.grpc_addr=%q but bootstrap.server.grpc.addr=%q (fields server.grpc_addr vs bootstrap.server.grpc.addr must agree)",
				a, bs.GetServer().GetGrpc().GetAddr())
		}
		if sv.GetGateway() {
			samePort := bs.GetServer().GetHttp().GetDriver() == gatewayDriver
			independent := bs.GetServer().GetGateway() != nil
			if !samePort && !independent {
				add("server.gateway=true but neither bootstrap.server.http.driver=%q nor bootstrap.server.gateway is declared "+
					"(fields server.gateway vs bootstrap.server.http.driver/bootstrap.server.gateway must agree)", gatewayDriver)
			}
		}
	}
	if backends := as.GetAuditBackends(); len(backends) > 0 {
		declared := bs.GetAudit().GetBackends()
		if !slices.Equal(backends, declared) {
			add("audit_backends=%v but bootstrap.audit.backends=%v (field audit_backends vs bootstrap.audit.backends must agree, including order)",
				backends, declared)
		}
	}
	if as.GetBundleNormalized() && !as.GetBundle().GetNormalized() {
		add("bundle_normalized=true but bundle.normalized is false (fields bundle_normalized vs bundle.normalized must agree; prefer bundle.normalized)")
	}
	if an := bs.GetApp().GetName(); an != "" && as.GetMeta().GetName() != "" && an != as.GetMeta().GetName() {
		add("bootstrap.app.name=%q but meta.name=%q (contract app.name defaults to meta.name; declare it in at most one place)",
			an, as.GetMeta().GetName())
	}
	return errs
}

// bootstrapEmpty 报告契约段是否整体为空（nil 或全字段零值）——为空时快捷
// 方式承担真相源职责，由 normalizeBootstrap 归一化为投影。
func bootstrapEmpty(bs *bootstrapv1.BootstrapConfig) bool {
	return bs == nil || proto.Equal(bs, &bootstrapv1.BootstrapConfig{})
}

// projectServer 把 ServerSpec 快捷方式投影为契约 server 段（AppSpec.server
// 与 RoleSpec.server 共用同一份投影——双真相源消亡）。未声明任何传输面时
// 返回 nil：段缺席即「不配置」，L3 段覆盖检查无从要求覆盖。
func projectServer(sv *appspecv1.ServerSpec) *bootstrapv1.Server {
	if sv == nil {
		return nil
	}
	var out *bootstrapv1.Server
	if sv.GetHttp() || sv.GetHttpAddr() != "" || sv.GetGateway() {
		driver := ""
		if sv.GetGateway() {
			driver = gatewayDriver
		}
		out = &bootstrapv1.Server{Http: &bootstrapv1.Server_Http{Addr: sv.GetHttpAddr(), Driver: driver}}
	}
	if sv.GetGrpc() || sv.GetGrpcAddr() != "" {
		if out == nil {
			out = &bootstrapv1.Server{}
		}
		out.Grpc = &bootstrapv1.Server_Grpc{Addr: sv.GetGrpcAddr()}
	}
	return out
}

// normalizeBootstrap 把「bootstrap 为空」的 AppSpec 的快捷方式归一化为契约
// 投影（旧 6 字段 spec 的兼容通道；bootstrap 非空时原样返回——一致性已由
// Gate1 校验）。此后 config 发射与能力探测只认归一化结果，双真相源消亡。
func normalizeBootstrap(as *appspecv1.AppSpec) *bootstrapv1.BootstrapConfig {
	bs := as.GetBootstrap()
	if !bootstrapEmpty(bs) {
		return bs
	}
	out := &bootstrapv1.BootstrapConfig{}
	out.Server = projectServer(as.GetServer())
	if len(as.GetAuditBackends()) > 0 {
		out.Audit = &bootstrapv1.Audit{Backends: slices.Clone(as.GetAuditBackends())}
	}
	return out
}

// ---------------------------------------------------------------------------
// 字段消费机器守卫（D2 §3.5 G8 的通用解：声明字段必须有消费者）
// ---------------------------------------------------------------------------

// fieldConsumers 是「声明字段 → 生成物标记」表：key = proto 字段全名，
// value = 消费者必须在生成物文本中留下的标记。遍历 proto 描述符逐字段校验，
// ServerSpec.gateway 型死声明从此不可能再上线（新增字段忘接消费者 = 测试红）。
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
	"bald.appspec.v1.AppSpec.bundle.normalized":             "bundle.Normalized(",
	"bald.appspec.v1.AppSpec.domains.name":                  "greet",
	"bald.appspec.v1.AppSpec.domains.proto_package":         "greet.v1",
	"bald.appspec.v1.AppSpec.domains.service":               "greet.v1.GreetService",
	"bald.appspec.v1.AppSpec.domains.model":                 "internal/apiserver/model/greet.go",
	"bald.appspec.v1.AppSpec.domains.go_package":            "demo.example/app/internal/apiserver/greetv1",
	"bald.appspec.v1.AppSpec.variants.name":                 "variant:grpcgw",
	"bald.appspec.v1.AppSpec.variants.build_tags":           `"grpcgw"`,
	"bald.appspec.v1.AppSpec.variants.description":          "gateway wiring behind the grpcgw tag",
}

// builderConsumed 是「由树构建器直接消费（不落生成物文本）」的字段集合：
// 快捷方式（server/audit_backends/bundle_normalized）的消费 = Gate1 投影
// 一致性校验 + normalizeBootstrap 归一化；bootstrap 段的消费 = Gate2 契约
// 门 + 配置反射发射；layout.root 的消费 = 产出根定位。仍须登记——漏登即
// 死声明（与 fieldConsumers 同一守卫）。
var builderConsumed = map[string]bool{
	"bald.appspec.v1.AppSpec.server":            true,
	"bald.appspec.v1.AppSpec.server.http":       true,
	"bald.appspec.v1.AppSpec.server.grpc":       true,
	"bald.appspec.v1.AppSpec.server.gateway":    true,
	"bald.appspec.v1.AppSpec.server.http_addr":  true,
	"bald.appspec.v1.AppSpec.server.grpc_addr":  true,
	"bald.appspec.v1.AppSpec.audit_backends":    true,
	"bald.appspec.v1.AppSpec.bundle_normalized": true,
	"bald.appspec.v1.AppSpec.bootstrap":         true,
	"bald.appspec.v1.AppSpec.layout.root":       true,
	// meta.roles：消费 = Gate1 角色校验（本文件 roles 块：name 唯一 +
	// domains 引用 + 并集覆盖）+ 树构建器按角色切分 cmd/<app>-<role>/ 与
	// configs/<app>-<role>.yaml（不落可断言的单一标记，故与快捷方式
	// server.* 同类登记）。
	"bald.appspec.v1.AppSpec.meta.roles":                  true,
	"bald.appspec.v1.AppSpec.meta.roles.name":             true,
	"bald.appspec.v1.AppSpec.meta.roles.domains":          true,
	"bald.appspec.v1.AppSpec.meta.roles.server":           true,
	"bald.appspec.v1.AppSpec.meta.roles.server.http":      true,
	"bald.appspec.v1.AppSpec.meta.roles.server.grpc":      true,
	"bald.appspec.v1.AppSpec.meta.roles.server.gateway":   true,
	"bald.appspec.v1.AppSpec.meta.roles.server.http_addr": true,
	"bald.appspec.v1.AppSpec.meta.roles.server.grpc_addr": true,
}

// appspecLocal 报告消息是否属于 appspec 本地声明面（守卫只遍历 AppSpec
// 根上的本地消息；bootstrap 子树是 bconf 契约自己的对账域——其每个字段的
// 消费由 bconf 的契约测试与 L3 覆盖检查守卫，不在本表重复登记）。
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
						// 与实例是否置位无关。此前空列表直接跳过，重复消息的
						// 子字段（domains/components/capability.requires/roles）全在
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
				// 树构建器直接消费（Gate1 投影校验 / normalizeBootstrap /
				// Gate2 契约门 / 产出根定位）。
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
