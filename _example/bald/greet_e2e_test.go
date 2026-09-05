//go:build grpcgw

// 本文件是 bald 示例的端到端回归测试，与 main 同包（package main）。
//
// 为什么同包：main 包是「程序包」，不可被 import。把测试放在同目录同包下，
// 才能直接调用 newApp / registerGRPCService，从而复用**真实的**应用构造逻辑
// —— 而不是在测试里另抄一份（复制出来的应用与真实运行的不一致，测试就失去回归价值）。
//
// 它启动真实的 AppKit（HTTP + gRPC + 校验链路），用 gRPC 客户端发起调用，
// 验证请求参数校验的两层机制都真实生效：
//   - 第 1 层：proto 的 buf.validate 注解（protovalidate 运行时从描述符读取）
//   - 第 2 层：手写 Go（pkg/validation 分发器，承载注解表达不了的逻辑）
//
// 运行方式：
//
//	cd _example/bald && go test -tags grpcgw ./... -v
//
// 前置：`make proto-example` 生成 proto 产物（本测试依赖 gen 包）。
//
// 注意：这是**长期保留的回归测试**，不是一次性冒烟脚本 ——
// 改动校验链路、proto 注解、拦截器接线后都应重跑它。
package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	berrors "github.com/kalandramo/bald/berrors"
	"github.com/kalandramo/bald/berrors/grpcerr"
	baldlogadapter "github.com/kalandramo/bald/log/slog"
	bconf "github.com/kalandramo/bald/bconf"
	grpcserver "github.com/kalandramo/bald/transport/grpc"
	httpserver "github.com/kalandramo/bald/transport/http"

	baldv1 "github.com/kalandramo/bald/example/bald/gen"
)

// startApp 启动一个真实的应用实例，返回 gRPC 目标地址与停止函数。
//
// 地址用 :0（内核分配空闲端口）：避免测试间端口冲突，
// 也避免与开发机上已运行的服务抢占端口。
func startApp(t *testing.T) (target string, stop func()) {
	t.Helper()

	// pflag.CommandLine 是全局的，appkit.Bind 会往里注册 flag（http.addr 等）。
	// 测试内多次启动会重复注册，触发 "flag redefined" panic，故临时替换。
	oldFlags := pflag.CommandLine
	pflag.CommandLine = pflag.NewFlagSet("e2e", pflag.ContinueOnError)
	defer func() { pflag.CommandLine = oldFlags }()

	// 配置文件由示例自带（_example/bald/configs/bald-demo.yaml），而 go test 的工作
	// 目录就是包目录 _example/bald/，因此 newApp 里的相对路径可直接命中，无需切目录。

	// 端口分配有两个约束：
	// ① 不能用 :0 —— grpc-gateway 需要连到 gRPC 服务，而 :0 是「监听时由内核
	//    分配」的语义，转发方无从得知实际端口。故显式申请空闲端口。
	// ② 必须带 127.0.0.1 —— 否则 ":9090" 会被客户端解析成 [::1]:9090（IPv6），
	//    而服务监听在 IPv4，出现 "connection refused"。
	//
	// 另外必须用 env 覆盖：newApp 会加载 configs/bald-demo.yaml（其中写死
	// grpc.addr: :9090），BeforeStart 的 Unmarshal 会覆盖掉这里设在
	// bootstrap 上的值。appkit 优先级是 flag > env > 文件，用 env 才能压过文件。
	grpcAddr := freeAddr(t)
	httpAddr := freeAddr(t)
	t.Setenv("BALD_DEMO_SERVER_GRPC_ADDR", grpcAddr)
	t.Setenv("BALD_DEMO_SERVER_HTTP_ADDR", httpAddr)
	gatewayAddr = freeAddr(t)
	t.Cleanup(func() { gatewayAddr = ":8081" }) // 还原，避免影响其他测试

	bootstrap := bconf.NewBootstrap()
	// ③ bootstrapv1 契约下 server 直接持有 BootstrapConfig 的子消息指针
	//    （legacy confv1 时代的 adapt 值快照桥接已删除）——env/flag 覆盖与
	//    Unmarshal 合并作用在同一对象上，这里直接改对象即可与 env 同值，
	//    Unmarshal 覆盖后语义不变。指针直通让「New 期构造的 server 读到
	//    最终配置」成为结构保证，不再依赖时序。
	bootstrap.GetServer().GetGrpc().Addr = grpcAddr
	bootstrap.GetServer().GetHttp().Addr = httpAddr

	ready := func(ctx context.Context) error { return nil }

	// 与 serveRunE 构造 httpSrv/grpcSrv 完全同构：直接传契约子消息指针，
	// 保证测的是生产同一条链路。
	httpSrv := httpserver.NewHTTPServer(bootstrap.GetServer().GetHttp(), http.NewServeMux(), ready)
	// 复用 main 的拦截器链构造（newGRPCServerOptions），确保测的就是生产那条链路：
	// 曾在这里传 nil 导致漏掉 ValidatorInterceptor，非法请求居然通过，
	// 测试却「看起来在跑」——这类不一致会让回归测试完全失去价值。
	grpcSrv := grpcserver.NewGRPCServerWithRegister(
		bootstrap.GetServer().GetGrpc(), newGRPCServerOptions(), registerGRPCService, ready)

	logOpts := baldlogadapter.NewOptions()
	app := newApp(bootstrap, logOpts, httpSrv, grpcSrv, ready)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- app.Run(ctx) }()

	stop = func() {
		cancel()
		select {
		case <-errCh:
		case <-time.After(10 * time.Second):
			t.Error("app did not stop within timeout")
		}
	}

	// 等待各服务真正 Listen。地址已知（我们先分配了端口），
	// 但启动是异步的，仍需轮询到可连接为止；否则会连到尚未监听的端口。
	if !waitForReady(t, grpcAddr, 5*time.Second) {
		// app.Run 失败时（如 BeforeStart 配置解析报错）服务永远不监听，
		// 这里把 Run 的返回一并报出，否则只会看到超时、无法定位根因。
		var runErr error
		select {
		case runErr = <-errCh:
		default:
		}
		stop()
		t.Fatalf("gRPC server not ready at %s; app.Run err = %v", grpcAddr, runErr)
	}
	if !waitForReady(t, gatewayAddr, 5*time.Second) {
		stop()
		t.Fatalf("gateway server not ready at %s", gatewayAddr)
	}
	return grpcAddr, stop
}

