package contract

// mapping_offline_test.go —— 契约 registry.nacos 九行映射里的**离线三行**（风险登记册 §7.22）。
//
// 观测面是进程内的一个 HTTP 桩：nacos SDK v2 在**构造 naming client 时**（`New` 里
// 同步调用，不是后台协程）向 server 的 `host:port` POST
// `/nacos/v1/auth/users/login`，form 体带 username/password。实测（本文件跑绿的那轮）
// Provider 返回的那一刻这个请求已经落在桩上，且整个构造只发这一个请求——所以桩同时
// 是 server_addrs（请求打到哪个 host:port）、username、password（form 里那两个键）
// 三行的直读口，不需要真后端。
//
// 覆盖与分工（另五行的口径见 mapping_backend_test.go）：
//   - server_addrs：本文件两条（首位直读 + 尾位在首位失联时接住登录）；
//   - username / password：本文件三条（值直读 + 两个方向的 absent 语义）；
//   - namespace / group / cluster_name / weight / default_kind：只有真后端读得回，
//     在那条文件里判；
//   - prefix：`options.prefix` 在全仓没有任何读者（契约字段是死的），无面可判，
//     只登记。
//
// 为什么不复用 provider_test.go 那几条：它们判的是**报错形状**（必填缺失、地址格式
// 非法），本文件判的是**成功路径上值真的落到 client**。provider_test.go 头部那句
// 「成功构造路径离线不可行」已经被上面那个观测面推翻了，见那里改过的口径。
//
// 归因口径（变异验活实测出来的）：登录请求是本文件唯一的构造期信号，所以**地址两条腿
// 的观察依赖 username 非空**。把 username 整行去掉时，本文件四条腿一起红（没有登录请求
// 可读）——不是「地址也漂了」。取值漂了的证据在凭据那条，它按 form 里的键逐一判值。

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	registry "github.com/kalandramo/bald/registry"
)

// stubRequest 是桩收到的一次请求。只留判据用到的三个维度：Host（= SDK 实际连的那个
// server_addr）、form 体（= 凭据两行）、path（用于把登录请求和其它请求分开）。
type stubRequest struct {
	host string
	path string
	form url.Values
}

// stubNacos 是一个记账的 nacos HTTP 桩。回包按实测的登录回包形状给，让 SDK 走完
// 「拿到 token」这条正常分支——回错误码会让 SDK 走重试/异常路径，那时候「一个请求」
// 这个判据就不是映射层的证据了。
type stubNacos struct {
	srv *httptest.Server

	mu       sync.Mutex
	requests []stubRequest
}

