package contract

// mapping_offline_test.go —— instrumentation.datadog 五行的**运行期**闸门：
// 契约字段不是落进某个 struct 就算数，要能在 UDP 报文里读到它。
// 观测面 = 本机 UDP 监听（DogStatsD 就是 UDP，不需要 agent 容器）。
//
// 三条口径是实测出来的，不是从 SDK 文档抄的（出处写在各行注释里）：
//   - buffer_size 是真上界：v5 默认档会自然合包到 2–3 行/报文，配 1/2 后
//     实测严格不超过配置值（各重复 3 轮一致）；
//   - sample_rate **只有 Histogram 生效**：聚合器在 flush 时把 count/gauge 的
//     rate 硬编成 1（datadog-go v5.8.2 metrics.go 的 countMetric/gaugeMetric
//     flushUnsafe），histogram 走 bufferedMetric 路径才带 |@；
//   - flush_period_millis 是聚合节拍：60000 时运行期一条不发，Close() 兜底
//     flush 才出第一条。

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/metrics/datadog"
)

// newListener 起一个本机 UDP 监听，返回地址和报文通道（每数据报一条）。
func newListener(t *testing.T) (string, chan string) {
	t.Helper()
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	ch := make(chan string, 65536)
	go func() {
		buf := make([]byte, 65536)
		for {
			n, _, err := pc.ReadFromUDP(buf)
			if err != nil {
				return
			}
			ch <- string(buf[:n])
		}
	}()
	return pc.LocalAddr().String(), ch
}

// waitFor 在 window 内收集前缀命中的行；want>0 时凑够就提前返回。
func waitFor(t *testing.T, ch chan string, prefix string, want int, window time.Duration) []string {
	t.Helper()
	var got []string
	deadline := time.After(window)
	for len(got) < want || want == 0 {
		select {
		case d := <-ch:
			for _, l := range strings.Split(strings.TrimSpace(d), "\n") {
				if strings.HasPrefix(l, prefix) {
					got = append(got, l)
				}
			}
		case <-deadline:
			return got
		}
	}
	return got
}

// collectUntil 一趟收包把多个前缀分别入桶，每桶都非空即返回（window 内没凑齐
// 就判红并列出空桶）。多个前语/多档配置**必须**用这个而不是一串 waitFor：
// statsd 会把多条指标合进同一个 UDP 报文，等第一个前缀时读走的报文里可能正好
// 带着第二个前缀的行——逐个等会把证据自己丢掉。
func collectUntil(t *testing.T, ch chan string, prefixes []string, window time.Duration) map[string][]string {
	t.Helper()
	buckets := make(map[string][]string, len(prefixes))
	deadline := time.After(window)
	for {
		complete := true
		for _, p := range prefixes {
			if len(buckets[p]) == 0 {
				complete = false
				break
			}
		}
		if complete {
			return buckets
		}
		select {
		case d := <-ch:
			for _, l := range strings.Split(strings.TrimSpace(d), "\n") {
				for _, p := range prefixes {
					if strings.HasPrefix(l, p) {
						buckets[p] = append(buckets[p], l)
					}
				}
			}
		case <-deadline:
			var empty []string
			for _, p := range prefixes {
				if len(buckets[p]) == 0 {
					empty = append(empty, p)
				}
			}
			t.Fatalf("window 内未集齐，空桶=%v，已收到=%v", empty, buckets)
		}
	}
}

// linesPerDatagram 在 window 内收集前缀命中的行，返回每数据报的本族行数分布
// （键=行数，值=出现次数）与总行数。合计不足 want 时判红——报文丢了就不能
// 拿「分布」当证据。
func linesPerDatagram(t *testing.T, ch chan string, prefix string, want int, window time.Duration) (map[int]int, int) {
	t.Helper()
	per := map[int]int{}
	total := 0
	deadline := time.After(window)
	for total < want {
		select {
		case d := <-ch:
			n := 0
			for _, l := range strings.Split(strings.TrimSpace(d), "\n") {
				if strings.HasPrefix(l, prefix) {
					n++
				}
			}
			if n > 0 {
				per[n]++
				total += n
			}
		case <-deadline:
			t.Fatalf("prefix %q: window 内只收到 %d/%d 行", prefix, total, want)
		}
	}
	return per, total
}

