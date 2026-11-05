package contract

// mapping_offline_test.go —— 契约 broker.rocketmq 八行映射的**离线**运行期闸门
// （风险登记册 §7.24，§7.14 同族）。
//
// 观测面见 fakewire_test.go 文件头：进程内的假名字服务/假 broker 能把每一次请求的
// code、extFields、body 原样交出来，所以八行**全部**有直读口，一行都不需要真后端：
//
//	name_servers    → 表里每一台都会收到路由查询（取址按 round-robin 且 index 单调递增；
//	                  表里只剩一台时全部查询都打到那一台，所以「每台都有」就是整张表的判据）
//	name_server_url → 域名接口的 HTTP GET + 从**响应体里才知道**的地址收到请求
//	access_key      → 每个请求的 extFields.AccessKey
//	secret_key      → extFields.Signature == 本机重算的 HMAC-SHA1（值判，不是存在判）
//	security_token  → extFields.SecurityToken
//	group_name      → 发送请求（code=10）的 extFields.producerGroup
//	namespace       → 消费侧路由查询的 `ns%topic` / `%RETRY%ns%GID` 前缀；生产侧见 v2 的修复
//	instance_name   → 心跳（code=34）body 里的 clientID = `ip@instance`
//	retry_count     → SendSync 的迭代次数 = 1+rc，本 topic 的路由查询条数**精确等于**它
//	topic           → Subscribe("") 落到该主题的路由查询；不给时当场报 topic is empty
//
// 三条贯穿全部腿的前提（都是实测出来的，不是想当然）：
//   - Provider 只 Init/Connect，**一个字节都不发**（生产者/消费者是 Publish、Subscribe
//     里惰性建的）。所以每条腿都必须跑一次真实操作，否则「零请求」既是缺陷也是正常，
//     闸门失去意义。这一条本身也由 TestMappingOffline_ProviderAloneEmitsNoTraffic 钉住。
//   - instance_name 每条腿唯一，否则进程内共享同一个 rmqClient（fakewire_test.go 的
//     uniqInstance 注释）。
//   - 假名字服务答 17（topic not exist）时生产者的失败**只花几毫秒**，答路由时会真的
//     连上假 broker。两种模式按腿选，别混。
//
// 跑法：全部不依赖服务容器，`-short` 照跑，进 scripts/acceptance-gate.sh 的 scope=ci 判据表。

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/broker"
)

// newBroker 走契约构造，把 cleanup 挂到 t.Cleanup 上。
// 注册顺序保证它在假线的监听器关闭**之前**跑（t.Cleanup 是 LIFO，假线先注册）。
func newBroker(t *testing.T, sec *bootstrapv1.Broker_Rocketmq) broker.Broker {
	t.Helper()

	out, cleanup, err := Provider(context.Background(), &bootstrapv1.Broker{Rocketmq: sec})
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	t.Cleanup(cleanup)

	b, ok := out.(broker.Broker)
	if !ok {
		t.Fatalf("Provider 交出 %T，不是 broker.Broker", out)
	}
	return b
}

// publish 跑一次真实发布并**忽略错误**：本族闸门只看线上出现了什么，
// 假线上发送必然失败（topic 不存在或 broker 不回话），失败本身不是判据。
func publish(t *testing.T, b broker.Broker, topic string) error {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	err := b.Publish(ctx, topic, &broker.Message{Body: map[string]string{"probe": "1"}})
	if err == nil {
		t.Fatalf("Publish(%s) 在假线上竟然成功了——观测面前提变了，本文件的判据要重立", topic)
	}
	return err
}

func routeHandler() broker.Handler {
	return func(context.Context, broker.Event) error { return nil }
}