// freeAddr 申请一个当前空闲的地址（形如 "127.0.0.1:45678"）。
//
// 做法：监听 :0 拿到内核分配的端口后立刻关闭，再用该端口。
// 这不是 100% 可靠（两次调用间端口可能被抢占），但对测试足够，
// 且比硬编码端口安全得多。
//
// 带 127.0.0.1 是刻意的：裸 ":port" 会被 gRPC 客户端解析成 [::1]:port（IPv6），
// 与监听在 IPv4 的服务不匹配，导致 "connection refused"。
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen 127.0.0.1:0: %v", err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// waitForReady 等待地址可建立 TCP 连接（服务已 Listen）。
func waitForReady(t *testing.T, addr string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func newClient(t *testing.T, target string) baldv1.GreetServiceClient {
	t.Helper()
	// Endpoint() 返回的是「对外展示」的地址，形如 grpc://192.168.1.2:9090
	// （带 scheme 前缀）。grpc.NewClient 不接受 scheme，会报
	// "too many colons in address"，故先剥掉。
	target = strings.TrimPrefix(target, "grpc://")
	target = strings.TrimPrefix(target, "http://")

	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return baldv1.NewGreetServiceClient(conn)
}

// TestGreet_ValidRequest 合法请求应正常返回（happy path 回归）。
func TestGreet_ValidRequest(t *testing.T) {
	target, stop := startApp(t)
	defer stop()

	cli := newClient(t, target)
	resp, err := cli.Greet(context.Background(), &baldv1.GreetRequest{Name: "world"})
	if err != nil {
		t.Fatalf("Greet(valid) unexpected error: %v", err)
	}
	if !strings.Contains(resp.GetGreet(), "world") {
		t.Errorf("greet = %q, want containing %q", resp.GetGreet(), "world")
	}
}

// TestGreet_ProtoAnnotationRules 验证第 1 层：proto 的 buf.validate 注解生效。
//
// greet.proto 中 name 的规则是 min_len:1 / max_len:32 / pattern:^[A-Za-z0-9_-]+$。
// 这些规则由 protovalidate 在运行时从描述符读取 —— 本测试是唯一能证明
// 「注解真的被执行」的地方。若注解没被读取（proto 未重新生成、
// 或拦截器没接 protovalidate），这些用例会失败。
func TestGreet_ProtoAnnotationRules(t *testing.T) {
	target, stop := startApp(t)
	defer stop()
	cli := newClient(t, target)

	// protovalidate 返回的是**人类可读文案**（"must be at least 1 characters"），
	// 而不是注解字段名（min_len/max_len/pattern）。断言应匹配文案，
	// 这也顺带验证了错误信息对调用方是可理解的。
	cases := []struct {
		name    string
		reqName string
		wantIn  string
	}{
		{"空值违反 min_len=1", "", "at least 1 characters"},
		{"超长违反 max_len=32", strings.Repeat("a", 33), "at most 32 characters"},
		{"非法字符违反 pattern", "bad name!", "does not match regex pattern"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := cli.Greet(context.Background(), &baldv1.GreetRequest{Name: tc.reqName})
			if err == nil {
				t.Fatalf("Greet(%q) succeeded, want validation error", tc.reqName)
			}
			// 经 ErrorInterceptor（最外层）转换后，code 应是 InvalidArgument(3)。
			if got := status.Code(err); got != codes.InvalidArgument {
				t.Errorf("status code = %v, want InvalidArgument", got)
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("error %q does not contain %q — 注解规则可能未生效", err.Error(), tc.wantIn)
			}
		})
	}
}

