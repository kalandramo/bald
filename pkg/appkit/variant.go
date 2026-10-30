package appkit

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"google.golang.org/protobuf/reflect/protoreflect"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// VariantDecl 是 AppSpec.variants 的运行期投影（Q7）：无论是否命中都进计划
// ——不带 tag 的构建也能回答「这个应用声明了哪些变体、命中了会加什么」；
// 命中的变体把声明增量（Effects）并入真实装配与计划。Active 由 Compile 按
// 当前二进制实际的构建约束求值算出，应用侧不猜 tag 组合。
type VariantDecl struct {
	Name string `json:"name"`
	// When 是 Go 构建约束表达式（`//go:build` 行语法，如
	// "grpcgw && !nacos"）；空 = 无条件（手写声明的「总是命中」形态，
	// 生成物必须声明条件——Gate1 拒绝空条件）。
	When string `json:"when,omitempty"`
	// BuildTags 是 When 的迁移糖：各项均须命中，等价于用 && 连接。
	// 与 When 互斥（同时声明在计划期报错），新声明请直接写 When。
	BuildTags []string `json:"buildTags,omitempty"`
	Detail    string   `json:"detail,omitempty"`
	// Effects 是变体命中时的装配增量（纯数据）：未命中也随声明进计划供审计，
	// 命中才真正并入装配。三种增量各有一条冲突规则（见 VariantEffect）。
	Effects VariantEffect `json:"effects,omitempty"`
	// Active 是命中结果（Compile 计算并写回；输入值被忽略）。
	Active bool `json:"active"`
}

// VariantEffect 是变体的装配增量：普通装配的一份声明式切片——命中时与手写
// 声明同等效力地进入计划与真实装配，带来源（variant:<name>）而非 ambient
// 逃逸舱（I11：已声明的变体不再产 ambient 诊断）。
//
// 冲突规则（I4「显式覆盖 + 理由」同款）：
//   - Components：组件名即节点身份，跨变体/应用重复是计划期错误（不猜谁赢）；
//   - Routes：与方法+路径（含 gin 通配形态）冲突时计划期报错，与域路由同一判据；
//   - Patches：改写契约里已存在的非空字符串字段必须给 Reason（产 variant-patch
//     诊断）；两个生效变体改写同一路径为不同值即编译错误。
type VariantEffect struct {
	Components []VariantComponent `json:"components,omitempty"`
	Routes     []VariantRoute     `json:"routes,omitempty"`
	Patches    []VariantPatch     `json:"patches,omitempty"`
}

// VariantComponent 是变体新增的进程内组件声明：Name 是组件身份（与
// Component.Name() 对账，计划节点键 component.<name>）；Config 是它读取的
// 契约段（如 "biz.telemetry"），进计划的段消费视图。
type VariantComponent struct {
	Name   string `json:"name"`
	Config string `json:"config,omitempty"`
}

// VariantRoute 是变体新增的 REST 路由（计划清单；真实挂载由 VariantImpl 的
// WrapHTTP 提供——声明与实现必须同时存在）。
type VariantRoute struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	RPC    string `json:"rpc,omitempty"`
}

// VariantPatch 是变体对 bootstrap 契约段的改写（点路径 + 字符串值）。路径必须
// 指向契约里以存在的字符串字段；覆盖非空值须给 Reason。
type VariantPatch struct {
	Path   string `json:"path"`
	Value  string `json:"value"`
	Reason string `json:"reason,omitempty"`
}

// VariantImpl 是变体的运行期实现（由 //go:build 文件注册，和声明的 When
// 必须一致——不一致在计划期报错，见 applyVariantEffects 的 I12 对账）：
//   - Components 与声明的 Components 按 Name 一一对账（少/多都报错）；
//   - WrapHTTP 在声明了 Routes 时必供：挂载声明路由；未声明 Routes 时不得提供；
//   - Opts 是该变体贡献的普通装配声明（能力面/Provider 等），与手写声明的
//     计划口径相同（计划看得见它们，没有 ambient 影子）。
type VariantImpl struct {
	Name       string
	Components []Component
	WrapHTTP   func(http.Handler) http.Handler
	Opts       []Option
}

// WithVariantDecls 声明构建期变体（可多次调用，追加；计划期按 Name 去重）。
func WithVariantDecls(vs ...VariantDecl) Option {
	return optionFunc(func(d *Decl) { d.Variants = append(d.Variants, vs...) })
}

