package contract

// mapping_offline_test.go —— server.redis_server 映射行的运行期判据
// （真 Start、真订阅 flush；「offline」指不需要任何 Redis 集群——后端是
// 进程内的假 RESP 应答器）。
//
// 为什么要这一层：contract_test.go 只证明「Option 被追加、broker.Options 落值」，
// 证明不了「Start 之后这些值活在服务端面上」。redis 与 kafka 的后端差异决定
// 这层的形状：
//   - pubsub broker 的 Connect **真探活**（取连接做 PING，失败即报错并丢池）
//     ——Start 成功当且仅当后端应答 RESP，没有 kafka 那种「无集群也能 Start 绿」
//     的中间态。所以这里用只回合法 RESP 的最小假服务器（PING/SUBSCRIBE/
//     UNSUBSCRIBE/AUTH/…），消息往返语义不在范围（属真集群集成判据）。
//   - pubsub 的地址在 Init 才规范化（裸 host:port 补 redis:// 前缀）——端点腿
//     同时锁「契约地址经规范化活在端点上」「带凭证的 URL 端点必脱敏」。
//   - 端点三态：构造 "" → Start "redis://host:port" → Stop ""，中间态是 appkit
//     waitForEndpoints 认「就绪」的根据。
//
// 假 RESP 的读超时口径：broker/redis/pubsub 的拨号读超时是
// DefaultHealthCheckPeriod(1min)+ReadTimeout（transport NewServer 强制 24h），
// 假应答器给每条连接 90s 空闲截止期——足够长到不会被健康 PING 掐断，又保证
// 测试收尾时连接必死、不留挂起的接收协程。

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kalandramo/bald/broker"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	redis "github.com/kalandramo/bald/transport/redis"
)

type payload struct {
	Msg string `json:"msg"`
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatalf("release port: %v", err)
	}
	return port
}

func loopbackAddr(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("127.0.0.1:%d", freePort(t))
}

// --- 进程内假 RESP 应答器 ----------------------------------------------------

// fakeRedis 只回合法 RESP，不实现 Redis 语义：订阅计数恒 1/0（退订即判结束），
// 其余命令一律 +OK / PING 回 +PONG。它存在的意义是让 pubsub 的 Connect 探活和
// Subscribe 的收发循环能离线走全。
type fakeRedis struct {
	addr string

	mu    sync.Mutex
	conns []net.Conn
}

func newFakeRedis(t *testing.T) *fakeRedis {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen fake redis: %v", err)
	}
	f := &fakeRedis{addr: ln.Addr().String()}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			f.track(conn)
			go f.serve(conn)
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		f.mu.Lock()
		for _, c := range f.conns {
			_ = c.Close()
		}
		f.mu.Unlock()
	})
	return f
}

func (f *fakeRedis) track(conn net.Conn) {
	f.mu.Lock()
	f.conns = append(f.conns, conn)
	f.mu.Unlock()
}

func (f *fakeRedis) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	br := bufio.NewReader(conn)
	for {
		if err := conn.SetDeadline(time.Now().Add(90 * time.Second)); err != nil {
			return
		}
		args, ok := readCommand(br)
		if !ok || len(args) == 0 {
			return
		}
		if _, err := conn.Write(replyFor(args)); err != nil {
			return
		}
	}
}

// replyFor 按命令名给最小合法应答（redigo 的 PubSubConn 在收到
// Subscription{Count:0} 时结束接收循环，所以 UNSUBSCRIBE 的计数回 0）。
func replyFor(args []string) []byte {
	switch strings.ToUpper(args[0]) {
	case "PING":
		return []byte("+PONG\r\n")
	case "SUBSCRIBE":
		ch := ""
		if len(args) > 1 {
			ch = args[1]
		}
		return []byte(fmt.Sprintf("*3\r\n$9\r\nsubscribe\r\n$%d\r\n%s\r\n:1\r\n", len(ch), ch))
	case "UNSUBSCRIBE":
		ch := ""
		if len(args) > 1 {
			ch = args[1]
		}
		return []byte(fmt.Sprintf("*3\r\n$11\r\nunsubscribe\r\n$%d\r\n%s\r\n:0\r\n", len(ch), ch))
	default:
		return []byte("+OK\r\n")
	}
}

