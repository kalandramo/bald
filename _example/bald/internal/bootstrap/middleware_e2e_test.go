//go:build !grpcgw

// middleware_e2e_test.go 是「契约 YAML → 真实链」的端到端实证。
//
// §6.2③ 之前，server.http.middleware / server.grpc.middleware 写进 YAML 也没有
// 消费者：std server 构造不吃 bundle，业务必须自己显式接线。后果是配置看着生效、
// 实际什么都没发生。本文件把两条链各自钉死——删掉任意一处接线（appkit 的
// WithHTTPContract / WithGRPCOptionsContract 注册、codegen 脚手架、bundle 的
// FromMiddleware / FromGRPCMiddleware 桥）都会让对应断言变红，而不是悄悄回到
// 200 / OK。
//
// 参数走临时契约文件而不是 env：契约的多词叶子键（rate_limit、
// default_timeout_ms）在 env 里表达不出来——environMap 把下划线一律当路径分隔符，
// 落树成 .server.http.middleware.rate.limit，而严格门禁不认折叠匹配，启动直接报
// 「未知契约键 .server.http.middleware.rate」。同款绕行先例见
// discovery_e2e_test.go 的 roleConfigWithGateway。
//
// 只跑默认构建：-tags grpcgw 时 server.http 由转码面接管，那条面按设计不挂 gin
// 链（所以 AppSpec 的 http middleware 段也不继承给网关角色，见 codegen
// inheritMiddleware 的注释）。
package bootstrap

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gopkg.in/yaml.v3"

	"github.com/kalandramo/bald/berrors"
	"github.com/kalandramo/bald/berrors/grpcerr"

	baldv1 "github.com/kalandramo/bald/example/bald/api/gen"
)

// TestE2E_HTTPContractRateLimit 证明 server.http.middleware.rate_limit 真的进了
// gin 链：契约桶打满后第二个请求 429。
func TestE2E_HTTPContractRateLimit(t *testing.T) {
	cfg := roleConfigOver(t, RoleAPI, func(doc map[string]any) {
		setContractMiddleware(t, doc, "http", map[string]any{
			"rate_limit": map[string]any{"rate": 1, "burst": 1},
		})
	})

	_, httpAddr, stop := startRoleWithConfig(t, RoleAPI, cfg)
	defer stop()

	if code, body := postGreet(t, httpAddr, greetPayload); code != http.StatusOK {
		t.Fatalf("first POST /v1/greet = %d %s, want 200 (桶起满 1 个令牌，首请求必须放行)", code, body)
	}
	if code, body := postGreet(t, httpAddr, greetPayload); code != http.StatusTooManyRequests {
		t.Errorf("second POST /v1/greet = %d %s, want 429 — server.http.middleware.rate_limit 没接线", code, body)
	}
}

// TestE2E_GRPCContractRateLimit 是它的 gRPC 对偶：server.grpc.middleware.rate_limit
// 真的进了 unary 链，拒绝形态是 RESOURCE_EXHAUSTED + Reason=RATE_LIMITED
// （链首 ErrorInterceptor 收口，Reason 不丢）。
func TestE2E_GRPCContractRateLimit(t *testing.T) {
	cfg := roleConfigOver(t, RoleAPI, func(doc map[string]any) {
		setContractMiddleware(t, doc, "grpc", map[string]any{
			"rate_limit": map[string]any{"rate": 1, "burst": 1},
		})
	})

	grpcAddr, _, stop := startRoleWithConfig(t, RoleAPI, cfg)
	defer stop()

	cli := newClient(t, grpcAddr)
	ctx := context.Background()
	if _, err := cli.Greet(ctx, &baldv1.GreetRequest{Name: "ratelimit"}); err != nil {
		t.Fatalf("first Greet = %v, want OK (桶起满 1 个令牌，首请求必须放行)", err)
	}

	_, err := cli.Greet(ctx, &baldv1.GreetRequest{Name: "ratelimit"})
	if err == nil {
		t.Fatal("second Greet succeeded, want RESOURCE_EXHAUSTED — server.grpc.middleware.rate_limit 没接线")
	}
	if got := status.Code(err); got != codes.ResourceExhausted {
		t.Errorf("status code = %v, want ResourceExhausted", got)
	}
	restored := grpcerr.FromStatus(status.Convert(err))
	berr, ok := berrors.FromError(restored)
	if !ok {
		t.Fatalf("restored error is not *berrors.Error: %T — 拒绝形态丢了结构化语义", restored)
	}
	if berr.Reason != "RATE_LIMITED" {
		t.Errorf("reason = %q, want RATE_LIMITED", berr.Reason)
	}
}

