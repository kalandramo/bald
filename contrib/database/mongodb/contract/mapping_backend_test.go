package contract

// mapping_backend_test.go —— 契约 database.mongodb 八行映射里**只有真后端能读回**的
// 四行（风险登记册 §7.23，§7.14 同族）。离线那四行见 mapping_offline_test.go。
//
// 四行各自的观测面（全部本机实测，mongo:7 standalone、未开 --auth）：
//   - database 的**取值**：契约库写文档，再用裸驱动在「契约库」和「邻居库」各数一次
//     文档数。只判前者是恒真式的（数自己写的库总能中），正反这一对才判得住「库选对了」。
//   - credentials（username+password 合成的一行 `WithCredentials`）：**假凭据在没开
//     鉴权的后端上照样报错**。驱动只要拿到 Credential 就一定会走 SASL 握手，后端没有
//     这个用户 ⇒ `AuthenticationFailed`。所以这一行不需要 auth 版 mongod 就能判，
//     而且是反向判（投递了才红得起来）。
//   - credentials 的**守卫形状**：`bald-crud` 那侧是 `if username != "" && password != ""`
//     才投 `SetAuth`，只给用户名 ⇒ 整行不投 ⇒ 后端不鉴权 ⇒ 操作**成功**。这条是
//     「配了用户名以为开了鉴权」这个误用的直接证据，与 §7.22 里 nacos 那条同形。
//   - heartbeat_interval_seconds：`serverStatus.network.numRequests` 是全服计数器，
//     绝对值没用，必须做**窗口差分**：基线（只有那个用来读计数的裸客户端）→ 建一个
//     hb=1 的契约客户端 → 同一个窗口再读。实测本机基线 Δ=1..3、hb=1 多出 14、hb=10
//     与 hb=30 各多出 2。差分的另一半（hb=30 多出得少）判的是「投的是契约值而不是
//     某个常量」——只有 hb=1 那条时，映射写成固定 1s 也照样绿。
//   - max_conn_idle_time_seconds：驱动**没有**定时回收，过期只在连接被取出/放回时对
//     `idleTimeoutExpired()` 判一下（实测静置 5s 后 `connections.current` 纹丝不动）。
//     所以判据必须「闲置一段时间**后再来一波并发操作**」，读 `connections.totalCreated`
//     的增量：maxIdle=1 ⇒ 一波 4 个并发全部重建（本机 Δ=4）；maxIdle=10（闲置 4s 不到）
//     与不给（默认 2 分钟）⇒ 复用（Δ=0）。中间那一格是**取值轴**，少它就能把这一行投成
//     常量而全绿（变异轮实测，见该条腿注释）。
//
// `timeout_seconds` 一行**没有运行期**判据，也不是遗漏（登记在 §7.23）：它唯一的执行
// 证据是 `contract_test.go` 的切片长度。它投给驱动的
// `SetTimeout`，而驱动文档写明「操作上下文上已经有截止期时 Timeout 不生效」；
// `bald-crud` 的**每一个**操作都用 `context.WithTimeout(ctx, c.timeout)` 包了一层，
// `c.timeout` 是构造期写死的 10s 且没有任何 Option 能改。于是这一行在 bald 的消费路径
// 上被上游的固定截止期完全遮住——实测：`sst=20 + timeout=1` 的操作耗时 10.002s，
// 正是包装层那个 10s。要让它生效要么改上游、要么在 bald 侧把 `Client` 从类型别名换成
// 包装类型（API 变更），都不是一条判据能收口的。
//
// 跑法与 CI 归属同 §7.22：`-short` 与后端不可达都跳过；地址取 `BALD_MONGO_URI`
// （与 CI integration 作业注入的 mongo:7 服务同一个变量）。这几条**不进**
// `scripts/acceptance-gate.sh` 的判据表（表在 test 作业跑，那里没有 mongo），
// 名字必须出现在 `.github/workflows/ci.yml` 的 integration EXPECTED 里，
// docscheck 的 `TestAcceptanceTableCoversShortSkippedTests` 认这条路径。

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	mongocrud "github.com/kalandramo/bald-crud/mongodb"

	"go.mongodb.org/mongo-driver/v2/bson"
	mongoV2 "go.mongodb.org/mongo-driver/v2/mongo"
	optionsV2 "go.mongodb.org/mongo-driver/v2/mongo/options"
)

