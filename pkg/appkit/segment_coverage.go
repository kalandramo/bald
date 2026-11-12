// segment_coverage.go 实现 L3 段覆盖检查（启动期 fail-fast）。
//
// 背景：BootstrapConfig 声明 17 个顶层段（bconf/proto/bootstrap/v1/
// bootstrap.proto:30-81）。「契约段 → 消费者」对应关系靠人工枚举极易漏加，
// 漏加即静默无消费——script 段是已发生的实例（全仓零消费者，
// 用户配置静默失效）。本检查用 protobuf 反射枚举 cfg 中实际出现的顶层段，
// 减去本次装配真实被消费的段与白名单（WithUnimplementedSegments），剩余即
// 启动期报错并列出段名。L4（段名拼错）由同一次检查覆盖：拼错 ⇒ 该段无人覆盖。
//
// server 段的双层判定：
//   - 顶层：声明了协议能力（WithHTTP/WithGRPC，或转码面 Provider 接管
//     server.http）、存在非内置协议的 Provider（server.asynq 等）、或段与
//     bconf.NewBootstrap() 预填默认 proto.Equal 深相等（预填是框架自己的行为，对其报错属自相
//     矛盾的噪音）→ 已消费；用户改过任一子字段且零能力声明 → 报错。
//   - 子段：非内置子段（http/grpc 之外）若被某 Provider 以 "server.X" 路径
//     接管则合法，否则报错（替代 bootstrap.validateServerSections 的
//     implemented 手工表——子段存在性由反射给出，新子段自动纳入）。
//
// 其它族的子段同判：族顶层被消费 ≠ 族内每个子段都有实现。broker.redis 有
// Provider 不代表 broker.kafka 会被装配——只点亮族顶层会让同族未实现子段
// 静默失效。因此逐族枚举已出现的子段，要求被 Provider 以 "<族>.<子段>" 精确
// 路径接管（或白名单豁免）。两种整体接管跳过子段检查：Provider 直接声明族
// 顶层（config/tracer/metrics/audit 等，域内子段由该 Provider 自行解释）、
// registry 段被 Registrar 显式实例接管（程序化接管 = 刻意覆盖）。
//
// 调用点：assemble 的 BeforeStart 装配链末尾（契约已装载定型、各段消费与
// 否已见分晓）——文件装载贡献的段（如 config 层带入的 script）在此处才能
// 被枚举到。热更路径不重跑本检查。
package appkit

