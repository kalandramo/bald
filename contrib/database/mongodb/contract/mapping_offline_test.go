package contract

// mapping_offline_test.go —— 契约 database.mongodb 八行映射里的**离线**四行
// （风险登记册 §7.23，§7.14 同族）。
//
// 观测面是**一个不说话的假线**：`net.Listen` 之后只接受 TCP、把字节读干、**永不回
// MongoDB 握手**。driver v2 的所有可观测行为都从「这条连接上发生了几次握手」里读出来，
// 于是四行各有各的读法：
//   - uri：拨号落在 URI 里那个 host:port 上（假线按端口记账，另一条假线必须零记账）；
//   - server_selection_timeout_seconds：死地址下**整个操作耗时**就是这一行的读数
//     （错误文案在所有超时机制下都是同一句 `context deadline exceeded`，只有时长能分）；
//   - connect_timeout_seconds：假线接受 TCP 但不回握手 ⇒ 握手必然挂到 connect 超时才
//     放弃并**重新拨号**，所以「窗口里出现几次 accept」是这一行的直读口；不给这行时
//     驱动用默认 30s，同一个窗口里只有 1 次 accept——这一对差值是**本行唯一的观测面**；
//   - database 的**缺席语义**：契约不给 database 时操作当场报 `database name cannot
//     be empty`，这一步发生在服务端选择**之前**，所以死地址也能判。
//
// 另外四行的口径见 mapping_backend_test.go：database 的**取值**、credentials 两态、
// heartbeat、max_conn_idle 那四行只有真后端读得回；本文件判掉 uri、
// server_selection_timeout、connect_timeout 三行，加 database 的**缺席语义**。
//
// `timeout_seconds` 一行**没有运行期**判据（它唯一的执行证据是 `contract_test.go` 的
// 切片长度），理由是一条实测的上游掩蔽，写在 mapping_backend_test.go 文件头。
//
// 归因耦合（本文件三条腿共享一个前提）：驱动是**惰性建连**的，`Provider` 返回时一个
// 包都不发，所以下面三条都要**先跑一次真实操作**才有线可看。这一句同时决定了本文件
// 不判「构造不报错」那类形状——`contract_test.go` 里已经有了。
//
// 跑法：四条都不依赖服务容器，`-short` 照跑，进 `scripts/acceptance-gate.sh` 的
// scope=ci 判据表。

import (
	"context"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	mongocrud "github.com/kalandramo/bald-crud/mongodb"
)

// silentWire 是一条接受连接、读干字节、永不回话的假线。
type silentWire struct {
	lis net.Listener

	mu     sync.Mutex
	accept int
}

func newSilentWire(t *testing.T) *silentWire {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	w := &silentWire{lis: lis}
	// 先关假线再停读：反过来的话在途连接的 EOF 会晚于 t.Cleanup 返回，race 下是噪声。
	t.Cleanup(func() { _ = lis.Close() })
	go func() {
		for {
			conn, err := lis.Accept()
			if err != nil {
				return
			}
			w.mu.Lock()
			w.accept++
			w.mu.Unlock()
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				_, _ = io.Copy(io.Discard, c)
			}(conn)
		}
	}()
	return w
}

// addr 返回 `127.0.0.1:port`，可直接拼进 `mongodb://`。
func (w *silentWire) addr(t *testing.T) string {
	t.Helper()
	host, port, err := net.SplitHostPort(w.lis.Addr().String())
	if err != nil {
		t.Fatalf("split %s: %v", w.lis.Addr(), err)
	}
	return net.JoinHostPort(host, port)
}

func (w *silentWire) seen() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.accept
}

// deadAddr 要一个**确认没人监听**的地址：临时监听再立刻关掉的端口。
// 写死端口不行——那台机器上可能真有东西听着，「连不上」这个前提就没了。
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

// opElapsed 跑一次真实写入，返回耗时与错误串。用它是因为本文件三条腿的读数都是
// 「这一次操作里线上发生了什么」，而操作不触发的话什么都没有。
func opElapsed(t *testing.T, sec *bootstrapv1.Database_MongoDB) (time.Duration, string) {
	t.Helper()
	c, closeFn, err := Provider(context.Background(), &bootstrapv1.Database{Mongodb: sec})
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	defer closeFn()
	cli, ok := c.(*mongocrud.Client)
	if !ok {
		t.Fatalf("Provider 交出 %T，不是 *mongodb.Client", c)
	}
	start := time.Now()
	_, err = cli.InsertOne(context.Background(), "zzmapping", map[string]any{"probe": "1"})
	if err == nil {
		t.Fatal("假线/死地址上操作竟然成功了，观测面失效")
	}
	return time.Since(start), err.Error()
}

