// greet.go 是 greet 域的 biz/model + 传输适配（新增域 3 文件目标的第 2 个
// 手写文件）：
//   - 域配置段读取（Settings 快照 greet.* 子树）与能力构造（greeter）；
//   - sayHello 业务函数（gRPC 面与 REST 面共用同一 biz）；
//   - REST 路由（gin）与 gRPC service 实现；
//   - 契约校验回调（第 1 层 proto 注解 + 第 2 层手写规则——校验规则是
//     域自己的契约知识）。
package greetv1

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"buf.build/go/protovalidate"
	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	"github.com/kalandramo/bald/berrors"
	baldv1 "github.com/kalandramo/bald/example/bald/gen"
	"github.com/kalandramo/bald/pkg/appkit"
	mid "github.com/kalandramo/bald/pkg/middleware/gin"
	"github.com/kalandramo/bald/pkg/validation"
	"github.com/kalandramo/bald/transport/web"
)

// ---------------------------------------------------------------------------
// 域端口：Wire 按自己的配置段构造并持有（能力策略住域，不住装配层）
// ---------------------------------------------------------------------------

// greeter 是 greet 域的核心能力：问候语前缀由域配置段 greet.greeting 供给。
type greeter struct {
	greeting string
}

func (g *greeter) Greet(name string) string {
	if name == "" {
		name = "world" // 缺省问候名
	}
	return g.greeting + ", " + name
}

// port 是域端口（Wire 构造；引擎保证 Routes/Services 晚于 Wire 执行——
// pkg/appkit 的 domain_test.go 时序窗口测试钉住）。
var port *greeter

// currentGreeter 取域端口；Wire 未跑的直构路径（NewApp）回退缺省配置——
// 域值自足，不因装配路径差异 panic。
func currentGreeter() *greeter {
	if port == nil {
		return &greeter{greeting: "hello"}
	}
	return port
}

// domainConfig 是 greet 域自己的配置形状（从 Settings 快照的 greet.* 子树
// 读取；业务段只进 Settings、不进契约，与 L3 覆盖检查无交互）。
type domainConfig struct {
	greeting string
}

// readOwnConfig 读域配置段：缺省 greeting="hello"（新增段缺省零回归）；
// env 覆盖通道 BALD_DEMO_GREET_GREETING 由配置装载的 env 层
// 展平供给。
func readOwnConfig(app *appkit.App) domainConfig {
	c := domainConfig{greeting: "hello"}
	m, ok := app.Settings()["greet"].(map[string]any)
	if !ok {
		return c
	}
	if v, ok := m["greeting"].(string); ok && v != "" {
		c.greeting = v
	}
	return c
}

// wire 是 biz 节点（domain.greet.biz）的执行体：读自己的配置段 → 构造自己的
// 能力 → 持有自己的端口。资源面此时已就绪（本域无资源依赖，恰作演示的最小
// 接线）。
func wire(_ context.Context, app *appkit.App) error {
	cfg := readOwnConfig(app)
	port = &greeter{greeting: cfg.greeting}
	return nil
}

// ---------------------------------------------------------------------------
// biz：gRPC 面与 REST 面共用同一业务函数
// ---------------------------------------------------------------------------

// sayHello 是 greet 域的业务函数。
func sayHello(ctx context.Context, name string) string {
	return currentGreeter().Greet(name)
}

// ---------------------------------------------------------------------------
// 传输适配①：gRPC service（Services 槽位——并入 server 构建的注册回调）
// ---------------------------------------------------------------------------

// greetService 实现 protoc 生成的 baldv1.GreetServiceServer 接口。
//
// 必须嵌入 UnimplementedGreetServiceServer：protoc-gen-go-grpc 默认开启
// require_unimplemented_servers，嵌入它才能满足接口。proto 后续新增 RPC
// 方法时，旧实现仍能编译（未实现的方法返回 Unimplemented 错误）。
type greetService struct {
	baldv1.UnimplementedGreetServiceServer
}

func (s *greetService) Greet(ctx context.Context, req *baldv1.GreetRequest) (*baldv1.GreetResponse, error) {
	return &baldv1.GreetResponse{Greet: sayHello(ctx, req.GetName())}, nil
}

func (s *greetService) GreetGet(ctx context.Context, req *baldv1.GreetGetRequest) (*baldv1.GreetResponse, error) {
	return &baldv1.GreetResponse{Greet: sayHello(ctx, req.GetName())}, nil
}

// registerService 把域的 gRPC service 注册进 server（引擎在迟后阶段 server
// 构建时调用——Wire 已完成，端口就绪）。
func registerService(s *grpc.Server) {
	baldv1.RegisterGreetServiceServer(s, &greetService{})
}

// ---------------------------------------------------------------------------
// 传输适配②：REST 路由（Routes 槽位——挂上 WithHTTP 声明的业务 handler）
// ---------------------------------------------------------------------------