// mongoURI 取真后端地址；没设或连不上都跳过（跳过在 CI 里会被判据名那条兜住）。
func mongoURI(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("需要真 mongo 的映射判据在 -short 下跳过")
	}
	uri := os.Getenv("BALD_MONGO_URI")
	if uri == "" {
		t.Skipf("BALD_MONGO_URI 未设，跳过需要真 mongo 的映射判据")
	}
	hostPort := strings.TrimPrefix(strings.TrimPrefix(uri, "mongodb://"), "mongodb+srv://")
	// 只判「这个 host:port 有没有人听着」：真后端语义由后面的判据自己负责。
	d := net.Dialer{Timeout: 2 * time.Second}
	conn, err := d.DialContext(context.Background(), "tcp", hostPort)
	if err != nil {
		t.Skipf("BALD_MONGO_URI=%s 不可达（%v），跳过", uri, err)
	}
	_ = conn.Close()
	return uri
}

// contractClient 走**契约构造**（Provider），与装配同一条路径——判据要落在映射层。
func contractClient(t *testing.T, sec *bootstrapv1.Database_MongoDB) (*mongocrud.Client, func()) {
	t.Helper()
	c, closeFn, err := Provider(context.Background(), &bootstrapv1.Database{Mongodb: sec})
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	cli, ok := c.(*mongocrud.Client)
	if !ok {
		t.Fatalf("Provider 交出 %T，不是 *mongodb.Client", c)
	}
	return cli, closeFn
}

// rawClient 是**绕开契约**的裸驱动客户端，只用来读服务端计数与跨库数文档。
// 用它的原因很直白：判据不能拿被检对象自己当量具。
func rawClient(t *testing.T, uri string) *mongoV2.Client {
	t.Helper()
	cli, err := mongoV2.Connect(optionsV2.Client().ApplyURI(uri))
	if err != nil {
		t.Fatalf("raw connect: %v", err)
	}
	t.Cleanup(func() { _ = cli.Disconnect(context.Background()) })
	return cli
}

// serverCounter 读 serverStatus 里某个 section 的一个数值字段。
// 走 JSON 往返而不是直接断言 `map[string]any` 的具体类型：驱动把子文档解成什么
// （map / bson.D / 数值是 int32 还是 int64）跨版本会变，解错就静默读到 0，
// 那正是 §7.19 记过的「计数读成 0 还当判据」的形状。这里读不到就当场 Fatal。
func serverCounter(t *testing.T, raw *mongoV2.Client, section, field string) int64 {
	t.Helper()
	var out map[string]any
	if err := raw.Database("admin").RunCommand(context.Background(),
		bson.D{{Key: "serverStatus", Value: 1}}).Decode(&out); err != nil {
		t.Fatalf("serverStatus: %v", err)
	}
	b, err := json.Marshal(out[section])
	if err != nil {
		t.Fatalf("marshal %s: %v", section, err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatalf("unmarshal %s（实际形态：%s）: %v", section, b, err)
	}
	var n int64
	if err := json.Unmarshal(fields[field], &n); err != nil {
		t.Fatalf("数值字段 %s.%s 读不到（该 section 实际形态：%s）: %v", section, field, b, err)
	}
	return n
}

// requestWindow 数一次窗口内的 `network.numRequests` 增量。窗口两端各有一次
// serverStatus 本身，这部分在差分里对消——所以调用方必须把「有契约客户端」和
// 「没有契约客户端」两个**同长**窗口相减，而不是拿绝对值当判据。
func requestWindow(t *testing.T, raw *mongoV2.Client, window time.Duration) int64 {
	t.Helper()
	before := serverCounter(t, raw, "network", "numRequests")
	time.Sleep(window)
	return serverCounter(t, raw, "network", "numRequests") - before
}

// wave 起 n 个并发插入。用来制造 n 条池连接——顺序操作只会复用同一条，那样
// 「闲置后重建」的增量就只有 1，和背景噪声贴边。
func wave(t *testing.T, c *mongocrud.Client, collection string, n int) {
	t.Helper()
	var wg sync.WaitGroup
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.InsertOne(context.Background(), collection, map[string]any{"probe": "1"}); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("并发插入失败: %v", err)
	}
}

