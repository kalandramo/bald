package contract

// mapping_runtime_test.go —— 契约 registry.consul 段 → consul options「那一跳」的
// 运行期判据（§7.18，与 registry/etcd/contract/mapping_runtime_test.go 同族）。
//
// ## 为什么要有这个文件
//
// Provider 里那九行 `append(opts, consul.With…)` 原先只被
// TestProvider_FieldMappingRuns 判到「构造不报错」。`consul.New` 不触网
// （api.NewClient 只建 client），所以那一跳长期是**零判据**：把任意一行删掉，
// 全套用例照样绿。etcd 侧同族缺陷（§7.14）抓出的教训是「翻译层有判据、
// 覆盖层有判据，中间那一跳没有」，本文件补的就是 consul 的中间那一跳。
//
// ## 观测面
//
// 不往公开 API 加只读 accessor，改为在**服务端那头**看：consul 的 SDK 把每个
// 装配结果都写成一次 agent HTTP 调用，于是起一个进程内假 agent（httptest），
// 把契约值断成请求体/查询串/请求头的字节。与 etcd 那条腿不同，这里全是
// 同步调用，没有时序余量要算，整文件亚秒级。
//
// ## 各腿判哪几行（逐行都有落点，变异验证见 §7.18）
//
//   - RegisterBodyCarriesContractValues：WithAddress、WithToken、
//     WithHealthCheckInterval、WithHealthCheckTimeout、
//     WithDeregisterCriticalServiceAfter、WithHealthCheck(true)、WithHeartbeat(true)
//   - CheckFlagsDriveCheckEntries：两个开关各自驱动**不同的**检查条目
//     （刻意不做成「或」判据，否则一行缺失会被另一行兜住）
//   - AbsentBoolsReallyDisableChecks：契约里**不写**这两个开关的运行期后果
//     （经 bconf 加载器，proto3 缺省 bool=false 覆盖构造默认 true）
//   - AbsentIntervalsKeepDefaults：三行 `if > 0` 守卫本身
//   - SchemeSectionReachesTransport：WithScheme（https 反证 + http 正证）
//   - DatacenterSectionReachesDiscovery：WithDatacenter(MULTI) 的分片扇出
//
// ## 两条已知边界（不是本文件的疏漏，写在这是为了让接手者不误判覆盖面）
//
//  1. `case "SINGLE"` 那一行**运行期不可判**：它写入的正是 newRegistry 的默认值，
//     删掉它观测不到任何变化。本文件对它只判「SINGLE 不扇出」这个行为，
//     不判「这行存在」。
//  2. `Registry.timeout`（Watch 的 resolve 截止期）改前是被 `health_check_timeout`
//     误接的；修好后它不再有任何契约字段驱动，停在 10s 默认。要让它可配需要
//     新增契约字段（bconf 的 minor），不在本轮范围。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	registry "github.com/kalandramo/bald/registry"

	"github.com/kalandramo/bald/bconf"
)

// agentRequest 是一条打到假 agent 的调用（含请求体原文）。
type agentRequest struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	Body   []byte
}

// fakeAgent 只实现契约装配真正会碰到的端点：注册/注销/TTL 续约/数据中心/健康发现。
// 其余路径回 404——判据若打到没实现的路径会立刻显形，而不是静默通过。
type fakeAgent struct {
	mu   sync.Mutex
	reqs []agentRequest

	// dcs 在 server 启动前写定、之后只读：MULTI 扇出就是按这个顺序逐个查。
	dcs []string

	hostPort string
}

