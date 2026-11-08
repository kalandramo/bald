package contract

// provider_test.go —— client 段的契约装配闸门：
//   1. 段路径/节点形状（阶段 B、优先级紧跟 registry、静态重载）；
//   2. 段缺席无产物、段在但条目为空合法；
//   3. grpc/http 产物键与构造字段（timeout、BaseURL、TLS transport）；
//   4. 校验失败面（空名、带点、重名、空地址、负超时、坏 URL、坏 TLS）；
//   5. discovery：无注册中心时 fail-fast，有来源时挂 resolver 且目标原样保留；
//   6. cleanup 关闭连接、访问器在未装配时返回 (nil, false)。

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/protobuf/types/known/durationpb"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/pkg/appkit"
	"github.com/kalandramo/bald/registry/inmemory"
	client "github.com/kalandramo/bald/transport/client"
)

func TestAppProvider_Segments(t *testing.T) {
	segs := AppProvider().Segments()
	if len(segs) != 1 || segs[0] != "client" {
		t.Fatalf("Segments() = %v, want [client]", segs)
	}
}

func TestAppProvider_PlanNode(t *testing.T) {
	nodes, err := AppProvider().Plan(context.Background(), appkit.PlanScope{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("Plan nodes = %d, want 1", len(nodes))
	}
	n := nodes[0]
	if n.Key != "client" || n.Kind != "client" {
		t.Fatalf("node = %+v, want key/kind client", n)
	}
	if n.Phase != appkit.PhaseBootstrap {
		t.Fatalf("node phase = %v, want %v", n.Phase, appkit.PhaseBootstrap)
	}
	if n.Priority != appkit.PriorityClient {
		t.Fatalf("node priority = %d, want %d", n.Priority, appkit.PriorityClient)
	}
	if n.Reload != appkit.ReloadStatic {
		t.Fatalf("node reload = %v, want %v", n.Reload, appkit.ReloadStatic)
	}
}

// Apply 落声明的是携带 Option 的接收者本身。
func TestAppProvider_ApplyPreservesOptions(t *testing.T) {
	sentinel := errors.New("sentinel transport")
	p := AppProvider(WithHTTPOptions(client.WithTransport(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, sentinel
	}))))
	d := &appkit.Decl{}
	p.Apply(d)
	if len(d.Providers) != 1 {
		t.Fatalf("decl providers = %d, want 1", len(d.Providers))
	}
	res, _, err := d.Providers[0].Build(context.Background(), appkit.ApplyScope{
		Config: cfgWith(&bootstrapv1.Client{Http: &bootstrapv1.Client_HTTP{Entries: []*bootstrapv1.Client_Entry{
			{Name: "billing", Addr: "http://billing.internal"},
		}}}),
	}, appkit.Node{})
	if err != nil {
		t.Fatalf("Build via decl: %v", err)
	}
	hc := product(t, res, "http.billing").(*client.HTTPClient)
	_, err = hc.Get(hc.BaseURL)
	if !errors.Is(err, sentinel) {
		t.Fatalf("GET error = %v, want sentinel from option transport", err)
	}
}

func TestMappingOffline_NoSection(t *testing.T) {
	res, cleanup, err := AppProvider().Build(context.Background(), appkit.ApplyScope{
		Config: &bootstrapv1.BootstrapConfig{},
	}, appkit.Node{})
	if err != nil || res != nil || cleanup != nil {
		t.Fatalf("Build = (%v, %v, %v), want (nil, nil, nil)", res, cleanup, err)
	}
}

