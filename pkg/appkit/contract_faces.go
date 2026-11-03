// contract_faces.go 把 [WithHTTPContract] / [WithGRPCOptionsContract] 声明的
// 契约钩子求值成 Decl 里的具体装配声明（handler / 选项工厂）。
//
// 动机：契约 server.http.middleware 与 server.grpc.middleware 两段此前没有任何
// 框架消费者——bundle 的两座桥（FromMiddleware / FromGRPCMiddleware）翻译得对，
// 但没有任何组件在启动时读那两个段，「配置文件里写了限流」与「限流生效」之间
// 缺一次调用。本文件把这次调用做成装配的固定环节，同时把认证姿态留在业务代码里
// （契约没有 authn 段，框架不代替业务宣布「本服务公开」）。
//
// 求值时点 = 装配期（契约已装载、变体与域接线之前）：
//   - HTTP handler 必须在此定型——域接线（wireDomains 把 dom.Routes 挂到 handler）
//     在 BeforeStart 链末尾、迟后阶段（server 构造）之前，那个窗口要求 handler
//     已经存在；
//   - gRPC 侧与它同点求值，为了「两面的链都由同一份契约段驱动」只有一个实现点。
//   - 钩子声明字段（HTTPContract / GRPCOptionContractFns）求值后**不清空**：计划
//     投影据此认面，清空会让 --plan 与热更重算得到不同计划（I2）。
//
// 求值恰一次：热更新重算走 planFrom（不调本文件），middleware 段与 server 段
// 同为静态面——改了要重启，不假装能热更。

package appkit

import (
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// HasHTTPFace 报告声明里有没有 HTTP 面（[WithHTTP] 或 [WithHTTPContract] 任一）。
//
// 域在 Declare 阶段判「要不要声明路由节点」必须走本函数，不能直接读
// Decl.HTTPHandler：契约钩子形态在计划期只有声明、还没有 handler，直读会让
// --plan 漏掉路由节点而真实启动装上它（计划与执行必须同源）。
func (d *Decl) HasHTTPFace() bool { return hasHTTPFace(d) }

// hasHTTPFace 判定 HTTP 面是否已声明（两条口任一即算）。存在性判据必须走本
// 函数而不是直接读 Decl.HTTPHandler：契约钩子形态在 --plan 路径上不执行用户
// 代码（计划期无副作用），此时只有声明、还没有 handler。
func hasHTTPFace(d *Decl) bool { return d.HTTPHandler != nil || d.HTTPContract != nil }

// hasGRPCChain 判定 gRPC 是否登记了服务端选项工厂（两条口任一即算）。
func hasGRPCChain(d *Decl) bool {
	return len(d.GRPCOptionFuncs) > 0 || len(d.GRPCOptionContractFns) > 0
}

// contractFaces 是求值产物：close 是钩子构造的资源的释放函数（停机 Effect
// 用），guard 在变体补丁之后核验「钩子读到的段没被变体改过」。
type contractFaces struct {
	close func() error
	guard func(cfg *bootstrapv1.BootstrapConfig) error
}

// resolveContractFaces 求值全部契约钩子并写回 Decl。未声明钩子时返回零值
// contractFaces（不调用、不核验），调用方可以无条件使用返回值的字段。
func resolveContractFaces(d *Decl, cfg *bootstrapv1.BootstrapConfig) (contractFaces, error) {
	var faces contractFaces
	if d.HTTPContract == nil && len(d.GRPCOptionContractFns) == 0 {
		return faces, nil
	}
	srv := cfg.GetServer()
	httpMW, grpcMW := srv.GetHttp().GetMiddleware(), srv.GetGrpc().GetMiddleware()
	// 变体补丁在求值之后重放契约段，钩子看到的是补丁前的值——那不是「变体没
	// 生效」，而是「变体改了配置却不改变链」。显式拦下来，别留静默。
	before := &patchSnapshot{http: proto.Clone(httpMW), grpc: proto.Clone(grpcMW)}
	var closes []func() error
	faces.close = func() error { return closeAll(closes) }
	faces.guard = func(cfg *bootstrapv1.BootstrapConfig) error {
		return before.verify(cfg)
	}

	if fn := d.HTTPContract; fn != nil {
		if d.HTTPHandler != nil {
			return contractFaces{}, fmt.Errorf(
				"appkit: the HTTP face is declared twice (WithHTTP and WithHTTPContract); keep one")
		}
		handler, closeFn, err := fn(httpMW)
		if err != nil {
			return contractFaces{}, fmt.Errorf("appkit: WithHTTPContract: %w", err)
		}
		if handler == nil {
			return contractFaces{}, fmt.Errorf("appkit: WithHTTPContract: handler is nil (declare the face with WithHTTP instead)")
		}
		d.HTTPHandler = handler
		if closeFn != nil {
			closes = append(closes, closeFn)
		}
	}

	for _, fn := range d.GRPCOptionContractFns {
		opts, closeFn, err := fn(grpcMW)
		if err != nil {
			return contractFaces{}, fmt.Errorf("appkit: WithGRPCOptionsContract: %w", err)
		}
		chain := opts
		d.GRPCOptionFuncs = append(d.GRPCOptionFuncs, func() []grpc.ServerOption { return chain })
		if closeFn != nil {
			closes = append(closes, closeFn)
		}
	}
	return faces, nil
}

// patchSnapshot 记下求值时点两面的 middleware 段（proto.Clone 深拷贝，之后的
// 原地补丁不会改到它）。
type patchSnapshot struct{ http, grpc proto.Message }

func (s *patchSnapshot) verify(cfg *bootstrapv1.BootstrapConfig) error {
	srv := cfg.GetServer()
	if !proto.Equal(s.http, srv.GetHttp().GetMiddleware()) {
		return fmt.Errorf("appkit: a build variant patched server.http.middleware, but the HTTP face was already built from the pre-patch segment (rebuild the chain outside the variant)")
	}
	if !proto.Equal(s.grpc, srv.GetGrpc().GetMiddleware()) {
		return fmt.Errorf("appkit: a build variant patched server.grpc.middleware, but the gRPC chain was already built from the pre-patch segment (rebuild the chain outside the variant)")
	}
	return nil
}

// closeAll 正序释放钩子资源，返回首个错误（限流器等资源的 Close 幂等，启动
// 失败回滚与停机回放共用本函数）。
func closeAll(closes []func() error) error {
	var firstErr error
	for _, c := range closes {
		if e := c(); e != nil && firstErr == nil {
			firstErr = e
		}
	}
	return firstErr
}
