package consul

// registry_test.go —— consul 注册中心测试（真实 consul）。
//
// ## 验证策略
//
// **真实 consul 依赖**（本地 `docker run -p 8500:8500 hashicorp/consul:1.19`，
// 镜像默认 CMD 就是 `agent -dev -client 0.0.0.0`；CI 由 ci.yml 的 `integration`
// 作业注入）。**集成测试，-short 下跳过**（test/race 作业无 consul）。
// 不 mock——consul 的语义（passing 过滤、TTL 续约、检查按 interval 刷新）
// 无法用 mock 有意义地覆盖。
//
// ## 与 etcd 那套的差异（为什么不能照搬）
//
//  1. 发现只返回 **passing** 实例：`GetService` 底层是
//     `Health().Service(name, "", true, …)`。而健康检查是 **agent 侧发起**的 TCP
//     探测，探测跑在 consul 自己的网络命名空间里——CI 的 service 容器探不到
//     runner 上的端口，注册 `127.0.0.1:<runner 端口>` 必被判 critical，于是
//     「注册成功但发现看不见」。故除 `TestGetService_HealthCheckFiltersNonPassing`
//     外，其余用例显式关掉 TCP 检查，只留由客户端自己续约的 TTL 心跳。
//  2. 检查状态不是注册即可见：实测（consul 1.19.2）interval=10s 时新实例的检查
//     在首次执行前一直是 `critical`，即最迟要等一个 interval 才可被发现；
//     判据一律写成「有界重试内成立」。
//  3. 服务名要当 DNS 标签用，不能带点——所以实例名后缀用 UnixNano，
//     而不是 etcd 那侧的 `150405.000000`。
//
// 覆盖：Register→GetService→Deregister 全链路（含字段回读）/ Watch 变更通知 /
// 心跳按实例隔离（注销 A 不影响 B 续约）/ passingOnly 过滤 / 未知服务名的错误形状。
// 离线的 Validate 闸门判据在 register_validate_test.go（-short 下也运行）。

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/consul/api"

	registry "github.com/kalandramo/bald/registry"
)

// consulAddr 返回集成测试用的 consul 地址：BALD_E2E_CONSUL_ADDR 优先（与 etcd 的
// BALD_E2E_ETCD_ADDR 同约定），默认 127.0.0.1:8500。允许写成 http://host:port，
// WithAddress 只吃 host:port，故带 scheme 时剥掉。
func consulAddr() string {
	v := os.Getenv("BALD_E2E_CONSUL_ADDR")
	if v == "" {
		return "127.0.0.1:8500"
	}
	if u, err := url.Parse(v); err == nil && u.Host != "" {
		return u.Host
	}
	return v
}

// agentHTTPAddr 是「consul agent 自己一定探测得到」的地址：它自己的 HTTP 端口。
// 这是 passingOnly 判据能成对成立的前提——换成 runner 侧端口，CI 的 service 容器
// 网络命名空间里根本没有监听，正例永远跑不起来。
func agentHTTPAddr() string {
	return "http://" + consulAddr()
}