// TestMappingOffline_ProviderAloneEmitsNoTraffic 钉住上面第一条前提：
// 构造 + Init + Connect 之后，名字服务上一个请求都没有。
//
// 这条不是「顺手加的」：它同时判 Connect() 不该偷偷建生产者（v2 的 Connect 只翻 connected
// 标志位），并且给其余八条腿一个统一的归因——线上一旦出现请求，只可能来自本条腿里的
// Publish/Subscribe，不可能来自 Provider。
func TestMappingOffline_ProviderAloneEmitsNoTraffic(t *testing.T) {
	ns := newFakeNameServer(t, "idle", "")

	newBroker(t, &bootstrapv1.Broker_Rocketmq{
		NameServers:  []string{ns.addr()},
		InstanceName: uniqInstance("idle"),
	})

	// 覆盖 SDK 的两个后台节拍（路由更新 +10ms、心跳 +1s）再加余量：这段时间里只要
	// Provider 建过任何一个客户端，流水里就一定有东西。
	time.Sleep(1500 * time.Millisecond)

	if reqs := ns.seen(); len(reqs) != 0 {
		t.Fatalf("仅 Provider（无 Publish/Subscribe）却发出 %d 个请求：\n%s", len(reqs), formatReqs(reqs))
	}
}

// TestMappingOffline_NameServersDriveDial 判 name_servers 行：契约给出的**整张地址表**
// 都要成为拨号目标，且表外的地址一个都不许碰。
//
// 三台假名字服务：A、B 在契约里，C 不在。判据是「A、B 各自至少收到过路由查询」，
// 不判**哪个主题**落在哪一台——本机实测的分布是稳定的（本 topic 4 次全在 A、
// 默认主题 TBW102 4 次全在 B），但那是上游取址节奏（每轮迭代两次取址，奇偶恒定）
// 造成的，写进判据就等于把 v2.1.2 的调度细节钉成 bald 的承诺。
//
// 为什么这样仍然判得住整张表：`queryTopicRouteInfoFromServer` 按 round-robin 取址
// （`getNameServerAddress` 每取一次 index+1），表里只剩一项时**所有**查询都会打到那一项。
// 于是「把 `WithNameServer(nameServers)` 写坏成 `nameServers[:1]`」这种漂移的表现形式是
// B 那边一条请求都没有——这条腿就是冲着它写的。C 用来证明拨号集合不外溢。
func TestMappingOffline_NameServersDriveDial(t *testing.T) {
	a := newFakeNameServer(t, "ns-a", "")
	b := newFakeNameServer(t, "ns-b", "")
	c := newFakeNameServer(t, "ns-outside", "")

	const topic = "mapping-name-servers"

	br := newBroker(t, &bootstrapv1.Broker_Rocketmq{
		NameServers:  []string{a.addr(), b.addr()},
		InstanceName: uniqInstance("nameservers"),
		RetryCount:   3,
	})
	publish(t, br, topic)

	if got := len(a.seen()); got == 0 {
		t.Fatalf("契约 name_servers=[A,B]，A(%s) 上却一条请求都没有", a.addr())
	}
	if got := len(b.seen()); got == 0 {
		t.Fatalf("B(%s) 上一条请求都没有——地址表被截成了第一项（round-robin 只剩一项时，"+
			"本 topic 与默认主题的查询会全部改打 A；实测本机 A 8 条 / B 0 条）", b.addr())
	}
	if reqs := c.seen(); len(reqs) != 0 {
		t.Fatalf("表外地址 C(%s) 收到 %d 个请求——拨号集合不再等于契约的 name_servers", c.addr(), len(reqs))
	}
}