import (
	"errors"
	"fmt"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	bconf "github.com/kalandramo/bald/bconf"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// builtinServerSegments 是框架内置协议子段（std server Provider 的管辖面）。
// 其余子段的合法性由「是否有 Provider 以 server.X 路径接管」判定。
var builtinServerSegments = map[string]bool{"http": true, "grpc": true}

// presentTopLevelSegments 用 protobuf 反射枚举 cfg 中实际出现的顶层段
// （消息型字段非 nil），按字段声明序返回段名（报错清单顺序确定，与契约
// 字段序一致）。消费本函数的检查点位于全部段读取之后，故新增顶层段若
// 尚无消费者会自动落入报错清单（安全缺省，与 script 同类）。
func presentTopLevelSegments(cfg *bootstrapv1.BootstrapConfig) []string {
	return presentChildSegments(cfg.ProtoReflect())
}

// checkSegmentCoverage 检查「实际出现的顶层段 − 本次装配被消费的段 − 白名单」
// 与 server 非内置子段的接管情况，有剩余即返回启动期 error（fail-closed）。
func checkSegmentCoverage(cfg *bootstrapv1.BootstrapConfig, spec *Decl, plan *providerPlan) error {
	covered := consumedSegments(cfg, spec, plan)
	var problems []string

	var uncovered []string
	for _, s := range presentTopLevelSegments(cfg) {
		if !covered[s] {
			uncovered = append(uncovered, s)
		}
	}
	if len(uncovered) > 0 {
		problems = append(problems, fmt.Sprintf(
			"top-level sections with no consumer: %s\n"+
				"  hint: 该段无消费者，配置不会生效；引入对应 Provider、经 appkit.WithUnimplementedSegments 显式豁免，或从契约移除该段",
			strings.Join(uncovered, ", ")))
	}

	// server 非内置子段：无 Provider 接管（或未整体豁免）即报错——契约段
	// 存在但框架没有对应协议实现，静默启动会让用户误以为协议面已监听
	// （原 validateServerSections 的语义，检查点与文案措辞保留）。
	if srv := cfg.GetServer(); srv != nil && !plan.unimplemented["server"] && !plan.ownsPath("server") {
		triggered := plan.triggeredPaths(cfg)
		var sub []string
		for _, child := range presentChildSegments(srv.ProtoReflect()) {
			if builtinServerSegments[child] {
				continue // http/grpc 由内置 server Provider 按能力声明装配
			}
			path := "server." + child
			if triggered[path] || plan.unimplemented[path] {
				continue
			}
			sub = append(sub, fmt.Sprintf(
				"server.%s is configured but has no implementation in this repo "+
					"(remove the section, provide a Provider for %q, or whitelist it via appkit.WithUnimplementedSegments)%s", child, path, hintSuffix(path)))
		}
		problems = append(problems, sub...)
	}

	problems = append(problems, familySubSegmentProblems(cfg, spec, plan, covered)...)

	if len(problems) == 0 {
		return nil
	}
	return errors.New("appkit: bootstrap config declares sections with no consumer:\n  " + strings.Join(problems, "\n  "))
}

// providerHints 把框架自有段的「段路径 → 佐证性 contract 包」做成**例外表**，
// 供 L3 报错指路（DX-13）：报错说 "provide a Provider"，新手下一个问题是
// "哪个包"。规则路径（server.<face>/broker.<backend>/client.*/log.<x>）由
// providerHint 前缀派生，不入表；registry 走 Registrar 接管、logger/audit
// 由 std 装配，同样不入表。表的完备性不承诺（新增例外段记得回填）——它是
// 提示不是契约。
var providerHints = map[string]string{
	// 例外表：仅收录**不能**按「server.<face>/broker.<backend>/log.<x> → 对应
	// contract 包」规则派生的段（database/cache/oss/metrics 的实现不在同名
	// 一级目录）。规则路径走 providerHint 的前缀派生，不入表。
	"database.sql":       "contrib/database/gorm/contract",
	"database.mongodb":   "contrib/database/mongodb/contract",
	"cache.local":        "cache/local/contract",
	"cache.redis":        "cache/redis/contract",
	"oss.s3":             "oss/s3/contract",
	"oss.minio":          "oss/minio/contract",
	"metrics.prometheus": "metrics/prometheus/contract",
	"metrics.otel":       "metrics/otel/contract",
	"metrics.datadog":    "metrics/datadog/contract",
}

// providerHint 返回段路径的佐证性提示（最长前缀命中；无匹配返回空串）。
func providerHint(path string) string {
	if h, ok := providerHints[path]; ok {
		return h
	}
	// server.<face> 与 broker.<backend> 两族按两段前缀派生 contract 包路径：
	// server.kafka → transport/kafka/contract；broker.redis → broker/redis/contract。
	// transport/<face> 的 contract 包与 face 同名成对（见仓库 contract 清单）。
	if strings.HasPrefix(path, "server.") {
		face := strings.TrimPrefix(path, "server.")
		if face == "http" || face == "grpc" {
			return "" // 内置面，不缺实现
		}
		return "transport/" + face + "/contract"
	}
	if strings.HasPrefix(path, "broker.") {
		return "broker/" + strings.TrimPrefix(path, "broker.") + "/contract"
	}
	if strings.HasPrefix(path, "client.") {
		return "transport/client/contract"
	}
	if strings.HasPrefix(path, "log.") {
		return "log/" + strings.TrimPrefix(path, "log.") + "/contract"
	}
	return ""
}

// hintSuffix 组装报错尾部的指路分句（有建议才返回）。
func hintSuffix(path string) string {
	if h := providerHint(path); h != "" {
		return fmt.Sprintf("——框架自带契约包 %s（在 customOptions 里接它的 AppProvider）", h)
	}
	return ""
}

// familySubSegmentProblems 逐族检查已出现的子段是否有实现（server 由上面的
// 能力语义单独处理，此处跳过）。整族归属（Provider 直接声明族顶层）、整族
// 白名单、registry 被 Registrar 程序化接管三种情形跳过——这些情况下子段由
// 接管方自行解释，框架无从判断域内选项是否生效。
func familySubSegmentProblems(cfg *bootstrapv1.BootstrapConfig, spec *Decl, plan *providerPlan, covered map[string]bool) []string {
	triggered := plan.triggeredPaths(cfg)
	root := cfg.ProtoReflect()
	fields := root.Descriptor().Fields()
	var problems []string
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if fd.Kind() != protoreflect.MessageKind || fd.IsList() {
			continue
		}
		name := fd.TextName()
		if name == "server" || !covered[name] {
			continue
		}
		if plan.ownsPath(name) || plan.unimplemented[name] {
			continue
		}
		if name == "registry" && spec.Registrar != nil {
			continue
		}
		msg := root.Get(fd).Message()
		if !msg.IsValid() {
			continue
		}
		for _, child := range presentChildSegments(msg) {
			path := name + "." + child
			if triggered[path] || plan.unimplemented[path] {
				continue
			}
			problems = append(problems, fmt.Sprintf(
				"%s is configured but has no implementation in this repo "+
					"(remove the section, provide a Provider for %q, or whitelist it via appkit.WithUnimplementedSegments)%s", path, path, hintSuffix(path)))
		}
	}
	return problems
}