func mustProvider(t *testing.T, sec *bootstrapv1.Instrumentation_Datadog) (*datadog.Provider, func()) {
	t.Helper()
	p, cleanup, err := Provider(sec)
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	if cleanup == nil {
		t.Fatal("cleanup = nil, want non-nil（Close 负责兜底 flush）")
	}
	return p, cleanup
}

// addr 行：报文只出现在配置的端口上。
func TestMappingOffline_AddressReachesPort(t *testing.T) {
	addr, ch := newListener(t)
	p, cleanup := mustProvider(t, &bootstrapv1.Instrumentation_Datadog{Address: addr, FlushPeriodMillis: 100})
	defer cleanup()

	p.Counter(context.Background(), "dd1_addr", 1, nil)
	lines := waitFor(t, ch, "dd1_addr", 1, 5*time.Second)
	if len(lines) == 0 {
		t.Fatal("address 端口上没有收到任何报文")
	}
	if !strings.HasPrefix(lines[0], "dd1_addr:") {
		t.Fatalf("line = %q, want 无前缀的本族名", lines[0])
	}
}

// namespace 行：前缀以 "<ns>." 上 wire；**留空必须原样上线**（这是本轮修掉的
// 缺陷——空 namespace 曾产出 ".dd2_ns:1|c" 这种前导点名字）。
func TestMappingOffline_NamespaceReachesWire(t *testing.T) {
	addr, ch := newListener(t)
	ctx := context.Background()

	p1, c1 := mustProvider(t, &bootstrapv1.Instrumentation_Datadog{Address: addr, Namespace: "ddns", FlushPeriodMillis: 100})
	defer c1()
	p2, c2 := mustProvider(t, &bootstrapv1.Instrumentation_Datadog{Address: addr, FlushPeriodMillis: 100})
	defer c2()

	p1.Counter(ctx, "dd2_ns", 1, nil)
	p2.Counter(ctx, "dd2_bare", 1, nil)

	got := collectUntil(t, ch, []string{"ddns.dd2_ns", "dd2_bare"}, 5*time.Second)
	for _, l := range append(got["ddns.dd2_ns"], got["dd2_bare"]...) {
		if strings.HasPrefix(l, ".") {
			t.Fatalf("空 namespace 产出了前导点名字: %q", l)
		}
	}
}

// buffer_size 行：真上界。默认档实测会自然合包（2–3 行/报文），因此两档一起判：
// 1 ⇒ 每报文恰好 1 行；2 ⇒ 出现 2 行/报文但绝不出现 ≥3。
// 反向可证伪：漏传该 Option ⇒ 第二档出现 3 行；写死 1 ⇒ 第二档等不到 2 行。
func TestMappingOffline_BufferSizeCapsDatagram(t *testing.T) {
	addr, ch := newListener(t)
	ctx := context.Background()
	const n = 60

	for _, row := range []struct {
		size    int32
		prefix  string
		maxLine int
		wantTwo bool
	}{
		{size: 1, prefix: "dd3_s1_", maxLine: 1, wantTwo: false},
		{size: 2, prefix: "dd3_s2_", maxLine: 2, wantTwo: true},
	} {
		p, cleanup := mustProvider(t, &bootstrapv1.Instrumentation_Datadog{
			Address: addr, BufferSize: row.size, FlushPeriodMillis: 100,
		})
		for i := 0; i < n; i++ {
			p.Counter(ctx, fmt.Sprintf("%s%d", row.prefix, i), 1, nil)
		}
		per, total := linesPerDatagram(t, ch, row.prefix, n, 5*time.Second)
		maxSeen := 0
		for lines, occ := range per {
			if lines > maxSeen {
				maxSeen = lines
			}
			if lines > row.maxLine {
				t.Fatalf("buffer_size=%d: 某数据报含 %d 行本族指标（出现 %d 次），超出上界", row.size, lines, occ)
			}
		}
		if row.wantTwo && per[2] == 0 {
			t.Fatalf("buffer_size=2: %d 行散在 %d 个数据报里却没有任何 2 行报文（上界没被用到 ⇒ 该字段可能根本没生效）",
				total, len(per))
		}
		if !row.wantTwo && maxSeen != 1 {
			t.Fatalf("buffer_size=1: 最大行数 = %d, want 1", maxSeen)
		}
		cleanup()
	}
}