// TestMappingOffline_NameServerUrlResolvesDialTargets 判 name_server_url 行，含它与
// name_servers 同时给出时的**优先序**。
//
// a) 只给 url：域名接口必须被 GET 一次以上，且**只有响应体里那个地址**会收到路由查询。
// 这条是 url 行唯一的取值判据——地址不在契约里出现过，只可能来自这次 HTTP 读取。
// 顺带记两处实测细节：HttpResolver 按 `;` 分隔响应体（换行会被 TrimSpace 吃掉，单条地址时
// 看不出差别，所以这里只写一条）；它会往 `$HOME/logs/rocketmq-go/snapshot/nameserver_addr-DEFAULT`
// 落一份快照，取不到实时列表时回落到快照。快照**不会把这条跑成假绿**：每次跑的都是新起的
// 随机端口，旧快照里那个地址此刻没人监听 ⇒ 本腿读到 0 个请求 ⇒ 判红。
//
// b) 两个都给：`createNsResolver` 先判 name_servers，于是域名接口一次都不该被访问。
// 这一半判的是优先序本身——反过来写（先 url）在本仓里是可读性上「也没错」的改动，
// 但它会把已经在用 name_servers 的工程改成去连一个契约里另有用途的地址。
func TestMappingOffline_NameServerUrlResolvesDialTargets(t *testing.T) {
	t.Run("url 解析出的地址成为拨号目标", func(t *testing.T) {
		ns := newFakeNameServer(t, "ns-from-domain", "")

		var hits int32
		domain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&hits, 1)
			_, _ = io.WriteString(w, ns.addr())
		}))
		t.Cleanup(domain.Close)

		const topic = "mapping-name-server-url"

		br := newBroker(t, &bootstrapv1.Broker_Rocketmq{
			NameServerUrl: domain.URL,
			InstanceName:  uniqInstance("url"),
			RetryCount:    1,
		})
		publish(t, br, topic)

		if got := atomic.LoadInt32(&hits); got == 0 {
			t.Fatalf("给了 name_server_url=%s 却没人访问域名接口（命中 %d 次）——这一行没进 NewHttpResolver",
				domain.URL, got)
		}
		if got := ns.countTopicQuery(topic); got == 0 {
			t.Fatalf("域名接口被访问了，但响应体里的地址 %s 没收到本 topic 的路由查询——"+
				"取到的列表被丢掉了（这条也是「地址只可能来自响应体」的取值判据）", ns.addr())
		}
	})

	t.Run("name_servers 优先于 url", func(t *testing.T) {
		ns := newFakeNameServer(t, "ns-both", "")

		var hits int32
		domain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&hits, 1)
			_, _ = io.WriteString(w, "127.0.0.1:1")
		}))
		t.Cleanup(domain.Close)

		const topic = "mapping-precedence"

		br := newBroker(t, &bootstrapv1.Broker_Rocketmq{
			NameServers:   []string{ns.addr()},
			NameServerUrl: domain.URL,
			InstanceName:  uniqInstance("both"),
			RetryCount:    1,
		})
		publish(t, br, topic)

		if got := ns.countTopicQuery(topic); got == 0 {
			t.Fatalf("两个字段都给了，name_servers 里的 %s 却没收到路由查询", ns.addr())
		}
		if got := atomic.LoadInt32(&hits); got != 0 {
			t.Fatalf("name_servers 非空时域名接口仍被访问 %d 次——优先序反了，"+
				"已经在用 name_servers 的工程会被改去连 name_server_url", got)
		}
	})
}

