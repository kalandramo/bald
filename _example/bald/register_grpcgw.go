//go:build grpcgw

// 本文件演示 bald 的 grpc-gateway transcoding 接线。
//
// 它依赖 protoc 生成的代码（见 proto/ 目录与下方 import 的 baldv1 包），
// 因此用 build tag `grpcgw` 保护：默认 `go build ./...` 不会编译它，
// 仓库在无 protoc 环境也能正常构建。本地装好工具链后：
//
//	cd _example/bald
//	make proto          # buf generate 生成 gen/baldv1/*.go
//	cd ../..
//	go run -tags grpcgw ./_example/bald            # 配置随示例自带，自动加载
//
// 此时 GreetService 同时可通过 gRPC 与 REST 访问（见 proto 的 google.api.http 注解）。
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	"buf.build/go/protovalidate"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	berrors "github.com/kalandramo/bald/berrors"
	grpcmw "github.com/kalandramo/bald/pkg/middleware/grpc"
	"github.com/kalandramo/bald/pkg/validation"
	"github.com/kalandramo/bald/transport"
	gateway "github.com/kalandramo/bald/transport/gateway"

	// 以下包由 `make proto` 生成（protoc-gen-go / protoc-gen-go-grpc /
	// protoc-gen-grpc-gateway）。未生成前本文件因 build tag 不参与编译。
	//
	// 路径说明：
	// ① proto 源文件位于 proto/greet.proto（不在 proto/bald/v1/ 子目录），
	//    配合 buf.gen.yaml 的 out: gen + paths=source_relative，产物落在 gen/ 根目录，
	//    Go package 名为 baldv1，故路径到 .../gen 而非 .../gen/baldv1。
	// ② _example 是独立 module `github.com/kalandramo/bald/example`（见 _example/go.mod），
	//    故前缀是 .../example/bald/gen，不是 .../bald/_example/bald/gen。
	baldv1 "github.com/kalandramo/bald/example/bald/gen"
)

// protoValidator 是 protovalidate 的预热实例，在 init 中构建。
//
// 用 New() 而非每次调 protovalidate.Validate(msg)：New() 会预编译注解中的 CEL 表达式，
// 之后每次校验复用编译结果（生产环境推荐做法）。
// 这里在 init 构建还能让「注解写错」在进程启动期就暴露（返回 CompilationError），
// 而不是等到第一个请求进来才炸。
var protoValidator protovalidate.Validator

// init 注入 gRPC 请求校验回调，供 main.go 的 ValidatorInterceptor 使用。
//
// 这里演示「分层校验」的完整接线（见 docs/devel/zh-CN/架构改进路线图.md P6）：
//   - 第 1 层：声明式字段规则（非空/长度/格式）→ 写在 greet.proto 的
//     buf.validate 注解里，由 protovalidate 运行时读取；
//   - 第 2 层：命令式复杂逻辑（依赖外部状态的判断）→ 手写 Go，
//     由 pkg/validation 的分发器按请求类型路由。
//
// 顺序是先跑注解规则再跑自定义逻辑：让廉价、可共享的声明式规则先拦截，
// 避免为明显非法的请求付出查库等代价。
//
// NewValidator 返回 error：方法名与请求类型名不符（拼写错误）时会在此处立即暴露，
// 而不是静默跳过导致「以为有校验其实没有」。
func init() {
	// 注入 gRPC service 注册函数，main.go 的 registerGRPCService 变量指向这里。
	registerGRPCService = registerGRPCServiceFn

	// 注入 grpc-gateway 工厂，main.go 的 buildServers 会挂上这个服务器。
	// 挂上后 REST 请求经转码进入 gRPC service，自动复用 proto 注解校验。
	gatewayFactory = newGatewayWithGreet

	// protovalidate 实例：注解编译错误在此暴露（进程启动即失败，fail fast）。
	pv, err := protovalidate.New()
	if err != nil {
		panic("build protovalidate validator: " + err.Error())
	}
	protoValidator = pv

	v, err := validation.NewValidator(greetRequestValidator{})
	if err != nil {
		panic("register validator: " + err.Error())
	}

	greetValidator = func(ctx context.Context, rq any) error {
		// 第 1 层：proto 注解规则（声明式，与契约同源）。
		if msg, ok := rq.(proto.Message); ok {
			if err := protoValidator.Validate(msg); err != nil {
				return mapValidationError(err)
			}
		}
		// 第 2 层：复杂逻辑（查库/权限/跨字段，注解表达不了）。
		return v.Validate(ctx, rq)
	}
}

// mapValidationError 把 protovalidate 的违规映射成 bald 的错误模型。
//
// protovalidate 默认**累积全部违规**（非 fail-fast），因此这里把所有 violation
// 聚合成一条消息，与 conf.Validate 收集全部问题的风格一致。
// 非 ValidationError（如注解编译错误 CompilationError）原样透传，不做吞掉。
func mapValidationError(err error) error {
	var valErr *protovalidate.ValidationError
	if !errors.As(err, &valErr) {
		return err
	}
	details := make([]string, 0, len(valErr.Violations))
	for _, v := range valErr.Violations {
		field := "<unknown>"
		if v.FieldDescriptor != nil {
			field = string(v.FieldDescriptor.Name())
		}
		details = append(details, fmt.Sprintf("%s: %s", field, v.Proto.GetMessage()))
	}
	// 注意：WithMessage 本身接受格式串（format string, args ...any），
	// 直接传参即可，不必先 fmt.Sprintf 再传入（后者会触发 vet 的
	// non-constant format string 告警）。
	return berrors.BadRequest("INVALID_ARGUMENT").
		WithMessage("请求参数校验失败: %s", strings.Join(details, "; "))
}

