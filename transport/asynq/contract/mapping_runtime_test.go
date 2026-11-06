package contract

// mapping_runtime_test.go —— §7.19：buildOpts 那十行 `opts = append` 的**运行期**闸门。
//
// 与 contract_test.go 的分工：那边判「Option 有没有被追加」（切片长度），本文件判
// 「追加进去的值真的改变了后端行为」。长度判据没有牙齿——把某行删掉，长度少 1 只是
// 一个算术事实，把值接错落点（§7.18 的 health_check_timeout 形状）长度照样对。
//
// 后端用进程内 miniredis：asynq 的入队与消费在假 redis 上都成立（实测），所以这十条
// 腿全离线、可进 CI 的 acceptance 面，不需要 redis 服务容器。
//
// 观测面（不需要给 bald 的 Server 加任何 accessor）：
//   - **心跳回声 `Inspector.Servers()`（主判据面）**：asynq v0.26 的消费者会把
//     `ServerInfo{Concurrency, Queues, StrictPriority, …}` 写进 redis，而这份结构体正是
//     传给 `asynq.NewServer(rc, cfg)` 的那份 cfg 的回声 ⇒ 直接读回装配结果，
//     与 §7.18 用 consul 注册请求体判装配同姿势；
//   - 公开 API `Endpoint()` —— redis 地址的直接读回；
//   - 假 redis 的键空间 —— 库号、载荷字节、`asynq:{q}:active` 与 `asynq:{q}:pending`
//     两个队列（v0.26 里流动的是 **task_id**，见 queueTaskIDs）、AUTH 是否放行；
//   - 处理器侧 —— 并发峰值封顶、任务是否跑完、cancel→Start 返回的等待时长；
//   - 返回值 —— `NewPeriodicTask` 的 entryID 与错误。
//
// 顺序类观测一律不用：strict_priority 最初判「出队顺序」，实测无区分度（见该条注释），
// 换成心跳回声后才判得动。
//
// 环境无关性（§7.18 教训的第一条在这里同样成立）：实现默认地址正是
// `127.0.0.1:6379`，本机起着真 redis 时「删掉 WithRedisAddress」也能入队成功。所以
// 地址那条腿判的是 `Endpoint()` 读回 + 「键出现在**这个**假实例里」，两者都不给本机
// 环境留余地。

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	hibiken "github.com/hibiken/asynq"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/encoding"
	"github.com/kalandramo/bald/transport/asynq"

	miniredis "github.com/alicebob/miniredis/v2"
)

// mappingTask 是本文件用的任务载荷（字段名故意与 json tag 一致，便于在 dump 里认形状）。
type mappingTask struct {
	Message string `json:"message"`
}

func ptrBool(b bool) *bool { return &b }

// init 把 json codec 注册进全局表：asynq.NewServer 在**构造期**就取
// encoding.GetCodec("json") 当默认编解码器，没注册时它拿到 nil，任何入队都会以
// "codec is nil" 失败——本文件的每条腿都要入队，先把它备好（与 contract_test.go 里
// registerTestCodec 同一动作，这里用 init 是因为腿多）。
func init() { registerTestCodec() }

// startFakeRedis 起一个进程内假 redis，测试结束自动关。
func startFakeRedis(t *testing.T) *miniredis.Miniredis {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	t.Cleanup(mr.Close)
	return mr
}

// buildServer 走 Provider 全链路：契约段 → buildOpts → asynq.NewServer。
// 返回的 *asynq.Server 未启动（除 WithHandlers 里的注册动作）。
func buildServer(t *testing.T, sec *bootstrapv1.Server_Asynq, opts ...Option) *asynq.Server {
	t.Helper()
	srv, _, err := Provider(opts...)(context.Background(), &bootstrapv1.Server{Asynq: sec})
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	a, ok := srv.(*asynq.Server)
	if !ok {
		t.Fatalf("Provider returned %T, want *asynq.Server", srv)
	}
	return a
}

// consume 后台启动消费者，返回「取消启动」与「Start 已返回」的信号（关闭型通道，
// 可被多处等非阻塞检查）。调用方在 cancel 之后用 cancelAndMeasure 量停机耗时。
func consume(t *testing.T, a *asynq.Server) (cancel context.CancelFunc, stopped chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	stopped = make(chan struct{})
	go func() {
		_ = a.Start(ctx)
		close(stopped)
	}()
	// 兜底：任何一条腿中途 Fatal 退出时也要让 Start 收尾，否则后台 goroutine 会带着
	// 已关闭的假 redis 继续跑，-race 下会咬到下一个测试。
	t.Cleanup(func() {
		cancel()
		select {
		case <-stopped:
		case <-time.After(30 * time.Second):
		}
	})
	return cancel, stopped
}