// probeConsul 直连 HTTP API 探一次活。
//
// 为什么需要：consul SDK 的 api.NewClient 不建连，New 成功不代表后端在；
// 后端缺失时用例一路等到超时比 Skip 更难懂。不可达即 Skip（环境缺失，非实现
// 缺陷），CI 侧由 ci.yml 的 EXPECTED 判据兜住「Skip 不算通过」。
func probeConsul(t *testing.T) {
	t.Helper()
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(agentHTTPAddr() + "/v1/status/leader")
	if err != nil {
		t.Skipf("consul 不可达，跳过（环境缺失）: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Skipf("consul 未就绪（HTTP %d），跳过", resp.StatusCode)
	}
}

func newTestRegistry(t *testing.T, opts ...Option) *Registry {
	t.Helper()
	if testing.Short() {
		t.Skip("集成测试：依赖真实 consul（:8500），-short 下跳过")
	}
	probeConsul(t)
	all := append([]Option{WithAddress(consulAddr())}, opts...)
	r, err := New(all...)
	if err != nil {
		t.Skipf("consul client 构造失败，跳过: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// rawAgent 直连同一 consul，用于绕开本模块抽象核对后端真实状态——判据要能区分
// 「发现层看不见」和「后端里根本没有」，否则空列表可以是被任何一环吞掉的假象。
func rawAgent(t *testing.T) *api.Client {
	t.Helper()
	c, err := api.NewClient(&api.Config{Address: consulAddr()})
	if err != nil {
		t.Fatalf("api.NewClient: %v", err)
	}
	return c
}

func uniqueName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

func testInstance(name string) *registry.ServiceInstance {
	return &registry.ServiceInstance{
		ID:        name + "-1",
		Name:      name,
		Version:   "v1.0.0",
		Metadata:  map[string]string{"zone": "cn-east-1a"},
		Endpoints: []string{"http://127.0.0.1:18080"},
	}
}

// waitFor 轮询 GetService 直到 cond 成立或超时，返回最后一次成功取到的列表
// （取不到/报错时保留上一次结果，由调用方收尾断言）。
func waitFor(t *testing.T, r *Registry, name string, timeout time.Duration,
	cond func([]*registry.ServiceInstance) bool) []*registry.ServiceInstance {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last []*registry.ServiceInstance
	for {
		if got, err := r.GetService(context.Background(), name); err == nil {
			last = got
			if cond(got) {
				return got
			}
		}
		if time.Now().After(deadline) {
			return last
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func hasID(ss []*registry.ServiceInstance, id string) bool {
	for _, s := range ss {
		if s.ID == id {
			return true
		}
	}
	return false
}

// TestRegisterDiscoverDeregister —— 全链路（真实 consul）。
//
// 关 TCP 检查（见文件头差异 1），心跳也关：本用例判的是「写进去 / 读得回 /
// 删得掉」，续约语义由 TestHeartbeat_… 单独判，混在一起红了分不清是谁。
func TestRegisterDiscoverDeregister(t *testing.T) {
	r := newTestRegistry(t, WithHealthCheck(false), WithHeartbeat(false))
	ctx := context.Background()
	name := uniqueName("ut-svc")
	inst := testInstance(name)

	if err := r.Register(ctx, inst); err != nil {
		t.Fatalf("Register: %v", err)
	}

	found := waitFor(t, r, name, 10*time.Second, func(ss []*registry.ServiceInstance) bool {
		return len(ss) == 1
	})
	if len(found) != 1 {
		t.Fatalf("GetService(%s) 在 10s 内没拿到 1 个实例，实际 %d 个", name, len(found))
	}
	got := found[0]
	if got.ID != inst.ID {
		t.Errorf("ID 回读不符: got %q want %q", got.ID, inst.ID)
	}
	if got.Name != name {
		t.Errorf("Name 回读不符: got %q want %q", got.Name, name)
	}
	// Version 经 tags（version=…）、Metadata 经 Meta、Endpoints 经 TaggedAddresses
	// 三条不同编码路径回来，逐条判才知映射有没有断。
	if got.Version != "v1.0.0" {
		t.Errorf("Version 回读不符（tags→resolver 路径）: got %q", got.Version)
	}
	if got.Metadata["zone"] != "cn-east-1a" {
		t.Errorf("Metadata 回读不符（Meta 路径）: got %v", got.Metadata)
	}
	if len(got.Endpoints) != 1 || got.Endpoints[0] != "http://127.0.0.1:18080" {
		t.Errorf("Endpoints 回读不符（TaggedAddresses 路径）: got %v", got.Endpoints)
	}

	if err := r.Deregister(ctx, inst); err != nil {
		t.Fatalf("Deregister: %v", err)
	}
	// 后端真相：注册项从 agent 消失。不用 GetService 判「没了」——consul 对空服务
	// 返回的是错误，网络错误也会落到同一个分支，会把「连不上」读成「已注销」。
	deadline := time.Now().Add(10 * time.Second)
	agent := rawAgent(t)
	for {
		services, err := agent.Agent().Services()
		if err != nil {
			t.Fatalf("Agent().Services: %v", err)
		}
		if _, ok := services[inst.ID]; !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("注销 10s 后该实例仍在 consul 里")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if ss := waitFor(t, r, name, 3*time.Second, func(got []*registry.ServiceInstance) bool {
		return hasID(got, inst.ID)
	}); hasID(ss, inst.ID) {
		t.Fatal("注销后 GetService 仍返回该实例")
	}
	t.Log("确认：注册→发现→字段回读→注销全链路在真实 consul 上成立")
}

// TestWatch_ReceivesChanges —— Watch 收到注册变更（真实 consul）。
//
// 只判「注册 → 通知」这一向。注销一向当前不会广播：`Registry.resolve` 的轮询
// 以 `len(tmpService) != 0 && tmpIdx != idx` 为条件（registry.go），而 consul 的
// 阻塞查询索引是整个集群的 raft index，任何无关写入都会推进它——保留空列表闸门
// 是为了不给「还没出现过实例的服务」推假的全量空集。代价是缩容（实例全掉）
// 只能靠下一次出现非空结果时才被观测到。这条口径的取舍见
// docs/architecture/risk-review.md §7.1 的「consul 真机」收口。
func TestWatch_ReceivesChanges(t *testing.T) {
	r := newTestRegistry(t, WithHealthCheck(false), WithHeartbeat(false))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	name := uniqueName("ut-watch")

	w, err := r.Watch(ctx, name)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer func() { _ = w.Stop() }()

	inst := testInstance(name)
	if err := r.Register(ctx, inst); err != nil {
		t.Fatalf("Register: %v", err)
	}

	type result struct {
		instances []*registry.ServiceInstance
		err       error
	}
	ch := make(chan result, 1)
	go func() {
		insts, werr := w.Next(ctx)
		ch <- result{insts, werr}
	}()

	select {
	case res := <-ch:
		if res.err != nil {
			t.Fatalf("Watch.Next 报错: %v", res.err)
		}
		if !hasID(res.instances, inst.ID) {
			t.Fatalf("Watch 收到变更但列表里没有刚注册的实例（%d 个）", len(res.instances))
		}
		t.Logf("确认：Watch 收到注册变更（%d 个实例）", len(res.instances))
	case <-time.After(15 * time.Second):
		t.Fatal("Watch 未在 15s 内收到变更通知")
	}
}

// TestHeartbeat_DeregisterKeepsOtherInstances —— 心跳按实例隔离（真实 consul）。
//
// 判的是 c293c67 修掉的缺陷：所有实例曾共用一条 ctx，Deregister 取消它会让其余
// 实例停止续约，并被 consul 逐出——即「注销一个实例，把同进程其他服务全打挂」。
// 这里把 interval 压到 2s（TTL 因此是 4s），注销 A 后等过 1.5 个 TTL 窗口：
//   - 缺陷版：A 的取消连带停掉 B 的续约，B 的心跳协程看到 ctx 结束并主动
//     ServiceDeregister(B) —— B 从 agent 消失，两条断言都红。
//   - 正确版：B 仍在 agent 里注册，且仍被发现（TTL 检查保持 passing）。
func TestHeartbeat_DeregisterKeepsOtherInstances(t *testing.T) {
	r := newTestRegistry(t, WithHealthCheck(false), WithHeartbeat(true), WithHealthCheckInterval(2))
	ctx := context.Background()
	name := uniqueName("ut-hb")
	instA := testInstance(name)
	instB := testInstance(name)
	instB.ID = name + "-2"

	for _, in := range []*registry.ServiceInstance{instA, instB} {
		if err := r.Register(ctx, in); err != nil {
			t.Fatalf("Register %s: %v", in.ID, err)
		}
	}
	// 两条心跳都要先完成首次 TTL 续约才可被发现（实测首次 pass 约在注册后 1s）。
	both := waitFor(t, r, name, 15*time.Second, func(ss []*registry.ServiceInstance) bool {
		return len(ss) == 2
	})
	if len(both) != 2 {
		t.Fatalf("注册两个实例后应发现 2 个，实际 %d 个（心跳/TTL 未生效？）", len(both))
	}

	if err := r.Deregister(ctx, instA); err != nil {
		t.Fatalf("Deregister A: %v", err)
	}
	time.Sleep(6 * time.Second) // > TTL(4s)，且跨过 2 个续约周期

	ss := waitFor(t, r, name, 3*time.Second, func(got []*registry.ServiceInstance) bool {
		return hasID(got, instB.ID)
	})
	if !hasID(ss, instB.ID) {
		t.Fatalf("注销 A 之后 B 停止续约/被逐出：GetService 只剩 %v —— 心跳没有按实例隔离", idsOf(ss))
	}
	if hasID(ss, instA.ID) {
		t.Errorf("注销 A 之后仍发现 A")
	}
	// 另核一层：B 是「还在注册且通过检查」，不是「恰好还没被逐出的空窗」。
	services, err := rawAgent(t).Agent().Services()
	if err != nil {
		t.Fatalf("Agent().Services: %v", err)
	}
	if _, ok := services[instB.ID]; !ok {
		t.Fatalf("B 已从 agent 消失（%v）——正是共用 ctx 缺陷的症状", idsOf(ss))
	}
	if _, ok := services[instA.ID]; ok {
		t.Errorf("A 已注销但仍在 agent 注册表里")
	}
	t.Log("确认：Deregister 只停该实例的心跳，其余实例续约不受影响")
}

// TestGetService_HealthCheckFiltersNonPassing —— passingOnly 语义成对成立。
//
// 这是 consul 后端最容易踩、也只有真机能判的一条：发现只给 passing 实例，
// 而检查由 agent 侧执行。所以两个实例都注册进同一个服务名：
//   - 健康实例的端点用 agent 自己的 HTTP 端口（它在自己网络命名空间里探得到）；
//   - 故障实例的端点用 127.0.0.1:1（一定连不上）。
//
// 判据：健康的被发现，故障的不被发现——并且用裸 API 证明故障实例**确实还注册着**
// 且检查不是 passing。否则「没被发现」可能只是根本没注册成功，判据就成了空的。
func TestGetService_HealthCheckFiltersNonPassing(t *testing.T) {
	r := newTestRegistry(t, WithHealthCheck(true), WithHeartbeat(false), WithHealthCheckInterval(2))
	ctx := context.Background()
	name := uniqueName("ut-health")

	healthy := testInstance(name)
	healthy.ID = name + "-ok"
	healthy.Endpoints = []string{agentHTTPAddr()}
	sick := testInstance(name)
	sick.ID = name + "-sick"
	sick.Endpoints = []string{"http://127.0.0.1:1"}

	if err := r.Register(ctx, healthy); err != nil {
		t.Fatalf("Register 健康实例: %v", err)
	}
	defer func() { _ = r.Deregister(ctx, healthy) }()
	if err := r.Register(ctx, sick); err != nil {
		t.Fatalf("Register 故障实例: %v", err)
	}
	defer func() { _ = r.Deregister(ctx, sick) }()

	// interval=2s，留 20s 覆盖「首次检查执行前一直是 critical」的实测空窗。
	got := waitFor(t, r, name, 20*time.Second, func(ss []*registry.ServiceInstance) bool {
		return hasID(ss, healthy.ID)
	})
	if !hasID(got, healthy.ID) {
		t.Fatalf("检查通过的实例没被发现（现返回 %v）——passing 侧判据失效", idsOf(got))
	}
	if hasID(got, sick.ID) {
		t.Fatalf("TCP 检查连不上的实例也被返回了——发现层没按 passing 过滤，CI 网络命名空间差异会让所有注册都「查得到但打不通」")
	}

	// 反证空转：两个实例都确实注册着、各自的 TCP 检查都真的挂上并跑过——
	// 否则「没被发现」可能只是根本没注册成功，判据就成了空的。
	all, _, err := rawAgent(t).Health().Service(name, "", false, nil)
	if err != nil {
		t.Fatalf("Health().Service(passing=false): %v", err)
	}
	healthyEntry := entryByID(all, healthy.ID)
	sickEntry := entryByID(all, sick.ID)
	if healthyEntry == nil || sickEntry == nil {
		t.Fatalf("注册表里缺实例（healthy=%v sick=%v，共 %d 条）——上面的发现结果判的是空气",
			healthyEntry == nil, sickEntry == nil, len(all))
	}
	healthyChecks := serviceChecks(healthyEntry, healthy.ID)
	sickChecks := serviceChecks(sickEntry, sick.ID)
	if len(healthyChecks) == 0 || len(sickChecks) == 0 {
		t.Fatalf("服务级检查没挂上去（healthy=%d sick=%d）——enableHealthCheck 失效",
			len(healthyChecks), len(sickChecks))
	}
	if !anyPassing(healthyChecks) {
		t.Fatalf("健康实例的 TCP 检查不是 passing（%s）——正例判据失效", statusesOf(healthyChecks))
	}
	if anyPassing(sickChecks) {
		t.Fatalf("故障实例的 TCP 检查居然是 passing（%s）", statusesOf(sickChecks))
	}
	t.Logf("确认：passing 实例可发现（%s）、critical 实例被过滤但仍注册着（%s）",
		statusesOf(healthyChecks), statusesOf(sickChecks))
}

// statusesOf 打印检查 ID 与状态，供失败信息与留档使用。
func statusesOf(cs []*api.HealthCheck) string {
	parts := make([]string, 0, len(cs))
	for _, c := range cs {
		parts = append(parts, c.CheckID+"="+c.Status)
	}
	return strings.Join(parts, ",")
}

func anyPassing(cs []*api.HealthCheck) bool {
	for _, c := range cs {
		if c.Status == "passing" {
			return true
		}
	}
	return false
}

func entryByID(all []*api.ServiceEntry, id string) *api.ServiceEntry {
	for _, e := range all {
		if e.Service != nil && e.Service.ID == id {
			return e
		}
	}
	return nil
}

// serviceChecks 只取属于该服务实例的检查。
//
// 必须这么过滤：健康查询结果里连节点级 serfHealth 一起返回，它恒 passing，
// 不过滤掉「有没有非 passing 检查」这条判据就会被它糊过去（实测就是这样红的）。
func serviceChecks(entry *api.ServiceEntry, id string) []*api.HealthCheck {
	var out []*api.HealthCheck
	for _, c := range entry.Checks {
		if c.ServiceID == id {
			out = append(out, c)
		}
	}
	return out
}

// TestGetService_UnknownNameReportsError —— 未知服务名返回错误而非空列表。
//
// 与 etcd 后端不同（etcd 给空列表 + nil），这里 `set == nil` 且远端为空时
// 返回 "not resolved in registry" 错误。写成用例钉住形状，是为了让调用方
// 「把错误当空处理」的写法必须显式，而不是被某次后端改动悄悄换了语义。
func TestGetService_UnknownNameReportsError(t *testing.T) {
	r := newTestRegistry(t, WithHealthCheck(false), WithHeartbeat(false))
	got, err := r.GetService(context.Background(), uniqueName("ut-none"))
	if err == nil {
		t.Fatalf("未知服务名应返回错误（consul 的形状），got %d 实例", len(got))
	}
	if len(got) != 0 {
		t.Errorf("报错时不该带实例：%v", idsOf(got))
	}
	t.Logf("确认形状: %v", err)
}

func idsOf(ss []*registry.ServiceInstance) []string {
	ids := make([]string, 0, len(ss))
	for _, s := range ss {
		ids = append(ids, s.ID)
	}
	return ids
}