func countDocs(t *testing.T, raw *mongoV2.Client, db, collection string) int64 {
	t.Helper()
	n, err := raw.Database(db).Collection(collection).
		CountDocuments(context.Background(), bson.D{{Key: "probe", Value: "1"}})
	if err != nil {
		t.Fatalf("count %s.%s: %v", db, collection, err)
	}
	return n
}

// TestMappingBackend_DatabaseSelectsNamespace 判 database 行的**取值**：契约库收到
// 写入，邻居库与驱动默认库 `test` 都收不到。
//
// 邻居库这一步不是装饰：映射写成 `WithDatabase("test")`（或整行不投但包装层有默认）时，
// 「契约库里有一条」照样成立吗？不成立——所以正判已经够咬。取反判是为了把「写进了别的
// 库」这件事**指名道姓**地报出来：报错时会给出邻居库的条数，比「契约库 0 条」更好定位。
func TestMappingBackend_DatabaseSelectsNamespace(t *testing.T) {
	uri := mongoURI(t)
	raw := rawClient(t, uri)
	const coll = "zzmapping_database"

	// 每次跑先清：库里留着上一轮的文档会让「邻居库 0 条」被残留污染。
	// 第三个库（`test`，驱动 URI 无路径时的默认库）不是装饰：变异轮实测过——一次把
	// database 映射成 `"test"` 的变异在这里留下一条文档，之后**每一条**变异都被这半个
	// 判据判红（0 条断言读的是全局状态，不清就不闭合）。
	for _, db := range []string{"bald_map_db_a", "bald_map_db_b", "test"} {
		if _, err := raw.Database(db).Collection(coll).DeleteMany(context.Background(), bson.D{}); err != nil {
			t.Fatalf("clean %s: %v", db, err)
		}
	}

	c, closeFn := contractClient(t, &bootstrapv1.Database_MongoDB{Uri: uri, Database: "bald_map_db_a"})
	defer closeFn()
	if _, err := c.InsertOne(context.Background(), coll, map[string]any{"probe": "1"}); err != nil {
		t.Fatalf("insert with contract database: %v", err)
	}

	if got := countDocs(t, raw, "bald_map_db_a", coll); got != 1 {
		t.Fatalf("契约库 bald_map_db_a 里 %d 条，want 1——database 行没落到 WithDatabase", got)
	}
	if got := countDocs(t, raw, "bald_map_db_b", coll); got != 0 {
		t.Fatalf("邻居库 bald_map_db_b 里 %d 条，want 0——文档写进了别的库"+
			"（契约的 database 值在映射里被替换或丢弃）", got)
	}
	if got := countDocs(t, raw, "test", coll); got != 0 {
		t.Fatalf("驱动默认库 test 里 %d 条，want 0——同上，文档落到了没被契约指定的库", got)
	}
}

// TestMappingBackend_CredentialsReachAuthHandshake 判 credentials 行（username+password
// 一起给）：假凭据必须让操作在 SASL 握手处失败。
//
// 后端**没开** --auth 也能判这一行，这是本轮实测出来的形状：驱动只要拿到 Credential 就
// 一定发起 SASL（`saslStart`），standalone mongod 里没有这个用户 ⇒ `AuthenticationFailed`。
// 所以这条判据反过来读更准：**没投递这行时操作会成功**（那正是下一条的口径）。
//
// 判错误串里的 `AuthenticationFailed` 而不是「操作失败」——后者在地址错、库名错、
// 网络抖动下都成立，判不出是凭据这一行带进来的。
func TestMappingBackend_CredentialsReachAuthHandshake(t *testing.T) {
	uri := mongoURI(t)
	c, closeFn := contractClient(t, &bootstrapv1.Database_MongoDB{
		Uri: uri, Database: "bald_map_creds",
		Username: "bald_no_such_user", Password: "bald_wrong_pass",
		ServerSelectionTimeoutSeconds: 5,
	})
	defer closeFn()

	_, err := c.InsertOne(context.Background(), "zzmapping_creds", map[string]any{"probe": "1"})
	if err == nil {
		t.Fatal("假凭据下插入竟然成功——credentials 行没落到 WithCredentials/SetAuth" +
			"（后端未开鉴权时，不投递才会让操作直接过）")
	}
	if !strings.Contains(err.Error(), "AuthenticationFailed") {
		t.Fatalf("假凭据报的是 %q, want 含 %q——不是 SASL 鉴权失败的话，"+
			"这条判据的证据链就断了（比如后端开了鉴权并建了这个用户，或地址根本不通）",
			err.Error(), "AuthenticationFailed")
	}
}