// cancelAndMeasure 取消启动上下文并等 Start 返回，返回这段等待时长。
func cancelAndMeasure(t *testing.T, cancel context.CancelFunc, stopped chan struct{}) time.Duration {
	t.Helper()
	t0 := time.Now()
	cancel()
	select {
	case <-stopped:
		return time.Since(t0)
	case <-time.After(20 * time.Second):
		t.Fatal("Start 在 cancel 后 20s 仍未返回——停机路径卡死")
		return 0
	}
}

// activeKey 是 asynq 的在途任务集合键名。
func activeKey(queue string) string { return fmt.Sprintf("asynq:{%s}:active", queue) }

// pendingKey 是 asynq 的待处理队列键名（internal/base.PendingKey）。
//
// 这条键是本文件停机判据的落点：上游把「停机预算到期时还没跑完的任务」原子地从 active
// 搬回 pending——`Requeue` 是一条 Lua，动 `ActiveKey`/`LeaseKey`/`PendingKey`/`TaskKey` 四个
// 键（`internal/rdb/rdb.go:497-507`），搬的就是 queueTaskIDs 里那个 ID，state 字段同时改写。
// 文档也这么写——hibiken/asynq v0.26 的 `Server.Shutdown` 注释：「If worker didn't finish
// processing a task during the timeout, the task will be pushed back to Redis」。
// 「被放弃」因此是一个**持久**状态，而不是一帧快照。
func pendingKey(queue string) string { return fmt.Sprintf("asynq:{%s}:pending", queue) }

// listMembers 读回一个 list 键：键不存在=空集合（redis 语义），类型不是 list 直接判红而不是
// 换个读法——那说明后端改了存储形状，本文件的观测面需要重画。
func listMembers(t *testing.T, mr *miniredis.Miniredis, key string) []string {
	t.Helper()
	switch typ := mr.Type(key); typ {
	case "":
		return nil
	case "list":
		members, err := mr.List(key)
		if err != nil {
			t.Fatalf("List %s: %v", key, err)
		}
		return members
	default:
		t.Fatalf("%s 类型 = %q，asynq 在测的队列是 list ⇒ 存储形状变了，判据要重画", key, typ)
		return nil
	}
}

// queueTaskIDs 把队列键的成员投影成 task_id 列表并判形状。
//
// asynq v0.26 的 pending/active **存的是 ID，不是整条消息**：dequeue 那条 Lua 是
// `RPOPLPUSH asynq:{q}:pending asynq:{q}:active`（`internal/rdb` 的 `dequeueCmd`，键序
// pending/paused/active/lease），消息体在 `asynq:{q}:t:<id>` 那个 hash 的 `msg` 字段里。
// （本文件最初按「成员=TaskMessage 的 JSON」解码，实测直接判红——见 §7.19 补条。）
//
// 判 ID 而不判条数：条数会被别的写蒙对，ID 只认停机前那一条在途任务。成员不是 ID 形状时
// 判红而不是退化成「前缀匹配」——那说明上游改回在队列里存消息或换了 ID 方案，观测面要重画。
func queueTaskIDs(t *testing.T, members []string) []string {
	t.Helper()
	for i, m := range members {
		if !isTaskID(m) {
			t.Fatalf("队列成员 #%d = %q 不是 asynq 的 task_id 形状（8-4-4-4-12 小写十六进制）⇒"+
				"队列里存的东西变了，本判据的观测面要重画", i, m)
		}
	}
	return members
}