func newStubNacos(t *testing.T) *stubNacos {
	t.Helper()
	s := &stubNacos{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(io.LimitReader(req.Body, 1<<20))
		form := url.Values{}
		if err == nil {
			form, _ = url.ParseQuery(string(body))
		}
		s.mu.Lock()
		s.requests = append(s.requests, stubRequest{host: req.Host, path: req.URL.Path, form: form})
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"accessToken":"stub-token","tokenTtl":18000,"globalAdmin":true}`))
	}))
	// 注册顺序决定倒序执行的顺序：先关桩会把 SDK 的在途请求变成 noise，所以桩最后关。
	t.Cleanup(s.srv.Close)
	return s
}

// addr 返回形如 `127.0.0.1:port` 的契约地址（SDK 的 server_addr 要的是 host:port，
// 不带 scheme——带 scheme 的写法会在 net.SplitHostPort 那一步报 too many colons）。
func (s *stubNacos) addr(t *testing.T) string {
	t.Helper()
	u, err := url.Parse(s.srv.URL)
	if err != nil {
		t.Fatalf("parse stub url %s: %v", s.srv.URL, err)
	}
	return u.Host
}

func (s *stubNacos) seen() []stubRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]stubRequest{}, s.requests...)
}

// deadAddr 要一个**确认没人监听**的地址：拿一个临时监听再立刻关掉的端口。
// 用写死的端口不行——那台机器上可能真有东西听着，「失联」这个前提就没了。
func deadAddr(t *testing.T) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := lis.Addr().String()
	_ = lis.Close()
	return addr
}

// newStubRegistrar 走**契约构造**（Provider），与装配同一条路径——判据要落在映射层，
// 不能绕开它去调 nacos.New。cleanup 由调用方拿着，t.Cleanup 兜底。
func newStubRegistrar(t *testing.T, sec *bootstrapv1.Registry_Nacos) (registry.Registrar, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	reg, closeReg, err := Provider(ctx, &bootstrapv1.Registry{Type: Type, Nacos: sec})
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	t.Cleanup(closeReg)
	return reg, closeReg
}

// TestMappingOffline_FirstServerAddrReachesSocket 判 server_addrs 首行：契约里给的
// 第一个地址必须**原样**成为 SDK 连的那个 host:port。
//
// 桩开两个，契约按 [A, B] 的顺序给，登录请求必须落在 A 上。实测顺序交换后请求跟着
// 换到新的首位——「打第一个 server」是 SDK 的行为，于是这条同时判住了「映射把顺序
// 弄反」和「映射只取某个元素」。
//
// 判 Host 相等而不是判「桩活着」：Host 是 SDK 实际发出的请求头，值漂了（多了 scheme
// 前缀、丢了端口、取错元素）都会在这里红，而连接成功本身对这些漂移无感。
func TestMappingOffline_FirstServerAddrReachesSocket(t *testing.T) {
	a := newStubNacos(t)
	b := newStubNacos(t)
	addrA, addrB := a.addr(t), b.addr(t)

	newStubRegistrar(t, &bootstrapv1.Registry_Nacos{
		ServerAddrs: []string{addrA, addrB},
		Username:    "first-user",
		Password:    "first-pass",
	})

	reqs := a.seen()
	if len(reqs) == 0 {
		t.Fatalf("构造期桩 A（%s）一个请求都没收到——server_addrs 首位没落到 client 上", addrA)
	}
	for _, r := range reqs {
		if r.host != addrA {
			t.Errorf("桩 A 收到 Host=%q 的请求，want %q——契约地址在映射里被改了", r.host, addrA)
		}
	}
	// 首位活着时，第二个地址此刻不可观测（SDK 只向一个 server 登录）；桩 B 上有没有
	// 请求不是映射层的判据，所以这里不断言它为空——尾行的证据在下一条。
}

// TestMappingOffline_TailServerAddrReachesSocket 判 server_addrs 的**尾项**：
// 首位是一个确认失联的地址时，登录必须落在第二项上。
//
// 这条是上一条补不上的那半个判据：映射写成 `WithServerAddrs(sec.GetServerAddrs()[0])`
// （只投首位）时，上一条照样全绿。实测 SDK 会在首位失联时把登录发到下一项，于是
// 「尾项也进了 client」变成可判的。
func TestMappingOffline_TailServerAddrReachesSocket(t *testing.T) {
	b := newStubNacos(t)
	addrB := b.addr(t)

	newStubRegistrar(t, &bootstrapv1.Registry_Nacos{
		ServerAddrs: []string{deadAddr(t), addrB},
		Username:    "tail-user",
		Password:    "tail-pass",
	})

	reqs := b.seen()
	if len(reqs) == 0 {
		t.Fatalf("首位失联后桩 B（%s）也没收到请求——server_addrs 的尾项没落到 client 上"+
			"（映射只投了首项，或 SDK 的失联切换行为变了）", addrB)
	}
	for _, r := range reqs {
		if r.host != addrB {
			t.Errorf("桩 B 收到 Host=%q 的请求，want %q", r.host, addrB)
		}
	}
}

// TestMappingOffline_CredentialsLandInLoginRequest 判 username / password 两行的值：
// 登录请求的 form 里两个键必须**逐一等于**契约值。
//
// 「Provider 返回时请求已经在桩上」这句也在这里判——它是「构造期同步发出」的证据，
// 也正是这个观测面成立的前提。改成异步的话这条会红，那时候要重新找观测面，而不是
// 在这里加 sleep 把它蒙过去。
func TestMappingOffline_CredentialsLandInLoginRequest(t *testing.T) {
	const user, pass = "probe-user", "probe-pass"
	stub := newStubNacos(t)

	newStubRegistrar(t, &bootstrapv1.Registry_Nacos{
		ServerAddrs: []string{stub.addr(t)},
		Username:    user,
		Password:    pass,
	})

	reqs := stub.seen()
	if len(reqs) == 0 {
		t.Fatal("构造期桩没收到登录请求——username 非空时 SDK 应当同步发起登录，观测面失效")
	}
	var found bool
	for _, r := range reqs {
		if r.form.Get("username") != user && r.form.Get("password") != pass {
			continue
		}
		found = true
		if got := r.form.Get("username"); got != user {
			t.Errorf("form.username=%q, want %q（契约 username 行没落到 ClientConfig.Username）", got, user)
		}
		if got := r.form.Get("password"); got != pass {
			t.Errorf("form.password=%q, want %q（契约 password 行没落到 ClientConfig.Password）", got, pass)
		}
	}
	if !found {
		t.Fatalf("桩收到的 %d 个请求里没有带这对凭据的（第一个是 %+v）——凭据两行被丢或被打乱",
			len(reqs), reqs[0])
	}
}

// TestMappingOffline_NoLoginWithoutUsername 判 username 行的 **absent 语义**：
// 契约只给密码不给用户名时，构造期一个 HTTP 请求都不发。
//
// 这条不是「映射没投递密码」的判据（那条上面已经有了），它判的是更值钱的一件事：
// nacos 的鉴权口在 username 为空时**整个跳过登录**，密码单独设置等于没设。这是个
// 会被误用的地方（配了密码以为有鉴权），所以要用判据钉住现状，而不是留在注释里。
//
// 判据取「Provider 返回时请求数为 0」而不是「等一会儿还是 0」：实测同步发出，所以
// 不给时间窗就是诚实的判据；加了窗口反而把它变成时序题。
func TestMappingOffline_NoLoginWithoutUsername(t *testing.T) {
	stub := newStubNacos(t)

	newStubRegistrar(t, &bootstrapv1.Registry_Nacos{
		ServerAddrs: []string{stub.addr(t)},
		Password:    "orphan-pass",
	})

	if reqs := stub.seen(); len(reqs) != 0 {
		t.Fatalf("没给 username 却收到 %d 个请求（第一个 %+v）——SDK 的登录前提变了，"+
			"本条的口径要重立", len(reqs), reqs[0])
	}
}

// TestMappingOffline_UsernameAloneStillLogsIn 是上一条的另一半：只给用户名（密码为
// 空）时登录照样发出去，且 form 里的 username 是那个值。
//
// 两条合起来才把 username 这行的守卫口径钉死：`if u := c.GetUsername(); u != ""`
// ——空则整行不投（SDK 匿名），非空则投。把守卫改成「用户名和密码都非空才投」的话，
// 这条红；改成「永远投」的话，上一条红。
//
// 密码这一侧不写成「form 必须有 password 键」：url.Values.Get 对缺失键与空值都回
// 空串，那个断言恒真（§7.20 记过的同类错误）。这里判的是**请求存在**与 username
// 的值，两个都不是恒真。
func TestMappingOffline_UsernameAloneStillLogsIn(t *testing.T) {
	const user = "user-only"
	stub := newStubNacos(t)

	newStubRegistrar(t, &bootstrapv1.Registry_Nacos{
		ServerAddrs: []string{stub.addr(t)},
		Username:    user,
	})

	reqs := stub.seen()
	if len(reqs) == 0 {
		t.Fatal("只给用户名时没发登录请求——守卫被改宽了（要求密码也非空），" +
			"实测：空密码照样带 username 登录")
	}
	var found bool
	for _, r := range reqs {
		if r.form.Get("username") == user {
			found = true
		}
	}
	if !found {
		t.Fatalf("登录请求的 form.username 里没有 %q：%+v", user, reqs)
	}
}
