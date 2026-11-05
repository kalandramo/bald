package contract

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	registry "github.com/kalandramo/bald/registry"

	etcdserverpb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeEtcd 是只实现「注册路径用到的四个 RPC」的进程内 etcd：
// Lease.Grant / KV.Put / Lease.LeaseKeepAlive / Auth.Authenticate。它存在的唯一理由是给契约映射层
// 一个**运行期观测面**——契约段的值只有落到真 gRPC 调用上才算装配成功，
// 而这件事在「不往公开 API 加只读 accessor」的前提下只能从服务端那头看。
//
// 断链的触发是确定的：把 LeaseKeepAlive 响应的 TTL 置 0，clientv3 见 TTL<=0
// 即关闭全部续约通道（`client/v3/lease.go` 的 handleResponse），
// 于是 heartBeat 走进重注册循环。之后每次 Grant 的到达时刻与次数都可测。
//
// Grant 失败回 codes.Internal —— 刻意不是 Unavailable：后者在 clientv3 自己的
// retry interceptor 里是可重试码，会把「一次尝试」变成若干次 RPC，
// 观测面就不再和循环的次数量级一一对应了。
type fakeEtcd struct {
	etcdserverpb.UnimplementedLeaseServer
	etcdserverpb.UnimplementedKVServer
	etcdserverpb.UnimplementedAuthServer

	mu      sync.Mutex
	linkOK  bool
	started time.Time
	mark    int // 断链时已记录的 Grant 条数；它之后才是重注册尝试
	grants  []grantCall
	puts    []string
	auths   []authCall
}

type grantCall struct {
	at  time.Duration
	ttl int64
}

// authCall 记录一次 Authenticate。clientv3 在 New() 里、建连之后调用
// getToken()，而后者只在 username **和** password 都非空时才发出这个 RPC
// （`client/v3/client.go` 的 getToken）。也就是说：这两行契约字段任何一行
// 没投递到 options，这里观测到的就是「零条」，而不是「一条缺半边」。
type authCall struct {
	name     string
	password string
}

func (f *fakeEtcd) Authenticate(_ context.Context, req *etcdserverpb.AuthenticateRequest) (*etcdserverpb.AuthenticateResponse, error) {
	f.mu.Lock()
	f.auths = append(f.auths, authCall{name: req.GetName(), password: req.GetPassword()})
	f.mu.Unlock()
	return &etcdserverpb.AuthenticateResponse{
		Token:  "fake-token",
		Header: &etcdserverpb.ResponseHeader{},
	}, nil
}

func (f *fakeEtcd) LeaseGrant(_ context.Context, req *etcdserverpb.LeaseGrantRequest) (*etcdserverpb.LeaseGrantResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.grants = append(f.grants, grantCall{at: time.Since(f.started), ttl: req.GetTTL()})
	if f.linkOK {
		return &etcdserverpb.LeaseGrantResponse{
			ID:     7,
			TTL:    req.GetTTL(),
			Header: &etcdserverpb.ResponseHeader{},
		}, nil
	}
	return nil, status.Error(codes.Internal, "fake: lease grant lost")
}

func (f *fakeEtcd) Put(_ context.Context, req *etcdserverpb.PutRequest) (*etcdserverpb.PutResponse, error) {
	f.mu.Lock()
	f.puts = append(f.puts, string(req.GetKey()))
	f.mu.Unlock()
	return &etcdserverpb.PutResponse{Header: &etcdserverpb.ResponseHeader{}}, nil
}

func (f *fakeEtcd) LeaseKeepAlive(stream grpc.BidiStreamingServer[etcdserverpb.LeaseKeepAliveRequest, etcdserverpb.LeaseKeepAliveResponse]) error {
	for {
		req, err := stream.Recv()
		if err != nil {
			return err
		}
		f.mu.Lock()
		linkOK := f.linkOK
		f.mu.Unlock()

		ttl := int64(5)
		if !linkOK {
			ttl = 0
		}
		if err := stream.Send(&etcdserverpb.LeaseKeepAliveResponse{
			ID:     req.GetID(),
			TTL:    ttl,
			Header: &etcdserverpb.ResponseHeader{},
		}); err != nil {
			return err
		}
		if !linkOK {
			return nil // 让 clientv3 处理完 TTL=0 的那一帧再结束流
		}
	}
}

func (f *fakeEtcd) snapshot() ([]grantCall, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]grantCall(nil), f.grants...), append([]string(nil), f.puts...)
}

// postAttempts 返回断链之后的重注册尝试。
func (f *fakeEtcd) postAttempts() []grantCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]grantCall(nil), f.grants[f.mark:]...)
}

// authAttempts 返回打到 Auth.Authenticate 上的调用（在 clientv3.New 内部同步发出）。
func (f *fakeEtcd) authAttempts() []authCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]authCall(nil), f.auths...)
}

