package contract

// metrics_segment_e2e_test.go —— §7.27 未闭合格的端到端取用证据：
// 「instrumentation 段 → 埋点实例 → WithMetrics → 抓取端点有数」。
//
// 前面各层判据（映射行、Plan/Build、类型化句柄）各自成立，不等于串起来成立。
// 这条跑的是完整链路，四跳各有观测点：
//
//	instrumentation.prometheus 段文本 → 段 Provider → *prom.Provider 落资源面
//	  → 消费侧经 Prometheus.Resource(app) 取回该实例（域 Wire 早于 server.tcp 构建）
//	  → after-build 把 tcp.WithMetrics(实例) 应用到契约构造的服务器
//	  → 真帧往返产生计数 → metricshttp 独立监听面上抓到该值
//
// 判据落在「私有 Registry 的抓取文本里出现带契约 namespace 前缀的计数，且值等于
// 真发过的帧数」。中间任何一环断掉都判红：落错注册表或 namespace 丢前缀 ⇒ 样本名
// 对不上；埋点没接进服务器 ⇒ 计数恒 0；接线换成手工构造的实例 ⇒ 抓取侧（独立读
// 同一段产物）拿不到数——正是「接线端与抓取端读同一个变量」会漏掉的那种假绿，所以
// 本文件把两侧拆成各自经句柄独立读取的两个消费者，最后再断言两者是同一实例。
//
// 反证腿（段缺席 ⇒ 消费侧立即失败）钉住第 1、2 跳不是靠兜底成立的。
//
// 消费姿势与 metrics/prometheus/contract/provider.go 头注释给的样例一致
// （`Prometheus.Resource(app)` → `WithMetrics(m)`），本文件即该样例的真进程版本。

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	promclient "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	prom "github.com/kalandramo/bald/metrics/prometheus"
	promcontract "github.com/kalandramo/bald/metrics/prometheus/contract"
	"github.com/kalandramo/bald/pkg/appkit"
	"github.com/kalandramo/bald/pkg/metricshttp"
	"github.com/kalandramo/bald/transport/tcp"
)

const (
	// metricsNamespace 写进契约 instrumentation.prometheus.namespace，因此指标名
	// 必须带上前缀（tcp_messages_total → e2em_tcp_messages_total）才算落点正确。
	metricsNamespace = "e2em"
	// framesSent 是真发出去的帧数，也是抓取文本里期望的计数值。
	framesSent = 3
	// okSample 是被断言的样本标签序列（处理器成功路径的 status）。
	okSample = "e2em_tcp_messages_total{status=\"ok\"}"
)

// readSegmentMeter 是两个消费侧共用的取用姿势：域 Wire 的入参是 *App，这是本仓
// 里「段产物 → 服务器构造参数」唯一的既有通道（工厂的 WithServerOptions 在声明期
// 求值，看不到资源面）。段缺席即 fail-fast，不留「悄悄没有埋点」的中间态。
func readSegmentMeter(app *appkit.App) (*prom.Provider, error) {
	m, ok := promcontract.Prometheus.Resource(app)
	if !ok {
		return nil, errors.New("instrumentation.prometheus 不在资源面上（段没构造、段 Provider 没装配，或产物类型不符）")
	}
	return m, nil
}

// consumerDomain 是一个只声明 Wire 的域：把段产物存进 dst 供测试方读。
// name 区分两个消费者（接线侧 / 抓取侧），两侧各自独立经句柄读同一段产物。
func consumerDomain(name string, dst *atomic.Pointer[prom.Provider]) appkit.Domain {
	return appkit.Domain{
		Name: name,
		Wire: func(_ context.Context, app *appkit.App) error {
			m, err := readSegmentMeter(app)
			if err != nil {
				return err
			}
			dst.Store(m)
			return nil
		},
	}
}

