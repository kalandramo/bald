// segment_coverage.go 实现 L3 段覆盖告警（装配层评估 §5.2 批 1：先做成
// 告警，批 2 的 StrictSegments 才转报错 + 白名单）。
//
// 背景：BootstrapConfig 声明 15 个顶层段（bconf/proto/bootstrap/v1/
// bootstrap.proto:26-71），但「契约段 → 消费者」靠 buildDatabases 等装配
// 函数人工维系，漏加即静默无消费——script 段是已发生的实例（全仓零消费
// 者，用户配置静默失效）。本检查用 protobuf 反射枚举 cfg 中实际出现的
// 顶层段，减去本次装配真实被消费的段，剩余即打一条 warning：不报错、
// 不做白名单。
//
// 调用点：assemble 的 BeforeStart 装配链末尾（契约已装载定型、各段
// 消费与否已见分晓）——见 consumedSegments 的注释。
package appkit

import (
	"context"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	bconf "github.com/kalandramo/bald/bconf"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	log "github.com/kalandramo/bald/log"
)

// presentTopLevelSegments 用 protobuf 反射枚举 cfg 中实际出现的顶层段
// （消息型字段非 nil），按字段声明序返回段名（警告清单顺序确定，与契约
// 字段序一致）。消费本函数的检查点位于全部段读取之后，故新增顶层段若
// 尚无消费者会自动落入告警清单（安全缺省，与 script 同类）。
func presentTopLevelSegments(cfg *bootstrapv1.BootstrapConfig) []string {
	var out []string
	m := cfg.ProtoReflect()
	fields := m.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if fd.Kind() != protoreflect.MessageKind {
			continue // 顶层段均为消息型；标量字段无消息 presence，跳过
		}
		if m.Get(fd).Message().IsValid() {
			out = append(out, fd.TextName())
		}
	}
	return out
}

// consumedSegments 计算本次装配真实消费的顶层段。逐条对应约定装配
// 的真实消费路径（以代码为准，勿凭段名推测）：
//
//   - app / logger：无条件消费——app 元数据在装配构造期读入
//     kitOpts（bootstrap.go 的 ID/Name/Version/Env/StopTimeout）；logger 由
//     BeforeStart 的 rebuildLogger 无条件按契约重建（段缺失时回退默认后端）。
//   - registry / tracer / metrics / database / cache / storage / ai /
//     workflow / broker / audit：fail-fast 守卫域——各 build* 对「段存在而
//     Registry 未接线」一律启动期报错（buildRegistrar / buildObservability /
//     六资源域 / buildAudit），能走到装配链末尾的检查点即代表段已被消费
//     （registry 段被 WithRegistrar 显式实例接管时，视为该域已被消费——
//     是刻意覆盖而非静默失效）。
//   - config：仅经 WithConfigRegistry 消费——ConfigRegistry.Build 是
//     cfg.GetConfig() 的唯一读取路径（bootstrap/build.go 各配置源）；而
//     WithConfigFile / WithRemoteConfig 走的是本地文件与远程源通道，不是
//     契约 config 段。
//   - server：仅在声明了服务器能力（WithHTTP / WithGatewayRegister /
//     WithGRPC）或注入 WithServerRegistry 时消费——此时 Run 期
//     BuildServers 必然运行并对已出现的子段逐个查表（未实现子段 fail-fast）；
//     未声明任何能力时 server.* 配置不产生任何服务器（刻意语义，见
//     「能力声明在代码」的约定），段值虽经 Bind 参与合并，
//     但无装配消费——照实告警。唯一豁口：server 段与 bconf.NewBootstrap()
//     的预填默认值深相等（proto.Equal）时视为已消费——预填是框架自己的
//     行为，对其告警属自相矛盾的噪音；用户改过其中任何子字段（哪怕只改
//     addr）即偏离默认，照常进入告警清单（该段配置确实不生效）。
//
// 之外的一切段名（含 script 与未来新增、本函数尚未认识的段）默认无消费者，
// 由调用方告警。
func consumedSegments(cfg *bootstrapv1.BootstrapConfig, spec *Decl, serverWired bool) map[string]bool {
	m := make(map[string]bool, 14)
	for _, s := range []string{
		"app", "logger",
		"registry", "tracer", "metrics", "database", "cache", "storage",
		"ai", "workflow", "broker", "audit",
	} {
		m[s] = true
	}
	if spec.ConfigRegistry != nil {
		m["config"] = true
	}
	if serverWired || proto.Equal(cfg.GetServer(), bconf.NewBootstrap().GetServer()) {
		m["server"] = true
	}
	return m
}

// warnUnconsumedSegments 检查「实际出现的顶层段 − 本次装配被消费的段」，
// 有剩余则打一条 warning 列出未覆盖段名。告警级：不报错、不拦截启动——
// 文案给出可行动的出路（等实现或从契约移除）。
func warnUnconsumedSegments(ctx context.Context, cfg *bootstrapv1.BootstrapConfig, spec *Decl, serverWired bool) {
	consumed := consumedSegments(cfg, spec, serverWired)
	var uncovered []string
	for _, s := range presentTopLevelSegments(cfg) {
		if !consumed[s] {
			uncovered = append(uncovered, s)
		}
	}
	if len(uncovered) == 0 {
		return
	}
	log.Warn(ctx, "appkit: bootstrap config has top-level sections with no consumer",
		"sections", strings.Join(uncovered, ", "),
		"hint", "该段无消费者，配置不会生效；若为尚未支持的段请等待实现，或从契约移除")
}