func newFakeAgent(t *testing.T) *fakeAgent {
	t.Helper()
	// 把环境变量毒成连不通的地址。api.DefaultConfig() 读 CONSUL_HTTP_ADDR，
	// 若跑测试的机器恰好把环境变量指向真 agent（本机 :8500 起着），
	// 「删掉 WithAddress 那行」照样能绿——判据的牙齿必须与环境无关。
	t.Setenv("CONSUL_HTTP_ADDR", "127.0.0.1:1")
	f := &fakeAgent{dcs: []string{"dc1", "dc2"}}
	srv := httptest.NewServer(f)
	// scheme=https 那条腿会故意制造 TLS 失败，服务端日志不该污染判据输出。
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	f.hostPort = strings.TrimPrefix(srv.URL, "http://")
	t.Cleanup(srv.Close)
	return f
}

func (f *fakeAgent) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.reqs = append(f.reqs, agentRequest{
		Method: r.Method,
		Path:   r.URL.Path,
		Query:  r.URL.Query(),
		Header: r.Header.Clone(),
		Body:   body,
	})
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.URL.Path == "/v1/catalog/datacenters":
		_ = json.NewEncoder(w).Encode(f.dcs)
	case strings.HasPrefix(r.URL.Path, "/v1/health/service/"):
		name := strings.TrimPrefix(r.URL.Path, "/v1/health/service/")
		fmt.Fprintf(w, `[{"Node":{"Node":"n-%s","Address":"10.0.0.1"},`+
			`"Service":{"ID":"d-%s","Service":%q,"Address":"10.0.0.1","Port":8080,`+
			`"Tags":["version=v1.0.0"]}}]`, name, name, name)
	case strings.HasPrefix(r.URL.Path, "/v1/agent/"), strings.HasPrefix(r.URL.Path, "/v1/status/"):
		// 写路径与探活：200 空体即成功，与真 agent 一致。
	default:
		http.Error(w, "fakeAgent: unexpected path "+r.URL.Path, http.StatusNotFound)
	}
}

// filter 返回路径以 prefix 开头的调用（按到达顺序）。
func (f *fakeAgent) filter(prefix string) []agentRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []agentRequest
	for _, r := range f.reqs {
		if strings.HasPrefix(r.Path, prefix) {
			out = append(out, r)
		}
	}
	return out
}

// jsonOf 把一条调用体解成 map。键就是 SDK 的 Go 字段名（AgentServiceRegistration
// 里 Checks/Check 无 tag，其余是 `json:",omitempty"`），所以断言按字段名写。
func jsonOf(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("请求体不是 JSON: %v（原文 %s）", err, body)
	}
	return m
}

// fieldStr 取顶层/检查体内的字符串字段；缺键即空串（omitempty 的缺省形状）。
func fieldStr(t *testing.T, m map[string]any, key string) string {
	t.Helper()
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		t.Fatalf("字段 %s 不是字符串：%#v", key, v)
	}
	return s
}

// checksOf 取注册请求体里的检查数组。AgentServiceRegistration.Checks 没有
// omitempty，所以「没有检查」的形状是空数组而非缺键——两种形状都判为空。
func checksOf(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	raw, ok := body["Checks"]
	if !ok || raw == nil {
		return nil
	}
	arr, ok := raw.([]any)
	if !ok {
		t.Fatalf("Checks 不是数组：%#v", raw)
	}
	out := make([]map[string]any, 0, len(arr))
	for _, e := range arr {
		m, ok := e.(map[string]any)
		if !ok {
			t.Fatalf("Checks 元素不是对象：%#v", e)
		}
		out = append(out, m)
	}
	return out
}

// providerWith 构造一次 Provider 并登记 cleanup。
func providerWith(t *testing.T, c *bootstrapv1.Registry_Consul) registry.Registrar {
	t.Helper()
	reg, cleanup, err := Provider(context.Background(), &bootstrapv1.Registry{Type: Type, Consul: c})
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	t.Cleanup(cleanup)
	return reg
}

func instance(id, name string) *registry.ServiceInstance {
	return &registry.ServiceInstance{
		ID:        id,
		Name:      name,
		Version:   "v1.0.0",
		Endpoints: []string{"http://127.0.0.1:19090"},
	}
}