// metricsTcpProvider 段驱动构造 tcp 服务器，并在 after-build（构造后、Start 前）
// 把接线侧读到的实例挂到埋点口上。
func metricsTcpProvider(meter *atomic.Pointer[prom.Provider]) appkit.Provider {
	return AppProvider(WithAfterBuild(func(_ context.Context, s *tcp.Server) error {
		m := meter.Load()
		if m == nil {
			// 非阻塞读：server 段节点在域 Wire 之后构建是引擎的时序约定，
			// 读不到就是时序断了——这里必须 fail-fast，不能退化成「没有埋点也照起」。
			return errors.New("接线侧 Wire 未在 server.tcp 构建前执行（装配时序与约定不符）")
		}
		// tcp.Option 是 func(*tcp.Server)，WithMetrics 因此在构造后同样可应用
		// （值来自资源面，与工厂声明期应用的 WithServerOptions 不同一条通道）。
		tcp.WithMetrics(m)(s)
		s.RegisterMessageHandler(testMsgType, func(sid tcp.SessionID, payload tcp.NetMessagePayload) error {
			raw, ok := payload.([]byte)
			if !ok {
				return fmt.Errorf("payload = %T, want []byte", payload)
			}
			return s.SendRawData(sid, []byte("pong:"+string(raw)))
		}, nil)
		return nil
	}))
}

func TestAppProviderEndToEnd_InstrumentationSegmentReachesScrapeEndpoint(t *testing.T) {
	isolateFlags(t)
	addr := loopbackAddr(t)
	scrapeAddr := loopbackAddr(t)
	cfgPath := writeContractFile(t, fmt.Sprintf(""+
		"app:\n"+
		"    name: tcp-metrics-e2e\n"+
		"    version: v0.0.0\n"+
		"instrumentation:\n"+
		"    prometheus:\n"+
		"        namespace: %s\n"+
		"server:\n"+
		"    tcp:\n"+
		"        addr: %s\n", metricsNamespace, addr))

	// Wire 在 Serve 的 goroutine 里写、测试 goroutine 里读，故用 atomic.Pointer
	// 而不是裸指针（race 作业会抓这一对）。
	var wired, exposed atomic.Pointer[prom.Provider]

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- appkit.Serve(ctx,
			appkit.WithRole("tcp-metrics-e2e"),
			appkit.ConfigFile(cfgPath),
			appkit.StrictContract(),
			appkit.Providers(promcontract.AppProvider(), metricsTcpProvider(&wired)),
			consumerDomain("metrics-wiring", &wired),
			consumerDomain("metrics-exposure", &exposed),
		)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-errCh:
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("appkit.Serve 返回 %v, want nil 或 context.Canceled", err)
			}
		case <-time.After(15 * time.Second):
			t.Error("appkit.Serve 未在取消后返回")
		}
	})

	conn, err := dialUntilReady(func() (net.Conn, error) {
		return net.DialTimeout("tcp", addr, testWait)
	})
	if err != nil {
		var serveErr error
		select {
		case serveErr = <-errCh:
		default:
		}
		t.Fatalf("契约地址 %s 没监听（Serve 早期返回 %v）: %v", addr, serveErr, err)
	}
	defer conn.Close()

	for i := 0; i < framesSent; i++ {
		body := fmt.Sprintf("m%d", i)
		if got := exchange(t, conn, body); got != "pong:"+body {
			t.Fatalf("第 %d 帧回包 = %q, want pong:%s", i, got, body)
		}
	}

	if wired.Load() == nil {
		t.Fatal("接线侧 Wire 没执行（段 Provider 没进计划？）")
	}
	exposedMeter := exposed.Load()
	if exposedMeter == nil {
		t.Fatal("抓取侧 Wire 没执行（第二个消费者没进装配？）")
	}
	if exposedMeter != wired.Load() {
		t.Fatalf("两个消费侧读到不同实例：%p 与 %p（资源面上同名键被写入两次？）", exposedMeter, wired.Load())
	}

	// 断的是**段构造的那个实例的私有 Registry**：use_default_registry 缺席即私有
	// 注册表，所以这里读到值就等于「埋点落进了段产物」——抓取侧与接线侧是两次独立
	// 读取，接线被换成手工实例时这一眼就是空的。
	scrapeUntil(t, scrapeAddr, exposedMeter.Registry(), okSample, fmt.Sprint(framesSent))
}

