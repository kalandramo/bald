package greetv1

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"buf.build/go/protovalidate"
	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	"github.com/kalandramo/bald/berrors"
	baldv1 "github.com/kalandramo/bald/example/bald/api/gen"
	ginhandler "github.com/kalandramo/bald/example/bald/internal/apiserver/handler/gin"
	"github.com/kalandramo/bald/pkg/appkit"
	mid "github.com/kalandramo/bald/pkg/middleware/gin"
	"github.com/kalandramo/bald/pkg/validation"
)

// greeter 是 greet 域的核心能力：问候前缀来自 biz.greet.greeting。
//
// delay 是演示旋钮（biz.greet.delay）：让 handler 真的占住一段时间，
// server.*.middleware.timeout 段才有可观测的行为——秒回的 handler 无论
// 超时接没接线都返回 200，那条链就成了「配了不生效且不吭声」。
type greeter struct {
	greeting string
	delay    time.Duration
}

func (g *greeter) Greet(name string) string {
	if name == "" {
		name = "world"
	}
	return g.greeting + ", " + name
}

// 热更时整份替换端口，读方永远看到完整快照。
var port atomic.Pointer[greeter]

func currentGreeter() *greeter {
	if g := port.Load(); g != nil {
		return g
	}
	return &greeter{greeting: "hello"}
}

type domainConfig struct {
	greeting string
	delay    time.Duration
}

func readOwnConfig(app *appkit.App) (domainConfig, error) {
	c := domainConfig{greeting: "hello"}
	biz, ok := app.Settings()["biz"].(map[string]any)
	if !ok {
		return c, nil
	}
	m, ok := biz["greet"].(map[string]any)
	if !ok {
		return c, nil
	}
	if v, ok := m["greeting"].(string); ok && v != "" {
		c.greeting = v
	}
	// 键名写作 delay（不含下划线）：env 覆盖把下划线一律当路径分隔符，
	// delay_ms 会被切成 delay.ms 落进业务子树——契约字段有折叠匹配兜底，
	// 业务键没有。
	if v, ok := m["delay"].(string); ok && v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return c, fmt.Errorf("greetv1: biz.greet.delay %q: %w", v, err)
		}
		c.delay = d
	}
	return c, nil
}

// wire 是 biz 节点（domain.greet.biz）的执行体，同时是热更协调体。
func wire(_ context.Context, app *appkit.App) error {
	cfg, err := readOwnConfig(app)
	if err != nil {
		return err
	}
	port.Store(&greeter{greeting: cfg.greeting, delay: cfg.delay})
	return nil
}

// sayHello 按 biz.greet.delay 占用一段时间，并让调用方的截止期说话：
// 到期即返回 ctx.Err()（经链首 ErrorInterceptor 收口成 DEADLINE_EXCEEDED），
// 而不是傻等到 delay 满了再回一份「迟到但成功」。
func sayHello(ctx context.Context, name string) (string, error) {
	g := currentGreeter()
	if g.delay > 0 {
		timer := time.NewTimer(g.delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return g.Greet(name), nil
}

type greetService struct {
	baldv1.UnimplementedGreetServiceServer
}

func (s *greetService) Greet(ctx context.Context, req *baldv1.GreetRequest) (*baldv1.GreetResponse, error) {
	msg, err := sayHello(ctx, req.GetName())
	if err != nil {
		return nil, err
	}
	return &baldv1.GreetResponse{Greet: msg}, nil
}

func (s *greetService) GreetGet(ctx context.Context, req *baldv1.GreetGetRequest) (*baldv1.GreetResponse, error) {
	msg, err := sayHello(ctx, req.GetName())
	if err != nil {
		return nil, err
	}
	return &baldv1.GreetResponse{Greet: msg}, nil
}

func registerService(s *grpc.Server) {
	baldv1.RegisterGreetServiceServer(s, &greetService{})
}

// registerRoutes 把注解派生的 REST 绑定挂到应用 HTTP 面（routes 节点执行体）。
// 生成的 BindGreet 只做 protojson 编解码，不跑校验；这里用适配器复用同一个
// Validator——REST 与 gRPC 两层入口的规则必须一致，否则换个协议就能绕过。
func registerRoutes(h http.Handler) error {
	e, ok := h.(*gin.Engine)
	if !ok {
		return fmt.Errorf("greetv1: HTTP face is %T, want *gin.Engine (the domain owns its router coupling)", h)
	}
	ginhandler.BindGreet(e.Group("/", mid.CORS(mid.DefaultCORS())), validatedService{inner: &greetService{}})
	return nil
}

// validatedService 在生成的绑定与域实现之间执行校验契约。
type validatedService struct {
	inner *greetService
}

func (s validatedService) Greet(ctx context.Context, req *baldv1.GreetRequest) (*baldv1.GreetResponse, error) {
	if err := Validator(ctx, req); err != nil {
		return nil, err
	}
	return s.inner.Greet(ctx, req)
}

func (s validatedService) GreetGet(ctx context.Context, req *baldv1.GreetGetRequest) (*baldv1.GreetResponse, error) {
	if err := Validator(ctx, req); err != nil {
		return nil, err
	}
	return s.inner.GreetGet(ctx, req)
}

// protoValidator 是预热实例：init 预编译注解中的 CEL，注解写错在启动期暴露。
var protoValidator protovalidate.Validator

// requestRules 承载注解表达不了、需要外部状态的校验。
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

// Validator 是 greet 域契约的全量校验回调：先注解规则，后手写规则。
func Validator(ctx context.Context, rq any) error {
	if msg, ok := rq.(proto.Message); ok {
		if err := protoValidator.Validate(msg); err != nil {
			return mapValidationError(err)
		}
	}
	return requestRules.Validate(ctx, rq)
}

// mapValidationError 把 protovalidate 违规聚合为一条 bald 错误；非校验错误原样透传。
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
	return berrors.BadRequest("INVALID_ARGUMENT").
		WithMessage("请求参数校验失败: %s", strings.Join(details, "; "))
}

// requestValidator 承载注解表达不了、需要外部状态的校验。
type requestValidator struct{}

var reservedNames = map[string]struct{}{
	"root": {}, "admin": {}, "system": {},
}

// ValidateGreetRequest 方法名必须严格等于 "Validate" + 请求类型名。
func (requestValidator) ValidateGreetRequest(_ context.Context, rq *baldv1.GreetRequest) error {
	if _, reserved := reservedNames[rq.GetName()]; reserved {
		return berrors.BadRequest("RESERVED_NAME").
			WithMessage("name %q 为系统保留字", rq.GetName())
	}
	return nil
}