// WithVariantImpl 注册变体的运行期实现（可多次调用；同一变体实现重复报错）。
func WithVariantImpl(impl VariantImpl) Option {
	return optionFunc(func(d *Decl) { d.VariantImpls = append(d.VariantImpls, impl) })
}

// variantProjection 把变体声明规范化为计划字段：Name 必填、重复 Name 报错
// （一个身份不能描述两个变体），When 与 BuildTags 合并为唯一表达式、按当前
// 二进制的构建约束求值出 Active；说明为空时补默认 Detail（说明必须能独立
// 读懂）。最终顺序由 Plan 收尾统一排序（置换不变）。
func variantProjection(vs []VariantDecl) ([]VariantDecl, error) {
	if len(vs) == 0 {
		return nil, nil
	}
	seen := make(map[string]bool, len(vs))
	out := make([]VariantDecl, 0, len(vs))
	for _, v := range vs {
		if v.Name == "" {
			return nil, fmt.Errorf("appkit: variant declaration requires a Name")
		}
		if seen[v.Name] {
			return nil, fmt.Errorf("appkit: variant %q declared twice", v.Name)
		}
		seen[v.Name] = true
		when, err := canonicalWhen(v)
		if err != nil {
			return nil, err
		}
		active, err := EvaluateBuildConstraint(when)
		if err != nil {
			return nil, fmt.Errorf("appkit: variant %q: %w", v.Name, err)
		}
		detail := v.Detail
		if detail == "" {
			if when == "" {
				detail = "variant declared in AppSpec.variants (unconditional)"
			} else {
				detail = "build-tag variant declared in AppSpec.variants (when: " + when + ")"
			}
		}
		out = append(out, VariantDecl{
			Name:      v.Name,
			When:      when,
			BuildTags: append([]string(nil), v.BuildTags...),
			Detail:    detail,
			Effects:   v.Effects,
			Active:    active,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// canonicalWhen 把 When/BuildTags 两种声明形式归一为唯一表达式：两者互斥，
// BuildTags 以 && 连接；结果必须是 go/build/constraint 能解析的表达式。
// 空表达式 = 无条件（仅手写声明可达；生成物由 Gate1 要求条件非空）。
func canonicalWhen(v VariantDecl) (string, error) {
	when := strings.TrimSpace(v.When)
	if when != "" && len(v.BuildTags) > 0 {
		return "", fmt.Errorf("appkit: variant %q declares both When and BuildTags (use When; BuildTags is join-by-&& sugar)", v.Name)
	}
	if when == "" && len(v.BuildTags) > 0 {
		tags := make([]string, 0, len(v.BuildTags))
		for _, t := range v.BuildTags {
			if t = strings.TrimSpace(t); t == "" {
				return "", fmt.Errorf("appkit: variant %q has an empty build tag", v.Name)
			}
			tags = append(tags, t)
		}
		when = strings.Join(tags, " && ")
	}
	if when == "" {
		return "", nil
	}
	if _, err := EvaluateBuildConstraint(when); err != nil {
		return "", fmt.Errorf("appkit: variant %q: %w", v.Name, err)
	}
	return when, nil
}

// variantWhenLabel 是变体条件的展示形态：无条件声明显式写 (always)，
// 不打印空串（doctor/--plan 的输出必须自解释）。
func variantWhenLabel(v VariantDecl) string {
	if v.When == "" {
		return "(always)"
	}
	return v.When
}

// rejectAmbientVariants 拒绝用手写 ambient 重复描述已声明的变体：声明面
// 只有一套身份空间（I11）——变体进计划就不能再有 ambient 旁白，否则同一
// 分叉会出现两种口径（声明增量 vs 注入说明）。
func rejectAmbientVariants(ambients []Ambient, vs []VariantDecl) error {
	if len(ambients) == 0 || len(vs) == 0 {
		return nil
	}
	declared := make(map[string]bool, len(vs))
	for _, v := range vs {
		declared[v.Name] = true
	}
	for _, a := range ambients {
		name, ok := strings.CutPrefix(a.Name, "variant:")
		if ok && declared[name] {
			return fmt.Errorf("appkit: variant %q is declared in AppSpec.variants; remove the WithAmbient(%q) escape (declared variants carry their effects in the plan)", name, a.Name)
		}
	}
	return nil
}

// variantEffectLines 是变体增量的可读行（--plan/doctor 共用）：未命中的
// 变体也列出增量声明，运维不需要命中才知道「这个分叉会加什么」。
func variantEffectLines(v VariantDecl) []string {
	var out []string
	for _, c := range v.Effects.Components {
		line := "component " + c.Name
		if c.Config != "" {
			line += " (segment " + c.Config + ")"
		}
		out = append(out, line)
	}
	for _, r := range v.Effects.Routes {
		line := "route " + strings.ToUpper(strings.TrimSpace(r.Method)) + " " + strings.TrimSpace(r.Path)
		if r.RPC != "" {
			line += " -> " + r.RPC
		}
		out = append(out, line)
	}
	for _, p := range v.Effects.Patches {
		line := "patch " + p.Path + "=" + p.Value
		if p.Reason != "" {
			line += " (reason: " + p.Reason + ")"
		}
		out = append(out, line)
	}
	return out
}

// variantPlan 是生效变体的计划视图：组件名→变体名（节点来源）、路由清单与
// 诊断。由 applyVariantEffects 产出，planFrom 消费；未生效变体只在
// VariantDecls 里出现，不产生节点/路由。
type variantPlan struct {
	variants   []VariantDecl
	components map[string]string
	routes     []Route
	warnings   []Diagnostic
}

// variantState 记录「效果已并入 Decl」的第一次应用结果：运行期配置变更重算
// 计划会对同一份 Decl 重复调用 applyVariantEffects，组件与 HTTP 包装不能装
// 两遍；补丁则每次重放（契约被重载后仍须生效，同值改写是幂等的）。
type variantState struct {
	plan    *variantPlan
	patches []VariantPatch
}

// applyVariantEffects 把变体声明并入装配：校验声明与实现的对账（I12），把
// 生效变体的组件、路由、契约补丁落到 Decl/cfg，并返回计划视图。纯函数边界
// 之外：它会改写调用方给的 Decl/cfg（位于 Compile/assemble 的装配前处理段，
// 与 composeDomainServices 同层）。
func applyVariantEffects(d *Decl, cfg *bootstrapv1.BootstrapConfig) (*variantPlan, error) {
	vs, err := variantProjection(d.Variants)
	if err != nil {
		return nil, err
	}
	if d.variantState != nil {
		// 二次调用：契约可能被重载（配置热更），补丁重放；组件/包装只应用一次。
		if err := replayVariantPatches(cfg, d.variantState.patches); err != nil {
			return nil, err
		}
		return d.variantState.plan, nil
	}

	vp := &variantPlan{variants: vs, components: map[string]string{}}
	if len(vs) == 0 && len(d.VariantImpls) == 0 {
		d.variantState = &variantState{plan: vp}
		return vp, nil
	}

	if err := validateVariantEffects(vs, cfg); err != nil {
		return nil, err
	}

	byName := make(map[string]VariantDecl, len(vs))
	for _, v := range vs {
		byName[v.Name] = v
	}
	impls := make(map[string]VariantImpl, len(d.VariantImpls))
	for _, impl := range d.VariantImpls {
		if impl.Name == "" {
			return nil, fmt.Errorf("appkit: variant implementation requires a Name")
		}
		if _, ok := impls[impl.Name]; ok {
			return nil, fmt.Errorf("appkit: variant %q implementation registered twice", impl.Name)
		}
		decl, ok := byName[impl.Name]
		if !ok {
			return nil, fmt.Errorf("appkit: variant %q implementation is not declared (add it to AppSpec.variants)", impl.Name)
		}
		if !decl.Active {
			// I12：编译进来的实现必须与声明的条件一致——不一致就是 //go:build
			// 约束漂移，让装配与声明各说各话。
			return nil, fmt.Errorf("appkit: variant %q implementation is compiled in but its condition %q is false; the //go:build constraint has drifted from the declaration", impl.Name, variantWhenLabel(decl))
		}
		impls[impl.Name] = impl
	}

	patches := map[string]variantPatchOwner{}
	var applied []VariantPatch
	for _, v := range vs {
		if !v.Active {
			continue
		}
		impl, hasImpl := impls[v.Name]
		eff := v.Effects

		// 组件：声明的名字与实现提供的组件必须一一对应。
		if len(eff.Components) > 0 {
			if !hasImpl {
				return nil, fmt.Errorf("appkit: variant %q declares %d component(s) but no implementation is compiled in", v.Name, len(eff.Components))
			}
			have := make(map[string]Component, len(impl.Components))
			for _, c := range impl.Components {
				have[c.Name()] = c
			}
			for _, c := range eff.Components {
				comp, ok := have[c.Name]
				if !ok {
					return nil, fmt.Errorf("appkit: variant %q declares component %q but the implementation does not provide it (I12: declaration and implementation must agree)", v.Name, c.Name)
				}
				delete(have, c.Name)
				if prev, ok := vp.components[c.Name]; ok {
					return nil, fmt.Errorf("appkit: component %q is added by both variant %q and %q; component names are plan node identities", c.Name, prev, v.Name)
				}
				vp.components[c.Name] = v.Name
				d.Components = append(d.Components, comp)
			}
			if extra := sortedComponentNames(have); len(extra) > 0 {
				return nil, fmt.Errorf("appkit: variant %q implementation provides undeclared component(s) %s (declare them in AppSpec.variants)", v.Name, strings.Join(extra, ", "))
			}
		} else if hasImpl && len(impl.Components) > 0 {
			return nil, fmt.Errorf("appkit: variant %q implementation provides component(s) %s but the declaration lists none", v.Name, strings.Join(componentNames(impl.Components), ", "))
		}

		// 路由：声明与 WrapHTTP 必须成对；挂载点必须是 HTTP 面。
		if len(eff.Routes) > 0 {
			if !hasImpl || impl.WrapHTTP == nil {
				return nil, fmt.Errorf("appkit: variant %q declares %d route(s) but the implementation provides no WrapHTTP", v.Name, len(eff.Routes))
			}
			if d.HTTPHandler == nil {
				return nil, fmt.Errorf("appkit: variant %q declares REST routes but the app has no HTTP face (WithHTTP)", v.Name)
			}
			d.HTTPHandler = impl.WrapHTTP(d.HTTPHandler)
			for _, r := range eff.Routes {
				vp.routes = append(vp.routes, Route{
					Domain:  "variant:" + v.Name,
					Method:  strings.ToUpper(strings.TrimSpace(r.Method)),
					Path:    strings.TrimSpace(r.Path),
					RPC:     r.RPC,
					Variant: v.Name,
				})
			}
		} else if hasImpl && impl.WrapHTTP != nil {
			return nil, fmt.Errorf("appkit: variant %q implementation wraps HTTP but the declaration lists no routes", v.Name)
		}

		// 实现自带的普通装配声明：能力面/Provider 等，按声明顺序末者胜。
		for _, opt := range impl.Opts {
			opt.Apply(d)
		}

		// 契约补丁：跨变体同一路径不同值即冲突（I4：不猜谁赢）。
		for _, p := range eff.Patches {
			if prev, ok := patches[p.Path]; ok && prev.value != p.Value {
				return nil, fmt.Errorf("appkit: bootstrap field %q is patched to %q by variant %q and to %q by variant %q; two active variants cannot disagree",
					p.Path, prev.value, prev.variant, p.Value, v.Name)
			}
			patches[p.Path] = variantPatchOwner{variant: v.Name, value: p.Value}
			applied = append(applied, p)
		}
	}

	warns, err := applyVariantPatches(cfg, applied)
	if err != nil {
		return nil, err
	}
	vp.warnings = warns
	d.variantState = &variantState{plan: vp, patches: applied}
	return vp, nil
}

// replayVariantPatches 在二次应用时重放补丁（契约可能被配置热更重载）。
func replayVariantPatches(cfg *bootstrapv1.BootstrapConfig, patches []VariantPatch) error {
	_, err := applyVariantPatches(cfg, patches)
	return err
}

// variantPatchOwner 是补丁的归属（跨变体冲突检测用）。
type variantPatchOwner struct {
	variant string
	value   string
}

// validateVariantEffects 校验全部声明增量的形状（未命中也校验——声明面必须
// 自洽，否则命中那天才发现就晚了）。
func validateVariantEffects(vs []VariantDecl, cfg *bootstrapv1.BootstrapConfig) error {
	components := map[string]string{}
	for _, v := range vs {
		for _, c := range v.Effects.Components {
			if strings.TrimSpace(c.Name) == "" {
				return fmt.Errorf("appkit: variant %q declares a component with an empty name", v.Name)
			}
			if prev, ok := components[c.Name]; ok {
				return fmt.Errorf("appkit: component %q is declared by variant %q and %q", c.Name, prev, v.Name)
			}
			components[c.Name] = v.Name
		}
		for _, r := range v.Effects.Routes {
			method := strings.ToUpper(strings.TrimSpace(r.Method))
			if !httpRoutingMethods[method] {
				return fmt.Errorf("appkit: variant %q route method %q is not a supported HTTP method", v.Name, r.Method)
			}
			if err := validateRouteShape("variant:"+v.Name, method, strings.TrimSpace(r.Path)); err != nil {
				return err
			}
		}
		for _, p := range v.Effects.Patches {
			if strings.TrimSpace(p.Path) == "" {
				return fmt.Errorf("appkit: variant %q declares a patch with an empty path", v.Name)
			}
			// 路径必须在契约 schema 里可解析（值的存在性在生效时校验——未生效
			// 变体不改契约，但路径写错是声明错误，任何构建都该报）。
			if _, err := bootstrapStringField(cfg, p.Path); err != nil {
				return fmt.Errorf("appkit: variant %q patch: %w", v.Name, err)
			}
		}
	}
	return nil
}

// bootstrapStringField 按点路径解析契约里的字符串字段（返回当前值）。路径
// 不存在、中间节点缺失或末字段非字符串都是声明错误。
func bootstrapStringField(cfg *bootstrapv1.BootstrapConfig, path string) (protoreflect.FieldDescriptor, error) {
	if cfg == nil {
		return nil, fmt.Errorf("bootstrap config is nil")
	}
	parts := strings.Split(path, ".")
	m := cfg.ProtoReflect()
	for i, name := range parts {
		if !m.IsValid() {
			return nil, fmt.Errorf("bootstrap field %q: parent section is not set", path)
		}
		fd := m.Descriptor().Fields().ByName(protoreflect.Name(name))
		if fd == nil {
			return nil, fmt.Errorf("bootstrap field %q does not exist", path)
		}
		if i == len(parts)-1 {
			if fd.Kind() != protoreflect.StringKind {
				return nil, fmt.Errorf("bootstrap field %q is not a string field", path)
			}
			return fd, nil
		}
		if fd.Kind() != protoreflect.MessageKind {
			return nil, fmt.Errorf("bootstrap field %q is not a message section", path)
		}
		m = m.Get(fd).Message()
	}
	return nil, fmt.Errorf("bootstrap field %q does not exist", path)
}

// applyVariantPatches 把补丁写到契约：覆盖非空值必须给 Reason（产
// variant-patch 诊断），同值改写幂等。返回诊断（不是错误——覆盖是显式声明的
// 逃逸舱，可见但不阻断）。
func applyVariantPatches(cfg *bootstrapv1.BootstrapConfig, patches []VariantPatch) ([]Diagnostic, error) {
	var warns []Diagnostic
	for _, p := range patches {
		fd, err := bootstrapStringField(cfg, p.Path)
		if err != nil {
			return nil, err
		}
		m := cfg.ProtoReflect()
		parts := strings.Split(p.Path, ".")
		for _, name := range parts[:len(parts)-1] {
			m = m.Get(m.Descriptor().Fields().ByName(protoreflect.Name(name))).Message()
		}
		prev := m.Get(fd).String()
		if prev == p.Value {
			continue
		}
		if prev != "" && p.Reason == "" {
			return nil, fmt.Errorf("appkit: variant patch %q overrides existing value %q; provide a Reason", p.Path, prev)
		}
		m.Set(fd, protoreflect.ValueOfString(p.Value))
		if prev != "" {
			warns = append(warns, Diagnostic{
				Level:   DiagWarn,
				Code:    "variant-patch",
				Subject: p.Path,
				Message: fmt.Sprintf("variant patch overrides %q with %q (reason: %s)", prev, p.Value, p.Reason),
			})
		}
	}
	return warns, nil
}

func componentNames(cs []Component) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Name())
	}
	sort.Strings(out)
	return out
}

func sortedComponentNames(byName map[string]Component) []string {
	out := make([]string, 0, len(byName))
	for n := range byName {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