// TestAppProviderEndToEnd_MetricsSegmentAbsentFailsWire 反证：instrumentation 段
// 缺席时链路必须在消费侧这一跳就失败，而不是「悄悄没有埋点」把启动放过去。
// 摘掉 readSegmentMeter 里的段存在判定（或让 after-build 兜底手工构造实例），这条就判红。
func TestAppProviderEndToEnd_MetricsSegmentAbsentFailsServe(t *testing.T) {
	isolateFlags(t)
	addr := loopbackAddr(t)
	cfgPath := writeContractFile(t, ""+
		"app:\n"+
		"    name: tcp-metrics-e2e\n"+
		"    version: v0.0.0\n"+
		"server:\n"+
		"    tcp:\n"+
		"        addr: "+addr+"\n")

	var wired, exposed atomic.Pointer[prom.Provider]
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	err := appkit.Serve(ctx,
		appkit.WithRole("tcp-metrics-e2e"),
		appkit.ConfigFile(cfgPath),
		appkit.StrictContract(),
		appkit.Providers(promcontract.AppProvider(), metricsTcpProvider(&wired)),
		consumerDomain("metrics-wiring", &wired),
		consumerDomain("metrics-exposure", &exposed),
	)
	if err == nil {
		t.Fatal("instrumentation 段缺席仍启动成功：消费侧的资源面判据没生效")
	}
	if !strings.Contains(err.Error(), "instrumentation.prometheus") {
		t.Fatalf("err = %v, want 点名 instrumentation.prometheus", err)
	}
	if wired.Load() != nil || exposed.Load() != nil {
		t.Fatal("段缺席却取到了埋点实例（资源面上有别处写入的同名键？）")
	}
}

// scrapeUntil 在 scrapeAddr 起一个 metricshttp 暴露端（独立监听面，与业务端口
// 不同），轮询抓取直到样本值等于 want；超时判红并附样本附近的原文。
//
// 轮询而不是抓一次：计数由服务器 goroutine 在处理器返回时写入，客户端收到回包
// 时它可能还在路上——这条腿断的是「有没有数」，不是「第几毫秒有数」。
func scrapeUntil(t *testing.T, scrapeAddr string, g promclient.Gatherer, sample, want string) {
	t.Helper()

	srv := metricshttp.Start(scrapeAddr, "/metrics", promhttp.HandlerFor(g, promhttp.HandlerOpts{}))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Stop(ctx); err != nil {
			t.Errorf("metricshttp Stop: %v", err)
		}
	})

	client := &http.Client{Timeout: testWait}
	url := "http://" + scrapeAddr + "/metrics"
	deadline := time.Now().Add(testWait)
	var last string
	var lastErr error
	for time.Now().Before(deadline) {
		body, err := httpGet(client, url)
		if err != nil {
			lastErr = err
		} else {
			last = body
			if got, ok := sampleValue(body, sample); ok {
				if got == want {
					return
				}
				lastErr = fmt.Errorf("样本 %s = %s, want %s", sample, got, want)
			} else {
				lastErr = fmt.Errorf("抓取文本里没有样本 %s", sample)
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("端点 %s 上没有等到 %s=%s：%v；末次抓取片段：\n%s",
		scrapeAddr, sample, want, lastErr, excerpt(last, sample))
}

func httpGet(client *http.Client, url string) (string, error) {
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s = %s", url, resp.Status)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// sampleValue 按 Prometheus 文本格式取样本值：命中行形如
// `name{labels} value`，返回最后一个字段。
func sampleValue(body, sample string) (string, bool) {
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, sample+" ") {
			fields := strings.Fields(line)
			return fields[len(fields)-1], true
		}
	}
	return "", false
}

// excerpt 给出样本名附近的抓取原文，判红时要能看出是「没这个名字」还是「名字有但值不对」。
func excerpt(body, sample string) string {
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "# TYPE ") && strings.Contains(line, sample) {
			end := min(i+4, len(lines))
			return strings.Join(lines[i:end], "\n")
		}
	}
	if len(body) > 400 {
		return body[:400] + "…(截断)"
	}
	return body
}
