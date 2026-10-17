// segment_coverage.go 实现 L3 段覆盖检查（装配层评估 §4.2 fail-fast 表 L3，
// 裁决 2：批 1 为告警级，批 2 W2b 起升级为启动期 error）。
//
// 背景：BootstrapConfig 声明 15 个顶层段（bconf/proto/bootstrap/v1/
// bootstrap.proto:26-71），但「契约段 → 消费者」此前靠 buildDatabases 等装配
// 函数人工维系，漏加即静默无消费——script 段是已发生的实例（全仓零消费者，
// 用户配置静默失效）。本检查用 protobuf 反射枚举 cfg 中实际出现的顶层段，
// 减去本次装配真实被消费的段与白名单（WithUnimplementedSegments），剩余即
// 启动期报错并列出段名。L4（段名拼错）由同一次检查覆盖：拼错 ⇒ 该段无人覆盖。
//
// server 段的双层判定（沿用批 1 语义，评估文档 §9 第 1 条）：
//   - 顶层：声明了协议能力（WithHTTP/WithGatewayRegister/WithGRPC）、存在
//     非内置协议的 Provider（server.asynq 等）、或段与 bconf.NewBootstrap()
//     预填默认 proto.Equal 深相等（预填是框架自己的行为，对其报错属自相
//     矛盾的噪音）→ 已消费；用户改过任一子字段且零能力声明 → 报错。
//   - 子段：非内置子段（http/grpc 之外）若被某 Provider 以 "server.X" 路径
//     接管则合法，否则报错（替代 bootstrap.validateServerSections 的
//     implemented 手工表——子段存在性由反射给出，新子段自动纳入）。
//
// 调用点：assemble 的 BeforeStart 装配链末尾（契约已装载定型、各段消费与
// 否已见分晓）——文件装载贡献的段（如 config 层带入的 script）在此处才能
// 被枚举到（批 1 端到端测试实证）。热更路径不重跑本检查（批 1 语义）。
package appkit

import (
	"errors"
	"fmt"
	"strings"

	"google.golang.org/protobuf/proto"

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
	if srv := cfg.GetServer(); srv != nil && !plan.unimplemented["server"] {
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
					"(remove the section, provide a Provider for %q, or whitelist it via appkit.WithUnimplementedSegments)", child, path))
		}
		problems = append(problems, sub...)
	}

	if len(problems) == 0 {
		return nil
	}
	return errors.New("appkit: bootstrap config declares sections with no consumer:\n  " + strings.Join(problems, "\n  "))
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
//     为已消费（程序化接管 = 刻意覆盖，批 1 语义）。
//   - config：仅经 config 段 Provider 消费（构造期产层）。ConfigFile /
//     RemoteConfig 走本地文件与远程桥通道，不是契约 config 段。
//   - server：能力声明（WithHTTP/WithGatewayRegister/WithGRPC）、非内置
//     协议 Provider 触发、或与 bconf.NewBootstrap() 预填默认深相等
//     （proto.Equal，批 1 预填豁免）→ 已消费；零能力声明且用户改过任一
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
		m["registry"] = true // 显式实例接管（批 1 语义：程序化接管按已消费）
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

// serverCovered 按批 1 预填豁免语义判定 server 段是否已消费（见包注释）。
func serverCovered(cfg *bootstrapv1.BootstrapConfig, spec *Decl, plan *providerPlan) bool {
	if spec.HTTPHandler != nil || spec.GatewayRegister != nil || spec.GRPCRegister != nil {
		return true // 能力声明在代码：内置 server Provider 必然构造对应面
	}
	if proto.Equal(cfg.GetServer(), bconf.NewBootstrap().GetServer()) {
		return true // 预填豁免：框架自填非用户意图（批 1 语义原样沿用）
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
