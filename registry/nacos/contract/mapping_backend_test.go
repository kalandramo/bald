package contract

// mapping_backend_test.go —— 契约 registry.nacos 九行映射里**只有真后端能读回**的五行
// （风险登记册 §7.22）。
//
// 观测面是 v1 HTTP 查询口（与 instance_lifecycle_e2e_test.go 同一个口，参数已做成
// namespace/group/clusters 三维），写侧走契约构造。理由与那条文件一样：用 SDK 读
// SDK 写只能证明 SDK 自洽，证明不了那份值真的落到后端。
//
// 五行各自的判据与「去掉这一行会怎样」：
//   - group：契约组里看得见 + DEFAULT_GROUP 里看不见。去掉 ⇒ 落在 DEFAULT_GROUP，
//     第一条读 0 个，红。
//   - cluster_name：回包项的 clusterName 等于契约值 + clusters=DEFAULT 读 0 个。
//   - weight：回包项的 weight 等于契约值。去掉 ⇒ 默认 100，红。
//   - weight 的 `>0` 守卫：契约给 0 时后端读到的是 100 而不是 0（实测绕过守卫直递
//     0 时后端就读回 0，所以这条判的是守卫本身）。
//   - namespace：契约命名空间里看得见 + public 里看不见。实测**未预建**的命名空间
//     也能注册成功（回 nil），所以这条不需要先建租户；「看得见」这一步本身就在判
//     那个 ID 真的进了 ClientConfig。
//   - default_kind：写侧绕开 SDK，用 v1 HTTP 注册一个**不带 metadata** 的持久实例
//     （SDK 注册必然带 kind，那样就永远走不到回落分支），读侧看 GetService 拼出的
//     endpoint scheme。
//
// prefix 行不在这儿也不在离线文件里：`options.prefix` 在全仓没有任何读者，注册/发现
// 两条路径都不用它——契约字段是死的，没有可判的运行期面。登记在 §7.22，不动代码。
//
// 跑法与 CI 归属同 instance_lifecycle_e2e_test.go：`-short` 与后端不可达都跳过；
// CI 的 integration 作业注入 nacos 服务容器并按名字点名 `--- PASS`。它们因此**不进**
// acceptance 判据表（表在 test 作业跑，那里没有 nacos），名字必须出现在 ci.yml 里，
// docscheck 的 TestAcceptanceTableCoversShortSkippedTests 认这条路径。

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	registry "github.com/kalandramo/bald/registry"
)

// newBackendRegistrar 走契约构造，段内容由用例给（判据对象就是这段到 client 的映射）。
// 与 newE2ERegistrar 的分工：那条只投地址（判的是注销/cleanup 机制），本条要能整段
// 定制，所以不复用它。
func newBackendRegistrar(t *testing.T, addr string, sec *bootstrapv1.Registry_Nacos) (registry.Registrar, func()) {
	t.Helper()
	sec.ServerAddrs = []string{addr}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	reg, closeReg, err := Provider(ctx, &bootstrapv1.Registry{Type: Type, Nacos: sec})
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	return reg, closeReg
}

// backendRegister 注册一个单 grpc 端点的实例并把注销挂进 cleanup。
// 服务名唯一到纳秒：容器在 CI 里是共享的，重名会让「某处读 0 个」这类反证被上一轮的
// 残留污染。
func backendRegister(t *testing.T, reg registry.Registrar, prefix string) (*registry.ServiceInstance, string) {
	t.Helper()
	service := e2EUniqueName(prefix)
	inst := &registry.ServiceInstance{
		ID:        service + "-1",
		Name:      service,
		Version:   "v1",
		Endpoints: []string{"grpc://" + e2ELiveAddr(t)},
	}
	if err := reg.Register(context.Background(), inst); err != nil {
		t.Fatalf("register %s: %v", service, err)
	}
	t.Cleanup(func() { _ = reg.Deregister(context.Background(), inst) })
	// nacos 上的服务名带协议后缀（<服务名>.<scheme>），查询口认的是这个名字。
	return inst, service + ".grpc"
}

// TestMappingBackend_GroupLandsInRegistration 判 registry.nacos.group：实例必须出现在
// 契约组下，且**不**出现在 DEFAULT_GROUP 下。
//
// 回包项里没有 groupName 字段（实测为无），所以这一行只能靠「按组读」这一对正反判。
func TestMappingBackend_GroupLandsInRegistration(t *testing.T) {
	addr := nacosE2EAddr(t)
	const group = "BALD_MAPPING_GROUP"
	reg, closeReg := newBackendRegistrar(t, addr, &bootstrapv1.Registry_Nacos{Group: group})
	defer closeReg()

	_, name := backendRegister(t, reg, "bald-nacos-map-group")

	hosts := waitForNacosE2EHosts(t, addr, name, nacosE2EQuery{group: group}, 1, 20*time.Second)
	if len(hosts) == 0 {
		t.Fatalf("契约 group=%q 下 20s 看不到实例——group 行没落到 RegisterInstanceParam.GroupName，"+
			"或者 nacos 的自建组行为变了", group)
	}
	if def := listNacosE2EHosts(t, addr, name, nacosE2EQuery{}.withDefaults()); len(def) != 0 {
		t.Fatalf("默认组下也看见了 %d 个实例：%+v——group 没投递（实例落回 DEFAULT_GROUP）", len(def), def)
	}
}

