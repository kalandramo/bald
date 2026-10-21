// Package std 打包框架自有后端的默认 Provider 集合。
//
// std.Standard() 返回一个 Option，内含框架**自有**后端的段驱动 Provider
// （对账基准 = 框架自有后端的实际默认接线，不多不少）：
//
//   - server（server.http / server.grpc）：协议服务器构造——能力声明在代码
//     （WithHTTP/WithGatewayRegister/WithGRPC），契约段驱动地址/TLS/driver
//     （appkit.StandardServerProvider）；
//   - audit（audit.backends 中的 "log" 项）：结构化日志审计后端，核心包
//     零依赖开箱即用（appkit.StandardAuditProvider；store/stream 属第三方
//     集成，见下）；
//   - logger（契约 logger.backends）：bslog（标准库 slog）后端——B7 起唯一
//     内置日志通道（appkit.StandardLoggerProvider；阶段 A 引导 logger 的
//     持有者。第三方日志后端经用户 logger Provider 覆盖，段路径末者胜）。
//
// 以下各段在原 12 段流里**没有默认接线**，故不进 std（「不多不少」对账）：
//   - registry / database / cache / storage / ai / workflow / broker /
//     tracer / metrics / config：均为第三方后端域（nacos/etcd、mysql、
//     redis、minio…或仅远程源描述）——段存在而无 Provider 时由 L3 覆盖
//     检查启动期报错（见 appkit.segment_coverage）；
//   - app：消费无条件（段缺失 = 默认元数据而非跳过）。logger 段已收编为
//     上方 StandardLoggerProvider（两阶段重建+热更协议仍在引擎——loggerSource
//     通道，零声明时引擎回退同一内置 Provider）。
//
// 实现落点说明：Provider 实现体住在 pkg/appkit
// （standard.go）而非本包——两者都深度消费 appkit 内部件（Decl 能力声明、
// 探针包装、产物入库），且包内测试需要不经 import 环引用；本包是纯打包面。
//
// 第三方后端（nacos/etcd/loki/minio/asynq/gateway…）一律**不进 std**：它们
// 经各自的 contrib/contract 包导出 Provider（加法式，范本见 contrib/store-gorm
// 的 Provider），显式 import 后与 std.Standard() 并列传入 MustServe——
// 「未 import 的后端零编译成本、依赖图可见」的原有哲学保持不变。
//
// 用法：
//
//	appkit.MustServe(ctx,
//	    std.Standard(),
//	    storegorm.Provider(),        // 第三方后端：加法式 Provider
//	    appkit.WithGRPC(registerSvc),
//	    ...)
package std

import (
	"github.com/kalandramo/bald/pkg/appkit"
)

// Standard 返回框架自有后端的默认 Provider 集合（一个 Option；成员可被
// 后声明的同段 Provider 覆盖——段路径末者胜，见 appkit.Provider）。
func Standard() appkit.Option {
	return appkit.Providers(
		appkit.StandardServerProvider(),
		appkit.StandardAuditProvider(),
		appkit.StandardLoggerProvider(),
	)
}