// TestMappingBackend_UsernameWithoutPasswordIsNoAuth 判 credentials 那行守卫的**缺席
// 语义**：只给用户名（密码为空）时，后端不鉴权、操作成功。
//
// 机制在 `bald-crud/mongodb` 里：`WithCredentials(u, p)` 要求 `u != "" && p != ""` 才
// 追加 `SetAuth`，只给用户名时**整行不投**。这与 §7.22 里 nacos 那条同形（只配密码
// 等于没配鉴权），是同一个误用的两个方向，所以两条都要有判据。
//
// 前提要写明：这条只在**后端未开 --auth** 时成立（本机与 CI 的 mongo:7 服务都是默认
// 不开）。在开了鉴权的后端上这条会红，而且红得对——那时「不投 Credential」的后果是
// 操作失败，不是成功，本条的口径必须重立。
//
// 变异对应关系：把上游守卫改成 `u != ""`（只看用户名）⇒ 这条红、上一条也红；
// 把契约侧的 `if u := sec.GetUsername(); u != ""` 去掉、无条件投递 ⇒ 这条红。
func TestMappingBackend_UsernameWithoutPasswordIsNoAuth(t *testing.T) {
	uri := mongoURI(t)
	c, closeFn := contractClient(t, &bootstrapv1.Database_MongoDB{
		Uri: uri, Database: "bald_map_useronly",
		Username:                      "bald_user_without_pass",
		ServerSelectionTimeoutSeconds: 5,
	})
	defer closeFn()

	_, err := c.InsertOne(context.Background(), "zzmapping_useronly", map[string]any{"probe": "1"})
	if err != nil {
		t.Fatalf("只给用户名却失败了：%q——`username != \"\" && password != \"\"` 这道守卫的"+
			"形状变了（只给用户名本该整行不投 SetAuth，后端不鉴权、操作成功）", err.Error())
	}
}

// TestMappingBackend_HeartbeatIntervalDrivesMonitorTraffic 判 heartbeat_interval 行的
// **取值与投递**，两半在同一条里：
//  1. hb=1 相对基线多出的请求 ≥8（实测本机 14，基线噪声 1..3）；
//  2. hb=30 相对基线多出的请求 ≤5（实测本机 2）。
//
// 第 2 半不可省：只有第 1 半时，映射写成固定的 `WithHeartbeatInterval(time.Second)`
// 也是绿的——「投了个数」不等于「投了契约里那个数」。
//
// 阈值余量的出处：窗口取 6s，hb=1 时一个客户端至少发 6 个 hello（实测含 RTT 的 ping
// 共 14），所以 ≥8 距实测 14 有 6 的余量、距「不投递」的 2 有 6 的余量；hb=30 在同一个
// 窗口里一个 hello 都不该发完，≤5 距实测 2 有余、距 8 有界。
func TestMappingBackend_HeartbeatIntervalDrivesMonitorTraffic(t *testing.T) {
	uri := mongoURI(t)
	raw := rawClient(t, uri)
	const window = 6 * time.Second

	baseline := requestWindow(t, raw, window)

	oneSec := func(sec *bootstrapv1.Database_MongoDB) int64 {
		c, closeFn := contractClient(t, sec)
		if _, err := c.InsertOne(context.Background(), "zzmapping_hb", map[string]any{"probe": "1"}); err != nil {
			closeFn()
			t.Fatalf("prime insert: %v", err)
		}
		// 让监控连接先建起来并跑过至少一个心跳，再开始数窗口。
		time.Sleep(1500 * time.Millisecond)
		d := requestWindow(t, raw, window)
		closeFn()
		// 关掉它，别让这个客户端的心跳污染下一个窗口。
		time.Sleep(2 * time.Second)
		return d
	}

	fast := oneSec(&bootstrapv1.Database_MongoDB{Uri: uri, Database: "bald_map_hb", HeartbeatIntervalSeconds: 1})
	if fast-baseline < 8 {
		t.Fatalf("hb=1 在 %s 窗口里只比基线多 %d 个请求（基线 %d，本窗口 %d），want ≥8——"+
			"heartbeat_interval 行没落到 WithHeartbeatInterval（不投递时是默认 10s，"+
			"实测只多 2；实测本机 hb=1 多 14）", window, fast-baseline, baseline, fast)
	}

	slow := oneSec(&bootstrapv1.Database_MongoDB{Uri: uri, Database: "bald_map_hb", HeartbeatIntervalSeconds: 30})
	if slow-baseline > 5 {
		t.Fatalf("hb=30 却比基线多 %d 个请求（基线 %d，本窗口 %d），want ≤5——"+
			"心跳间隔没跟着契约值走（比如投成了某个常量）", slow-baseline, baseline, slow)
	}
}