// TestMappingBackend_ClusterNameLandsInRegistration 判 cluster_name：回包项的
// clusterName 等于契约值，且 clusters=DEFAULT 读不到。
func TestMappingBackend_ClusterNameLandsInRegistration(t *testing.T) {
	addr := nacosE2EAddr(t)
	const cluster = "bald-mapping-cluster"
	reg, closeReg := newBackendRegistrar(t, addr, &bootstrapv1.Registry_Nacos{ClusterName: cluster})
	defer closeReg()

	_, name := backendRegister(t, reg, "bald-nacos-map-cluster")

	hosts := waitForNacosE2EHosts(t, addr, name, nacosE2EQuery{clusters: cluster}, 1, 20*time.Second)
	if len(hosts) == 0 {
		t.Fatalf("clusters=%q 下 20s 看不到实例——cluster_name 行没落到 RegisterInstanceParam.ClusterName", cluster)
	}
	if got := hosts[0].ClusterName; got != cluster {
		t.Fatalf("回包 clusterName=%q, want %q", got, cluster)
	}
	if def := listNacosE2EHosts(t, addr, name, nacosE2EQuery{clusters: "DEFAULT"}); len(def) != 0 {
		t.Fatalf("默认集群下也看见了 %d 个实例：%+v——cluster_name 没投递", len(def), def)
	}
}

// TestMappingBackend_WeightLandsInRegistration 判 weight：契约值原样成为后端权重。
// 42 是刻意避开默认值 100 的：默认构造也是「一个数」，取一个不可能撞上的值才让
// 「这行没投出去」推得开。
func TestMappingBackend_WeightLandsInRegistration(t *testing.T) {
	addr := nacosE2EAddr(t)
	const weight = 42
	reg, closeReg := newBackendRegistrar(t, addr, &bootstrapv1.Registry_Nacos{Weight: weight})
	defer closeReg()

	_, name := backendRegister(t, reg, "bald-nacos-map-weight")

	hosts := waitForNacosE2EHosts(t, addr, name, nacosE2EQuery{}, 1, 20*time.Second)
	if len(hosts) == 0 {
		t.Fatal("注册后 20s 在后端看不见实例，weight 判据无从谈起")
	}
	if got := hosts[0].Weight; got != weight {
		t.Fatalf("后端 weight=%v, want %v（契约 weight 行没落到 RegisterInstanceParam.Weight；"+
			"读到 100 即默认值，说明整行没投）", got, weight)
	}
}

// TestMappingBackend_ZeroWeightFallsBackToDefault 判 weight 那行的 `>0` 守卫：
// 契约写 0 时后端必须是默认的 100。
//
// 这条不是凑数的：实测把守卫去掉、直接 nacos.New(WithWeight(0)) 注册时，后端读回的
// 就是 0——权重 0 在 nacos 的负载均衡里等于「不该被选中」，也就是说去掉守卫会把一个
// 合法的「没配权重」变成一个会让服务查不到的配置。守卫值得一条判据。
func TestMappingBackend_ZeroWeightFallsBackToDefault(t *testing.T) {
	addr := nacosE2EAddr(t)
	reg, closeReg := newBackendRegistrar(t, addr, &bootstrapv1.Registry_Nacos{Weight: 0})
	defer closeReg()

	_, name := backendRegister(t, reg, "bald-nacos-map-weight0")

	hosts := waitForNacosE2EHosts(t, addr, name, nacosE2EQuery{}, 1, 20*time.Second)
	if len(hosts) == 0 {
		t.Fatal("契约 Weight=0 时注册后看不见实例（守卫被去掉的话 0 权重会被后端拒/看不见）")
	}
	if got := hosts[0].Weight; got != 100 {
		t.Fatalf("契约 Weight=0 却读到后端 weight=%v, want 默认 100——"+
			"`if w := c.GetWeight(); w > 0` 这道守卫没了，0 被原样投给 SDK", got)
	}
}

// TestMappingBackend_NamespaceIsolatesInstance 判 namespace：实例只在契约命名空间下
// 可见，public 读不到。
//
// 实测两点决定了这条的形状：①**未预建**的命名空间也能注册成功（SDK 与服务端都不报
// 错），所以判据不必先建租户——也因此「在 ns 下看得见」这一步就是「那个 ID 进了
// ClientConfig」的证据；②换成 public 读回 0 个，隔离成立，去掉映射那行时实例会落在
// public，第一条判据即红。
func TestMappingBackend_NamespaceIsolatesInstance(t *testing.T) {
	addr := nacosE2EAddr(t)
	ns := e2EUniqueName("bald-mapping-ns")
	reg, closeReg := newBackendRegistrar(t, addr, &bootstrapv1.Registry_Nacos{Namespace: ns})
	defer closeReg()

	_, name := backendRegister(t, reg, "bald-nacos-map-ns")

	hosts := waitForNacosE2EHosts(t, addr, name, nacosE2EQuery{namespace: ns}, 1, 20*time.Second)
	if len(hosts) == 0 {
		t.Fatalf("契约 namespace=%q 下 20s 看不到实例——namespace 行没落到 ClientConfig.NamespaceId", ns)
	}
	if pub := listNacosE2EHosts(t, addr, name, nacosE2EQuery{}.withDefaults()); len(pub) != 0 {
		t.Fatalf("public 下也看见了 %d 个实例：%+v——namespace 没投递（落回默认租户）", len(pub), pub)
	}
}