// TestProvider_RegisterBodyCarriesContractValues —— 契约里的地址/令牌与三个时长
// 值，必须原样出现在打到 agent 的注册请求体里。
//
// 每个值都取**与 SDK 默认值不同**的数：interval 7s（默认 10s）、timeout 3s
// （默认 5s）、deregister 42s（默认 600s）、TTL 14s（= interval*2，默认 20s）。
// 于是「那行没投递」与「投递了但取了默认」在判据上是同一件会红的事，
// 而「换了个恰好等于默认的数」不会把这腿改成恒真。
func TestProvider_RegisterBodyCarriesContractValues(t *testing.T) {
	f := newFakeAgent(t)
	reg := providerWith(t, &bootstrapv1.Registry_Consul{
		Address:                        f.hostPort,
		Token:                          "contract-token-77",
		Datacenter:                     "SINGLE",
		EnableHealthCheck:              true,
		Heartbeat:                      true,
		HealthCheckInterval:            7,
		HealthCheckTimeout:             3,
		DeregisterCriticalServiceAfter: 42,
	})
	if err := reg.Register(context.Background(), instance("ut-rt-1", "ut-rt")); err != nil {
		t.Fatalf("Register: %v", err)
	}

	regs := f.filter("/v1/agent/service/register")
	if len(regs) != 1 {
		t.Fatalf("注册调用应为 1 条，实得 %d 条（0 条＝WithAddress 没投递到 SDK，请求没进这个假 agent）", len(regs))
	}
	// WithToken：consul SDK 把它写成每个请求的 X-Consul-Token 头。
	if got := regs[0].Header.Get("X-Consul-Token"); got != "contract-token-77" {
		t.Fatalf("X-Consul-Token=%q，want \"contract-token-77\"（WithToken 那行没落到请求头）", got)
	}

	body := jsonOf(t, regs[0].Body)
	checks := checksOf(t, body)
	if len(checks) != 2 {
		t.Fatalf("enable_health_check+heartbeat 两真应有 2 条检查（TCP + TTL），实得 %d：%v", len(checks), body["Checks"])
	}
	tcp, ttlCheck := checks[0], checks[1]

	if got := fieldStr(t, tcp, "TCP"); got != "127.0.0.1:19090" {
		t.Fatalf("TCP 检查地址=%q，want 端点解析出的 \"127.0.0.1:19090\"", got)
	}
	for key, want := range map[string]string{
		"Interval":                       "7s",  // WithHealthCheckInterval
		"Timeout":                        "3s",  // WithHealthCheckTimeout（修 §7.18 落点前这行是硬编码 "5s"）
		"DeregisterCriticalServiceAfter": "42s", // WithDeregisterCriticalServiceAfter
	} {
		if got := fieldStr(t, tcp, key); got != want {
			t.Fatalf("TCP 检查的 %s=%q，want %q（对应契约字段没投递到注册体）", key, got, want)
		}
	}
	if got := fieldStr(t, ttlCheck, "CheckID"); got != "service:ut-rt-1" {
		t.Fatalf("心跳检查 CheckID=%q，want \"service:ut-rt-1\"", got)
	}
	if got := fieldStr(t, ttlCheck, "TTL"); got != "14s" {
		t.Fatalf("心跳 TTL=%q，want \"14s\"（= health_check_interval*2；interval 没投递时这里是默认的 20s）", got)
	}
	if got := fieldStr(t, ttlCheck, "DeregisterCriticalServiceAfter"); got != "42s" {
		t.Fatalf("心跳的 DeregisterCriticalServiceAfter=%q，want \"42s\"", got)
	}
	t.Log("确认：address/token/interval/timeout/deregister/enable_health_check/heartbeat 七行都落到注册请求")
}

