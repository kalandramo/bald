package contract

// 真 nacos 腿：ephemeral 实例的两条摘除路径各判一条（风险登记册 §4.1 / §7.10）。
//
// 为什么必须拆开判——下面是 nacos v2.3.2 单机上的实测，不是推断：
//   - 显式 Deregister（服务名拼对）：调用返回 3.6ms，实例 0.56s 归零。
//   - 不注销、只关 naming client：实例 0.5s 归零——服务端随连接断开摘除 ephemeral 实例。
//   - 既不注销也不关：每 5s 一次心跳续租，75s 后实例仍在。
//   - 服务名拼错的 Deregister：SDK 回 ok=true、err=nil，一个实例都没少（删除口对
//     「实例本来就不存在」幂等）。
//
// 于是两条推论决定了本文件的形状：
//   1. 「停机后实例归零」是前两条路径的**或**，任何一条单独坏掉它都照样绿，所以它
//      只能当终态守卫（示例的装配用例里留着），不能当注销的证据。
//   2. 看 Deregister 的返回值也判不出来（第四条），§4.1 那条「反注册失败不再被吞掉」
//      在这条路径上无从判红。
//
// 所以两条用例各锁一条机制：本文件第一条**不关 client**（只剩注销一条路）、第二条
// **只关 client 不注销**。读侧一律绕开 SDK 走 v1 HTTP 查询口——用 SDK 读 SDK 写只能
// 证明 SDK 自洽，证明不了那份值真的落到后端。
//
// 跑法：BALD_E2E_NACOS_ADDR（默认 127.0.0.1:8848）。`-short` 与后端不可达都跳过。
// CI 的 integration 作业注入 nacos/nacos-server:v2.3.2（MODE=standalone、
// NACOS_AUTH_ENABLE=false，并且**必须同时映射 9848**：v2 SDK 的 naming 走 gRPC，
// 只映射 8848 时实例注册不上），先 TCP 探活、再按名字点名 `--- PASS`，Skip 不算通过。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	registry "github.com/kalandramo/bald/registry"
)

// nacosE2EAddr 取后端地址；环境缺失（-short 或连不上）时 Skip。
func nacosE2EAddr(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("集成测试：依赖真实 nacos，-short 下跳过")
	}
	addr := os.Getenv("BALD_E2E_NACOS_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8848"
	}
	// 探活用 TCP 而不是查服务：查询口对「服务不存在」也回错误，用它探活会把环境
	// 缺失和后端故障混成同一句话。
	probe, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		t.Skipf("nacos %s 不可达，跳过（环境缺失）: %v", addr, err)
	}
	_ = probe.Close()
	return addr
}

// newE2ERegistrar 走契约构造（与装配同一条路径），cleanup 交回调用方自己拿着——
// 挂在 t.Cleanup 里就失去了「只在某一步之后才关」这个判据形状。
func newE2ERegistrar(t *testing.T, addr string) (registry.Registrar, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	reg, closeReg, err := Provider(ctx, &bootstrapv1.Registry{
		Type:  Type,
		Nacos: &bootstrapv1.Registry_Nacos{ServerAddrs: []string{addr}},
	})
	if err != nil {
		t.Skipf("nacos 注册中心构造失败（环境缺失，非验证失败）: %v", err)
	}
	return reg, closeReg
}

// e2ELiveAddr 只借一个真实端口：nacos 不校验后端是否真在服务，但注册的实例要一个
// 形如 host:port 的地址，:0 动态端口比写死端口稳（同机并发用例不打架）。
func e2ELiveAddr(t *testing.T) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := lis.Addr().String()
	_ = lis.Close()
	return addr
}