// TestMappingOffline_CredentialsReachAclHeaders 判 access_key / secret_key / security_token
// 三行，其中 secret_key 是**值判**。
//
// 三行在请求头上的落点各自独立：AccessKey 与 SecurityToken 是原样写入 extFields，
// Signature 却是 HMAC-SHA1(secret, content+body) 的摘要——只判「有没有 Signature」
// 等于让 secret_key 可以投成任何东西。这里按上游算法（fakewire_test.go 的 wantSignature）
// 用契约里的 secret 重算一遍，逐条比对线上值：本机实测与本函数逐字节一致（两条不同 topic
// 各验过一次），所以这条能钉住「投的就是这个 secret」。
//
// SecurityToken 单独判存在性而不是参与签名：ACLInterceptor 只把它放进 map、没放进 order，
// 路由查询的 extFields 里本来没有它 ⇒ 不进签名内容。把这条口径写下来是因为「三个字段都在
// 同一个 if 里投出去」看起来像一件事，实际判法不同。
func TestMappingOffline_CredentialsReachAclHeaders(t *testing.T) {
	ns := newFakeNameServer(t, "ns-acl", "")

	const (
		ak = "AK-mapping-test"
		sk = "SK-mapping-test"
		st = "ST-mapping-test"
	)
	const topic = "mapping-credentials"

	br := newBroker(t, &bootstrapv1.Broker_Rocketmq{
		NameServers:   []string{ns.addr()},
		AccessKey:     ak,
		SecretKey:     sk,
		SecurityToken: st,
		InstanceName:  uniqInstance("acl"),
		RetryCount:    1,
	})
	publish(t, br, topic)

	var queries int
	for _, r := range ns.seen() {
		got, ok := r.topicQuery()
		if !ok || got != topic {
			continue
		}
		queries++

		if r.ext("AccessKey") != ak {
			t.Fatalf("extFields.AccessKey=%q, want %q（流水：\n%s）", r.ext("AccessKey"), ak, formatReqs(ns.seen()))
		}
		if r.ext("SecurityToken") != st {
			t.Fatalf("extFields.SecurityToken=%q, want %q", r.ext("SecurityToken"), st)
		}
		if want := wantSignature(ak, sk, r.Ext, r.Body); r.ext("Signature") != want {
			t.Fatalf("extFields.Signature=%q, want %q（=HMAC-SHA1(secret_key, access_key+topic)）——"+
				"secret_key 没落到签名里，或者投的是别的值", r.ext("Signature"), want)
		}
	}
	if queries == 0 {
		t.Fatalf("没有任何本 topic 的路由查询可判（流水：\n%s）", formatReqs(ns.seen()))
	}
}

// TestMappingOffline_AccessKeyWithoutSecretSendsNoAuth 判 credentials 那行的**误用形状**：
// 只给 access_key 不给 secret_key 时，线上必须一个鉴权字段都不出现。
//
// 映射行写的是 `if v := sec.GetAccessKey(); v != ""`，也就是说这一行的开关只看 access_key；
// 真正把三值交给 SDK 的地方有两处闸门（v2 的 createProducer 判 `AK != "" && SK != ""`，
// SDK 自己判 Credentials.IsEmpty），两处都得是「两把钥匙齐全」才成立。所以这条判的是
// **半套凭据不会把没签名的 AccessKey 发出去**——那种请求在服务端只会得到一句
// 让人摸不着头脑的签名失败，比现在「当作没配凭据」更难查。
//
// 与 §7.23 mongodb 的「只给 username」同形：这一族的 credentials 行都是
// 「成对才生效」，把成对语义跑没掉的方式恰好都是只填一半，所以每族都要有一条误用形状。
func TestMappingOffline_AccessKeyWithoutSecretSendsNoAuth(t *testing.T) {
	ns := newFakeNameServer(t, "ns-half-cred", "")

	const topic = "mapping-half-credentials"

	br := newBroker(t, &bootstrapv1.Broker_Rocketmq{
		NameServers:  []string{ns.addr()},
		AccessKey:    "AK-only",
		InstanceName: uniqInstance("half"),
		RetryCount:   1,
	})
	publish(t, br, topic)

	reqs := ns.seen()
	var queries int
	for _, r := range reqs {
		if got, ok := r.topicQuery(); !ok || got != topic {
			continue
		}
		queries++
		if _, ok := r.Ext["AccessKey"]; ok {
			t.Fatalf("只有 access_key 却把 AccessKey 发上线了（ext=%v）——createProducer 的"+
				"「两把钥匙都非空」闸门被改成了单判 access_key", r.Ext)
		}
		if _, ok := r.Ext["Signature"]; ok {
			t.Fatalf("只有 access_key 却出现了 Signature=%q——空 secret 也能签，"+
				"服务端只会回一个难查的签名失败", r.ext("Signature"))
		}
	}
	if queries == 0 {
		t.Fatalf("没有任何本 topic 的路由查询可判（流水：\n%s）", formatReqs(reqs))
	}
}