// consumedSegments 计算本次装配真实消费的顶层段。逐条对应段驱动装配的
// 真实消费路径（以代码为准，勿凭段名推测）：
//
//   - app：无条件消费——元数据在装配构造期读入 kitOpts（bootstrap.go 的
//     ID/Name/Version/Env/StopTimeout）；不在 Provider 循环（消费无条件，
//     与「段在才构建」语义不同构）。
//   - logger：无条件消费——阶段 A 回退默认 bslog、阶段 B/热更按契约重建
//     （段缺失 = 默认后端而非跳过）；logger Provider 覆盖经专用通道。
//   - registry / tracer / metrics / database / cache / storage / ai /
//     workflow / broker / audit：**段在而 Provider 声明了该段**即消费——
//     阶段 B 循环按 canonical 序构建（runProviderLoop），构建失败 fail-fast
//     能走到检查点即代表已消费。registry 段被 Registrar 显式实例接管时视
//     为已消费（程序化接管 = 刻意覆盖）。
//   - config：仅经 config 段 Provider 消费（构造期产层）。ConfigFile /
//     RemoteConfig 走本地文件与远程桥通道，不是契约 config 段。
//   - server：能力声明（WithHTTP/WithGRPC）、接管 server.http 的转码面
//     Provider、非内置协议 Provider 触发、或与 bconf.NewBootstrap() 预填默认深相等
//     （proto.Equal 预填豁免）→ 已消费；零能力声明且用户改过任一
//     子字段即报错（该段配置确实不产生任何服务器）。
//
// 之外的一切段名（含 script 与未来新增、本函数尚未认识的段）默认无消费者，
// 由调用方报错——白名单（WithUnimplementedSegments）是唯一豁免出口。
func consumedSegments(cfg *bootstrapv1.BootstrapConfig, spec *Decl, plan *providerPlan) map[string]bool {
	m := map[string]bool{"app": true, "logger": true}
	for path := range plan.triggeredPaths(cfg) {
		m[segmentTop(path)] = true
	}
	if spec.Registrar != nil {
		m["registry"] = true // 显式实例接管（程序化接管按已消费）
	}
	if plan.ownsPath("config") {
		m["config"] = true // config Provider 无条件构建（源可独立于 config 段存在）
	}
	if serverCovered(cfg, spec, plan) {
		m["server"] = true
	}
	for name := range plan.unimplemented {
		m[segmentTop(name)] = true
	}
	return m
}

// serverCovered 按预填豁免语义判定 server 段是否已消费（见包注释）。
func serverCovered(cfg *bootstrapv1.BootstrapConfig, spec *Decl, plan *providerPlan) bool {
	if hasHTTPFace(spec) || spec.GRPCRegister != nil {
		return true // 能力声明在代码：内置 server Provider 必然构造对应面
	}
	// server.http 被非内置 Provider 接管（grpc-gateway 转码面）时该段有消费者，
	// 但 builtinServerSegments 按段名把 "http" 归为内置——归属者才是判据。
	if plan.httpFaceTakenByProvider() {
		return true
	}
	if proto.Equal(cfg.GetServer(), bconf.NewBootstrap().GetServer()) {
		return true // 预填豁免：框架自填非用户意图
	}
	// 非内置协议 Provider（server.asynq 等）触发即消费——用户显式接入了
	// 第三方协议面，server 段确有消费者。
	for path := range plan.triggeredPaths(cfg) {
		if segmentTop(path) == "server" && !builtinServerSegments[strings.TrimPrefix(path, "server.")] {
			return true
		}
	}
	return false
}