// TestGreet_CustomValidator 验证第 2 层：手写 Go 校验器生效。
//
// register_grpcgw.go 的 reservedNames 含 "root"，该判断依赖外部状态、
// 无法写进 proto 注解。用它证明「两层串联」都通，而非只有注解那层。
func TestGreet_CustomValidator(t *testing.T) {
	target, stop := startApp(t)
	defer stop()

	cli := newClient(t, target)
	_, err := cli.Greet(context.Background(), &baldv1.GreetRequest{Name: "root"})
	if err == nil {
		t.Fatal(`Greet("root") succeeded, want RESERVED_NAME error`)
	}

	// reason 走 errdetails.ErrorInfo（不在 Error() 文本里），
	// 需用 grpcerr.FromStatus 还原 —— 这同时验证了错误语义的双向转换闭环。
	restored := grpcerr.FromStatus(status.Convert(err))
	berr, ok := berrors.FromError(restored)
	if !ok {
		t.Fatalf("FromStatus result is not *berrors.Error: %T", restored)
	}
	if berr.Reason != "RESERVED_NAME" {
		t.Errorf("reason = %q, want RESERVED_NAME", berr.Reason)
	}
	if !strings.Contains(err.Error(), "为系统保留字") {
		t.Errorf("error %q does not contain the message text", err.Error())
	}
}

// TestGreet_REST_SharesSameRules 验证 grpc-gateway 的价值：
// **同一份 proto 注解校验，在 REST 与 gRPC 两种协议下都生效。**
//
// REST 请求经 grpc-gateway 转码后进入同一个 gRPC service，
// 因此 buf.validate 注解与手写校验器都无需为 HTTP 侧重写一遍。
// 若本用例失败而 gRPC 用例通过，说明网关的转码或校验链路接线有问题。
func TestGreet_REST_SharesSameRules(t *testing.T) {
	_, stop := startApp(t)
	defer stop()

	base := "http://" + gatewayAddr // gatewayAddr 形如 127.0.0.1:45678

	// happy path：REST 正常返回。
	resp, err := http.Post(base+"/v1/greet", "application/json", strings.NewReader(`{"name":"world"}`))
	if err != nil {
		t.Fatalf("POST /v1/greet: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "world") {
		t.Errorf("body = %s, want containing world", body)
	}

	// 非法 name：应被 proto 注解规则拦下（与 gRPC 侧同一条规则）。
	resp2, err := http.Post(base+"/v1/greet", "application/json", strings.NewReader(`{"name":""}`))
	if err != nil {
		t.Fatalf("POST /v1/greet (invalid): %v", err)
	}
	defer resp2.Body.Close()
	body2, _ := io.ReadAll(resp2.Body)
	if resp2.StatusCode == http.StatusOK {
		t.Fatalf("invalid name should be rejected, got 200; body = %s", body2)
	}
	if !strings.Contains(string(body2), "at least 1 characters") {
		t.Errorf("body = %s, want containing the annotation rule message", body2)
	}
}

// TestGreet_ErrorMessageIsStructured protovalidate 默认累积全部违规，
// 映射后应是一条聚合消息（而非只报第一个字段）。
func TestGreet_ErrorMessageIsStructured(t *testing.T) {
	target, stop := startApp(t)
	defer stop()

	cli := newClient(t, target)
	// 同时违反 max_len 与 pattern。
	bad := strings.Repeat("a", 40) + "!"
	_, err := cli.Greet(context.Background(), &baldv1.GreetRequest{Name: bad})
	if err == nil {
		t.Fatal("expected validation error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "at most 32 characters") ||
		!strings.Contains(msg, "does not match regex pattern") {
		t.Errorf("error %q should aggregate all violations (max_len + pattern)", msg)
	}
}