// TestMappingOffline_GroupNameReachesProducerRequest 判 group_name 行在生产侧的落点：
// 发送请求（code=10）的 extFields.producerGroup 必须就是契约值。
//
// 为什么要一条会答路由的名字服务：生产者拿不到路由就永远发不出发送请求（答 17 的假线
// 只能看到路由查询）。所以本腿让路由指向一台**不回话**的假 broker——发送会超时失败，
// 但请求帧已经落进流水，判据读的是帧里的 producerGroup。
//
// 不给这一行时 SDK 的默认组是 `DEFAULT_PRODUCER`，所以断言写成等值而不是「非空」：
// 漂移成默认值时非空判据照样绿。
func TestMappingOffline_GroupNameReachesProducerRequest(t *testing.T) {
	bk := newFakeBroker(t, "bk-group")
	ns := newFakeNameServer(t, "ns-group", bk.addr())

	const (
		group = "GID-mapping-group"
		topic = "mapping-group-name"
	)

	br := newBroker(t, &bootstrapv1.Broker_Rocketmq{
		NameServers:  []string{ns.addr()},
		GroupName:    group,
		InstanceName: uniqInstance("group"),
		RetryCount:   1,
	})
	publish(t, br, topic)

	sends := sendsOf(bk.seen(), topic)
	if len(sends) == 0 {
		t.Fatalf("假 broker 上没有任何本 topic 的发送请求（流水：\n%s）——路由没答通，"+
			"或者生产者压根没建", formatReqs(bk.seen()))
	}
	for _, r := range sends {
		if got := r.ext("producerGroup"); got != group {
			t.Fatalf("发送请求的 producerGroup=%q, want %q——group_name 没落到生产者组名"+
				"（不给这一行时 SDK 默认是 DEFAULT_PRODUCER）", got, group)
		}
	}
}

// TestMappingOffline_NamespacePrefixesConsumerTopics 判 namespace 行。
//
// 这一行在两侧的形状不同，本腿两侧都判：
//   - 消费侧：pushConsumer 把订阅主题写成 `ns%topic`，于是路由查询里出现
//     `ns%mapping-ns-topic`，重试队列出现 `%RETRY%ns%GID-mapping-ns`（消费组同样带前缀）。
//   - 生产侧：本轮之前 `createProducer` **从不投 namespace**，生产者发的是裸 topic ⇒
//     同一个 namespace 下生产落在 A 主题、消费落在 ns%A 主题。现在补上投递，
//     所以 Publish 的路由查询必须是 `ns%mapping-ns-pub`。这一半就是那次修复的回归位。
//     （空 namespace 时 SDK 的 messagesWithNamespace 首行判空短路，老用户零影响。）
func TestMappingOffline_NamespacePrefixesConsumerTopics(t *testing.T) {
	bk := newFakeBroker(t, "bk-ns")
	ns := newFakeNameServer(t, "ns-ns", bk.addr())

	const (
		namespace  = "ns-mapping"
		group      = "GID-mapping-ns"
		subTopic   = "mapping-ns-topic"
		pubTopic   = "mapping-ns-pub"
		consumerOf = namespace + "%" + subTopic
		retryOf    = "%RETRY%" + namespace + "%" + group
		producerOf = namespace + "%" + pubTopic
	)

	br := newBroker(t, &bootstrapv1.Broker_Rocketmq{
		NameServers:  []string{ns.addr()},
		Namespace:    namespace,
		GroupName:    group,
		InstanceName: uniqInstance("ns"),
		RetryCount:   1,
	})

	if _, err := br.Subscribe(subTopic, routeHandler(), nil); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// 消费侧前缀：两个都得有——只判订阅主题会漏掉「消费组没带前缀」，那在阿里云
	// 命名空间实例上正是收不到消息的形态。Start() 之后由后台节拍发出，所以轮询。
	ns.eventually(t, 15*time.Second,
		"订阅后没看到带 namespace 前缀的路由查询（"+consumerOf+" 与 "+retryOf+"）",
		func(reqs []wireReq) bool {
			var sawTopic, sawRetry bool
			for _, r := range reqs {
				switch got, ok := r.topicQuery(); {
				case !ok:
				case got == consumerOf:
					sawTopic = true
				case got == retryOf:
					sawRetry = true
				}
			}
			return sawTopic && sawRetry
		})

	publish(t, br, pubTopic)

	if got := ns.countTopicQuery(producerOf); got == 0 {
		t.Fatalf("带 namespace 前缀的生产路由查询是 0 次，want ≥1——生产者又发裸 topic 了，" +
			"与消费侧落在两个不同主题上（本轮修复的回归位）")
	}
	// 裸主题必须不再出现：只看「有没有 ns%pub」会让「两条都发」的变异判绿。
	if got := ns.countTopicQuery(pubTopic); got != 0 {
		t.Fatalf("裸 topic %q 仍被查询 %d 次——namespace 在生产侧只生效了一半", pubTopic, got)
	}
}