// flush_period_millis 行：节拍。大节拍运行期静默、Close() 才兜底；
// 小节拍不等 Close 就能收到。两半合起来才排除「字段被忽略」（忽略 ⇒ 两半都判红：
// 前者立刻到，后者……同前，所以判据落在这半边）。
func TestMappingOffline_FlushPeriodGatesAggregationTick(t *testing.T) {
	addr, ch := newListener(t)
	ctx := context.Background()

	// 大节拍：1.5s 内一条不发
	pSlow, cSlow := mustProvider(t, &bootstrapv1.Instrumentation_Datadog{Address: addr, FlushPeriodMillis: 60000})
	defer cSlow()
	pSlow.Counter(ctx, "dd4_slow", 1, nil)
	if got := waitFor(t, ch, "dd4_slow", 0, 1500*time.Millisecond); len(got) > 0 {
		t.Fatalf("flush_period_millis=60000 却在运行期就发出报文: %q", got)
	}
	cSlow() // Close 触发最终 flush
	if got := waitFor(t, ch, "dd4_slow", 1, 5*time.Second); len(got) == 0 {
		t.Fatal("Close() 后仍未收到报文（cleanup 没接上最终 flush）")
	}

	// 小节拍：不 Close 也要到。窗口取 1s——SDK 的聚合是**默认开启**的
	// （datadog-go v5.8.2 options.go:24-25：defaultAggregation=true，
	// defaultAggregationFlushInterval=2s），所以「摘掉 WithAggregationInterval」
	// 只会把节拍退回 2s 而不是关掉聚合；1s 窗口能把这两档分开
	// （配置节拍 100ms 有 10 次重复，2s 默认档一次都不到）。
	pFast, cFast := mustProvider(t, &bootstrapv1.Instrumentation_Datadog{Address: addr, FlushPeriodMillis: 100})
	defer cFast()
	pFast.Counter(ctx, "dd4_fast", 1, nil)
	if got := waitFor(t, ch, "dd4_fast", 1, 1000*time.Millisecond); len(got) == 0 {
		t.Fatal("flush_period_millis=100 时 1s 内没有发出报文（节拍没落到聚合器上）")
	}
}

// sample_rate 行的**不对称**口径（实测 + SDK 源码双向确认）：
//   - Histogram 走 bufferedMetric 路径：保留 specifiedRate ⇒ 报文带 |@0.5，
//     且到达数明显小于发送数（真采样）；
//   - Counter/Gauge/GaugeAdd 走聚合器：flushUnsafe 把 rate 硬编成 1 ⇒ 报文
//     只有一条合并值且**没有** |@。契约把这格喂给 SDK 后只能对 1/4 的原语生效，
//     proto 注释与本测试共同钉住这个事实。
func TestMappingOffline_SampleRateAffectsHistogramOnly(t *testing.T) {
	addr, ch := newListener(t)
	ctx := context.Background()

	p, cleanup := mustProvider(t, &bootstrapv1.Instrumentation_Datadog{
		Address: addr, SampleRate: 0.5, FlushPeriodMillis: 200,
	})
	defer cleanup()

	p.Histogram(ctx, "dd5_h", 0.4, nil)
	for i := 1; i < 200; i++ {
		p.Histogram(ctx, "dd5_h", 0.4, nil)
	}
	// 只等 histogram：want=0 让 waitFor 把整个窗口读满，才拿得到「到达数」。
	hLines := waitFor(t, ch, "dd5_h", 0, 3*time.Second)
	if len(hLines) == 0 {
		t.Fatal("histogram 一条都没收到，无法判 sample_rate")
	}
	for _, l := range hLines {
		if !strings.Contains(l, "|@0.5") {
			t.Fatalf("histogram 报文缺 |@0.5 标记（sample_rate 没落到 histogram 上）: %q", l)
		}
	}
	if len(hLines) >= 200 {
		t.Fatalf("sample_rate=0.5 下 histogram 收到 %d 条，未见采样/合并丢弃", len(hLines))
	}

	p.Counter(ctx, "dd5_c", 1, nil)
	for i := 1; i < 50; i++ {
		p.Counter(ctx, "dd5_c", 1, nil)
	}
	p.Gauge(ctx, "dd5_g", 1, nil)
	p.GaugeAdd(ctx, "dd5_ga", 1, nil)
	// 三个聚合原语在同一趟收包里取齐（合包会把它们和 histogram 残留放进同一报文）。
	got := collectUntil(t, ch, []string{"dd5_c:", "dd5_g:", "dd5_ga:"}, 5*time.Second)
	for _, l := range append(append(got["dd5_c:"], got["dd5_g:"]...), got["dd5_ga:"]...) {
		if strings.Contains(l, "|@") {
			t.Fatalf("聚合路径本应丢弃 sample_rate，实测却带上标记（SDK 行为变了，proto 注释需要重审）: %q", l)
		}
	}
}