// 段在而条目为空：合法，产物为空 map、无 cleanup。
func TestMappingOffline_EmptySection(t *testing.T) {
	res, cleanup, err := AppProvider().Build(context.Background(), appkit.ApplyScope{
		Config: cfgWith(&bootstrapv1.Client{}),
	}, appkit.Node{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	m, ok := res.(map[string]any)
	if !ok || len(m) != 0 {
		t.Fatalf("product = %#v, want empty map", res)
	}
	if cleanup != nil {
		t.Fatal("cleanup != nil for empty product")
	}
}

func TestMappingOffline_Products(t *testing.T) {
	res, cleanup, err := AppProvider().Build(context.Background(), appkit.ApplyScope{
		Config: cfgWith(&bootstrapv1.Client{
			Grpc: &bootstrapv1.Client_GRPC{Entries: []*bootstrapv1.Client_Entry{{
				Name:    "user",
				Addr:    "127.0.0.1:19090",
				Timeout: durationpb.New(2 * time.Second),
			}}},
			Http: &bootstrapv1.Client_HTTP{Entries: []*bootstrapv1.Client_Entry{{
				Name:    "billing",
				Addr:    "https://billing.internal/api/",
				Timeout: durationpb.New(3 * time.Second),
			}}},
		}),
	}, appkit.Node{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if cleanup == nil {
		t.Fatal("cleanup = nil, want connection closer")
	}
	defer func() { _ = cleanup(context.Background()) }()

	conn, ok := product(t, res, "grpc.user").(*grpc.ClientConn)
	if !ok || conn == nil {
		t.Fatalf("grpc.user = %v, want *grpc.ClientConn", product(t, res, "grpc.user"))
	}
	hc := product(t, res, "http.billing").(*client.HTTPClient)
	if hc.BaseURL != "https://billing.internal/api" {
		t.Fatalf("BaseURL = %q, want trailing slash trimmed", hc.BaseURL)
	}
	if hc.Timeout != 3*time.Second {
		t.Fatalf("http timeout = %v, want 3s", hc.Timeout)
	}

	// 同名条目在两个面上并存合法（资源键带面前缀）。
	if _, _, err := AppProvider().Build(context.Background(), appkit.ApplyScope{
		Config: cfgWith(&bootstrapv1.Client{
			Grpc: &bootstrapv1.Client_GRPC{Entries: []*bootstrapv1.Client_Entry{{Name: "x", Addr: "127.0.0.1:1"}}},
			Http: &bootstrapv1.Client_HTTP{Entries: []*bootstrapv1.Client_Entry{{Name: "x", Addr: "http://x.internal"}}},
		}),
	}, appkit.Node{}); err != nil {
		t.Fatalf("same name on both faces: %v", err)
	}
}

func TestMappingOffline_ValidationFailures(t *testing.T) {
	neg := durationpb.New(-time.Second)
	badTLS := &bootstrapv1.Server_TLS{Config: &bootstrapv1.Server_TLS_Config{
		CertPem: []byte("not a pem"),
		KeyPem:  []byte("not a pem"),
	}}
	cases := []struct {
		name string
		sec  *bootstrapv1.Client
		want string
	}{
		{"missing name", &bootstrapv1.Client{Grpc: &bootstrapv1.Client_GRPC{Entries: []*bootstrapv1.Client_Entry{
			{Addr: "127.0.0.1:1"},
		}}}, "name is required"},
		{"dotted name", &bootstrapv1.Client{Grpc: &bootstrapv1.Client_GRPC{Entries: []*bootstrapv1.Client_Entry{
			{Name: "a.b", Addr: "127.0.0.1:1"},
		}}}, `contains '.'`},
		{"duplicate grpc", &bootstrapv1.Client{Grpc: &bootstrapv1.Client_GRPC{Entries: []*bootstrapv1.Client_Entry{
			{Name: "dup", Addr: "127.0.0.1:1"},
			{Name: "dup", Addr: "127.0.0.1:2"},
		}}}, `duplicate entry name "dup"`},
		{"duplicate http", &bootstrapv1.Client{Http: &bootstrapv1.Client_HTTP{Entries: []*bootstrapv1.Client_Entry{
			{Name: "dup", Addr: "http://a.internal"},
			{Name: "dup", Addr: "http://b.internal"},
		}}}, `duplicate entry name "dup"`},
		{"empty addr", &bootstrapv1.Client{Grpc: &bootstrapv1.Client_GRPC{Entries: []*bootstrapv1.Client_Entry{
			{Name: "user"},
		}}}, "addr is required"},
		{"negative timeout", &bootstrapv1.Client{Grpc: &bootstrapv1.Client_GRPC{Entries: []*bootstrapv1.Client_Entry{
			{Name: "user", Addr: "127.0.0.1:1", Timeout: neg},
		}}}, "must be >= 0"},
		{"bad http url", &bootstrapv1.Client{Http: &bootstrapv1.Client_HTTP{Entries: []*bootstrapv1.Client_Entry{
			{Name: "billing", Addr: "billing.internal"},
		}}}, "http base url"},
		{"bad grpc tls", &bootstrapv1.Client{Grpc: &bootstrapv1.Client_GRPC{Entries: []*bootstrapv1.Client_Entry{
			{Name: "user", Addr: "127.0.0.1:1", Tls: badTLS},
		}}}, "client.grpc.user.tls"},
		{"bad http tls", &bootstrapv1.Client{Http: &bootstrapv1.Client_HTTP{Entries: []*bootstrapv1.Client_Entry{
			{Name: "billing", Addr: "https://billing.internal", Tls: badTLS},
		}}}, "client.http.billing.tls"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, cleanup, err := AppProvider().Build(context.Background(), appkit.ApplyScope{
				Config: cfgWith(tc.sec),
			}, appkit.Node{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
			if res != nil || cleanup != nil {
				t.Fatalf("failed Build leaked product/cleanup: (%v, %v)", res, cleanup)
			}
		})
	}
}

// 半装配失败：前一条已建连接必须当场关闭，不能留在进程里。
// ClientConn 不落产物面，只能经构造入口取句柄判状态。
func TestMappingOffline_FailureClosesEarlierConns(t *testing.T) {
	var built []*grpc.ClientConn
	prev := newGRPC
	newGRPC = func(target string, opts ...client.GRPCOption) (*grpc.ClientConn, error) {
		conn, err := client.NewGRPC(target, opts...)
		if conn != nil {
			built = append(built, conn)
		}
		return conn, err
	}
	t.Cleanup(func() { newGRPC = prev })

	res, cleanup, err := AppProvider().Build(context.Background(), appkit.ApplyScope{
		Config: cfgWith(&bootstrapv1.Client{
			Grpc: &bootstrapv1.Client_GRPC{Entries: []*bootstrapv1.Client_Entry{
				{Name: "good", Addr: "127.0.0.1:1"},
				{Name: "bad"},
			}},
		}),
	}, appkit.Node{})
	if err == nil {
		t.Fatal("Build succeeded, want validation error on second entry")
	}
	if res != nil || cleanup != nil {
		t.Fatalf("failed Build leaked product/cleanup: (%v, %v)", res, cleanup)
	}
	if len(built) != 1 {
		t.Fatalf("built %d conns before failure, want 1", len(built))
	}
	if got := built[0].GetState(); got != connectivity.Shutdown {
		t.Fatalf("earlier conn state = %v, want Shutdown", got)
	}
}

// countingTransport 记 CloseIdleConnections 的次数（http.Client 会转发给
// 实现了该方法的 Transport）。
type countingTransport struct{ idleCloses int }

func (t *countingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("unexpected request")
}

func (t *countingTransport) CloseIdleConnections() { t.idleCloses++ }

// 同一失败路径对 http 面：失败前已建的客户端必须释放空闲连接。
func TestMappingOffline_FailureClosesEarlierHTTPClients(t *testing.T) {
	tr := &countingTransport{}
	_, _, err := AppProvider(WithHTTPOptions(client.WithTransport(tr))).Build(context.Background(), appkit.ApplyScope{
		Config: cfgWith(&bootstrapv1.Client{
			Http: &bootstrapv1.Client_HTTP{Entries: []*bootstrapv1.Client_Entry{
				{Name: "good", Addr: "http://127.0.0.1:1"},
				{Name: "bad"},
			}},
		}),
	}, appkit.Node{})
	if err == nil {
		t.Fatal("Build succeeded, want validation error on second entry")
	}
	if tr.idleCloses != 1 {
		t.Fatalf("CloseIdleConnections called %d times, want 1", tr.idleCloses)
	}
}

// 正常 cleanup 对 http 面：空闲连接必须释放。
func TestMappingOffline_CleanupReleasesHTTPIdleConns(t *testing.T) {
	tr := &countingTransport{}
	_, cleanup, err := AppProvider(WithHTTPOptions(client.WithTransport(tr))).Build(context.Background(), appkit.ApplyScope{
		Config: cfgWith(&bootstrapv1.Client{
			Http: &bootstrapv1.Client_HTTP{Entries: []*bootstrapv1.Client_Entry{
				{Name: "billing", Addr: "http://127.0.0.1:1"},
			}},
		}),
	}, appkit.Node{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if cleanup == nil {
		t.Fatal("cleanup is nil, want release func")
	}
	if err := cleanup(context.Background()); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if tr.idleCloses != 1 {
		t.Fatalf("CloseIdleConnections called %d times, want 1", tr.idleCloses)
	}
}

// discovery 目标没有装配注册中心时 fail-fast，而不是留给拨号期。
func TestMappingOffline_DiscoveryWithoutRegistry(t *testing.T) {
	_, _, err := AppProvider().Build(context.Background(), appkit.ApplyScope{
		Config: cfgWith(&bootstrapv1.Client{Grpc: &bootstrapv1.Client_GRPC{Entries: []*bootstrapv1.Client_Entry{
			{Name: "user", Addr: "discovery:///user"},
		}}}),
	}, appkit.Node{})
	if err == nil || !strings.Contains(err.Error(), "no registry is assembled") {
		t.Fatalf("err = %v, want no-registry failure", err)
	}
}

// 有注册中心时挂 resolver；目标原样交给 gRPC（非阻塞构造，不要求实例存在）。
func TestMappingOffline_DiscoveryWithRegistrar(t *testing.T) {
	res, cleanup, err := AppProvider().Build(context.Background(), appkit.ApplyScope{
		Config: cfgWith(&bootstrapv1.Client{Grpc: &bootstrapv1.Client_GRPC{Entries: []*bootstrapv1.Client_Entry{
			{Name: "user", Addr: "discovery:///user"},
		}}}),
		Decl: &appkit.Decl{Registrar: inmemory.New()},
	}, appkit.Node{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer func() { _ = cleanup(context.Background()) }()
	conn, ok := product(t, res, "grpc.user").(*grpc.ClientConn)
	if !ok || conn == nil {
		t.Fatalf("grpc.user = %v, want *grpc.ClientConn", product(t, res, "grpc.user"))
	}
}

func TestMappingOffline_CleanupShutsDownConn(t *testing.T) {
	res, cleanup, err := AppProvider().Build(context.Background(), appkit.ApplyScope{
		Config: cfgWith(&bootstrapv1.Client{Grpc: &bootstrapv1.Client_GRPC{Entries: []*bootstrapv1.Client_Entry{
			{Name: "user", Addr: "127.0.0.1:1"},
		}}}),
	}, appkit.Node{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	conn, ok := product(t, res, "grpc.user").(*grpc.ClientConn)
	if !ok || conn == nil {
		t.Fatalf("grpc.user = %v, want *grpc.ClientConn", product(t, res, "grpc.user"))
	}
	if err := cleanup(context.Background()); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if got := conn.GetState(); got != connectivity.Shutdown {
		t.Fatalf("conn state after cleanup = %v, want Shutdown", got)
	}
}

// TLS 段在 HTTP 条目上必须落到独立 Transport，不改进程共享的默认值。
func TestMappingOffline_HTTPTLSIsolatesTransport(t *testing.T) {
	res, cleanup, err := AppProvider().Build(context.Background(), appkit.ApplyScope{
		Config: cfgWith(&bootstrapv1.Client{Http: &bootstrapv1.Client_HTTP{Entries: []*bootstrapv1.Client_Entry{{
			Name: "billing",
			Addr: "https://billing.internal",
			Tls:  &bootstrapv1.Server_TLS{InsecureSkipVerify: true},
		}}}}),
	}, appkit.Node{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer func() { _ = cleanup(context.Background()) }()
	hc := product(t, res, "http.billing").(*client.HTTPClient)
	if hc.Transport == nil || hc.Transport == http.DefaultTransport {
		t.Fatalf("transport = %v, want isolated clone", hc.Transport)
	}
}

func TestAccessors_NilApp(t *testing.T) {
	if conn, ok := GRPC(nil, "user"); ok || conn != nil {
		t.Fatalf("GRPC(nil) = (%v, %v), want (nil, false)", conn, ok)
	}
	if hc, ok := HTTP(nil, "billing"); ok || hc != nil {
		t.Fatalf("HTTP(nil) = (%v, %v), want (nil, false)", hc, ok)
	}
}

// 按名字绑定的 http 选项只落到该条目，其余条目不受影响。
func TestEntryBinding_HTTPAppliesOnlyToNamedEntry(t *testing.T) {
	sentinel := errors.New("bound transport")
	bound := client.WithTransport(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, sentinel
	}))
	res, cleanup, err := AppProvider(WithEntryHTTPOptions("billing", bound)).Build(context.Background(), appkit.ApplyScope{
		Config: cfgWith(&bootstrapv1.Client{Http: &bootstrapv1.Client_HTTP{Entries: []*bootstrapv1.Client_Entry{
			{Name: "billing", Addr: "http://billing.internal"},
			{Name: "other", Addr: "http://other.internal"},
		}}}),
	}, appkit.Node{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer func() { _ = cleanup(context.Background()) }()

	billing := product(t, res, "http.billing").(*client.HTTPClient)
	if _, err := billing.Get(billing.BaseURL); !errors.Is(err, sentinel) {
		t.Fatalf("billing GET err = %v, want bound-transport sentinel", err)
	}
	other := product(t, res, "http.other").(*client.HTTPClient)
	if other.Transport == billing.Transport {
		t.Fatal("unbound entry shares billing's transport; binding leaked to other entry")
	}
}

// 全局选项在前、条目选项在后：条目绑定可覆盖全局默认。
func TestEntryBinding_HTTPAppendsAfterGlobal(t *testing.T) {
	global := errors.New("global")
	perEntry := errors.New("per-entry")
	res, cleanup, err := AppProvider(
		WithHTTPOptions(client.WithTransport(roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, global
		}))),
		WithEntryHTTPOptions("billing", client.WithTransport(roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, perEntry
		}))),
	).Build(context.Background(), appkit.ApplyScope{
		Config: cfgWith(&bootstrapv1.Client{Http: &bootstrapv1.Client_HTTP{Entries: []*bootstrapv1.Client_Entry{
			{Name: "billing", Addr: "http://billing.internal"},
		}}}),
	}, appkit.Node{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer func() { _ = cleanup(context.Background()) }()
	hc := product(t, res, "http.billing").(*client.HTTPClient)
	if _, err := hc.Get(hc.BaseURL); !errors.Is(err, perEntry) {
		t.Fatalf("GET err = %v, want per-entry transport to win over global", err)
	}
}

// grpc 条目绑定：计数拦截器只接到命名条目上，且排在全局拦截器之后执行。
// 拦截器在 Invoke 时先于传输运行，故无需真实后端即可观察接线。
func TestEntryBinding_GRPCAppliesOnlyToNamedEntry(t *testing.T) {
	var order []string
	record := func(tag string) grpc.UnaryClientInterceptor {
		return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
			order = append(order, tag)
			return invoker(ctx, method, req, reply, cc, opts...)
		}
	}
	res, cleanup, err := AppProvider(
		WithGRPCDialOptions(grpc.WithChainUnaryInterceptor(record("global"))),
		WithEntryGRPCDialOptions("user", grpc.WithChainUnaryInterceptor(record("user"))),
	).Build(context.Background(), appkit.ApplyScope{
		Config: cfgWith(&bootstrapv1.Client{Grpc: &bootstrapv1.Client_GRPC{Entries: []*bootstrapv1.Client_Entry{
			{Name: "user", Addr: "127.0.0.1:1"},
			{Name: "other", Addr: "127.0.0.1:2"},
		}}}),
	}, appkit.Node{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer func() { _ = cleanup(context.Background()) }()

	user := product(t, res, "grpc.user").(*grpc.ClientConn)
	other := product(t, res, "grpc.other").(*grpc.ClientConn)
	// 无后端：Invoke 会在传输层失败，但拦截器已先运行并记录。
	_ = user.Invoke(context.Background(), "/svc/Method", nil, nil)
	if strings.Join(order, ",") != "global,user" {
		t.Fatalf("user call order = %v, want [global user] (per-entry appended after global)", order)
	}
	order = nil
	_ = other.Invoke(context.Background(), "/svc/Method", nil, nil)
	if strings.Join(order, ",") != "global" {
		t.Fatalf("other call order = %v, want [global] only (user binding must not leak)", order)
	}
}

// 绑定名不在契约里 → 装配期 fail-fast，两个面各自的错误文案。
func TestEntryBinding_UnknownNameFailsFast(t *testing.T) {
	cases := []struct {
		name string
		opt  Option
		sec  *bootstrapv1.Client
		want string
	}{
		{
			"grpc typo", WithEntryGRPCDialOptions("usr", grpc.WithUserAgent("x")),
			&bootstrapv1.Client{Grpc: &bootstrapv1.Client_GRPC{Entries: []*bootstrapv1.Client_Entry{
				{Name: "user", Addr: "127.0.0.1:1"},
			}}},
			`WithEntryGRPCDialOptions("usr")`,
		},
		{
			"http typo", WithEntryHTTPOptions("bill", client.WithTransport(http.DefaultTransport)),
			&bootstrapv1.Client{Http: &bootstrapv1.Client_HTTP{Entries: []*bootstrapv1.Client_Entry{
				{Name: "billing", Addr: "http://billing.internal"},
			}}},
			`WithEntryHTTPOptions("bill")`,
		},
		{
			"grpc name only on http face", WithEntryGRPCDialOptions("billing", grpc.WithUserAgent("x")),
			&bootstrapv1.Client{Http: &bootstrapv1.Client_HTTP{Entries: []*bootstrapv1.Client_Entry{
				{Name: "billing", Addr: "http://billing.internal"},
			}}},
			`WithEntryGRPCDialOptions("billing")`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, cleanup, err := AppProvider(tc.opt).Build(context.Background(), appkit.ApplyScope{
				Config: cfgWith(tc.sec),
			}, appkit.Node{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
			if res != nil || cleanup != nil {
				t.Fatalf("failed Build leaked product/cleanup: (%v, %v)", res, cleanup)
			}
		})
	}
}

// 同名条目在两个面上并存时，绑定要各落其面，互不误伤。
func TestEntryBinding_SameNameBothFaces(t *testing.T) {
	sentinel := errors.New("http-bound")
	res, cleanup, err := AppProvider(
		WithEntryHTTPOptions("x", client.WithTransport(roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, sentinel
		}))),
	).Build(context.Background(), appkit.ApplyScope{
		Config: cfgWith(&bootstrapv1.Client{
			Grpc: &bootstrapv1.Client_GRPC{Entries: []*bootstrapv1.Client_Entry{{Name: "x", Addr: "127.0.0.1:1"}}},
			Http: &bootstrapv1.Client_HTTP{Entries: []*bootstrapv1.Client_Entry{{Name: "x", Addr: "http://x.internal"}}},
		}),
	}, appkit.Node{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer func() { _ = cleanup(context.Background()) }()
	hc := product(t, res, "http.x").(*client.HTTPClient)
	if _, err := hc.Get(hc.BaseURL); !errors.Is(err, sentinel) {
		t.Fatalf("http.x GET err = %v, want http-bound sentinel", err)
	}
	if _, ok := product(t, res, "grpc.x").(*grpc.ClientConn); !ok {
		t.Fatal("grpc.x missing; http binding should not disturb the grpc face")
	}
}

// ---- helpers ----

func cfgWith(c *bootstrapv1.Client) *bootstrapv1.BootstrapConfig {
	return &bootstrapv1.BootstrapConfig{Client: c}
}

func product(t *testing.T, res any, key string) any {
	t.Helper()
	m, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("product is %T, want map[string]any", res)
	}
	v, ok := m[key]
	if !ok {
		t.Fatalf("product has no key %q (keys: %v)", key, keysOf(m))
	}
	return v
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