// TestMappingOffline_InstanceNameIsClientIDSuffix 判 instance_name 行的取值。
//
// 唯一的直读口是心跳（code=34）body 里的 clientID：`ip@instanceName`。心跳在
// client.Start() 后约 1s 触发，且只发给**路由里那台 broker**，所以要一条会答路由的
// 名字服务加一台假 broker。本机实测心跳在 +1001ms 到达，这里轮询到 15s。
//
// 判后缀而不是整串：clientIP 随机器变（CI runner、本机、容器都不同）。
// 「@」之后必须正好是契约值——不给这一行时 SDK 用 `DEFAULT` 兜底并改写成 pid
// （`MakeClientID`：InstanceName=="DEFAULT" 时拼 pid），后缀对不上 ⇒ 判红。
func TestMappingOffline_InstanceNameIsClientIDSuffix(t *testing.T) {
	bk := newFakeBroker(t, "bk-instance")
	ns := newFakeNameServer(t, "ns-instance", bk.addr())

	const topic = "mapping-instance-name"

	inst := uniqInstance("instance-value")

	br := newBroker(t, &bootstrapv1.Broker_Rocketmq{
		NameServers:  []string{ns.addr()},
		GroupName:    "GID-mapping-instance",
		InstanceName: inst,
		RetryCount:   1,
	})
	publish(t, br, topic)

	bk.eventually(t, 15*time.Second, "假 broker 上没看到 clientID 以 @"+inst+" 结尾的心跳",
		func(reqs []wireReq) bool {
			for _, r := range reqs {
				if id, ok := r.clientIDOfHeartbeat(); ok && strings.HasSuffix(id, "@"+inst) {
					return true
				}
			}
			return false
		})
}

// TestMappingOffline_RetryCountIsAttemptCount 判 retry_count 行的**计数律**。
//
// SendSync 每轮迭代都会重查一次本 topic 的路由（topic 不存在 ⇒ 路由永远进不了缓存），
// 迭代次数 = 1 + WithRetry 的值，所以「本 topic 的路由查询条数」就是这一行的精确读数，
// 而且假线答 17 时整轮只有几毫秒——不需要拿墙上时钟猜。
//
// 三个点各自堵住一种写法：
//   - rc=1 ⇒ 2 次：证明「投了」。
//   - rc=9 ⇒ 10 次：证明投的是**契约值**而不是别的常量。
//   - 不给 ⇒ 3 次：v2 的 NewBroker 默认 retryCount=2。这一条管的是 `if v != 0` 那个守卫——
//     守卫改成无条件投递时，0 会进 producer.WithRetry(0) ⇒ 只剩 1 次查询，判红。
//     （0 不该被当成「重试 0 次」：契约里 0 的含义就是「没配」，缺省语义由默认值兜。）
func TestMappingOffline_RetryCountIsAttemptCount(t *testing.T) {
	cases := []struct {
		name string
		rc   int32
		want int
	}{
		{"rc=1", 1, 2},
		{"rc=9", 9, 10},
		{"不给则落回 v2 默认 2", 0, 3},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ns := newFakeNameServer(t, "ns-retry", "")
			topic := fmt.Sprintf("mapping-retry-%d", c.rc)

			sec := &bootstrapv1.Broker_Rocketmq{
				NameServers:  []string{ns.addr()},
				InstanceName: uniqInstance("retry"),
			}
			// 0 保持字段缺省，别写成显式给 0——两种写法在 proto3 里是同一个值，
			// 但读代码的人要知道这一条判的是「不给」。
			if c.rc != 0 {
				sec.RetryCount = c.rc
			}

			br := newBroker(t, sec)
			publish(t, br, topic)

			if got := ns.countTopicQuery(topic); got != c.want {
				t.Fatalf("本 topic 的路由查询 %d 次, want %d 次（1+retry 的迭代律）；流水：\n%s",
					got, c.want, formatReqs(ns.seen()))
			}
		})
	}
}

