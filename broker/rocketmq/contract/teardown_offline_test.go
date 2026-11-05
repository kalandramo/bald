package contract

// teardown_offline_test.go —— 「停机把订阅者带走没有」的线级判据（§7.24 的第四处缺陷）。
//
// 为什么要在这一族里判：v2 的 Unsubscribe 原先只调 SDK 的 `pushConsumer.Unsubscribe`，
// 而那一句**只删订阅表项**（consumer/push_consumer.go 里直接
// `subscriptionDataTable.Delete(topic)` 后 `return nil`），消费线程、拉取线程、
// rmqClient 的心跳线程全都还在跑。后果是每一只「已注销」的订阅者在进程里留下一整套
// 后台线程和到 broker 的连接，Disconnect 之后仍然占着 broker —— 调用方以为摘掉了，
// 线还在动。这一族读的就是「线还在动没有」。
// （本机 -race 曾经把「心跳线程读写 rlog 全局」也算作这条泄漏的后果之一；那个全局现在
// 由本包只在第一次 Init 时注入一次，不再有并发写侧，所以这里的理由只剩泄漏本身。）
//
// 主判据是**流水冻结**。本机实测未修时的泄漏形状：假 broker 上每 3s 多一条请求
// （+3002ms 一条 code=38 注销尝试、+6004ms 又一条 code=34 心跳），所以「停机之后再也不
// 增加」是这套线程真退了的直读口。停机前必须先看到流水**在长**（liveConsumer 里等心跳再
// 等一个节拍），否则「从一开始就没连上」也会跑成绿——两个方向都判才叫闸门。
//
// 辅助读数是假 broker 侧的**连接归零**：`remoteClient.ShutDown` 关掉自己的连接池，实测
// Disconnect 当场让 bk.open 从 1 变 0。名字服务那侧**会留一条空闲连接**（同一时刻实测
// ns.open 恒为 1，但 ns.reqs 永远不再增加）——所以「连接归零」只写在 broker 上，不写 namesrv。

import (
	"testing"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/broker"
)

// frozenWindow 既是「停机后不许再有新请求」的观察窗口，也是停机前等第二条后台节拍落地的
// 时长。下限来自实测节拍：心跳与注销各 3s 一次 ⇒ 3s 刚好压线，取 4s 留一档余量。
// 冻结判据是**绝对**的（新增条数必须为 0，不是「比均值低」），慢机器上只会更稳，不会误判绿。
const frozenWindow = 4 * time.Second

// startFlow 记窗口开始时每台端点的请求条数，与 assertFlowFrozen 配对使用。
func startFlow(ends ...*fakeEnd) []int {
	counts := make([]int, len(ends))
	for i, f := range ends {
		counts[i] = len(f.seen())
	}
	return counts
}

// assertFlowFrozen 判窗口内每台端点的请求条数都不再增加。
//
// 窗口是**固定时长**的观察，且只在窗口走完后再比首尾计数，不提前退出：「停机后一条都不再
// 增加」这件事的语义就是「整个窗口为空」，提前判红会睡不满窗口——而未修时的泄漏形状恰好
// 是窗口里的第 3.0s 与第 6.0s 各一条（code=38、code=34，本机实测），走完窗口才看得到这两条，
// 判红信息里带的是**哪一个节拍**在漏，而不只是「长了」。
// 正向对照由 liveConsumer 负责（停机前计数已 ≥2），这里不重复等增长。
func assertFlowFrozen(t *testing.T, ends []*fakeEnd, before []int) {
	t.Helper()

	time.Sleep(frozenWindow)

	for i, f := range ends {
		reqs := f.seen()
		if grew := len(reqs) - before[i]; grew != 0 {
			t.Fatalf("%s(%s) 停机后 %s 内又收到 %d 个新请求（停机前已有 %d 个，从第 %d 条起是新增）"+
				"——后台线程没随停机退出，到 broker 的连接也还挂着。\n流水：\n%s",
				f.kind, f.addr(), frozenWindow, grew, before[i], before[i], formatReqs(reqs))
		}
	}
}

