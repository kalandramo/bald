package greetv1

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"

	"buf.build/go/protovalidate"
	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	"github.com/kalandramo/bald/berrors"
	baldv1 "github.com/kalandramo/bald/example/bald/api/gen"
	"github.com/kalandramo/bald/pkg/appkit"
	mid "github.com/kalandramo/bald/pkg/middleware/gin"
	"github.com/kalandramo/bald/pkg/validation"
	"github.com/kalandramo/bald/transport/web"
)

// greeter 是 greet 域的核心能力：问候前缀来自 biz.greet.greeting。
type greeter struct {
	greeting string
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
}

func readOwnConfig(app *appkit.App) domainConfig {
	c := domainConfig{greeting: "hello"}
	biz, ok := app.Settings()["biz"].(map[string]any)
	if !ok {
		return c
	}
	m, ok := biz["greet"].(map[string]any)
	if !ok {
		return c
	}
	if v, ok := m["greeting"].(string); ok && v != "" {
		c.greeting = v
	}
	return c
}

// wire 是 biz 节点（domain.greet.biz）的执行体，同时是热更协调体。
func wire(_ context.Context, app *appkit.App) error {
	cfg := readOwnConfig(app)
	port.Store(&greeter{greeting: cfg.greeting})
	return nil
}

func sayHello(ctx context.Context, name string) string {
	return currentGreeter().Greet(name)
}

type greetService struct {
	baldv1.UnimplementedGreetServiceServer
}

func (s *greetService) Greet(ctx context.Context, req *baldv1.GreetRequest) (*baldv1.GreetResponse, error) {
	return &baldv1.GreetResponse{Greet: sayHello(ctx, req.GetName())}, nil
}

func (s *greetService) GreetGet(ctx context.Context, req *baldv1.GreetGetRequest) (*baldv1.GreetResponse, error) {
	return &baldv1.GreetResponse{Greet: sayHello(ctx, req.GetName())}, nil
}

func registerService(s *grpc.Server) {
	baldv1.RegisterGreetServiceServer(s, &greetService{})
}

// registerRoutes 把域 REST 路由挂到应用 HTTP 面（routes 节点执行体）。
func registerRoutes(h http.Handler) error {
	e, ok := h.(*gin.Engine)
	if !ok {
		return fmt.Errorf("greetv1: HTTP face is %T, want *gin.Engine (the domain owns its router coupling)", h)
	}
	v1 := e.Group("/v1", mid.CORS(mid.DefaultCORS()))

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
					return greetResp{}, berrors.BadRequest("EMPTY_NAME").
						WithMessage("name 不能为空")
				}
				return greetResp{Greet: sayHello(c.Request.Context(), g.Name)}, nil
			})
	})
	return nil
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