func (f *fakeEtcd) breakLink() {
	// 重设起点：断链之后的时间戳才是「尝试之间的等待」，与注册阶段的耗时无关。
	f.mu.Lock()
	f.linkOK = false
	f.mark = len(f.grants)
	f.started = time.Now()
	f.mu.Unlock()
}

// startFakeEtcd 起假后端并返回它的地址。
func startFakeEtcd(t *testing.T) (string, *fakeEtcd) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	f := &fakeEtcd{linkOK: true, started: time.Now()}
	gs := grpc.NewServer()
	etcdserverpb.RegisterLeaseServer(gs, f)
	etcdserverpb.RegisterKVServer(gs, f)
	etcdserverpb.RegisterAuthServer(gs, f)
	go func() { _ = gs.Serve(ln) }()
	t.Cleanup(gs.Stop)
	return ln.Addr().String(), f
}

// TestProvider_ContractSectionsReachRuntime 是映射层的运行期闸门：
// 契约 registry.etcd 段的每一个字段都必须改变**打到后端的那次调用**。
//
// 这条用例存在的原因（登记册 §7.14）：先前 9 条变异里有一条**存活**——
// 把 contract.go 里 `opts = append(opts, etcd.WithRetreat(b, j))` 整行删掉，
// 契约段照样被读出、校验、翻译成 retry.Backoff，然后丢在原地，
// 相关判据全绿。翻译层有判据、覆盖层有判据，中间那一跳没有。
//
// 为什么这里的等待阈值是「下界」而不是「区间」：下界在慢机器上只会更慢，
// 不会假红。可判性靠的是数字选得开：契约给 FIXED 2s + jitter NONE ⇒
// 两次尝试之间**至少** 2s；默认策略在 attempt=0 是 FullJitter(1s) ⇒ 严格小于 1s。
// 于是 1.5s 这条线两边各有 0.5s 以上的余量，而删掉那一行必然落到线下。
//
// 同一份观测面还顺住另外两行：Put 的 key 证明 prefix、Grant 的 TTL 证明 ttl，
// Register 本身成功证明 endpoints。
//
// max_retry **不**在这里判。先前写过「断链后恰好 3 次尝试」的计数断言，变异
// （删掉 contract.go 里 `WithMaxRetry` 那行 append）实测判绿：2s 一档的退避下，
// 默认 maxRetry=5 的第 4、5 次尝试落在收尾窗口之外，「3 次」和「5 次」在窗口里
// 长得一样。那条断言没有牙齿，已移除；次数由
// TestProvider_MaxRetrySectionReachesAttemptCount 换成 fast 退避单独钉。
func TestProvider_ContractSectionsReachRuntime(t *testing.T) {
	addr, f := startFakeEtcd(t)

	const (
		prefix = "/bald-contract"
		ttl    = 5
	)
	cfg := &bootstrapv1.Registry{
		Type: Type,
		Etcd: &bootstrapv1.Registry_Etcd{
			Endpoints: []string{addr},
			Prefix:    prefix,
			Ttl:       ttl,
			MaxRetry:  3,
			Backoff: &bootstrapv1.Backoff{
				Curve:   bootstrapv1.Backoff_FIXED,
				Initial: d(2 * time.Second),
				Jitter:  bootstrapv1.Backoff_NONE,
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	reg, cleanup, err := Provider(ctx, cfg)
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	t.Cleanup(cleanup)

	svc := &registry.ServiceInstance{
		Name:      "contract-runtime",
		ID:        "1",
		Endpoints: []string{"grpc://127.0.0.1:0"},
	}
	if err := reg.Register(ctx, svc); err != nil {
		t.Fatalf("Register 应当成功（契约 endpoints 没接上就直接红在这里）: %v", err)
	}

	grants, puts := f.snapshot()
	if len(grants) != 1 || len(puts) != 1 {
		t.Fatalf("注册应当是 1 次 Grant + 1 次 Put，实得 grants=%d puts=%d", len(grants), len(puts))
	}
	if got := grants[0].ttl; got != ttl {
		t.Errorf("Grant 的 TTL=%d，want %d：契约 registry.etcd.ttl 没落到租约上", got, ttl)
	}
	if want := prefix + "/contract-runtime/1"; puts[0] != want {
		t.Errorf("Put 的 key=%q, want %q：契约 registry.etcd.prefix 没落到命名空间上", puts[0], want)
	}

	// 断链：续约被判过期 ⇒ heartBeat 进入重注册循环 ⇒ 每次尝试都打到假后端。
	f.breakLink()

	// 三次尝试之间要等两次 2s；超时就带着实测序列判红。
	// 「凑够 3 次」只是**前置条件**（少了就没得可测），不是对 max_retry 的判决。
	deadline := time.Now().Add(15 * time.Second)
	var post []grantCall
	for time.Now().Before(deadline) {
		if post = f.postAttempts(); len(post) >= 3 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(post) < 3 {
		t.Fatalf("断链后只观测到 %d 次重注册尝试，want ≥3 才能测出两次间隔", len(post))
	}
	for i := 1; i < len(post) && i <= 2; i++ {
		gap := post[i].at - post[i-1].at
		if gap < 1500*time.Millisecond {
			t.Errorf("第 %d→%d 次尝试之间只等了 %s, want ≥1.5s：契约 backoff（FIXED 2s / jitter NONE）没落到重注册节奏上；默认策略在这一档给不满 1s", i, i+1, gap.Round(10*time.Millisecond))
		}
	}
}

// TestProvider_MaxRetrySectionReachesAttemptCount 钉住 `WithMaxRetry` 那一行。
//
// 退避必须换快的原因见上面：判「次数」要求**所有**可能的尝试都落在观测窗口内，
// 默认 maxRetry=5 与契约 max_retry=2 在 FIXED 50ms 下分别是 ~200ms 和 ~50ms 的
// 节奏，两条曲线不会互相掩盖。
//
// 断言是「恰好 2 次」而不是「≥2 次」：重试耗尽后 heartBeat 直接退出（见 registry.go
// 的 `if kac == nil` / `_, ok := <-kac` 两支），所以窗口末尾还在增长就是没接上。
func TestProvider_MaxRetrySectionReachesAttemptCount(t *testing.T) {
	addr, f := startFakeEtcd(t)

	cfg := &bootstrapv1.Registry{
		Type: Type,
		Etcd: &bootstrapv1.Registry_Etcd{
			Endpoints: []string{addr},
			MaxRetry:  2,
			Backoff: &bootstrapv1.Backoff{
				Curve:   bootstrapv1.Backoff_FIXED,
				Initial: d(50 * time.Millisecond),
				Jitter:  bootstrapv1.Backoff_NONE,
			},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	reg, cleanup, err := Provider(ctx, cfg)
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	t.Cleanup(cleanup)

	if err := reg.Register(ctx, &registry.ServiceInstance{
		Name:      "max-retry",
		ID:        "1",
		Endpoints: []string{"grpc://127.0.0.1:0"},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	f.breakLink()

	// 收尾窗口：错误实现（默认 5 次）在第 5 次尝试前 ~200ms 就到齐了，
	// 1.5s 足以让它长满，而正确实现在第 2 次后就退出心跳循环。
	time.Sleep(1500 * time.Millisecond)
	got := f.postAttempts()
	if len(got) != 2 {
		t.Errorf("断链后观测到 %d 次重注册尝试, want 恰好 2：契约 registry.etcd.max_retry 没落到 maxRetry 上（默认是 5）", len(got))
	}
}

// TestProvider_AuthSectionReachesAuthenticate 钉住最后两行：`WithUsername` 和 `WithPassword`。
//
// 判据形状是「一条都不许少」而不是「用户名对、密码对」两条独立断言：
// clientv3 的 getToken 在 username 或 password 为空时**直接返回**，
// 所以任一行没投递，这里就是零条 Authenticate。少任何一行都红。
//
// 不需要等异步：Authenticate 在 clientv3.New() 内部同步发出（建连之后、
// New 返回之前），Provider 返回时那一帧已经落在假后端上。
func TestProvider_AuthSectionReachesAuthenticate(t *testing.T) {
	addr, f := startFakeEtcd(t)

	const (
		user = "svc-registrar"
		pass = "p@ssw0rd-not-default"
	)
	cfg := &bootstrapv1.Registry{
		Type: Type,
		Etcd: &bootstrapv1.Registry_Etcd{
			Endpoints: []string{addr},
			Username:  user,
			Password:  pass,
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	reg, cleanup, err := Provider(ctx, cfg)
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	t.Cleanup(cleanup)
	if err := reg.Register(ctx, &registry.ServiceInstance{
		Name:      "auth",
		ID:        "1",
		Endpoints: []string{"grpc://127.0.0.1:0"},
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	auths := f.authAttempts()
	if len(auths) != 1 {
		t.Fatalf("观测到 %d 次 Authenticate, want 1：契约 username/password 至少有一行没投递到 clientv3（任一空即不发这个 RPC）", len(auths))
	}
	if auths[0].name != user {
		t.Errorf("Authenticate 的 name=%q, want %q：契约 registry.etcd.username 没落到认证请求上", auths[0].name, user)
	}
	if auths[0].password != pass {
		t.Errorf("Authenticate 的 password=%q, want %q：契约 registry.etcd.password 没落到认证请求上", auths[0].password, pass)
	}
}