// 四原语 + 标签的线格式（控件：证明上面的判红不是「什么都没收到」）。
// GaugeAdd 落 |c 是后端的既有口径（增量语义用 statsd count 表达），不是缺陷。
func TestMappingOffline_PrimitivesAndTagsOnWire(t *testing.T) {
	addr, ch := newListener(t)
	p, cleanup := mustProvider(t, &bootstrapv1.Instrumentation_Datadog{Address: addr, FlushPeriodMillis: 100})
	defer cleanup()

	ctx := context.Background()
	p.Counter(ctx, "dd6_c", 3, map[string]string{"status": "ok"})
	p.Gauge(ctx, "dd6_g", 2, nil)
	p.GaugeAdd(ctx, "dd6_ga", 5, nil)
	p.Histogram(ctx, "dd6_h", 0.4, nil)

	want := map[string]bool{
		"dd6_c:3|c|#status:ok": true,
		"dd6_g:2|g":            true,
		"dd6_ga:5|c":           true,
		"dd6_h:0.4|h":          true,
	}
	deadline := time.After(5 * time.Second)
	for len(want) > 0 {
		select {
		case d := <-ch:
			for _, l := range strings.Split(strings.TrimSpace(d), "\n") {
				delete(want, l)
			}
		case <-deadline:
			t.Fatalf("5s 内未收到的线格式: %v", want)
		}
	}
}

// 必填与越界行：构造期 fail-fast，不产出「配了但静默丢包」的实例。
func TestMappingOffline_FailFastRows(t *testing.T) {
	addr, _ := newListener(t)
	cases := []struct {
		name     string
		sec      *bootstrapv1.Instrumentation_Datadog
		wantSubs string
	}{
		{"nil 子段", nil, "子段为 nil"},
		{"address 必填", &bootstrapv1.Instrumentation_Datadog{}, "address 必填"},
		{"buffer_size 负数", &bootstrapv1.Instrumentation_Datadog{Address: addr, BufferSize: -1}, "buffer_size=-1 非法"},
		{"flush_period 非正", &bootstrapv1.Instrumentation_Datadog{Address: addr, FlushPeriodMillis: -5}, "flush_period_millis=-5 非法"},
		{"sample_rate 越界上", &bootstrapv1.Instrumentation_Datadog{Address: addr, SampleRate: 1.5}, "sample_rate=1.5 非法"},
		{"sample_rate 负", &bootstrapv1.Instrumentation_Datadog{Address: addr, SampleRate: -0.5}, "sample_rate=-0.5 非法"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, cleanup, err := Provider(tc.sec)
			if err == nil {
				t.Fatalf("err = nil, want %q", tc.wantSubs)
			}
			if !strings.Contains(err.Error(), tc.wantSubs) {
				t.Fatalf("err = %q, want 含 %q", err, tc.wantSubs)
			}
			if p != nil || cleanup != nil {
				t.Fatal("fail-fast 时仍产出了实例/cleanup")
			}
		})
	}
}