// TestProvider_CheckFlagsDriveCheckEntries —— 两个开关各判各的。
//
// 刻意排成四行真值表而不是只测「都关」：都关时「TCP 检查消失」和「TTL 检查
// 消失」会被同一条断言兜住，删掉其中一行映射照样绿（§7.14 那条「或判据没牙齿」
// 的教训）。这里 enable 只管 TCP 条、heartbeat 只管 TTL 条，四行两两可分。
func TestProvider_CheckFlagsDriveCheckEntries(t *testing.T) {
	cases := []struct {
		name         string
		enable       bool
		heartbeat    bool
		wantTCP      bool
		wantTTLCheck bool
	}{
		{"both-off", false, false, false, false},
		{"tcp-only", true, false, true, false},
		{"ttl-only", false, true, false, true},
		{"both-on", true, true, true, true},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeAgent(t)
			// 时长字段一律给非默认值：开关判断若被写反，检查条目会带着这些值出现，
			// 而不是「没有检查」。
			reg := providerWith(t, &bootstrapv1.Registry_Consul{
				Address:                        f.hostPort,
				EnableHealthCheck:              tc.enable,
				Heartbeat:                      tc.heartbeat,
				HealthCheckInterval:            7,
				HealthCheckTimeout:             3,
				DeregisterCriticalServiceAfter: 42,
			})
			id := fmt.Sprintf("ut-flags-%d", i)
			if err := reg.Register(context.Background(), instance(id, "ut-flags")); err != nil {
				t.Fatalf("Register: %v", err)
			}
			regs := f.filter("/v1/agent/service/register")
			if len(regs) != 1 {
				t.Fatalf("注册调用应为 1 条，实得 %d 条", len(regs))
			}
			var haveTCP, haveTTL bool
			for _, c := range checksOf(t, jsonOf(t, regs[0].Body)) {
				if fieldStr(t, c, "TCP") != "" {
					haveTCP = true
				}
				if fieldStr(t, c, "TTL") != "" {
					haveTTL = true
				}
			}
			if haveTCP != tc.wantTCP {
				t.Fatalf("%s: TCP 检查存在=%v，want %v（WithHealthCheck 那行没落到 enable_health_check=%v）",
					tc.name, haveTCP, tc.wantTCP, tc.enable)
			}
			if haveTTL != tc.wantTTLCheck {
				t.Fatalf("%s: TTL 心跳检查存在=%v，want %v（WithHeartbeat 那行没落到 heartbeat=%v；默认值是 true，删行即恒有）",
					tc.name, haveTTL, tc.wantTTLCheck, tc.heartbeat)
			}
		})
	}
}

// TestProvider_AbsentIntervalsKeepDefaults —— 三个 `if > 0` 守卫本身。
//
// 契约里没写（proto3 数值缺省 0）时，映射层必须**跳过**那一行，让 SDK 侧默认
// 值生效；去掉守卫会把 0 直接投成 "0s"，consul 会拒掉这种检查定义。
func TestProvider_AbsentIntervalsKeepDefaults(t *testing.T) {
	f := newFakeAgent(t)
	reg := providerWith(t, &bootstrapv1.Registry_Consul{
		Address:           f.hostPort,
		EnableHealthCheck: true,
		Heartbeat:         true,
		// interval/timeout/deregister 全部留空（=0）
	})
	if err := reg.Register(context.Background(), instance("ut-zero-1", "ut-zero")); err != nil {
		t.Fatalf("Register: %v", err)
	}
	regs := f.filter("/v1/agent/service/register")
	if len(regs) != 1 {
		t.Fatalf("注册调用应为 1 条，实得 %d 条", len(regs))
	}
	checks := checksOf(t, jsonOf(t, regs[0].Body))
	if len(checks) != 2 {
		t.Fatalf("应有 TCP + TTL 两条检查，实得 %d", len(checks))
	}
	for key, want := range map[string]string{
		"Interval":                       "10s",
		"Timeout":                        "5s",
		"DeregisterCriticalServiceAfter": "600s",
	} {
		if got := fieldStr(t, checks[0], key); got != want {
			t.Fatalf("缺省时 TCP 检查的 %s=%q，want 默认 %q（两种成因：`if > 0` 守卫被去掉把 0 投了进去，"+
				"或 newRegistry 的默认值本身丢了）", key, got, want)
		}
	}
	if got := fieldStr(t, checks[1], "TTL"); got != "20s" {
		t.Fatalf("缺省时心跳 TTL=%q，want \"20s\"", got)
	}
	t.Log("确认：三个时长字段缺省时装配层不投递，默认值保住")
}

