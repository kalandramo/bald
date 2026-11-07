package appkit

// contract_health_test.go 钉住契约 app 段三个健康字段的取值链（§7.34 ③′ 那格
// 余量的收口判据）。分三层，与 transport/sse 的 §7.42 同形：
//
//	映射形状：applyContractHealth 逐位填「业务未表态」的位，业务 Option 覆盖契约；
//	构造期校验：非法值在 assemble 与 compilePlan 两条路上同判（I2），且 compile
//	          不改调用方 Decl（I1）；
//	真进程运行期：探针路由用真 listener + 真 http.Get 观测，gRPC 轮询间隔用真
//	          健康客户端观测状态翻转耗时——不是读回自己刚写进去的字段。
//
// 间隔判据的界是推导出来的，不是「放宽到绿」：bootstrap 的 poll 先立即 sync 一次
// 再起 ticker，所以「确认首轮 sync 已发生（读到 NOT_SERVING）→ 翻转就绪 → 下一次
// sync」的耗时 ≈ interval 本身。据此 60ms 档上界取 1s（16× 余量）、默认 2s 档下界
// 取 1s（2× 余量）、1h 档只判 600ms 内仍 NOT_SERVING（6000× 余量）。

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/protobuf/types/known/durationpb"

	bconf "github.com/kalandramo/bald/bconf"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/health"
)

// healthContractCfg 造一份只含 app 段健康字段的最小契约（server 两面都在，
// 地址由调用方决定是否改成动态端口）。
func healthContractCfg(live, ready string, interval *durationpb.Duration) *bootstrapv1.BootstrapConfig {
	cfg := bconf.NewBootstrap()
	cfg.App.ProbeLivePath = live
	cfg.App.ProbeReadyPath = ready
	cfg.App.HealthPollInterval = interval
	return cfg
}

// gatedHealth 造一个就绪态可由测试翻转的健康源（翻转前 Down、翻转后 Up）。
func gatedHealth(ready *atomic.Bool) *health.Health {
	h := health.New()
	h.Register("gate", health.PingFunc(func(context.Context) error {
		if ready.Load() {
			return nil
		}
		return errors.New("gate closed")
	}))
	return h
}

func TestContractHealth_Mapping(t *testing.T) {
	cases := []struct {
		name         string
		opts         []Option
		live, ready  string
		interval     *durationpb.Duration
		wantLive     string
		wantReady    string
		wantInterval time.Duration
	}{
		{
			name:     "unset business keeps implementation defaults",
			opts:     []Option{WithHealth(health.New())},
			wantLive: DefaultHealthPath, wantReady: DefaultReadyPath, wantInterval: 0,
		},
		{
			name: "contract fills all three when business only declares WithHealth",
			opts: []Option{WithHealth(health.New())},
			live: "/livez", ready: "/readyz-custom", interval: durationpb.New(250 * time.Millisecond),
			wantLive: "/livez", wantReady: "/readyz-custom", wantInterval: 250 * time.Millisecond,
		},
		{
			name: "business WithProbePaths overrides contract on both paths",
			opts: []Option{WithHealth(health.New(), WithProbePaths("/biz-live", "/biz-ready"))},
			live: "/livez", ready: "/readyz-custom",
			wantLive: "/biz-live", wantReady: "/biz-ready",
		},
		{
			name: "business overrides one path only, contract still fills the other",
			opts: []Option{WithHealth(health.New(), WithProbePaths("/biz-live", ""))},
			live: "/livez", ready: "/readyz-custom",
			wantLive: "/biz-live", wantReady: "/readyz-custom",
		},
		{
			name: "empty business value is not a stance (ready side still from contract)",
			opts: []Option{WithHealth(health.New(), WithProbePaths("", "/biz-ready"))},
			live: "/livez", ready: "/readyz-custom",
			wantLive: "/livez", wantReady: "/biz-ready",
		},
		{
			name:     "business WithHealthPollInterval overrides contract",
			opts:     []Option{WithHealth(health.New(), WithHealthPollInterval(3*time.Second))},
			interval: durationpb.New(250 * time.Millisecond),
			wantLive: DefaultHealthPath, wantReady: DefaultReadyPath, wantInterval: 3 * time.Second,
		},
		{
			name:     "non-positive business interval is not a stance, contract fills it",
			opts:     []Option{WithHealth(health.New(), WithHealthPollInterval(0))},
			interval: durationpb.New(250 * time.Millisecond),
			wantLive: DefaultHealthPath, wantReady: DefaultReadyPath, wantInterval: 250 * time.Millisecond,
		},
		{
			name: "no health source means no mapping at all",
			opts: []Option{WithProbePaths("/biz-live", "/biz-ready"), WithHealthPollInterval(time.Second)},
			live: "/livez", ready: "/readyz-custom", interval: durationpb.New(250 * time.Millisecond),
			wantLive: "/biz-live", wantReady: "/biz-ready", wantInterval: time.Second,
		},
		{
			name:     "explicit zero duration is presence, not unset",
			opts:     []Option{WithHealth(health.New())},
			interval: durationpb.New(0),
			wantLive: DefaultHealthPath, wantReady: DefaultReadyPath, wantInterval: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newDecl(tc.opts...)
			applyContractHealth(d, healthContractCfg(tc.live, tc.ready, tc.interval))
			if d.ProbeLivePath != tc.wantLive {
				t.Errorf("ProbeLivePath = %q, want %q", d.ProbeLivePath, tc.wantLive)
			}
			if d.ProbeReadyPath != tc.wantReady {
				t.Errorf("ProbeReadyPath = %q, want %q", d.ProbeReadyPath, tc.wantReady)
			}
			if d.HealthInterval != tc.wantInterval {
				t.Errorf("HealthInterval = %s, want %s", d.HealthInterval, tc.wantInterval)
			}
		})
	}
}