// TestMappingOffline_URIHostPortReachesDial 判 uri 行：拨号必须落在 URI 里那个
// host:port 上。
//
// 两条假线：契约只给 A，于是 A 要有握手、B 要一个都没有。判「端口对不对」而不是
// 「有没有连接」——丢了端口、取错 host、把整串塞给错误的字段，都只会让 A 那边空空。
func TestMappingOffline_URIHostPortReachesDial(t *testing.T) {
	a := newSilentWire(t)
	b := newSilentWire(t)
	addrA := a.addr(t)

	_, _ = opElapsed(t, &bootstrapv1.Database_MongoDB{
		Uri:                           "mongodb://" + addrA,
		Database:                      "zzdb",
		ServerSelectionTimeoutSeconds: 2,
	})

	if got := a.seen(); got == 0 {
		t.Fatalf("契约 uri=%q 却没有任何拨号落在 A（%s）上——uri 行没进 ApplyURI，"+
			"或者地址在映射里被改坏了", addrA, addrA)
	}
	if got := b.seen(); got != 0 {
		t.Fatalf("契约只给了 A，B 却也收到 %d 次拨号——uri 里的主机被换掉了", got)
	}
}

// TestMappingOffline_ServerSelectionTimeoutBoundsFailure 判 server_selection_timeout
// 行的**取值**：两个不同的契约值必须给出两个不同的失败时长。
//
// 为什么只能判时长：错误文案在所有超时机制下都是同一句
// `server selection error: context deadline exceeded`（死地址实测），驱动不会把
// 「SST 到了」和「ctx 到了」写成两句话。所以这条按 [下界, 上界] 区间判：
// sst=1 ⇒ ≤3s，sst=3 ⇒ ≥2s。写成 `len(opts)` 或「构造不报错」都判不出这一行。
//
// 上界留 3 倍余量是给 CI runner 的调度抖动；下界取 2s（契约值的一半）而不是 2.9s，
// 因为「映射把 1 写成了 3」这种变异由**第二条**判据接住，不该靠第一条的边界精度。
func TestMappingOffline_ServerSelectionTimeoutBoundsFailure(t *testing.T) {
	dead := deadAddr(t)

	el1, err1 := opElapsed(t, &bootstrapv1.Database_MongoDB{
		Uri: "mongodb://" + dead, Database: "zzdb", ServerSelectionTimeoutSeconds: 1,
	})
	if el1 > 3*time.Second {
		t.Fatalf("sst=1s 却跑了 %s（err=%s）——这一行没落到 WithServerSelectionTimeout"+
			"（不投递时是包装层的 10s 截止期在兜底）", el1.Round(time.Millisecond), err1)
	}

	el3, err3 := opElapsed(t, &bootstrapv1.Database_MongoDB{
		Uri: "mongodb://" + dead, Database: "zzdb", ServerSelectionTimeoutSeconds: 3,
	})
	if el3 < 2*time.Second {
		t.Fatalf("sst=3s 却在 %s 就失败（err=%s）——契约值被换成了别的常量"+
			"（比如把两行都投成 1s）", el3.Round(time.Millisecond), err3)
	}
	if el3 > 6*time.Second {
		t.Fatalf("sst=3s 却跑了 %s（err=%s）——超出了这一行该有的上界", el3.Round(time.Millisecond), err3)
	}
}