// TestProvider_SchemeSectionReachesTransport —— scheme 那行的正反两证。
//
// 反证用 https 打**明文** httptest：TLS 握手必失败，于是「scheme 投递到了 SDK」
// 表现为「假 agent 一条注册也没收到 + Register 报错」。两证同腿是因为
// 只留反证会被「SDK 根本不读 scheme」伪装成绿。
func TestProvider_SchemeSectionReachesTransport(t *testing.T) {
	t.Run("https-反证", func(t *testing.T) {
		f := newFakeAgent(t)
		reg := providerWith(t, &bootstrapv1.Registry_Consul{
			Address: f.hostPort,
			Scheme:  "https",
		})
		err := reg.Register(context.Background(), instance("ut-tls-1", "ut-tls"))
		if err == nil {
			t.Fatal("https 打明文 agent 应失败（失败说明 scheme 真的进了传输层）")
		}
		if n := len(f.filter("/v1/agent/service/register")); n != 0 {
			t.Fatalf("https 下假 agent 不该收到注册请求，实得 %d 条（说明 scheme 被忽略，走了明文）", n)
		}
		t.Logf("确认 https 生效（连接按预期失败）: %v", err)
	})

	t.Run("http-正证", func(t *testing.T) {
		f := newFakeAgent(t)
		reg := providerWith(t, &bootstrapv1.Registry_Consul{
			Address: f.hostPort,
			Scheme:  "http",
		})
		if err := reg.Register(context.Background(), instance("ut-plain-1", "ut-plain")); err != nil {
			t.Fatalf("显式 http 应注册成功: %v", err)
		}
		if n := len(f.filter("/v1/agent/service/register")); n != 1 {
			t.Fatalf("注册调用应为 1 条，实得 %d 条", n)
		}
	})
}

// TestProvider_AbsentBoolsReallyDisableChecks —— 「契约里没写」的运行期后果。
//
// proto3 的 bool 没有 presence，未写出即 0/false，而 newRegistry 的默认值是
// true，所以**不写这两项等于关掉**。这条不是推断：它经 bconf 的加载器
// （UnmarshalMap）走一遍，只有 address 与三个时长进了消息，两个开关全程没出现，
// 于是要么加载器补了默认、要么装配层把 0 当「不覆盖」，两者都会让检查条目出现。
// 时长刻意取非默认值（7/3/42），这样「出现了检查」一定是带着契约值的可见事件。
func TestProvider_AbsentBoolsReallyDisableChecks(t *testing.T) {
	f := newFakeAgent(t)
	msg := &bootstrapv1.Registry{}
	if err := bconf.UnmarshalMap(map[string]any{
		"type": Type,
		"consul": map[string]any{
			"address":                           f.hostPort,
			"health_check_interval":             7,
			"health_check_timeout":              3,
			"deregister_critical_service_after": 42,
		},
	}, msg); err != nil {
		t.Fatalf("UnmarshalMap: %v", err)
	}
	if msg.GetConsul().GetEnableHealthCheck() || msg.GetConsul().GetHeartbeat() {
		t.Fatalf("加载器给缺省 bool 补了默认值（enable=%v heartbeat=%v）——那会推翻 proto3 无 presence 的口径",
			msg.GetConsul().GetEnableHealthCheck(), msg.GetConsul().GetHeartbeat())
	}

	reg, cleanup, err := Provider(context.Background(), msg)
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	defer cleanup()
	if err := reg.Register(context.Background(), instance("ut-absent-1", "ut-absent")); err != nil {
		t.Fatalf("Register: %v", err)
	}
	regs := f.filter("/v1/agent/service/register")
	if len(regs) != 1 {
		t.Fatalf("注册调用应为 1 条，实得 %d 条", len(regs))
	}
	if checks := checksOf(t, jsonOf(t, regs[0].Body)); len(checks) != 0 {
		t.Fatalf("两个开关都没写 ⇒ 应零条检查，实得 %d 条：%v（proto3 缺省 bool=false 会覆盖构造默认 true，"+
			"这是契约口径而不是 bug）", len(checks), checks)
	}
	t.Log("确认：不写 enable_health_check/heartbeat 就是关掉（缺省 false 覆盖构造默认 true）")
}

