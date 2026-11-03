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
// 多词契约键（rate_limit、default_timeout_ms）两种注入姿势都走得通：文件面
// 用临时契约 YAML（下面的 TestE2E_HTTPContract* 用它注入极值，避免改动
// owned 的 configs/*.yaml），env 面用 BALD_DEMO_SERVER_HTTP_MIDDLEWARE_RATE_LIMIT_BURST
// ——environMap 按下划线切出的嵌套链在合树前由 bconf.FoldEnvKeys 折回规范键
// （§7.7），因此 env 覆盖与文件同键时是正常的高层压低层，见
// TestE2E_EnvOverridesContractRateLimit / TestE2E_EnvOverridesContractGRPCRateLimit。
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
// 键名不带下划线：env 会把下划线当路径分隔符，业务子树没有契约描述符可折。
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

// TestE2E_EnvOverridesContractRateLimit 钉 env 面对多词契约键的覆盖能力：角色
// 文件已写 server.http.middleware.rate_limit {rate: 1000, burst: 200}，env 把它
// 压到 {1, 1} 后第二个请求必须 429。
//
// §7.7 之前这一条红在另一个方向：environMap 把 env 切成
// middleware.rate.limit.{rate,burst}，与文件层的规范键 middleware.rate_limit
// 不同名，读侧「平级匹配优先」让 env 说了不算——两个请求都是 200。折叠发生在
// 合树之前，所以 env > file 的分层承诺在这里第一次可测。
func TestE2E_EnvOverridesContractRateLimit(t *testing.T) {
	t.Setenv("BALD_DEMO_SERVER_HTTP_MIDDLEWARE_RATE_LIMIT_RATE", "1")
	t.Setenv("BALD_DEMO_SERVER_HTTP_MIDDLEWARE_RATE_LIMIT_BURST", "1")

	_, httpAddr, stop := startRole(t, RoleAPI)
	defer stop()

	if code, body := postGreet(t, httpAddr, greetPayload); code != http.StatusOK {
		t.Fatalf("first POST /v1/greet = %d %s, want 200（env 若把桶配坏了，这条先红）", code, body)
	}
	if code, body := postGreet(t, httpAddr, greetPayload); code != http.StatusTooManyRequests {
		t.Errorf("second POST /v1/greet = %d %s, want 429 — env 的 middleware.rate_limit 没覆盖文件的 {1000,200}（§7.7 的优先级倒置）", code, body)
	}
}

// TestE2E_EnvOverridesContractGRPCRateLimit 是它的 gRPC 对偶：env 压
// server.grpc.middleware.rate_limit，拒绝形态仍是 RESOURCE_EXHAUSTED +
// Reason=RATE_LIMITED。
func TestE2E_EnvOverridesContractGRPCRateLimit(t *testing.T) {
	t.Setenv("BALD_DEMO_SERVER_GRPC_MIDDLEWARE_RATE_LIMIT_RATE", "1")
	t.Setenv("BALD_DEMO_SERVER_GRPC_MIDDLEWARE_RATE_LIMIT_BURST", "1")

	grpcAddr, _, stop := startRole(t, RoleAPI)
	defer stop()

	cli := newClient(t, grpcAddr)
	ctx := context.Background()
	if _, err := cli.Greet(ctx, &baldv1.GreetRequest{Name: "env-ratelimit"}); err != nil {
		t.Fatalf("first Greet = %v, want OK", err)
	}

	_, err := cli.Greet(ctx, &baldv1.GreetRequest{Name: "env-ratelimit"})
	if err == nil {
		t.Fatal("second Greet succeeded, want RESOURCE_EXHAUSTED — env 的 grpc middleware.rate_limit 没覆盖文件")
	}
	if got := status.Code(err); got != codes.ResourceExhausted {
		t.Errorf("status code = %v, want ResourceExhausted", got)
	}
	restored := grpcerr.FromStatus(status.Convert(err))
	berr, ok := berrors.FromError(restored)
	if !ok {
		t.Fatalf("restored error is not *berrors.Error: %T", restored)
	}
	if berr.Reason != "RATE_LIMITED" {
		t.Errorf("reason = %q, want RATE_LIMITED", berr.Reason)
	}
}