func e2EUniqueName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// nacosE2EHost 是 nacos v1 instance/list 回包 hosts[] 里的一个实例。
//
// 字段表按实测回包写：hosts 项只有 healthy / enabled / ephemeral，`valid` 在**回包
// 顶层**而不是每项上——把它放进本结构会得到一个恒假的零值，判据于是恒红。
// weight / clusterName 在 hosts 项上实测存在（§7.22 的映射判据读它们），
// groupName **不在**项上——它在查询参数里，回包项读不回来。
type nacosE2EHost struct {
	IP          string  `json:"ip"`
	Port        uint64  `json:"port"`
	Weight      float64 `json:"weight"`
	ClusterName string  `json:"clusterName"`
	Healthy     bool    `json:"healthy"`
	Enabled     bool    `json:"enabled"`
	Ephemeral   bool    `json:"ephemeral"`
}

// nacosE2EQuery 是 v1 instance/list 的三个租户维度。零值即「public + DEFAULT_GROUP
// + 不限集群」，也就是两条机制用例原本的写法；§7.22 的映射判据要按契约段给的那组
// 维度读，所以把它做成参数而不是各写一遍查询口。
type nacosE2EQuery struct {
	namespace string
	group     string
	clusters  string
}

func (q nacosE2EQuery) withDefaults() nacosE2EQuery {
	if q.namespace == "" {
		q.namespace = "public"
	}
	if q.group == "" {
		q.group = "DEFAULT_GROUP"
	}
	return q
}