// TestProvider_DatacenterSectionReachesDiscovery —— datacenter 那行只在 MULTI
// 时可判：MULTI 让发现先去 /v1/catalog/datacenters 取列表，再按每个 dc 查一次
// 健康接口，并把 dc 写回实例元数据。SINGLE 只判「不扇出」这个行为
// （那一行本身与构造默认值同值，删了测不出来——见文件头边界 1）。
func TestProvider_DatacenterSectionReachesDiscovery(t *testing.T) {
	t.Run("MULTI-扇出到每个数据中心", func(t *testing.T) {
		f := newFakeAgent(t)
		reg := providerWith(t, &bootstrapv1.Registry_Consul{
			Address:    f.hostPort,
			Datacenter: "MULTI",
		})
		disc, ok := reg.(registry.Discovery)
		if !ok {
			t.Fatalf("%T 不满足 registry.Discovery", reg)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		got, err := disc.GetService(ctx, "ut-dc")
		if err != nil {
			t.Fatalf("GetService: %v", err)
		}
		if n := len(f.filter("/v1/catalog/datacenters")); n != 1 {
			t.Fatalf("数据中心列表应取 1 次，实得 %d 次（0 次＝WithDatacenter(MULTI) 没投递，退回单中心路径）", n)
		}
		health := f.filter("/v1/health/service/")
		if len(health) != len(f.dcs) {
			t.Fatalf("健康查询应每个数据中心 1 次（%d 次），实得 %d 次", len(f.dcs), len(health))
		}
		for i, dc := range f.dcs {
			if gotDC := health[i].Query.Get("dc"); gotDC != dc {
				t.Fatalf("第 %d 次健康查询的 dc=%q，want %q", i, gotDC, dc)
			}
		}
		if len(got) != len(f.dcs) {
			t.Fatalf("发现结果应为 %d 个实例，实得 %d", len(f.dcs), len(got))
		}
		for i, in := range got {
			if in.Metadata["dc"] != f.dcs[i] {
				t.Fatalf("第 %d 个实例的元数据 dc=%q，want %q", i, in.Metadata["dc"], f.dcs[i])
			}
		}
		t.Log("确认 MULTI 装配出跨中心发现")
	})

	t.Run("SINGLE-不扇出", func(t *testing.T) {
		f := newFakeAgent(t)
		reg := providerWith(t, &bootstrapv1.Registry_Consul{
			Address:    f.hostPort,
			Datacenter: "SINGLE",
		})
		disc, ok := reg.(registry.Discovery)
		if !ok {
			t.Fatalf("%T 不满足 registry.Discovery", reg)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := disc.GetService(ctx, "ut-single"); err != nil {
			t.Fatalf("GetService: %v", err)
		}
		if n := len(f.filter("/v1/catalog/datacenters")); n != 0 {
			t.Fatalf("SINGLE 不该取数据中心列表，实得 %d 次", n)
		}
		health := f.filter("/v1/health/service/")
		if len(health) != 1 {
			t.Fatalf("SINGLE 应只查 1 次健康接口，实得 %d 次", len(health))
		}
		if dc := health[0].Query.Get("dc"); dc != "" {
			t.Fatalf("SINGLE 的健康查询不应带 dc 参数，实得 %q", dc)
		}
	})
}