// greetRequestValidator 承载 protovalidate 注解表达不了、需要外部状态的校验。
type greetRequestValidator struct{}

// reservedNames 模拟「外部状态」（真实场景来自 DB / 配置中心 / 权限系统）。
// 这类依赖运行时数据的判断无法写进 proto 注解，正是需要手写 Go 的部分。
var reservedNames = map[string]struct{}{
	"root": {}, "admin": {}, "system": {},
}

// ValidateGreetRequest 校验 GreetRequest。
// 方法名必须严格等于 "Validate" + 请求类型名，否则 NewValidator 会报错（P2 修复）。
//
// 注意：本方法**不重复**校验非空/长度/格式——那些已在 proto 注解里（第 1 层），
// 这里只做注解表达不了的判断，避免同一规则在两处各写一遍（双真相源）。
func (greetRequestValidator) ValidateGreetRequest(_ context.Context, rq *baldv1.GreetRequest) error {
	if _, reserved := reservedNames[rq.GetName()]; reserved {
		return berrors.BadRequest("RESERVED_NAME").
			WithMessage("name %q 为系统保留字", rq.GetName())
	}
	return nil
}

// greetValidator 的类型来自 grpcmw，确保该 import 有效。
var _ grpcmw.MessageValidator = greetValidator

// Greet 是 biz 层业务函数：被 gin 的 web.HandleJSONRequest 与 gRPC 的
// GreetService 同时调用，实证「gin 与 grpc-gateway 复用同一 biz 层」。
// 若要彻底统一，可把 exampleRoutes 里的内联 greet 闭包也改为调用本函数。
func Greet(ctx context.Context, name string) (string, error) {
	if name == "" {
		name = "world"
	}
	return "hello, " + name, nil
}

// greetService 实现 protoc 生成的 baldv1.GreetServiceServer 接口。
//
// 必须嵌入 UnimplementedGreetServiceServer：protoc-gen-go-grpc 默认开启
// require_unimplemented_servers，嵌入它才能满足接口（否则编译报
// missing method mustEmbedUnimplementedGreetServiceServer）。
// 好处是 proto 后续新增 RPC 方法时，旧实现仍能编译（未实现的方法返回
// Unimplemented 错误），而不是直接编译失败。
type greetService struct {
	baldv1.UnimplementedGreetServiceServer
}

func (s *greetService) Greet(ctx context.Context, req *baldv1.GreetRequest) (*baldv1.GreetResponse, error) {
	greet, err := Greet(ctx, req.GetName())
	if err != nil {
		return nil, err
	}
	return &baldv1.GreetResponse{Greet: greet}, nil
}

func (s *greetService) GreetGet(ctx context.Context, req *baldv1.GreetGetRequest) (*baldv1.GreetResponse, error) {
	greet, err := Greet(ctx, req.GetName())
	if err != nil {
		return nil, err
	}
	return &baldv1.GreetResponse{Greet: greet}, nil
}

// registerGRPCServiceFn 把 gRPC service 实现注册进 *grpc.Server，
// 由 init 注入到 main.go 的 registerGRPCService 变量。
func registerGRPCServiceFn(s *grpc.Server) {
	baldv1.RegisterGreetServiceServer(s, &greetService{})
}

// registerGateway 把 grpc-gateway 的 HTTP handler 注册到一个
// runtime.ServeMux（grpc-gateway v2 的 mux，非标准库 http.ServeMux），
// 并把它作为 http.Handler 交回给 transport.NewGatewayServer。
//
// conn 是到本进程 gRPC 服务（grpcCfg.Addr）的连接，由 GatewayServer 内部建立。
// 返回 http.Handler 而非在入参 mux 上注册，是为了让 pkg/server 不必依赖
// grpc-gateway（见 NewGatewayServer 的注释）。
func registerGateway(ctx context.Context, conn *grpc.ClientConn) (http.Handler, error) {
	mux := runtime.NewServeMux()
	if err := baldv1.RegisterGreetServiceHandler(ctx, mux, conn); err != nil {
		return nil, err
	}
	return mux, nil
}

// newGatewayWithGreet 构造 grpc-gateway 服务器：把 REST 请求转发到本地 gRPC 服务。
// 由主程序在启用 grpcgw tag 时调用（HTTP 服务旁再挂一个网关服务器）。
//
// gRPC service 的注册不需要构造新 server —— init 已把 registerGRPCService
// 注入为真实的 baldv1.RegisterGreetServiceServer。
func newGatewayWithGreet(
	httpCfg *bootstrapv1.Server_Http,
	grpcBackend *bootstrapv1.Server_Grpc,
	ready transport.ReadinessFunc,
) (*gateway.GatewayServer, error) {
	return gateway.NewGatewayServer(httpCfg, grpcBackend, registerGateway, ready)
}