// readCommand 解析 RESP 数组命令与单行内联命令（redigo 的 PubSubConn.Ping 走
// 内联 "PING\r\n"，其余走数组）。ok=false 表示流结束/坏帧/超时。
func readCommand(br *bufio.Reader) ([]string, bool) {
	prefix, err := br.ReadByte()
	if err != nil {
		return nil, false
	}
	if prefix != '*' {
		_ = br.UnreadByte()
		line, err := br.ReadString('\n')
		if err != nil {
			return nil, false
		}
		return strings.Fields(line), true
	}
	nLine, err := br.ReadString('\n')
	if err != nil {
		return nil, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(nLine))
	if err != nil || n <= 0 {
		return nil, false
	}
	args := make([]string, 0, n)
	for i := 0; i < n; i++ {
		hdr, err := br.ReadString('\n')
		if err != nil {
			return nil, false
		}
		if hdr[0] != '$' {
			return nil, false
		}
		size, err := strconv.Atoi(strings.TrimSpace(hdr[1:]))
		if err != nil || size < 0 {
			return nil, false
		}
		buf := make([]byte, size+2) // 含结尾 CRLF
		if _, err := readFull(br, buf); err != nil {
			return nil, false
		}
		args = append(args, string(buf[:size]))
	}
	return args, true
}