// TestContractHealth_MappingIsPerField 是「显式零值 duration 与不表态可区分」的
// 承重腿：普通 proto3 标量分不开这两者，Duration 是消息型所以分得开。映射必须
// 把显式 0s 落成 0（随后由校验拒绝），而不是当成未表态保留业务值。
func TestContractHealth_MappingIsPerField(t *testing.T) {
	d := newDecl(WithHealth(health.New(), WithHealthPollInterval(5*time.Second)))
	applyContractHealth(d, healthContractCfg("", "", durationpb.New(0)))
	if d.HealthInterval != 5*time.Second {
		t.Fatalf("business stance must survive an explicit zero in the contract, got %s", d.HealthInterval)
	}

	d2 := newDecl(WithHealth(health.New()))
	applyContractHealth(d2, healthContractCfg("", "", durationpb.New(0)))
	if d2.HealthInterval != 0 {
		t.Fatalf("explicit zero must map to 0 (presence), got %s", d2.HealthInterval)
	}
	if err := validateHealthContract(d2, healthContractCfg("", "", durationpb.New(0))); err == nil {
		t.Fatal("explicit zero interval must be rejected, not silently fall back to the 2s default")
	}
}

func TestContractHealth_Validation(t *testing.T) {
	cases := []struct {
		name        string
		opts        []Option
		live, ready string
		interval    *durationpb.Duration
		wantErr     string
	}{
		{
			name: "contract fields without a health source",
			opts: nil, live: "/livez",
			wantErr: "no health source is declared",
		},
		{
			name: "interval alone without a health source",
			opts: nil, interval: durationpb.New(time.Second),
			wantErr: "no health source is declared",
		},
		{
			name: "contract paths identical",
			opts: []Option{WithHealth(health.New())}, live: "/same", ready: "/same",
			wantErr: "probe paths must differ",
		},
		{
			name: "contract path collides with the default of the other side",
			opts: []Option{WithHealth(health.New())}, live: DefaultReadyPath,
			wantErr: "probe paths must differ",
		},
		{
			name:    "business paths identical are judged by the same rule",
			opts:    []Option{WithHealth(health.New(), WithProbePaths("/same", "/same"))},
			wantErr: "probe paths must differ",
		},
		{
			name: "contract live path is not a ServeMux pattern",
			opts: []Option{WithHealth(health.New())}, live: "livez",
			wantErr: `must start with "/"`,
		},
		{
			name: "contract ready path is not a ServeMux pattern",
			opts: []Option{WithHealth(health.New())}, ready: "readyz",
			wantErr: `must start with "/"`,
		},
		{
			name:    "business path is not a ServeMux pattern either",
			opts:    []Option{WithHealth(health.New(), WithProbePaths("livez", ""))},
			wantErr: `must start with "/"`,
		},
		{
			name: "zero interval is rejected instead of falling back",
			opts: []Option{WithHealth(health.New())}, interval: durationpb.New(0),
			wantErr: "must be positive",
		},
		{
			name: "negative interval is rejected",
			opts: []Option{WithHealth(health.New())}, interval: durationpb.New(-time.Second),
			wantErr: "must be positive",
		},
		{
			name:     "invalid contract interval is rejected even when business overrides it",
			opts:     []Option{WithHealth(health.New(), WithHealthPollInterval(time.Second))},
			interval: durationpb.New(0),
			wantErr:  "must be positive",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			restoreLogger(t)
			cfg := healthContractCfg(tc.live, tc.ready, tc.interval)
			dynamicAddr(cfg)
			_, err := assemble(newDecl(tc.opts...), cfg, nil)
			if err == nil {
				t.Fatalf("expected startup error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

// TestContractHealth_CompileSharesVerdict 预检与真实启动同判（I2），且 compile
// 的映射写在拷贝上、不动调用方 Decl（I1：计划期无副作用）。
func TestContractHealth_CompileSharesVerdict(t *testing.T) {
	cfg := healthContractCfg("/same", "/same", nil)
	dynamicAddr(cfg)
	opts := []Option{stdFaces(), WithHTTP(http.NewServeMux()), WithHealth(health.New())}

	d := newDecl(opts...)
	beforeLive, beforeReady := d.ProbeLivePath, d.ProbeReadyPath
	_, err := compilePlan(context.Background(), d, cfg, nil)
	if err == nil || !strings.Contains(err.Error(), "probe paths must differ") {
		t.Fatalf("compile must reject what assemble rejects, got %v", err)
	}
	if _, aerr := assemble(newDecl(opts...), cfg, nil); aerr == nil || aerr.Error() != err.Error() {
		t.Fatalf("assemble/compile verdicts differ: assemble=%v compile=%v", aerr, err)
	}
	if d.ProbeLivePath != beforeLive || d.ProbeReadyPath != beforeReady {
		t.Fatalf("compile mutated the caller's Decl: %q/%q (was %q/%q)",
			d.ProbeLivePath, d.ProbeReadyPath, beforeLive, beforeReady)
	}
}

// TestContractHealth_CompileMapsOnCopy 合法值那一侧也要同源：compile 的拷贝必须
// 拿到契约值（否则预检通过的配置在真实启动才红），而调用方 Decl 仍保持原样。
func TestContractHealth_CompileMapsOnCopy(t *testing.T) {
	cfg := healthContractCfg("/livez", "/readyz-custom", durationpb.New(time.Second))
	dynamicAddr(cfg)
	d := newDecl(stdFaces(), WithHTTP(http.NewServeMux()), WithHealth(health.New()))
	if _, err := compilePlan(context.Background(), d, cfg, nil); err != nil {
		t.Fatalf("compile: %v", err)
	}
	if d.ProbeLivePath != DefaultHealthPath {
		t.Fatalf("caller Decl must stay untouched, got %q", d.ProbeLivePath)
	}
}

// declCaptureProvider 接管 server.grpc 段，只记录 Build 时看到的 Decl——
// transport/gateway 的 HTTPProvider 正是这样直读 Decl.ProbeLivePath/ReadyPath
// 的（它自己不兜默认值），所以这条腿钉住的是「映射落在 Provider 真读到的那份
// 声明上」，而不是落在某个只有内置 Provider 看得到的旁路。
type declCaptureProvider struct{ seen chan *Decl }

func (declCaptureProvider) Apply(*Decl)        {}
func (declCaptureProvider) Segments() []string { return []string{"server.grpc"} }
func (declCaptureProvider) Plan(context.Context, PlanScope) ([]Node, error) {
	return []Node{{
		Key: "server.grpc", Kind: "server", Segments: []string{"server.grpc"},
		Phase: PhaseServe, Reload: ReloadStatic, Opaque: true, Origin: OriginStd,
	}}, nil
}
func (p declCaptureProvider) Build(_ context.Context, s ApplyScope, _ Node) (any, Cleanup, error) {
	p.seen <- s.Decl
	return nil, nil, nil
}

func TestContractHealth_ProviderSeesResolvedDecl(t *testing.T) {
	restoreLogger(t)
	cfg := healthContractCfg("/livez", "/readyz-custom", durationpb.New(250*time.Millisecond))
	dynamicAddr(cfg)
	seen := make(chan *Decl, 1)
	opts := []Option{
		Providers(StandardHTTPProvider(), declCaptureProvider{seen: seen}),
		WithHTTP(http.NewServeMux()),
		WithHealth(health.New()),
	}
	a, err := assemble(newDecl(opts...), cfg, nil)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	// Build 发生在 Run 的迟后阶段（PhaseServe），assemble 本身不构建 Provider。
	runThenStop(t, a, func() {
		select {
		case d := <-seen:
			if d.ProbeLivePath != "/livez" || d.ProbeReadyPath != "/readyz-custom" {
				t.Fatalf("provider saw %q/%q, want the contract values /livez//readyz-custom",
					d.ProbeLivePath, d.ProbeReadyPath)
			}
			if d.HealthInterval != 250*time.Millisecond {
				t.Fatalf("provider saw interval %s, want 250ms", d.HealthInterval)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("capture provider was never built")
		}
	})
}

// getProbe 对真 listener 发一次 GET，返回状态码。endpoint 是 Server.Endpoint()
// 的原样返回值（已带 http:// scheme），不要再拼一层。
func getProbe(t *testing.T, endpoint, path string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+path, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func TestContractHealth_ProbeRoutesRuntime(t *testing.T) {
	cases := []struct {
		name                string
		opts                []Option
		live, ready         string
		wantLive, wantReady string
		// 契约/业务取值之后必须**不再**服务的那条路径（证明不是两条都挂上了）。
		dead string
	}{
		{
			name:     "unset keeps the framework defaults",
			opts:     []Option{WithHealth(health.New())},
			wantLive: DefaultHealthPath, wantReady: DefaultReadyPath, dead: "/livez",
		},
		{
			name: "contract routes are the ones actually served",
			opts: []Option{WithHealth(health.New())},
			live: "/livez", ready: "/readyz-custom",
			wantLive: "/livez", wantReady: "/readyz-custom", dead: DefaultHealthPath,
		},
		{
			name: "business option wins over the contract route",
			opts: []Option{WithHealth(health.New(), WithProbePaths("/biz-live", "/biz-ready"))},
			live: "/livez", ready: "/readyz-custom",
			wantLive: "/biz-live", wantReady: "/biz-ready", dead: "/livez",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			restoreLogger(t)
			cfg := healthContractCfg(tc.live, tc.ready, nil)
			dynamicAddr(cfg)
			business := http.NewServeMux()
			business.HandleFunc("/biz", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
			opts := append([]Option{stdFaces(), WithHTTP(business)}, tc.opts...)
			a, err := assemble(newDecl(opts...), cfg, nil)
			if err != nil {
				t.Fatalf("assemble: %v", err)
			}
			runThenStop(t, a, func() {
				endpoint := a.servers[0].Endpoint()
				if endpoint == "" {
					t.Fatal("HTTP face has no endpoint")
				}
				if code := getProbe(t, endpoint, tc.wantLive); code != http.StatusOK {
					t.Errorf("liveness %s = %d, want 200", tc.wantLive, code)
				}
				if code := getProbe(t, endpoint, tc.wantReady); code != http.StatusOK {
					t.Errorf("readiness %s = %d, want 200 (no checker registered ⇒ Up)", tc.wantReady, code)
				}
				if code := getProbe(t, endpoint, tc.dead); code != http.StatusNotFound {
					t.Errorf("unconfigured path %s = %d, want 404 (must fall through to business)", tc.dead, code)
				}
				if code := getProbe(t, endpoint, "/biz"); code != http.StatusOK {
					t.Errorf("business route /biz = %d, want 200 (probe mux must not swallow it)", code)
				}
			})
		})
	}
}

// TestContractHealth_ReadinessFollowsContractPath 契约改的不只是路由名字：
// 就绪探针必须仍挂在健康源上（Down ⇒ 503），存活探针恒 200。把两者接反
// （或都接成 liveness）这条腿就红。
func TestContractHealth_ReadinessFollowsContractPath(t *testing.T) {
	restoreLogger(t)
	cfg := healthContractCfg("/livez", "/readyz-custom", nil)
	dynamicAddr(cfg)
	var ready atomic.Bool
	a, err := assemble(newDecl(stdFaces(), WithHTTP(http.NewServeMux()), WithHealth(gatedHealth(&ready))), cfg, nil)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	runThenStop(t, a, func() {
		endpoint := a.servers[0].Endpoint()
		if code := getProbe(t, endpoint, "/readyz-custom"); code != http.StatusServiceUnavailable {
			t.Fatalf("readiness while Down = %d, want 503", code)
		}
		if code := getProbe(t, endpoint, "/livez"); code != http.StatusOK {
			t.Fatalf("liveness while Down = %d, want 200 (process is alive)", code)
		}
		ready.Store(true)
		if code := getProbe(t, endpoint, "/readyz-custom"); code != http.StatusOK {
			t.Fatalf("readiness after Up = %d, want 200", code)
		}
	})
}

// grpcHealthStatus 用真健康客户端读整体服务状态。
func grpcHealthStatus(t *testing.T, ctx context.Context, conn *grpc.ClientConn) healthpb.HealthCheckResponse_ServingStatus {
	t.Helper()
	resp, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{Service: ""})
	if err != nil {
		t.Fatalf("grpc health Check: %v", err)
	}
	return resp.Status
}

func TestContractHealth_GRPCPollIntervalRuntime(t *testing.T) {
	cases := []struct {
		name     string
		interval *durationpb.Duration
		opts     []Option
		// 翻转就绪后允许等到 SERVING 的上界；0 = 只判「窗口内必须仍未就绪」。
		maxWait time.Duration
		// 必须仍未就绪的观测窗（只在 maxWait == 0 时用）。
		stillDownFor time.Duration
		wantMin      time.Duration
	}{
		{
			name: "contract interval drives the poll ticker", interval: durationpb.New(60 * time.Millisecond),
			maxWait: time.Second,
		},
		{
			name: "unset keeps the 2s implementation default", interval: nil,
			maxWait: 5 * time.Second, wantMin: time.Second,
		},
		{
			name: "long contract interval really postpones the sync", interval: durationpb.New(time.Hour),
			stillDownFor: 600 * time.Millisecond,
		},
		{
			name: "business option overrides the contract interval", interval: durationpb.New(time.Hour),
			opts:    []Option{WithHealthPollInterval(60 * time.Millisecond)},
			maxWait: time.Second,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			restoreLogger(t)
			cfg := healthContractCfg("", "", tc.interval)
			dynamicAddr(cfg)
			var ready atomic.Bool
			opts := append([]Option{
				stdFaces(), WithGRPC(func(*grpc.Server) {}), WithHealth(gatedHealth(&ready)),
			}, tc.opts...)
			a, err := assemble(newDecl(opts...), cfg, nil)
			if err != nil {
				t.Fatalf("assemble: %v", err)
			}
			runThenStop(t, a, func() {
				endpoint := a.servers[0].Endpoint()
				if endpoint == "" {
					t.Fatal("gRPC face has no endpoint")
				}
				conn, err := grpc.NewClient(strings.TrimPrefix(endpoint, "grpc://"),
					grpc.WithTransportCredentials(insecure.NewCredentials()))
				if err != nil {
					t.Fatalf("grpc.NewClient: %v", err)
				}
				defer conn.Close()

				ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
				defer cancel()
				// 先确认首轮 sync 已发生（poll 立即 sync 一次再起 ticker），否则
				// 翻转后的第一次 sync 可能就是首轮，耗时判据会失去意义。
				deadline := time.Now().Add(3 * time.Second)
				for grpcHealthStatus(t, ctx, conn) != healthpb.HealthCheckResponse_NOT_SERVING {
					if time.Now().After(deadline) {
						t.Fatal("initial sync never reported NOT_SERVING")
					}
					time.Sleep(10 * time.Millisecond)
				}

				start := time.Now()
				ready.Store(true)
				if tc.maxWait == 0 {
					time.Sleep(tc.stillDownFor)
					if s := grpcHealthStatus(t, ctx, conn); s != healthpb.HealthCheckResponse_NOT_SERVING {
						t.Fatalf("status = %v after %s, want still NOT_SERVING (interval must postpone the sync)",
							s, tc.stillDownFor)
					}
					return
				}
				for grpcHealthStatus(t, ctx, conn) != healthpb.HealthCheckResponse_SERVING {
					if time.Since(start) > tc.maxWait {
						t.Fatalf("status never became SERVING within %s (contract interval not applied)", tc.maxWait)
					}
					time.Sleep(10 * time.Millisecond)
				}
				if elapsed := time.Since(start); elapsed < tc.wantMin {
					t.Fatalf("became SERVING after %s, want >= %s (default interval was replaced)", elapsed, tc.wantMin)
				}
			})
		})
	}
}

// TestContractHealth_EnvRoute 三个字段在契约 env 层同样可达（§7.34 ③′ 量的是
// 「除代码外无任何落点」：契约字段 + 派生前缀 env + StrictContract 认键，一条
// 真进程腿把这三件事一起判掉）。env 名由 app.name 派生，与 §7.7 同一条通道；
// 文件里写 /from-file、env 覆盖成 /from-env，服务的是后者才证明 env 层真挂上。
func TestContractHealth_EnvRoute(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := lis.Addr().String()
	_ = lis.Close()

	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "probe-app.yaml")
	yml := "app:\n  name: \"probe-app\"\n  probe_live_path: \"/from-file\"\n  probe_ready_path: \"/ready-file\"\n" +
		"server:\n  http: { addr: \":18451\" }\n"
	if err := os.WriteFile(yamlPath, []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROBE_APP_SERVER_HTTP_ADDR", addr)
	t.Setenv("PROBE_APP_APP_PROBE_LIVE_PATH", "/from-env")
	t.Setenv("PROBE_APP_APP_PROBE_READY_PATH", "/ready-env")
	t.Setenv("PROBE_APP_APP_HEALTH_POLL_INTERVAL", "250ms")

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		errCh <- Serve(ctx, StandardHTTPProvider(), StandardGRPCProvider(),
			ConfigFile(yamlPath), WithHTTP(http.NewServeMux()), WithHealth(health.New()),
			AfterStart(func(context.Context) error { close(started); return nil }))
	}()
	defer func() {
		cancel()
		select {
		case err := <-errCh:
			if err != nil {
				t.Errorf("Serve: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("app did not stop within timeout")
		}
	}()
	select {
	case <-started:
	case err := <-errCh:
		t.Fatalf("Serve failed before start: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not reach running state")
	}

	base := "http://" + addr
	if code := getProbe(t, base, "/from-env"); code != http.StatusOK {
		t.Errorf("env-provided liveness path = %d, want 200", code)
	}
	if code := getProbe(t, base, "/ready-env"); code != http.StatusOK {
		t.Errorf("env-provided readiness path = %d, want 200", code)
	}
	if code := getProbe(t, base, "/from-file"); code != http.StatusNotFound {
		t.Errorf("file value must be overridden by env, got %d for /from-file, want 404", code)
	}
	if code := getProbe(t, base, DefaultHealthPath); code != http.StatusNotFound {
		t.Errorf("default path must not be mounted when the contract sets one, got %d, want 404", code)
	}
}