// registerRoutes 把 greet 域的 REST 路由挂到应用 HTTP 面（routes 节点
// domain.greet.routes 的执行体）。参数是 WithHTTP 声明的 handler（gin 场景即
// *gin.Engine——路由器耦合由域自持断言，appkit 不 import 路由器）。仅业务
// handler 面被调用；gateway 转码面占用 server.http 端口时该节点不出现在计划
// 里（同端口单面语义，REST 走转码）。
func registerRoutes(h http.Handler) error {
	e, ok := h.(*gin.Engine)
	if !ok {
		return fmt.Errorf("greetv1: HTTP face is %T, want *gin.Engine (the domain owns its router coupling)", h)
	}
	// 版本组：与 main 的示例路由同一前缀；CORS 子链是域自己的安全策略。
	v1 := e.Group("/v1", mid.CORS(mid.DefaultCORS()))

	// 结构化示例：纯 JSON 绑定 + 业务错误（400 + ErrorResponse，e2e 回归钉住）。
	type greetReq struct {
		Name string `json:"name"`
	}
	type greetResp struct {
		Greet string `json:"greet"`
	}
	v1.POST("/greet", func(c *gin.Context) {
		web.HandleJSONRequest[greetReq, greetResp](c,
			func(_ context.Context, g *greetReq) (greetResp, error) {
				if g.Name == "" {
					// BadRequest 的代码-原因-消息三者分离：Reason 稳定可枚举，
					// Message 给人看，二者经统一 ErrorResponse 返回。
					return greetResp{}, berrors.BadRequest("EMPTY_NAME").
						WithMessage("name 不能为空")
				}
				return greetResp{Greet: sayHello(c.Request.Context(), g.Name)}, nil
			})
	})
	return nil
}

// ---------------------------------------------------------------------------
// 契约校验回调（gRPC 拦截器链消费——应用侧链构造经 ValidatorInterceptor 挂载；
// 校验规则随域迁移：注解在域契约上，外部状态判断是域的业务知识）
// ---------------------------------------------------------------------------

// protoValidator 是 protovalidate 的预热实例（init 构建）：New() 预编译注解
// 中的 CEL 表达式，之后每次校验复用编译结果；「注解写错」在进程启动期暴露
// （CompilationError panic——fail fast）。
var protoValidator protovalidate.Validator

// requestRules 承载 protovalidate 注解表达不了、需要外部状态的校验
// （pkg/validation 分发器按请求类型路由）。
var requestRules *validation.Validator

func init() {
	pv, err := protovalidate.New()
	if err != nil {
		panic("greetv1: build protovalidate validator: " + err.Error())
	}
	protoValidator = pv

	v, err := validation.NewValidator(requestValidator{})
	if err != nil {
		panic("greetv1: register validator: " + err.Error())
	}
	requestRules = v
}

// Validator 是 greet 域契约的全量校验回调（两层串联，顺序 = 先廉价的声明式
// 规则后复杂逻辑）：
//   - 第 1 层：proto 注解规则（buf.validate，与域契约同源）；
//   - 第 2 层：手写规则（依赖外部状态，注解表达不了）。
//
// 拦截器链是应用策略（main 的 newGRPCServerOptions）；域只贡献自己的回调——
// 新增域的校验组合点在应用的链构造（由生成物承担）。
func Validator(ctx context.Context, rq any) error {
	if msg, ok := rq.(proto.Message); ok {
		if err := protoValidator.Validate(msg); err != nil {
			return mapValidationError(err)
		}
	}
	return requestRules.Validate(ctx, rq)
}

// mapValidationError 把 protovalidate 的违规映射成 bald 的错误模型：默认累积
// 全部违规，聚合为一条消息。非 ValidationError（如注解编译错误）原样透传。
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
	// WithMessage 本身接受格式串，直接传参（先 Sprintf 再传会触发 vet 的
	// non-constant format string 告警）。
	return berrors.BadRequest("INVALID_ARGUMENT").
		WithMessage("请求参数校验失败: %s", strings.Join(details, "; "))
}

// requestValidator 承载 protovalidate 注解表达不了、需要外部状态的校验。
type requestValidator struct{}

// reservedNames 模拟「外部状态」（真实场景来自 DB / 配置中心 / 权限系统）。
var reservedNames = map[string]struct{}{
	"root": {}, "admin": {}, "system": {},
}

// ValidateGreetRequest 校验 GreetRequest。
// 方法名必须严格等于 "Validate" + 请求类型名，否则 NewValidator 会报错。
//
// 本方法不重复校验非空/长度/格式——那些已在 proto 注解里（第 1 层），这里只
// 做注解表达不了的判断，避免同一规则两处各写一遍（双真相源）。
func (requestValidator) ValidateGreetRequest(_ context.Context, rq *baldv1.GreetRequest) error {
	if _, reserved := reservedNames[rq.GetName()]; reserved {
		return berrors.BadRequest("RESERVED_NAME").
			WithMessage("name %q 为系统保留字", rq.GetName())
	}
	return nil
}