// listNacosE2EHosts 走 nacos 的 HTTP 查询口，独立于装配用的 SDK。
func listNacosE2EHosts(t *testing.T, addr, service string, query nacosE2EQuery) []nacosE2EHost {
	t.Helper()
	q := url.Values{
		"serviceName": {service},
		"namespaceId": {query.namespace},
		"groupName":   {query.group},
		"healthyOnly": {"false"},
	}
	if query.clusters != "" {
		q.Set("clusters", query.clusters)
	}
	u := "http://" + addr + "/nacos/v1/ns/instance/list?" + q.Encode()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		t.Fatalf("new request %s: %v", u, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("query nacos %s: %v", service, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("query nacos %s: %s", service, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatalf("read nacos %s body: %v", service, err)
	}
	var out struct {
		Hosts []nacosE2EHost `json:"hosts"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode nacos %s 回包: %v（原文 %s）", service, err, body)
	}
	return out.Hosts
}

// waitForNacosE2EHosts 轮询到 want 个实例为止（want=0 即「归零」）。两个方向共用一
// 个轮询口：注册可见与注销归零是同一个时间竞态的两面，各写一遍必漏一份容忍。
func waitForNacosE2EHosts(t *testing.T, addr, service string, query nacosE2EQuery, want int, timeout time.Duration) []nacosE2EHost {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		hosts := listNacosE2EHosts(t, addr, service, query.withDefaults())
		if (want == 0 && len(hosts) == 0) || (want > 0 && len(hosts) >= want) {
			return hosts
		}
		if time.Now().After(deadline) {
			return hosts
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// TestNacosDeregisterRemovesInstanceWithClientOpen 钉住注销本身：naming client
// **保持打开**时 Deregister 必须真的把后端里那两个服务名下的实例摘掉。
//
// client 不关是这条用例的立身前提——那样摘除只剩注销一条路径，「实例归零」才唯一地
// 归因到 Deregister（含它的服务名拼法）。窗口 10s：实测正确注销→归零 0.56s，
// 泄漏（client 开着又不注销）→75s 仍在，两端差一个数量级以上。
//
// 一个 ServiceInstance 的两个 endpoint 在 nacos 上是**两个服务名**
// （`<服务名>.<scheme>`，口径见示例的 TestNacosDiscoveryNameCarriesSchemeSuffix），
// 所以两个名字都得等出来、也都得摘掉——只对一个名字发注销是这条判据要拦的退化之一。
func TestNacosDeregisterRemovesInstanceWithClientOpen(t *testing.T) {
	addr := nacosE2EAddr(t)
	reg, closeReg := newE2ERegistrar(t, addr)
	// 兜底关闭（断言失败时也别留一个一直在续租的 client 给后面的用例）。
	t.Cleanup(closeReg)

	live := e2ELiveAddr(t)
	service := e2EUniqueName("bald-nacos-dereg")
	inst := &registry.ServiceInstance{
		ID:        service + "-1",
		Name:      service,
		Version:   "v1",
		Endpoints: []string{"http://" + live, "grpc://" + live},
	}
	if err := reg.Register(context.Background(), inst); err != nil {
		t.Fatalf("register %s: %v", service, err)
	}

	names := []string{service + ".http", service + ".grpc"}
	for _, name := range names {
		hosts := waitForNacosE2EHosts(t, addr, name, nacosE2EQuery{}, 1, 20*time.Second)
		if len(hosts) == 0 {
			t.Fatalf("注册后 20s 内 %s 在后端始终不可见（注册没到位，注销判据无从谈起）", name)
		}
		for _, h := range hosts {
			// healthy=心跳到位、enabled=没被手工摘掉、ephemeral=服务端会自己过期。
			// 第三个尤其要紧：非 ephemeral 的实例不会因为关连接而消失，那时本用例
			// 与下一条都会把「生命周期类型变了」误读成「摘除路径坏了」。
			if !h.Healthy || !h.Enabled || !h.Ephemeral {
				t.Fatalf("%s 的实例状态不对（注册参数变了）: %+v", name, h)
			}
		}
	}

	if err := reg.Deregister(context.Background(), inst); err != nil {
		t.Fatalf("deregister %s: %v", service, err)
	}
	for _, name := range names {
		hosts := waitForNacosE2EHosts(t, addr, name, nacosE2EQuery{}, 0, 10*time.Second)
		if len(hosts) != 0 {
			t.Fatalf("Deregister 后 10s（client 仍打开）%s 仍有 %d 个实例——注销没发到那个服务名上，"+
				"命名口径（<服务名>.<scheme>）或 Deregister 的调用本身变了: %+v", name, len(hosts), hosts)
		}
	}
}

// TestNacosContractCleanupClosesClient 钉住契约 Provider 交回的那个 cleanup：调它
// 必须真的关掉 naming client，实例随后从后端消失。
//
// 与上一条的分工：这条**只**调 cleanup、不调 Deregister，于是摘除只可能来自连接释放
// （实测 0.5s 内归零；client 还开着则心跳把实例续到 75s 之后仍在）。两条合起来才把
// 「停机时到底发生了什么」判全：Deregister 发得对 + client 关得掉。
//
// cleanup 交给 t.Cleanup 而体内不调，就把这条判据变成上一条的复述了——所以这里显式
// 拿着那个闭包，用一个 closed 标志避免兜底再关一次。
func TestNacosContractCleanupClosesClient(t *testing.T) {
	addr := nacosE2EAddr(t)
	reg, closeReg := newE2ERegistrar(t, addr)

	closed := false
	t.Cleanup(func() {
		if !closed {
			closeReg()
		}
	})

	service := e2EUniqueName("bald-nacos-cleanup")
	inst := &registry.ServiceInstance{
		ID:        service + "-1",
		Name:      service,
		Version:   "v1",
		Endpoints: []string{"grpc://" + e2ELiveAddr(t)},
	}
	if err := reg.Register(context.Background(), inst); err != nil {
		t.Fatalf("register %s: %v", service, err)
	}
	name := service + ".grpc"
	if len(waitForNacosE2EHosts(t, addr, name, nacosE2EQuery{}, 1, 20*time.Second)) == 0 {
		t.Fatalf("注册后 20s 内 %s 在后端始终不可见，本用例的摘除判据无从谈起", name)
	}

	closeReg()
	closed = true
	if hosts := waitForNacosE2EHosts(t, addr, name, nacosE2EQuery{}, 0, 10*time.Second); len(hosts) != 0 {
		t.Fatalf("Provider 的 cleanup 调了但 %s 仍有 %d 个实例（client 没被关掉，Deregister 也没发生）: %+v",
			name, len(hosts), hosts)
	}
}