// liveConsumer 起一台会答路由的名字服务加一台假 broker，订阅一个主题，并把「停机前流水
// 确实在长」做成两条**判据**（先等到第一条心跳，再等到计数 ≥2），返回端点、broker 与订阅者。
// 这两步是正向对照：少了它们，「停机后不再长」可以由「从来就没长」跑成绿。
func liveConsumer(t *testing.T, tag, topic string) (ns, bk *fakeEnd, br broker.Broker, sub broker.Subscriber) {
	t.Helper()

	bk = newFakeBroker(t, "bk-"+tag)
	ns = newFakeNameServer(t, "ns-"+tag, bk.addr())

	br = newBroker(t, &bootstrapv1.Broker_Rocketmq{
		NameServers:  []string{ns.addr()},
		GroupName:    "GID-teardown-" + tag,
		InstanceName: uniqInstance(tag),
		RetryCount:   1,
	})

	var err error
	sub, err = br.Subscribe(topic, routeHandler(), nil)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// 心跳在 client 启动后约 1s 出现（本机实测 +1001ms，见 instance_name 那条腿）。
	bk.eventually(t, 20*time.Second, "假 broker 上没出现心跳——后台线程压根没跑起来，本腿没有可判的东西",
		func(reqs []wireReq) bool {
			for _, r := range reqs {
				if _, ok := r.clientIDOfHeartbeat(); ok {
					return true
				}
			}
			return false
		})

	// 正向对照做实：**停机前**这条假 broker 上的流水计数已经 ≥2。
	// 原来这里是一段裸 `time.Sleep(frozenWindow)`——睡过就当「在长」，于是「后台线程只发了
	// 一条就哑掉」也会让停机后那条「不再长」的判据跑成绿。改成计数判据，且判的是绝对条数而不是
	// 「比某个快照多」：后一种写法一旦快照落在一拍之后，就要等下一拍才知道，未修/半死的客户端
	// 根本不发第三拍，会空等满超时（本机实测把这条腿拖到 18s 才红）。
	// 心跳节拍实测 3s 一条，两拍最慢 ~6s ⇒ 10s 已有约 4 倍余量。
	bk.eventually(t, 10*time.Second, "停机前假 broker 上的后台请求不足 2 条——这条腿没有可判的流水",
		func(reqs []wireReq) bool { return len(reqs) >= 2 })

	return ns, bk, br, sub
}

// TestMappingOffline_UnsubscribeStopsConsumer 判调用方主动摘除这一支：
// `Unsubscribe(true)` 必须把 consumer 关掉，而不只是删掉订阅表项。
func TestMappingOffline_UnsubscribeStopsConsumer(t *testing.T) {
	const topic = "teardown-unsubscribe"

	ns, bk, _, sub := liveConsumer(t, "unsub", topic)

	if err := sub.Unsubscribe(true); err != nil {
		t.Fatalf("Unsubscribe(true): %v", err)
	}

	before := startFlow(ns, bk)
	assertFlowFrozen(t, []*fakeEnd{ns, bk}, before)

	if got := bk.openConns(); got != 0 {
		t.Fatalf("Unsubscribe 后假 broker 上仍挂着 %d 条连接——连接池没关掉", got)
	}
}

// TestMappingOffline_DisconnectStopsConsumer 判 Provider 交出的 cleanup 那一支
// （cleanup 就是 `b.Disconnect()`，contract.go 尾部）：停机后订阅者带的 consumer
// 必须一起退出。走的是 `subscribers.Clear() → Unsubscribe(false)`。
//
// 这一支与上一条**分别**判，是因为布尔值不同、代码路径不同：Clear() 自己清过表，
// 所以传给 Unsubscribe 的是 false；把「Shutdown」只挂在 removeFromManager 为 true 的
// 分支上，这条就会红。
func TestMappingOffline_DisconnectStopsConsumer(t *testing.T) {
	const topic = "teardown-disconnect"

	ns, bk, br, sub := liveConsumer(t, "disconnect", topic)

	if err := br.Disconnect(); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	before := startFlow(ns, bk)
	assertFlowFrozen(t, []*fakeEnd{ns, bk}, before)

	if got := bk.openConns(); got != 0 {
		t.Fatalf("Disconnect 后假 broker 上仍挂着 %d 条连接", got)
	}

	// 线级判据之外还要判簿记面：注册表里没摘干净时，Disconnect 只是「碰巧」把线程带走了，
	// 订阅者仍被 broker 持有，同 topic 的下一次 Add 会覆盖掉它。
	if got := sub.Topic(); got != topic {
		t.Fatalf("停机后订阅者 Topic()=%q, want %q", got, topic)
	}
}
