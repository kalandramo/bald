package bootstrap

import (
	"google.golang.org/grpc"

	greetv1 "github.com/kalandramo/bald/example/bald/greetv1"
	grpcmw "github.com/kalandramo/bald/pkg/middleware/grpc"
)

// newGRPCServerOptions 构造 gRPC 服务器选项（拦截器链）。
//
// 拦截器顺序（由外到内）：请求 ID → 可观测性 → 默认值填充 → 校验。
// 认证/授权（Authn/Authz）接入用户系统后，通过注入 TokenExtractor /
// UserRetriever / Authorizer 串到此链即可（详见 pkg/middleware/grpc/authn.go、
// authz.go 预留接口）。
//
// 抽成函数供装配与本包测试共用：曾因测试里另写一份而漏掉 ValidatorInterceptor，
// 导致「非法请求居然通过了」却让测试看起来在跑 —— 复用同一构造函数可杜绝
// 这类「测试与生产不一致」。
//
// 校验器：拦截器接受回调（依赖倒置），具体实现由注入方决定，本包不绑定任何
// 校验库。三种接法：
//
//	// (a) protovalidate：读取 proto 的 buf.validate 注解（声明式字段规则）
//	//     需先 go get buf.build/go/protovalidate（会引入 cel-go，按需引入）
//	grpcmw.ValidatorInterceptor(func(ctx context.Context, rq any) error {
//	    msg, ok := rq.(proto.Message)
//	    if !ok {
//	        return nil
//	    }
//	    return protovalidate.Validate(msg)
//	})
//
//	// (b) pkg/validation 分发器：复杂命令式逻辑（查库/权限），框架自带零外部依赖
//	v, err := validation.NewValidator(myValidator{})
//	grpcmw.ValidatorInterceptor(v.Validate)
//
//	// (c) 串联：先跑注解规则，再跑复杂逻辑（grpcgw 构建即此接法）
func newGRPCServerOptions() []grpc.ServerOption {
	unaryInterceptors := []grpc.UnaryServerInterceptor{
		// ErrorInterceptor 必须在最外层：收口转换内层（校验等）抛出的错误，
		// 否则 *berrors.Error 的 Code/Reason/Details 会在 gRPC 边界丢失，
		// 客户端只能看到一个空的 Unknown（详见该函数注释）。
		grpcmw.ErrorInterceptor(),
		grpcmw.RequestIDInterceptor(),
		grpcmw.UnaryObservability(),
		grpcmw.DefaulterInterceptor(),
		// 校验回调随域自持（greetv1.Validator：第 1 层 proto 注解 + 第 2 层
		// 手写规则，见 greetv1/greet.go）。默认构建同样生效——域迁移后
		// GreetService 在两种构建下都真实注册，链路无差别。
		grpcmw.ValidatorInterceptor(greetv1.Validator),
	}
	return []grpc.ServerOption{grpc.ChainUnaryInterceptor(unaryInterceptors...)}
}

// registerGRPCService 注册业务 gRPC service。
//
// 本示例的业务 service 全部随域自持（greetv1.Domain.Services），故这里留空：
// 只有 worker 角色走这条显式声明（它不装域，gRPC 服务器因此需要在这里显式
// 声明能力才存在），service 列表为空时框架仍注册 health。
func registerGRPCService(*grpc.Server) {}