// TestMappingOffline_ConnectTimeoutAbortsHungHandshake 判 connect_timeout 行。
//
// 这一行在死地址上完全隐形（ECONNREFUSED 当场就返回，拨号根本没花时间可超时），所以
// 要一条**接受 TCP 但永不回握手**的假线：驱动的握手会挂住，直到 connect 超时才放弃，
// 放弃之后重新拨号 ⇒ 同一个窗口里 A（connect=1）攒出多次 accept，B（不给这一行，驱动
// 默认 30s）只有 1 次。实测本机 A=5 次 / B=1 次（窗口 = sst=4s）。
//
// 阈值取 ≥3 且 ≤10。下界：不给这一行时（驱动默认 30s）只 1 次 accept，与投了 1s 的 4～5
// 次隔着两次以上。**上界不是装饰**——变异轮实测：只写下界时 `time.Second`→
// `time.Millisecond` 这条单位手滑判绿（放弃得更快 ⇒ accept 更多 ⇒ 照样越过下界）。把上界
// 临时压到 0 读出的两个数：按秒投 4 次、投成毫秒 **16** 次 ⇒ 界取 10（离基线 2.5 倍、离
// 毫秒形态 1.6 倍）。这条上界判的是**换算/单位**：窗口里塞得下几次拨号由监控节奏（这里
// hb=1s）兜着，契约值再大也越不过窗口，所以「1s 与 2s」这种正常取值差不在本条的射程内——
// 能判的是「投了且投的是秒级」。判**次数**而不是判错误文案：文案里那句
// `incomplete read of message header` 是驱动内部措辞，版本一变就漂。
func TestMappingOffline_ConnectTimeoutAbortsHungHandshake(t *testing.T) {
	fast := newSilentWire(t)
	slow := newSilentWire(t)

	fastAddr, slowAddr := fast.addr(t), slow.addr(t)
	const sst = 4

	_, _ = opElapsed(t, &bootstrapv1.Database_MongoDB{
		Uri: "mongodb://" + fastAddr, Database: "zzdb",
		ServerSelectionTimeoutSeconds: sst, HeartbeatIntervalSeconds: 1, ConnectTimeoutSeconds: 1,
	})
	fastSeens := fast.seen()

	_, _ = opElapsed(t, &bootstrapv1.Database_MongoDB{
		Uri: "mongodb://" + slowAddr, Database: "zzdb",
		ServerSelectionTimeoutSeconds: sst, HeartbeatIntervalSeconds: 1,
	})
	slowSeens := slow.seen()

	// 把两侧实测打出来：这条腿的界（≥3 / ≤10）距基线只有一到两次的余量，判绿时不报数
	// 就等于把余量交给下一个换机器的人去猜。
	t.Logf("窗口 %ds：connect=1s 一侧 accept=%d，不给 connect 的对照一侧 accept=%d", sst, fastSeens, slowSeens)

	if fastSeens < 3 {
		t.Fatalf("connect=1s 在 %ds 窗口里只拨了 %d 次，want ≥3——这一行没落到"+
			" WithConnectTimeout（不投递时驱动默认 30s，握手一直挂着，只有 1 次 accept；"+
			"实测本机 A=%d B=%d）", sst, fastSeens, fastSeens, slowSeens)
	}
	if fastSeens > 10 {
		t.Fatalf("connect=1s 在 %ds 窗口里拨了 %d 次，want ≤10——放弃得比契约值快得多，"+
			"这一行的**单位或换算**漂了（`time.Second` 写成 `time.Millisecond` 实测 16 次，"+
			"正是这一侧响；按秒投时实测 4～5 次）", sst, fastSeens)
	}
	if slowSeens > 2 {
		t.Fatalf("没给 connect 的对照线却拨了 %d 次，want ≤2——假线不再「接受但永不回话」，"+
			"或者驱动的握手超时口径变了，本条的观测面要重立（实测基线：A=%d B=%d）",
			slowSeens, fastSeens, slowSeens)
	}
}

// TestMappingOffline_EmptyDatabaseHasNoDefault 判 database 那行 `!= ""` 守卫的**缺席
// 语义**：契约不给 database 时，包装层没有任何默认库，操作当场报
// `database name cannot be empty`。
//
// 这条值得钉住是因为它是个静默的形状：`if db := sec.GetDatabase(); db != ""` 看上去
// 像「不填就用驱动的默认」，实际是「不填就每次操作都失败」。把它改成永远投递
// （`WithDatabase(sec.GetDatabase())`）时，空串进 `c.database`，报错形状一模一样，
// 所以这条判的是**有没有默认库**这件事本身，不是守卫的实现细节；取值对不对在
// mapping_backend_test.go 里判。
//
// 顺带记一条实测：这一步**不需要真后端**——驱动在组装命名空间时就报这个错，早于服务端
// 选择，所以死地址上它立刻返回（本机 <10ms）。这条判据因此留在离线文件，不进集成作业。
func TestMappingOffline_EmptyDatabaseHasNoDefault(t *testing.T) {
	el, errText := opElapsed(t, &bootstrapv1.Database_MongoDB{
		Uri: "mongodb://" + deadAddr(t), ServerSelectionTimeoutSeconds: 5,
	})
	if !strings.Contains(errText, "database name cannot be empty") {
		t.Fatalf("契约不给 database 时报的是 %q, want 含 %q——包装层默认库的语义变了，"+
			"或者这行不再是条件投递", errText, "database name cannot be empty")
	}
	if el > 2*time.Second {
		t.Fatalf("这个错本该在组装命名空间时当场返回（早于服务端选择），却花了 %s——"+
			"说明它现在来自别处，本条的口径要重立", el.Round(time.Millisecond))
	}
}