// TestMappingBackend_DefaultKindLandsInEndpointScheme 判 default_kind：它不参与注册，
// 只在**读侧**决定 endpoint 的 scheme——实例 metadata 没有 kind 时回落到 opts.kind。
//
// 所以写侧必须绕开 SDK：Registry.Register 总会写 metadata["kind"]（取 endpoint 的
// scheme），那条实例永远走不到回落分支，default_kind 改成什么读口都不会变，判据恒真。
// 这里用 v1 HTTP 注册一个不带 metadata 的持久实例（ephemeral=false + healthy=true，
// 免得没有心跳被 SelectInstances(HealthyOnly=true) 过滤掉），再用契约构造出来的
// Discovery 读它。
func TestMappingBackend_DefaultKindLandsInEndpointScheme(t *testing.T) {
	addr := nacosE2EAddr(t)
	const kind = "bald-mapping-kind"

	service := e2EUniqueName("bald-nacos-map-kind")
	ipPort := e2ELiveAddr(t)
	rawRegisterPersistentInstance(t, addr, service, ipPort)

	reg, closeReg := newBackendRegistrar(t, addr, &bootstrapv1.Registry_Nacos{DefaultKind: kind})
	defer closeReg()
	d, ok := reg.(registry.Discovery)
	if !ok {
		t.Fatalf("契约产物 %T 不是 Discovery，读口判不了", reg)
	}

	deadline := time.Now().Add(20 * time.Second)
	var svcs []*registry.ServiceInstance
	var err error
	for {
		svcs, err = d.GetService(context.Background(), service)
		if len(svcs) > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("GetService(%s): %v", service, err)
	}
	if len(svcs) != 1 {
		t.Fatalf("GetService 命中 %d 个实例，want 1（写侧那个持久实例没被读到？）", len(svcs))
	}
	if ep := svcs[0].Endpoints[0]; !strings.HasPrefix(ep, kind+"://") {
		t.Fatalf("endpoint=%q, want scheme %q——default_kind 行没落到 options.kind"+
			"（读到 grpc:// 即默认值，说明整行没投）", ep, kind)
	}

	// 反证：换一个 default_kind，读口的 scheme 跟着换。常量对常量的判据在没有这条时
	// 只能证明「不是默认值」，证明不了「跟着契约变」。
	reg2, closeReg2 := newBackendRegistrar(t, addr, &bootstrapv1.Registry_Nacos{DefaultKind: kind + "-other"})
	defer closeReg2()
	d2, ok := reg2.(registry.Discovery)
	if !ok {
		t.Fatalf("反证构造的产物 %T 不是 Discovery", reg2)
	}
	svcs2, err := d2.GetService(context.Background(), service)
	if err != nil || len(svcs2) != 1 {
		t.Fatalf("反证读口 GetService: %v（命中 %d 个）", err, len(svcs2))
	}
	if ep := svcs2[0].Endpoints[0]; !strings.HasPrefix(ep, kind+"-other://") {
		t.Fatalf("反证 endpoint=%q, want scheme %q", ep, kind+"-other")
	}
}

// rawRegisterPersistentInstance 用 v1 HTTP 写侧口注册一个**无 metadata** 的持久实例，
// 并把删除挂进 cleanup。
//
// 为什么不用 SDK：见上面那条用例的注释——SDK 必然写 metadata["kind"]。
func rawRegisterPersistentInstance(t *testing.T, addr, service, ipPort string) {
	t.Helper()
	host, port, err := net.SplitHostPort(ipPort)
	if err != nil {
		t.Fatalf("split %s: %v", ipPort, err)
	}
	form := url.Values{
		"serviceName": {service},
		"namespaceId": {"public"},
		"groupName":   {"DEFAULT_GROUP"},
		"clusters":    {"DEFAULT"},
		"ip":          {host},
		"port":        {port},
		"ephemeral":   {"false"},
		"enabled":     {"true"},
		"healthy":     {"true"},
		"weight":      {"1"},
	}
	doRaw(t, http.MethodPost, "http://"+addr+"/nacos/v1/ns/instance", form)
	t.Cleanup(func() {
		doRaw(t, http.MethodDelete, "http://"+addr+"/nacos/v1/ns/instance?"+form.Encode(), nil)
	})
}

func doRaw(t *testing.T, method, u string, form url.Values) {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(context.Background(), method, u, body)
	if err != nil {
		t.Fatalf("new request %s: %v", method, err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, u, err)
	}
	defer resp.Body.Close()
	text, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s %s: %s %s", method, u, resp.Status, text)
	}
}