// isTaskID 判 asynq 的 ID 形状：它由 google/uuid 生成，所以是标准 36 字符 UUID。
func isTaskID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range []byte(s) {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// onlyInFlightTaskID 取停机前的那一条在途任务 ID；不是一条即判红。
func onlyInFlightTaskID(t *testing.T, mr *miniredis.Miniredis, queue string) string {
	t.Helper()
	ids := queueTaskIDs(t, listMembers(t, mr, activeKey(queue)))
	if len(ids) != 1 {
		t.Fatalf("停机前 %s 有 %d 条在途任务，want 1 ⇒ 消费链或假 redis 状态不干净，判据没有唯一对象",
			activeKey(queue), len(ids))
	}
	return ids[0]
}

// hasTaskID 单次读一个队列键里是否有该 task_id。只用于「某键必须没有它」这类终态判据
// ——轮询一个不存在的键会立刻给出假答案，等不到任何东西。
func hasTaskID(t *testing.T, mr *miniredis.Miniredis, key, want string) bool {
	t.Helper()
	for _, id := range queueTaskIDs(t, listMembers(t, mr, key)) {
		if id == want {
			return true
		}
	}
	return false
}

// awaitTaskIn 在 timeout 内有界轮询 key，直到 task_id=want 出现（wantPresent=true）
// 或从该键消失（wantPresent=false）为止，返回退出时「want 是否在该键里」。
// 因此 wantPresent=false 的调用形如 `if awaitTaskIn(...) { 判红 }`：true 就是「等到超时还没消失」。
//
// 为什么这里的「有界轮询」不是把竞态藏进 timeout：bald 的停机分支把**同一个契约值**同时
// 用作自己的 ctx 预算和 hibiken 的 `Config.ShutdownTimeout`，而 `Server.Shutdown()` 是在
// 另一条 goroutine 里跑的（`transport/asynq` 的 server.go 停机第 3 步），所以它对队列的那次
// 写（`Requeue` 或 `markAsDone`）落在 `Start` 返回之前还是之后**由调度决定**——实测本机 60 轮
// 读到「还没写」，runner 读到「已经写完」（run 42128651637：elapsed=301.35ms、active=0）。
// 快照判据因此两头都可能红。本函数判的是两头共有的**终态**：两种落盘先后下，被放弃的任务
// 最终都在 pending、被等完的任务最终两处都不在，而这才是契约值决定的那件事。
// timeout 取的是终态到达的上界（一次 redis 写），与「等完 vs 放弃」的区分无关。
func awaitTaskIn(t *testing.T, mr *miniredis.Miniredis, key, want string, wantPresent bool, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		present := hasTaskID(t, mr, key, want)
		if present == wantPresent || time.Now().After(deadline) {
			return present
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// serverInfoFrom 启动消费者，等 asynq 的心跳把「这台消费者的装配结果」写进假 redis，
// 再用 hibiken 自己的 Inspector 读回来。
//
// 为什么这是 buildOpts 的落点直读：心跳写的 ServerInfo 就是传给
// `asynq.NewServer(rc, cfg)` 那份 cfg 的回声（concurrency / queues 权重 /
// strict_priority），而 cfg 正是 bald 的 `s.asynqConfig`。契约值只有真的进了那个
// 结构体，读回来才对得上——这跟 §7.18 用 consul 的注册请求体判装配是同一个姿势，
// 不需要给 bald 的 Server 加任何 accessor。
func serverInfoFrom(t *testing.T, mr *miniredis.Miniredis, a *asynq.Server) *hibiken.ServerInfo {
	t.Helper()
	insp := hibiken.NewInspector(hibiken.RedisClientOpt{Addr: mr.Addr()})
	t.Cleanup(func() { _ = insp.Close() })

	cancel, stopped := consume(t, a)
	defer func() {
		cancel()
		<-stopped
	}()

	start := time.Now()
	deadline := start.Add(10 * time.Second)
	for {
		infos, err := insp.Servers()
		if err != nil {
			t.Fatalf("Inspector.Servers: %v", err)
		}
		if len(infos) == 1 {
			t.Logf("心跳写出 ServerInfo 耗时 %v", time.Since(start))
			return infos[0]
		}
		if time.Now().After(deadline) {
			t.Fatalf("10s 内假 redis 里只有 %d 条 ServerInfo，want 1（心跳没落体，装配结果判不了）", len(infos))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// ---------------------------------------------------------------------------
// 1. redis_address
// ---------------------------------------------------------------------------

// TestMapping_RedisAddressReachesClientAndEndpoint 判契约地址真的投递到客户端与端点。
//
// 删掉 buildOpts 的 WithRedisAddress 那行 ⇒ 实现默认地址 127.0.0.1:6379 顶上来：
// Endpoint() 立刻不是 mr 的地址，且键不会落进这个假实例（本机的 :6379 与它是两个后端）。
func TestMapping_RedisAddressReachesClientAndEndpoint(t *testing.T) {
	mr := startFakeRedis(t)
	a := buildServer(t, &bootstrapv1.Server_Asynq{RedisAddress: mr.Addr()})

	if got, want := a.Endpoint(), "asynq://"+mr.Addr(); got != want {
		t.Fatalf("Endpoint() = %q, want %q（契约地址没投递时这里会是实现默认的 127.0.0.1:6379）", got, want)
	}
	if err := a.NewTask("addr:task", &mappingTask{Message: "addr"}); err != nil {
		t.Fatalf("NewTask: %v", err)
	}
	if len(mr.Keys()) == 0 {
		t.Fatal("假 redis 里一个键都没有 ⇒ 客户端连的不是契约地址")
	}
}

// ---------------------------------------------------------------------------
// 2. redis_password
// ---------------------------------------------------------------------------

// TestMapping_RedisPasswordReachesAuth 判密码真的进了连接（假 redis 开 AUTH 后，
// 无密码必被拒）。两行对照：契约给密码 ⇒ 入队成功；契约不给 ⇒ 入队失败且失败因是认证。
func TestMapping_RedisPasswordReachesAuth(t *testing.T) {
	const secret = "contract-mapping-secret"
	mr := startFakeRedis(t)
	mr.RequireAuth(secret)

	noAuth := buildServer(t, &bootstrapv1.Server_Asynq{RedisAddress: mr.Addr()})
	errNoAuth := noAuth.NewTask("auth:task", &mappingTask{Message: "x"})
	if errNoAuth == nil {
		t.Fatal("契约未给密码却入队成功 ⇒ 假 redis 的 AUTH 闸门没生效，本判据失去对照")
	}
	if !strings.Contains(errNoAuth.Error(), "AUTH") {
		t.Fatalf("无密码的失败因不是认证（%v）⇒ 判据在测别的东西", errNoAuth)
	}

	withAuth := buildServer(t, &bootstrapv1.Server_Asynq{RedisAddress: mr.Addr(), RedisPassword: secret})
	if err := withAuth.NewTask("auth:task", &mappingTask{Message: "x"}); err != nil {
		t.Fatalf("带契约密码仍入队失败 ⇒ redis_password 没投递: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 3. redis_db
// ---------------------------------------------------------------------------

// TestMapping_RedisDbSelectsOtherDatabase 判库号让键落到 DB(3) 而不是默认库 DB(0)。
func TestMapping_RedisDbSelectsOtherDatabase(t *testing.T) {
	mr := startFakeRedis(t)
	a := buildServer(t, &bootstrapv1.Server_Asynq{RedisAddress: mr.Addr(), RedisDb: 3})
	if err := a.NewTask("db:task", &mappingTask{Message: "db3"}); err != nil {
		t.Fatalf("NewTask: %v", err)
	}
	db3 := mr.DB(3)
	if db3 == nil {
		t.Fatal("DB(3) 为 nil")
	}
	if len(db3.Keys()) == 0 {
		t.Fatalf("DB(3) 里没有键 ⇒ redis_db 没投递（db0 键=%v）", mr.DB(0).Keys())
	}
	if got := mr.DB(0).Keys(); len(got) != 0 {
		t.Fatalf("默认库里出现了键 %v ⇒ 库号没生效", got)
	}
}

// ---------------------------------------------------------------------------
// 4. concurrency
// ---------------------------------------------------------------------------

// TestMapping_ConcurrencyReachesConsumerConfig 判契约并发数落进了消费者那份 cfg。
func TestMapping_ConcurrencyReachesConsumerConfig(t *testing.T) {
	mr := startFakeRedis(t)
	a := buildServer(t, &bootstrapv1.Server_Asynq{RedisAddress: mr.Addr(), Concurrency: 7})
	info := serverInfoFrom(t, mr, a)
	if info.Concurrency != 7 {
		t.Fatalf("ServerInfo.Concurrency = %d, want 7（删掉 WithConcurrency 那行会读到实现默认 20）", info.Concurrency)
	}
}

// TestMapping_ConcurrencyCapsParallelHandlers 判并发上限在真实消费里封顶。
//
// 与上一条的分工：那条读装配回声（值对不对），这条看行为（上限真的限制了同时在跑的
// 处理器数）。「值投递了但没生效」的形状只有这条能抓。阈值出处：契约 1 ⇒ 峰值必须 1；
// 删掉那行 ⇒ 默认 20 ⇒ 三个任务同时在跑 ⇒ 峰值 3 ⇒ 红。
func TestMapping_ConcurrencyCapsParallelHandlers(t *testing.T) {
	mr := startFakeRedis(t)

	var running, peak atomic.Int32
	const tasks = 3
	handled := make(chan struct{}, tasks)
	srv := buildServer(t, &bootstrapv1.Server_Asynq{
		RedisAddress: mr.Addr(),
		Concurrency:  1,
	}, WithHandlers(func(_ context.Context, a *asynq.Server) error {
		return a.RegisterSubscriber("par:task", func(_ string, _ asynq.MessagePayload) error {
			cur := running.Add(1)
			for {
				old := peak.Load()
				if cur <= old || peak.CompareAndSwap(old, cur) {
					break
				}
			}
			time.Sleep(150 * time.Millisecond)
			running.Add(-1)
			handled <- struct{}{}
			return nil
		}, func() any { return &mappingTask{} })
	}))

	cancel, stopped := consume(t, srv)
	for i := 0; i < tasks; i++ {
		if err := srv.NewTask("par:task", &mappingTask{Message: "p"}); err != nil {
			t.Fatalf("入队 %d: %v", i, err)
		}
	}
	t0 := time.Now()
	for i := 0; i < tasks; i++ {
		select {
		case <-handled:
		case <-time.After(15 * time.Second):
			t.Fatalf("只消费到 %d/%d 个任务（峰值 %d）", i, tasks, peak.Load())
		}
	}
	got := peak.Load()
	t.Logf("契约并发 1：峰值 %d，%d 个任务共耗时 %v", got, tasks, time.Since(t0))
	cancel()
	<-stopped

	if got != 1 {
		t.Fatalf("同时在跑的最大处理器数 = %d, want 1（并发上限没投递时会看到实现默认 20 放行更多）", got)
	}
}

// ---------------------------------------------------------------------------
// 5. queues
// ---------------------------------------------------------------------------

// TestMapping_QueuesReachConsumerConfigWithContractWeights 判队列名列表连同**递减权重**
// 一起落到消费者那份 cfg 里。
//
// 这是 queueWeights 那条「有序列表 ⇒ 递减权重，不退化为等权/轮询」不变量的运行期版本：
// 读回来的是 hibiken 实际拿到的 map，而不只是我们构造的 map。
// 删掉 WithQueues 那行 ⇒ 读到实现默认的 {"default":1} ⇒ 红；把 queueWeights 改成等权
// ⇒ 权重对不上 ⇒ 红。
func TestMapping_QueuesReachConsumerConfigWithContractWeights(t *testing.T) {
	mr := startFakeRedis(t)
	a := buildServer(t, &bootstrapv1.Server_Asynq{
		RedisAddress: mr.Addr(),
		Queues:       []string{"critical", "default", "low"},
	})
	info := serverInfoFrom(t, mr, a)
	want := map[string]int{"critical": 3, "default": 2, "low": 1}
	if len(info.Queues) != len(want) {
		t.Fatalf("ServerInfo.Queues = %v, want %v", info.Queues, want)
	}
	for name, weight := range want {
		if info.Queues[name] != weight {
			t.Fatalf("队列 %q 权重 = %d, want %d（有序列表要映射成递减权重，等权会退化成轮询）", name, info.Queues[name], weight)
		}
	}
}

// ---------------------------------------------------------------------------
// 6. codec
// ---------------------------------------------------------------------------

var markerCodecOnce sync.Once

// markerCodec 的 Marshal 结果与 json 完全不同形，所以载荷字节就是它的证据。
type markerCodec struct{}

func (markerCodec) Name() string { return "contract-marker" }
func (markerCodec) Marshal(v any) ([]byte, error) {
	task, ok := v.(*mappingTask)
	if !ok {
		return nil, fmt.Errorf("marker codec: unexpected payload type %T", v)
	}
	return []byte("MARKER[" + task.Message + "]"), nil
}
func (markerCodec) Unmarshal([]byte, any) error { return nil }

func registerMarkerCodec() {
	markerCodecOnce.Do(func() { encoding.MustRegister(markerCodec{}) })
}

// TestMapping_CodecDecidesPayloadBytes 判契约 codec 名决定了落到后端的载荷字节。
//
// 实现默认 codec 是 json，所以「判据要红」必须用一个**非默认**的 codec 名：删掉
// WithCodec 那行 ⇒ json 的 `{"message":...}` 顶上来 ⇒ MARKER 不在字节里 ⇒ 红。
// 顺带钉住 WithCodecRegistration 那条文档约束：名字没注册时 asynq 静默设 nil，
// 故障拖到入队才以 "codec is nil" 显形。
func TestMapping_CodecDecidesPayloadBytes(t *testing.T) {
	registerMarkerCodec()
	mr := startFakeRedis(t)

	a := buildServer(t,
		&bootstrapv1.Server_Asynq{RedisAddress: mr.Addr(), Codec: "contract-marker"},
		WithCodecRegistration(registerMarkerCodec),
	)
	if err := a.NewTask("codec:task", &mappingTask{Message: "marked"}); err != nil {
		t.Fatalf("NewTask: %v", err)
	}
	dump := mr.Dump()
	if !strings.Contains(dump, "MARKER[marked]") {
		t.Fatalf("假 redis 的字节里没有 marker codec 的输出 ⇒ codec 没投递。dump 片段：%.300s", dump)
	}
	if strings.Contains(dump, `{"message":"marked"}`) {
		t.Fatalf("字节里同时出现 json 形状 ⇒ 载荷不是由契约 codec 编的：%.300s", dump)
	}
}

// ---------------------------------------------------------------------------
// 7. strict_priority
// ---------------------------------------------------------------------------

// TestMapping_StrictPriorityReachesConsumerConfig 判严格优先级开关落进消费者那份 cfg。
//
// 为什么不用「出队顺序」判（本条最初是顺序观测，实测后换掉）：单并发 + 权重 [2,1] 下
// 关掉严格优先级时，实测 4 次里有 3 次仍得到 `high,high,high,low` —— 加权轮询的展开
// 在高优队列未排空前本来就不会切到低优，顺序**观测不到**该字段。按「不许为了绿放宽断言」
// 的口径，这不是阈值问题而是判据选错了观测面，于是换成心跳回声直接读 `StrictPriority`。
// 删掉 WithStrictPriority 那行 ⇒ 读到实现默认 false ⇒ true 行红；写死 true ⇒ false 行红。
func TestMapping_StrictPriorityReachesConsumerConfig(t *testing.T) {
	for _, strict := range []bool{true, false} {
		t.Run(fmt.Sprintf("strict=%v", strict), func(t *testing.T) {
			mr := startFakeRedis(t)
			a := buildServer(t, &bootstrapv1.Server_Asynq{
				RedisAddress:   mr.Addr(),
				Queues:         []string{"contract-high", "contract-low"},
				StrictPriority: ptrBool(strict),
			})
			info := serverInfoFrom(t, mr, a)
			if info.StrictPriority != strict {
				t.Fatalf("ServerInfo.StrictPriority = %v, want %v（该字段没投递时读到的是实现默认 false）",
					info.StrictPriority, strict)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 8. shutdown_timeout_ms
// ---------------------------------------------------------------------------

// TestMapping_ShutdownTimeoutCapsGracefulWait 判停机预算取的是契约值而不是实现默认。
//
// 形状：优雅关闭 + 在途任务睡 1.5s + 契约 shutdown_timeout_ms=300。
//   - 投递成功 ⇒ Start 在 cancel 后约 300ms 返回（判上界 1.2s），且那条任务被搬回
//     pending（`Requeue` 的落点，见 pendingKey 上面那段）；
//   - 删掉 WithShutdownTimeout ⇒ shutdownTimeout() 回落到实现默认 10s，asynq 自己那层
//     也是默认 8s ⇒ 会等满 1.5s 且任务跑完 ⇒ 窗口判据红，而且任务永远不会回到
//     pending（跑完走的是 markAsDone），pending 判据也红。
//
// 阈值出处：上界 1.2s 介于「契约 300ms」与「处理器 1.5s」之间，两边各有 300ms/300ms
// 以上余量；下界判的是「没等就返回」——那是 gracefully_shutdown=false 的形状，由第 9 条腿负责。
//
// 判据形状在 2026-11-06 重画过一次，原因值得留在原地：原先这里断的是「停机后 active
// 集合非空」，把「任务还在途」当成了被放弃的证据。它本机 60 轮全绿，却在 runner 上红
// （run 42128651637：elapsed=301.35ms、active=0），根因是 bald 把**同一个契约值**同时
// 用作自己的 ctx 预算和 hibiken 的 ShutdownTimeout，而 `Server.Shutdown()` 跑在另一条
// goroutine 里——那次 requeue 落在 Start 返回之前还是之后由调度决定，快照读哪头都可能。
// 上游文档给的持续事实是「任务被推回 Redis」，判据就改判它。见 awaitTaskIn 的注释。
func TestMapping_ShutdownTimeoutCapsGracefulWait(t *testing.T) {
	mr := startFakeRedis(t)
	inFlight := make(chan struct{})
	finished := make(chan struct{})

	srv := buildServer(t, &bootstrapv1.Server_Asynq{
		RedisAddress:       mr.Addr(),
		Concurrency:        1,
		Queues:             []string{"default"},
		GracefullyShutdown: ptrBool(true),
		ShutdownTimeoutMs:  300,
	}, WithHandlers(func(_ context.Context, a *asynq.Server) error {
		return a.RegisterSubscriber("slow:task", func(_ string, _ asynq.MessagePayload) error {
			close(inFlight)
			time.Sleep(1500 * time.Millisecond)
			close(finished)
			return nil
		}, func() any { return &mappingTask{} })
	}))

	cancel, stopped := consume(t, srv)
	if err := srv.NewTask("slow:task", &mappingTask{Message: "slow"}); err != nil {
		t.Fatalf("NewTask: %v", err)
	}
	select {
	case <-inFlight:
	case <-time.After(10 * time.Second):
		t.Fatal("10s 内任务没进入在途 ⇒ 假 redis 上的消费链断了")
	}

	// 停机前把这条在途任务的 ID 取出来，后面的判据只认它。
	taskID := onlyInFlightTaskID(t, mr, "default")

	elapsed := cancelAndMeasure(t, cancel, stopped)
	// 下界 200ms 而不是 300ms：300ms 就是被量的那个定时器，判据要认的是「等的是契约预算」
	// 而不是「等满了处理器」或「根本没等」。实测本机 301.5ms，距下界有 100ms 余量，
	// 距「根本没等」的硬切形状（实测 1ms）与「等满 1.5s」的形状各差一个数量级。
	if elapsed < 200*time.Millisecond || elapsed > 1200*time.Millisecond {
		t.Fatalf("cancel 到 Start 返回 = %v, want 约 300ms（契约 shutdown_timeout_ms）且明显短于处理器的 1.5s——"+
			"等满 1.5s 说明预算取的是实现默认而不是契约值", elapsed)
	}
	// 预算到期时处理器还没跑完（1.5s 的睡眠只走了 300ms）。这条必须在轮询**之前**判：
	// 轮询到终态可能已经越过 1.5s，那时后台的 handler goroutine 自己会跑完。
	select {
	case <-finished:
		t.Fatal("处理器跑完了，但契约预算只有 300ms ⇒ 停机窗口没按契约收口")
	default:
	}

	// 判「放弃」的持久痕迹：任务被原子地搬回 pending，而不是留在 active 里等谁去读快照。
	if !awaitTaskIn(t, mr, pendingKey("default"), taskID, true, 3*time.Second) {
		t.Fatalf("停机 3s 后 %s 里仍没有 task_id=%s（active=%v，elapsed=%v）⇒ 任务没被搬回队列，"+
			"即预算没按契约到期放弃：被等完（实现默认 8s/10s 的形状）或被丢掉",
			pendingKey("default"), taskID, listMembers(t, mr, activeKey("default")), elapsed)
	}
	// 搬运是一条动四个键的 Lua 脚本，落点有了另一边就必须空——这条判的是上游脚本的原子性，
	// 不是本包的装配（它变了说明观测面要重画，所以宁可响）。
	if got := queueTaskIDs(t, listMembers(t, mr, activeKey("default"))); len(got) != 0 {
		t.Fatalf("任务已回到 pending，但 %s 仍有 %v ⇒ 上游的 requeue 不再是原子搬运，本判据的观测面要重画",
			activeKey("default"), got)
	}
}

// ---------------------------------------------------------------------------
// 9. gracefully_shutdown
// ---------------------------------------------------------------------------

// TestMapping_GracefullyShutdownDecidesStopPath 判开关选择的是 Shutdown（等在途跑完）还是 Stop（立刻返回）。
//
// 两行不是「或」判据（§7.18 第二条教训）：
//   - true 行判「Start 返回那一刻处理器已经跑完」+「任务从 active 消失且落点不是 pending」；
//   - false 行判「Start 返回时处理器还没跑完」（契约要的是没等）+「后台仍然把它跑完，
//     落点同样不是 pending」。上游 `Server.Stop` 的文档就是这么写的：「Stop can be used
//     before shutting down the server to ensure that all currently active tasks are
//     processed before server shutdown」——硬切切的是「不再拉新任务」，不是砍掉在途。
//
// 「active 消失」两头都成立，所以真正的区分度在两处：`finished` 通道的**非阻塞**状态
// （等没等）与 elapsed（等了多久）。落点判 pending 是为了把「做完」和「搬回队列」分开——
// requeue 同样会让任务从 active 消失，只看 active 会把它读成「等完了」。
// 删掉 WithGracefullyShutdown ⇒ 两行都落到实现默认 false ⇒ true 行红；把开关写死 true ⇒
// false 行的「没等」判据红。
//
// 判据形状同样在 2026-11-06 重画：原先两行各读一帧 active 快照（true 行要求空、false 行
// 要求非空），跟第 8 条腿红在 runner 上是同一个病——把落盘先后当成了契约语义。见 awaitTaskIn。
func TestMapping_GracefullyShutdownDecidesStopPath(t *testing.T) {
	for _, graceful := range []bool{true, false} {
		t.Run(fmt.Sprintf("graceful=%v", graceful), func(t *testing.T) {
			mr := startFakeRedis(t)
			inFlight := make(chan struct{})
			finished := make(chan struct{})

			srv := buildServer(t, &bootstrapv1.Server_Asynq{
				RedisAddress:       mr.Addr(),
				Concurrency:        1,
				Queues:             []string{"default"},
				GracefullyShutdown: ptrBool(graceful),
			}, WithHandlers(func(_ context.Context, a *asynq.Server) error {
				return a.RegisterSubscriber("slow:task", func(_ string, _ asynq.MessagePayload) error {
					close(inFlight)
					time.Sleep(700 * time.Millisecond)
					close(finished)
					return nil
				}, func() any { return &mappingTask{} })
			}))

			cancel, stopped := consume(t, srv)
			if err := srv.NewTask("slow:task", &mappingTask{Message: "g"}); err != nil {
				t.Fatalf("NewTask: %v", err)
			}
			select {
			case <-inFlight:
			case <-time.After(10 * time.Second):
				t.Fatal("10s 内任务没进入在途 ⇒ 假 redis 上的消费链断了")
			}

			// 停机前把这条在途任务的 ID 取出来，后面的终态判据只认它。
			taskID := onlyInFlightTaskID(t, mr, "default")

			elapsed := cancelAndMeasure(t, cancel, stopped)
			t.Logf("graceful=%v：cancel→Start 返回 %v", graceful, elapsed)
			// 「Start 返回时处理器是否已经跑完」读的是通道，不是 redis 快照，因此不受
			// 落盘先后影响——这条才是开关的观测面。
			select {
			case <-finished:
				if !graceful {
					t.Fatalf("gracefully_shutdown=false 下 cancel→Start 返回 %v，但处理器已经跑完 ⇒"+
						"等到底的 Shutdown 路径顶上了硬切路径", elapsed)
				}
			default:
				if graceful {
					t.Fatal("gracefully_shutdown=true 在在途任务跑完之前就返回了 ⇒ 走的是 Stop（硬切）路径")
				}
			}

			if graceful {
				// 下界 300ms：处理器的 700ms 睡眠必然等满，而硬切路径实测约 1ms，两边各差一个
				// 数量级（本机 -count 分布见登记册 §7.19 补条）。
				if elapsed < 300*time.Millisecond {
					t.Fatalf("优雅关闭下 cancel→返回 = %v，太短说明走的是硬切路径", elapsed)
				}
				// `processor.shutdown()` 靠收满并发令牌来等 worker，而 worker 释放令牌的 defer
				// 注册在 `handleSucceededMessage`（→ markAsDone 那次 redis 写）之后 ⇒ 优雅路径上
				// 「任务离开 active」在 Shutdown 返回之前就已成立；这里仍用有界轮询读终态，是为了
				// 不把上游的 defer 顺序当成契约（它变了只该让本条的观测面重画，而不是判红装配）。
				if awaitTaskIn(t, mr, activeKey("default"), taskID, false, 3*time.Second) {
					t.Fatalf("优雅关闭后 3s，%s 里仍有 task_id=%s（elapsed=%v）⇒ 在途任务没被收尾",
						activeKey("default"), taskID, elapsed)
				}
			} else {
				if elapsed > 500*time.Millisecond {
					t.Fatalf("非优雅关闭下 cancel→返回 = %v，等这么久说明走的是 Shutdown 路径", elapsed)
				}
				// Stop 不动在途 worker ⇒ 处理器在后台跑完并写回 redis。
				select {
				case <-finished:
				case <-time.After(3 * time.Second):
					t.Fatal("硬切后 3s 处理器仍未跑完 ⇒ Stop 的语义变成了砍掉在途任务本身")
				}
				if awaitTaskIn(t, mr, activeKey("default"), taskID, false, 3*time.Second) {
					t.Fatalf("处理器跑完 3s 后 %s 里仍有 task_id=%s ⇒ 收尾的那次写没落体",
						activeKey("default"), taskID)
				}
			}

			// 两头共用的落点判据：等完（markAsDone）与放弃（Requeue→pending）是两件事，
			// 本条腿要的是前者——后者由第 8 条腿（shutdown_timeout_ms 到期）判。
			if hasTaskID(t, mr, pendingKey("default"), taskID) {
				t.Fatalf("task_id=%s 出现在 %s ⇒ 任务被搬回队列（预算到期放弃），而本行 graceful=%v 要求把它做完",
					taskID, pendingKey("default"), graceful)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 10. scheduler_enabled
// ---------------------------------------------------------------------------

// TestMapping_SchedulerEnabledDecidesPeriodicTaskRegistration 判开关决定周期任务能不能注册。
//
// 这一行同时是 §7.19 的缺陷回归：改前 `NewPeriodicTask` 在 scheduler 关掉时先建后判，
// 建不出东西（createAsynqScheduler 直接 return nil），下一句对 nil 接收者调 Register
// ⇒ panic。现在判的是「明确的错误 + 不 panic」。
//
// 三行的牙齿分布（诚实口径）：
//   - false 行：删掉 WithSchedulerEnabled ⇒ 回落实现默认 true ⇒ 注册成功 ⇒ 红；
//   - nil 行：把开关写死 false（为了少判一行）⇒ 注册被拒 ⇒ 红；
//   - true 行：与实现默认同值，删掉那行观测不到变化 ⇒ **不判「那一行存在」，只判
//     「修复没把字段改成恒拒」**（这是 §7.18 M5/SINGLE 同一形状的边界）。
func TestMapping_SchedulerEnabledDecidesPeriodicTaskRegistration(t *testing.T) {
	for _, tc := range []struct {
		name    string
		section *bool
		wantErr bool
	}{
		{name: "absent_keeps_default_enabled", section: nil, wantErr: false},
		{name: "explicit_true", section: ptrBool(true), wantErr: false},
		{name: "explicit_false", section: ptrBool(false), wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mr := startFakeRedis(t)
			sec := &bootstrapv1.Server_Asynq{RedisAddress: mr.Addr(), SchedulerEnabled: tc.section}
			a := buildServer(t, sec)

			entryID, err := a.NewPeriodicTask("* * * * *", "cron:task", &mappingTask{Message: "tick"})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("契约关掉调度器却注册成功（entryID=%q）⇒ scheduler_enabled 没投递", entryID)
				}
				if !strings.Contains(err.Error(), "scheduler is disabled") {
					t.Fatalf("错误形状不是「调度器被关掉」而是别的：%v", err)
				}
				if entryID != "" {
					t.Fatalf("返回了 entryID=%q 但同时又报错", entryID)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewPeriodicTask: %v", err)
			}
			if entryID == "" {
				t.Fatal("entryID 为空但错误为 nil")
			}
		})
	}
}
