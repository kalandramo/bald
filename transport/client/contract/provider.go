// Package contract 提供 client 段的契约装配：bootstrapv1.Client 段 → 出站连接
// 产物，落 App 资源面 `client.grpc.<name>` / `client.http.<name>`。
//
// 分工与 transport/gateway/contract 一致：transport/client 保持零契约依赖，
// 契约字段（addr / tls / timeout）到构造参数的映射只在本包。
//
// 资源键的命名空间按传输面分开（`client.grpc.<name>` 与 `client.http.<name>`），
// 同名条目可以同时出现在两个面上；同一面内重名判配置错（静默末者胜会让一处
// 配置的改动落到另一个调用点上）。
//
// 契约示例：
//
//	client:
//	  grpc:
//	    entries:
//	      - name: user
//	        addr: discovery:///user
//	        tls: { insecure_skip_verify: true }
//	        timeout: 2s
//	  http:
//	    entries:
//	      - name: billing
//	        addr: https://billing.internal/api
//	        timeout: 3s
//
// 消费侧按名字取实例（不必知道地址）：
//
//	conn, ok := clientcontract.GRPC(app, "user")
//	billing, ok := clientcontract.HTTP(app, "billing")
package contract

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/bconf/tlsconf"
	"github.com/kalandramo/bald/pkg/appkit"
	"github.com/kalandramo/bald/pkg/grpcdiscovery"
	"github.com/kalandramo/bald/registry"
	client "github.com/kalandramo/bald/transport/client"
)

// Option 配置本 Provider 的接线行为（不是 client 包 Option 的替代）。
type Option func(*providerConfig)

type providerConfig struct {
	// grpcDialOpts 追加到全部 grpc 条目（追踪/认证拦截器等契约表达不了的
	// 能力），排在契约字段之后。
	grpcDialOpts []grpc.DialOption
	// httpOpts 同义，作用于全部 http 条目。
	httpOpts []client.HTTPOption
}

// WithGRPCDialOptions 给全部 grpc 条目追加原生拨号选项。
//
// 追加在契约字段之后：可覆盖凭据，也可叠加拦截器（超时拦截器仍由契约的
// timeout 决定，且在链的最外层）。
func WithGRPCDialOptions(opts ...grpc.DialOption) Option {
	return func(c *providerConfig) { c.grpcDialOpts = append(c.grpcDialOpts, opts...) }
}

// WithHTTPOptions 给全部 http 条目追加构造选项。
func WithHTTPOptions(opts ...client.HTTPOption) Option {
	return func(c *providerConfig) { c.httpOpts = append(c.httpOpts, opts...) }
}

// AppProvider 返回消费契约 client 段的 appkit.Provider。
//
// 段缺席即无产物（合法）：不声明 client 段的应用不受影响。段在而条目为空
// 也不报错——「段已就位、暂时没有出站」是合法状态。
//
// 阶段 B 构建，优先级排在 registry 之后：grpc 条目的 discovery 目标要在
// Build 期取注册中心的发现半边。
func AppProvider(opts ...Option) appkit.Provider {
	c := providerConfig{}
	for _, o := range opts {
		o(&c)
	}
	return appProvider{cfg: c}
}

// appProvider 消费 client 段。
type appProvider struct{ cfg providerConfig }

func (appProvider) Segments() []string { return []string{"client"} }

// Apply 把自身追加进声明（Provider 即 Option，可直接作为 MustServe 变参项）。
func (p appProvider) Apply(d *appkit.Decl) { d.Providers = append(d.Providers, p) }

// Plan 声明 client 节点（阶段 B；优先级紧跟 registry）。
func (appProvider) Plan(context.Context, appkit.PlanScope) ([]appkit.Node, error) {
	return []appkit.Node{{
		Key:      "client",
		Kind:     "client",
		Segments: []string{"client"},
		Phase:    appkit.PhaseBootstrap,
		Priority: appkit.PriorityClient,
		Reload:   appkit.ReloadStatic,
	}}, nil
}

// Build 按契约构造全部出站连接。
//
// 产物是 map[string]any：键 "grpc.<name>" / "http.<name>"，由引擎按
// "<顶层段>.<键>" 展开成资源面键。任一条目失败即整体失败（已建连接当场
// 关闭）——半装配的连接不落 App，避免「配置说有三个、实际只有两个」。
func (p appProvider) Build(_ context.Context, s appkit.ApplyScope, _ appkit.Node) (any, appkit.Cleanup, error) {
	sec := s.Config.GetClient()
	if sec == nil {
		return nil, nil, nil // 段缺席：无产物
	}

	var d registry.Discovery
	if usesDiscovery(sec) {
		var err error
		d, err = s.Discovery()
		if err != nil {
			return nil, nil, fmt.Errorf("client: grpc entries use %q targets, but %w", grpcdiscovery.Scheme, err)
		}
	}

	grpcConns := make([]*grpc.ClientConn, 0, len(sec.GetGrpc().GetEntries()))
	httpClients := make([]*client.HTTPClient, 0, len(sec.GetHttp().GetEntries()))
	fail := func(err error) (any, appkit.Cleanup, error) {
		closeAll(grpcConns, httpClients)
		return nil, nil, err
	}

	res := make(map[string]any, len(sec.GetGrpc().GetEntries())+len(sec.GetHttp().GetEntries()))
	// 两个面各自一套重名表：资源键本来就带面前缀（client.grpc.* / client.http.*），
	// 同名条目并存是合法配置。
	seenGRPC := make(map[string]bool, len(sec.GetGrpc().GetEntries()))
	for i, e := range sec.GetGrpc().GetEntries() {
		if err := validateEntry("grpc", i, e, seenGRPC); err != nil {
			return fail(err)
		}
		conn, err := buildGRPC(e, d, p.cfg.grpcDialOpts)
		if err != nil {
			return fail(err)
		}
		grpcConns = append(grpcConns, conn)
		res[grpcKey(e.GetName())] = conn
	}
	seenHTTP := make(map[string]bool, len(sec.GetHttp().GetEntries()))
	for i, e := range sec.GetHttp().GetEntries() {
		if err := validateEntry("http", i, e, seenHTTP); err != nil {
			return fail(err)
		}
		hc, err := buildHTTP(e, p.cfg.httpOpts)
		if err != nil {
			return fail(err)
		}
		httpClients = append(httpClients, hc)
		res[httpKey(e.GetName())] = hc
	}
	return res, cleanupConns(grpcConns, httpClients), nil
}