// TestE2E_HTTPContractTimeout 证明 server.http.middleware.timeout 挂上了硬性
// 响应超时：handler 按 biz.greet.delay 占住 600ms，契约给 50ms 预算，客户端拿到
// 503 而不是等满 600ms 的 200。
func TestE2E_HTTPContractTimeout(t *testing.T) {
	cfg := roleConfigOver(t, RoleAPI, func(doc map[string]any) {
		setContractMiddleware(t, doc, "http", map[string]any{
			"timeout": map[string]any{"default_timeout_ms": 50},
		})
		setBizGreetDelay(t, doc, "600ms")
	})

	_, httpAddr, stop := startRoleWithConfig(t, RoleAPI, cfg)
	defer stop()

	code, toHeaders := greetStatusAtHeaders(t, httpAddr)
	if code != http.StatusServiceUnavailable {
		t.Errorf("POST /v1/greet = %d, want 503 — server.http.middleware.timeout 没接线", code)
	}
	// 超时是「提前响应」，不是等 handler 回：gin 侧的 Timeout 在超时分支写完 503
	// 后显式 Flush（handler 继续在后台跑完，它的写入被丢弃）。量响应头到达的
	// 时刻才测得到这一条——等 body 读完就变成量 handler 的 delay 了。
	if toHeaders > 500*time.Millisecond {
		t.Errorf("超时响应头在 %v 后才到达，want ~ the 50ms contract budget（handler delay 是 600ms）", toHeaders)
	}
}

// TestE2E_GRPCContractTimeout 证明 server.grpc.middleware.timeout 的预算真的到了
// handler 可见的 ctx：gRPC 侧不做「提前返回」（见 grpcmw.TimeoutInterceptor 的
// 取舍），到期表现为 handler 主动交出 DEADLINE_EXCEEDED。
func TestE2E_GRPCContractTimeout(t *testing.T) {
	cfg := roleConfigOver(t, RoleAPI, func(doc map[string]any) {
		setContractMiddleware(t, doc, "grpc", map[string]any{
			"timeout": map[string]any{"default_timeout_ms": 50},
		})
		setBizGreetDelay(t, doc, "600ms")
	})

	grpcAddr, _, stop := startRoleWithConfig(t, RoleAPI, cfg)
	defer stop()

	cli := newClient(t, grpcAddr)
	start := time.Now()
	_, err := cli.Greet(context.Background(), &baldv1.GreetRequest{Name: "timeout"})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Greet succeeded, want DEADLINE_EXCEEDED — server.grpc.middleware.timeout 没落到 handler 的 ctx")
	}
	if got := status.Code(err); got != codes.DeadlineExceeded {
		t.Errorf("status code = %v, want DeadlineExceeded", got)
	}
	// 客户端没设 deadline：这个截止期只能来自服务端契约预算；等满了 handler 的
	// 600ms delay 才失败说明预算没进去。
	if elapsed > 500*time.Millisecond {
		t.Errorf("Greet took %v, want ~ the 50ms contract budget — handler 没拿到截止期（等满了 delay）", elapsed)
	}
}

const greetPayload = `{"name":"middleware"}`

// greetStatusAtHeaders 发一次 POST /v1/greet，返回状态码与**响应头到达**的耗时
// （http.Post 在头部可读时即返回，不排空 body）。
func greetStatusAtHeaders(t *testing.T, addr string) (int, time.Duration) {
	t.Helper()
	start := time.Now()
	resp, err := http.Post("http://"+addr+"/v1/greet", "application/json", strings.NewReader(greetPayload))
	toHeaders := time.Since(start)
	if err != nil {
		t.Fatalf("POST /v1/greet: %v", err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode, toHeaders
}

// roleConfigOver 把角色的契约文件复制进临时目录，交给他 mutate 就地改文档，
// 再序列化回去。改配置面而不改仓里的 configs/*.yaml：owned 文件每次重生成都会
// 被覆写，测试用的极值不该进产品配置。
func roleConfigOver(t *testing.T, role string, mutate func(doc map[string]any)) string {
	t.Helper()
	// 相对包目录解析：调用点在 startRoleWithConfig 的 t.Chdir 之前。
	src := filepath.Join("..", "..", configFileForRole(role))
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode %s: %v", src, err)
	}
	mutate(doc)
	dst := filepath.Join(t.TempDir(), filepath.Base(src))
	out, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatalf("encode patched config: %v", err)
	}
	if err := os.WriteFile(dst, out, 0o600); err != nil {
		t.Fatalf("write %s: %v", dst, err)
	}
	return dst
}

// setContractMiddleware 整段替换 server.<face>.middleware。面必须存在：静默产出
// 一份没有该段的配置，测试测的就是「没配置」。
func setContractMiddleware(t *testing.T, doc map[string]any, face string, mw map[string]any) {
	t.Helper()
	sv, ok := doc["server"].(map[string]any)
	if !ok {
		t.Fatalf("config has no top-level server section: %+v", doc)
	}
	f, ok := sv[face].(map[string]any)
	if !ok {
		t.Fatalf("config has no server.%s section: %+v", face, sv)
	}
	f["middleware"] = mw
}

// setBizGreetDelay 拧上演示旋钮 biz.greet.delay（handler 占用时长）。
// 键名不带下划线：env 会把下划线当路径分隔符，业务子树没有折叠匹配兜底。
func setBizGreetDelay(t *testing.T, doc map[string]any, delay string) {
	t.Helper()
	biz, _ := doc["biz"].(map[string]any)
	if biz == nil {
		biz = map[string]any{}
		doc["biz"] = biz
	}
	greet, _ := biz["greet"].(map[string]any)
	if greet == nil {
		greet = map[string]any{}
		biz["greet"] = greet
	}
	if _, dup := greet["delay"]; dup {
		t.Fatal("biz.greet.delay already set in the shipped config — the test would be overriding itself")
	}
	greet["delay"] = delay
}