func readFull(br *bufio.Reader, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := br.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// --- Start 助手 ---------------------------------------------------------------

// startRedis 走 Provider 构造并 Start（redis 的 Start 同步完成 Init+Connect+
// flush 后即返回，不阻塞），注册 Stop 清理。addr 必须落在应答得住的假 RESP 上，
// 否则 Connect 探活红——这里红了红的是装配/环境，不是后端语义。
func startRedis(t *testing.T, opts []Option, cfg *bootstrapv1.Server) *redis.Server {
	t.Helper()
	srv, cleanup, err := Provider(opts...)(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	if cleanup != nil {
		t.Fatal("cleanup != nil, want nil")
	}
	rsrv, ok := srv.(*redis.Server)
	if !ok {
		t.Fatalf("srv = %T, want *redis.Server", srv)
	}
	if err := rsrv.Start(context.Background()); err != nil {
		t.Fatalf("Start against fake RESP: %v", err)
	}
	t.Cleanup(func() { _ = rsrv.Stop(context.Background()) })
	return rsrv
}

// --- Endpoint 生命周期（readiness 闸门的数据源） -----------------------------

// TestMappingOffline_StartMakesEndpointDescriptiveAndStopResets 端点三态：
// 构造 "" → Start "redis://<addr>" → Stop ""。Start 前是假就绪红线，Start 后
// 是 appkit waitForEndpoints 认「就绪」的根据，末态保证 Stop 后不会被误判还在
// 服务（Stop 先复位 started 再 Disconnect，Disconnect 清空地址）。
func TestMappingOffline_StartMakesEndpointDescriptiveAndStopResets(t *testing.T) {
	f := newFakeRedis(t)
	rsrv := startRedis(t, nil, redisServerCfg(f.addr, ""))

	if ep := rsrv.Endpoint(); ep != "redis://"+f.addr {
		t.Fatalf("Endpoint() after Start = %q, want redis://%s（契约地址经 Init 规范化后必须活在端点上）", ep, f.addr)
	}
	if err := rsrv.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if ep := rsrv.Endpoint(); ep != "" {
		t.Fatalf("Endpoint() after Stop = %q, want \"\"（started 复位是就绪闸门的一部分）", ep)
	}
}

// TestMappingOffline_EndpointStripsCredentials 带凭证的 URL 作地址时，端点必须
// 脱敏——端点会进日志与服务注册面，用户名/密码不能随端点外泄。
// （假 RESP 对 AUTH 回 +OK，redigo DialURL 的鉴权腿能走全。）
func TestMappingOffline_EndpointStripsCredentials(t *testing.T) {
	f := newFakeRedis(t)
	rsrv := startRedis(t, nil, redisServerCfg("redis://user:hunter2@"+f.addr, ""))

	ep := rsrv.Endpoint()
	if strings.Contains(ep, "hunter2") || strings.Contains(ep, "user:") {
		t.Fatalf("Endpoint() = %q, want credentials stripped", ep)
	}
	if want := "redis://" + f.addr; ep != want {
		t.Fatalf("Endpoint() = %q, want %s（脱敏后主机端口必须原样保留）", ep, want)
	}
}

// TestMappingOffline_ConnectProbeFailureKeepsEndpointEmpty 反证就绪闸门不被假端点
// 骗过：Connect 真探活，端口无人应答时 Start 必红、started 不落真、端点保持空
// ——「配了段但后端不可达」不会被误报成可服务。
func TestMappingOffline_ConnectProbeFailureKeepsEndpointEmpty(t *testing.T) {
	dead := loopbackAddr(t) // freePort 已释放，拨号即拒
	srv, _, err := Provider()(context.Background(), redisServerCfg(dead, ""))
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	rsrv, ok := srv.(*redis.Server)
	if !ok {
		t.Fatalf("srv = %T, want *redis.Server", srv)
	}
	if err := rsrv.Start(context.Background()); err == nil {
		t.Fatal("Start on a refused port succeeded, want Connect probe to fail")
	} else if !strings.Contains(err.Error(), "redis: connect") {
		t.Fatalf("Start error = %v, want attribution to redis connect probe", err)
	}
	if ep := rsrv.Endpoint(); ep != "" {
		t.Fatalf("Endpoint() after failed Start = %q, want \"\"", ep)
	}
}

// --- codec / addr 行在 Start 后的落点 -----------------------------------------

// TestMappingOffline_BusinessAddressOverridesContract 业务 WithAddress 顶掉契约
// addr 之后，Start 后端点读的是业务地址——覆盖序的线上形态（构造期证据见
// TestProvider_BusinessOptionsAppendAfterContract）。两台假 RESP 让「落在哪台」
// 有唯一判据。
func TestMappingOffline_BusinessAddressOverridesContract(t *testing.T) {
	contractFake := newFakeRedis(t)
	businessFake := newFakeRedis(t)
	rsrv := startRedis(t,
		[]Option{WithServerOptions(redis.WithAddress(businessFake.addr))},
		redisServerCfg(contractFake.addr, ""))
	if ep := rsrv.Endpoint(); ep != "redis://"+businessFake.addr {
		t.Fatalf("Endpoint() = %q, want redis://%s（契约地址 %s 不该还在服务面上）",
			ep, businessFake.addr, contractFake.addr)
	}
}

// --- 订阅 flush（deferred map → Start） ----------------------------------------

// TestMappingOffline_SubscriberFromAfterBuildFlushesAtStart WithAfterBuild 登记的
// 消费者在 Start 的 flush 里真的被 Subscribe：flush 失败会 errors.Join 上抛、
// started 不落真——这里 Subscribe 对假 RESP 的 SUBSCRIBE 有确认回包，红了就说明
// flush 链断了。
func TestMappingOffline_SubscriberFromAfterBuildFlushesAtStart(t *testing.T) {
	f := newFakeRedis(t)
	var registered bool
	rsrv := startRedis(t, []Option{WithAfterBuild(func(_ context.Context, s *redis.Server) error {
		if err := s.RegisterSubscriber("kt.contract.topic",
			func(context.Context, broker.Event) error { return nil },
			func() any { return &payload{} }); err != nil {
			return err
		}
		registered = true
		return nil
	})}, redisServerCfg(f.addr, ""))

	if !registered {
		t.Fatal("RegisterSubscriber in after-build was not run")
	}
	if ep := rsrv.Endpoint(); ep != "redis://"+f.addr {
		t.Fatalf("Endpoint() = %q, want redis://%s（带订阅者的 Start 没走完 flush？）", ep, f.addr)
	}
}

// TestMappingOffline_RegisterSubscriberAfterStartUsesLivePath Start 之后的
// RegisterSubscriber 走 doRegisterSubscriber 直连路径（不再是 deferred map），
// 对假 RESP 必须成功——两个分支都要有判据，否则 flush 路径绿掩盖 live 路径坏。
func TestMappingOffline_RegisterSubscriberAfterStartUsesLivePath(t *testing.T) {
	f := newFakeRedis(t)
	rsrv := startRedis(t, nil, redisServerCfg(f.addr, ""))

	if err := rsrv.RegisterSubscriber("kt.live.topic",
		func(context.Context, broker.Event) error { return nil },
		func() any { return &payload{} }); err != nil {
		t.Fatalf("RegisterSubscriber after Start: %v", err)
	}
	if ep := rsrv.Endpoint(); ep != "redis://"+f.addr {
		t.Fatalf("Endpoint() = %q, want redis://%s", ep, f.addr)
	}
}