// usesDiscovery 报告是否有 grpc 条目要求服务发现。
func usesDiscovery(sec *bootstrapv1.Client) bool {
	for _, e := range sec.GetGrpc().GetEntries() {
		if grpcdiscovery.UsesTarget(e.GetAddr()) {
			return true
		}
	}
	return false
}

// validateEntry 校验单条目的 name/addr/timeout，并把 name 记进当前面的重名表。
func validateEntry(face string, idx int, e *bootstrapv1.Client_Entry, seen map[string]bool) error {
	name := e.GetName()
	if name == "" {
		return fmt.Errorf("client.%s.entries[%d].name is required", face, idx)
	}
	if strings.Contains(name, ".") {
		return fmt.Errorf("client.%s.entries[%d].name = %q contains '.' (names are flat resource-key segments)",
			face, idx, name)
	}
	if seen[name] {
		return fmt.Errorf("client.%s: duplicate entry name %q", face, name)
	}
	seen[name] = true
	if e.GetAddr() == "" {
		return fmt.Errorf("client.%s.%s.addr is required", face, name)
	}
	if t := e.GetTimeout(); t != nil && t.AsDuration() < 0 {
		return fmt.Errorf("client.%s.%s.timeout = %v must be >= 0", face, name, t.AsDuration())
	}
	return nil
}

// newGRPC 是 grpc 条目的构造入口；测试替换它以观察「失败时已建连接是否真的关闭」
// ——ClientConn 句柄不落任何产物面，失败路径上没有别的可观测点。
var newGRPC = client.NewGRPC

// buildGRPC 把单条 grpc 条目映射为连接。
//
// tls 段存在即经 bconf/tlsconf 解析（与 gateway 出站同一姿势）；缺席即明文。
// discovery 目标只在有来源时挂 resolver——Build 已保证来源存在。
func buildGRPC(e *bootstrapv1.Client_Entry, d registry.Discovery, extra []grpc.DialOption) (*grpc.ClientConn, error) {
	opts := make([]client.GRPCOption, 0, 4)
	if tlsSec := e.GetTls(); tlsSec != nil {
		tc, err := tlsconf.Client(tlsSec)
		if err != nil {
			return nil, fmt.Errorf("client.grpc.%s.tls: %w", e.GetName(), err)
		}
		opts = append(opts, client.WithDialOptions(grpc.WithTransportCredentials(credentials.NewTLS(tc))))
	}
	if d != nil && grpcdiscovery.UsesTarget(e.GetAddr()) {
		opts = append(opts, client.WithDiscovery(d))
	}
	if t := e.GetTimeout(); t != nil {
		opts = append(opts, client.WithCallTimeout(t.AsDuration()))
	}
	if len(extra) > 0 {
		opts = append(opts, client.WithDialOptions(extra...))
	}
	conn, err := newGRPC(e.GetAddr(), opts...)
	if err != nil {
		return nil, fmt.Errorf("client.grpc.%s: %w", e.GetName(), err)
	}
	return conn, nil
}

// buildHTTP 把单条 http 条目映射为客户端。
//
// tls 段存在即在独立 Transport 上挂客户端 TLS 配置（不改进程共享的
// http.DefaultTransport）；缺席即默认 Transport。
func buildHTTP(e *bootstrapv1.Client_Entry, extra []client.HTTPOption) (*client.HTTPClient, error) {
	opts := make([]client.HTTPOption, 0, 2)
	if tlsSec := e.GetTls(); tlsSec != nil {
		tc, err := tlsconf.Client(tlsSec)
		if err != nil {
			return nil, fmt.Errorf("client.http.%s.tls: %w", e.GetName(), err)
		}
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.TLSClientConfig = tc
		opts = append(opts, client.WithTransport(tr))
	}
	if t := e.GetTimeout(); t != nil {
		opts = append(opts, client.WithClientTimeout(t.AsDuration()))
	}
	opts = append(opts, extra...)
	hc, err := client.NewHTTP(e.GetAddr(), opts...)
	if err != nil {
		return nil, fmt.Errorf("client.http.%s: %w", e.GetName(), err)
	}
	return hc, nil
}

// cleanupConns 结束全部连接：gRPC 连接关闭，HTTP 客户端释放空闲连接。
func cleanupConns(conns []*grpc.ClientConn, https []*client.HTTPClient) appkit.Cleanup {
	if len(conns) == 0 && len(https) == 0 {
		return nil
	}
	return func(context.Context) error {
		closeAll(conns, https)
		return nil
	}
}

func closeAll(conns []*grpc.ClientConn, https []*client.HTTPClient) {
	for _, c := range conns {
		_ = c.Close()
	}
	for _, h := range https {
		h.CloseIdleConnections()
	}
}