// TestMappingBackend_MaxConnIdleTimeReapsIdleConnections 判 max_conn_idle_time 行。
//
// 形状的来由（实测否掉了一个想当然的口径）：驱动**没有**后台定时回收，过期只在连接
// 被取出/放回时对 `idleTimeoutExpired()` 判一次，所以「静置后看 connections.current
// 掉下来」根本不成立（本机静置 5s，current 纹丝不动）。可行的读法是
// `connections.totalCreated`：**闲置后再来一波并发**，被回收的连接必须重建 ⇒ 计数涨。
//
// 一波取 4 个并发：顺序操作只会复用同一条连接，增量就只有 1，和背景噪声（RTT 监控偶尔
// 重连）贴边。4 个并发下实测 maxIdle=1s ⇒ Δ=4，默认 2 分钟 ⇒ Δ=0，两侧阈值各留 1。
//
// 三个案例而不是两个：`maxIdle=10`（闲置 4s，未到 10s ⇒ 不该回收）判的是**取值**。
// 变异轮实测：只有「1s 回收 / 不给则不回收」这一对时，把映射写成
// `WithMaxConnIdleTime(time.Second)`（契约值读都不读）照样全绿——「投了个数」不等于
// 「投了契约里那个数」，这一格的牙齿由第三个案例补。它和对照组（Δ≤1）同形但不同因：
// 对照组证明「不投时不回收」，这一组证明「投了但不按契约值投」会被抓住。
//
// 心跳同时给 30s：这条判的是池，不是监控，把监控的请求节流掉能少一类抖动。
func TestMappingBackend_MaxConnIdleTimeReapsIdleConnections(t *testing.T) {
	uri := mongoURI(t)
	raw := rawClient(t, uri)
	const coll = "zzmapping_idle"

	reapDelta := func(maxIdle int32) int64 {
		sec := &bootstrapv1.Database_MongoDB{Uri: uri, Database: "bald_map_idle", HeartbeatIntervalSeconds: 30}
		if maxIdle > 0 {
			sec.MaxConnIdleTimeSeconds = maxIdle
		}
		c, closeFn := contractClient(t, sec)
		defer closeFn()

		wave(t, c, coll, 4)
		time.Sleep(1500 * time.Millisecond)
		before := serverCounter(t, raw, "connections", "totalCreated")
		// 闲置要明显长于 maxIdle=1s、又短于 maxIdle=10s，且让对照组（默认 2 分钟）绝不触发。
		time.Sleep(4 * time.Second)
		wave(t, c, coll, 4)
		after := serverCounter(t, raw, "connections", "totalCreated")
		time.Sleep(2 * time.Second)
		return after - before
	}

	fast := reapDelta(1)
	if fast < 3 {
		t.Fatalf("maxIdle=1s、闲置 4s 后的一波并发只新建了 %d 条连接，want ≥3——"+
			"max_conn_idle_time 行没落到 WithMaxConnIdleTime（不投递时默认 2 分钟，"+
			"连接全部复用；实测本机 4）", fast)
	}

	// 取值轴：契约给 10s，闲置只有 4s ⇒ 连接**不该**过期。映射把这一行投成常量时红在这里。
	mid := reapDelta(10)
	if mid > 1 {
		t.Fatalf("maxIdle=10s、只闲置 4s 却新建了 %d 条连接，want ≤1——契约值没被读，"+
			"这一行被投成了某个更小的常量（实测：1s 时 Δ=4，10s 与不给时 Δ=0）", mid)
	}

	slow := reapDelta(0)
	if slow > 1 {
		t.Fatalf("没给 maxIdle 的对照组却新建了 %d 条连接，want ≤1——对照组不该回收连接，"+
			"这条一响就说明 either 后端行为变了 either 映射把这一行投成了常量"+
			"（实测基线：投递 1s 时 Δ=4，不投时 Δ=0）", slow)
	}
}