// TestMappingOffline_DefaultTopicFeedsSubscribeAndAbsentFails 判 topic（默认主题）行。
//
// a) 给 topic：`Subscribe("")` 不报「topic is empty」，而是落到契约值上——判两处：
//   - 返回的订阅者 Topic() 是该值（映射的落点）；
//   - 名字服务收到该值的路由查询（**真的订阅了它**，而不是只在内存里带了个名字）。
//     第二处才把「填了个字段但没人读」这类形状判掉。
//
// b) 不给 topic：`Subscribe("")` 当场返回 rocketmq: topic is empty。这一半判的是守卫的
// 缺席语义——v2 在缺省时会退回 SDK 的 DefaultTopicKey（`defaultTopic`），
// 那会让一个没配主题的契约静默订阅到一个所有人都在用的公共主题上。
func TestMappingOffline_DefaultTopicFeedsSubscribeAndAbsentFails(t *testing.T) {
	const topic = "mapping-default-topic"

	t.Run("契约的 topic 成为空串订阅的落点", func(t *testing.T) {
		bk := newFakeBroker(t, "bk-default")
		ns := newFakeNameServer(t, "ns-default", bk.addr())

		br := newBroker(t, &bootstrapv1.Broker_Rocketmq{
			NameServers:  []string{ns.addr()},
			GroupName:    "GID-mapping-default",
			InstanceName: uniqInstance("default"),
			Topic:        topic,
			RetryCount:   1,
		})

		sub, err := br.Subscribe("", routeHandler(), nil)
		if err != nil {
			t.Fatalf("给了 topic 却 Subscribe(\"\") 失败: %v", err)
		}
		if got := sub.Topic(); got != topic {
			t.Fatalf("sub.Topic()=%q, want %q", got, topic)
		}

		ns.eventually(t, 15*time.Second,
			"Subscribe(\"\") 后名字服务上没有 "+topic+" 的路由查询——默认主题只进了内存，没被订阅",
			func(reqs []wireReq) bool {
				for _, r := range reqs {
					if got, ok := r.topicQuery(); ok && got == topic {
						return true
					}
				}
				return false
			})
	})

	t.Run("不给 topic 时空串订阅当场失败", func(t *testing.T) {
		bk := newFakeBroker(t, "bk-nodefault")
		ns := newFakeNameServer(t, "ns-nodefault", bk.addr())

		br := newBroker(t, &bootstrapv1.Broker_Rocketmq{
			NameServers:  []string{ns.addr()},
			GroupName:    "GID-mapping-nodefault",
			InstanceName: uniqInstance("nodefault"),
			RetryCount:   1,
		})

		sub, err := br.Subscribe("", routeHandler(), nil)
		if err == nil {
			t.Fatalf("契约没给 topic 时 Subscribe(\"\") 却成功了（topic=%q）——"+
				"退回了 SDK 的 defaultTopic，等于订阅到一个谁都在用的公共主题", sub.Topic())
		}
		if !strings.Contains(err.Error(), "topic is empty") {
			t.Fatalf("报的是 %q, want 含 %q", err, "rocketmq: topic is empty")
		}
	})
}

// sendsOf 取「发往 topic 的发送请求」。
func sendsOf(reqs []wireReq, topic string) []wireReq {
	var out []wireReq
	for _, r := range reqs {
		if r.Code == codeSend && r.ext("topic") == topic {
			out = append(out, r)
		}
	}
	return out
}
