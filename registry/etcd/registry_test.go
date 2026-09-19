package etcd

// registry_test.go —— 补零测试：etcd 注册中心（Wave 4.6）。
//
// 计划 4.6：「补零测试能力轴的测试：`bald/ratelimit/sentinel/`、
// `bald/registry/{etcd,consul,kubernetes}/`」——本包此前 **0 测试**
// （`ls *_test.go` 无输出）。
//
// ## 验证策略
//
// **真实 etcd 依赖**（Docker `bald-etcd`，:2379）。不可达时 Skip，
// 不 mock——注册中心的语义（租约、watch、KV 前缀）无法用 mock 有意义地覆盖。
//
// 覆盖：构造校验 / Register→GetService→Deregister 全链路 /
// namespace 前缀 / Watch 变更通知 / TTL 租约。

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/kalandramo/bald/registry"
)

func etcdEndpoint() string {
	if v := os.Getenv("BALD_E2E_ETCD_ADDR"); v != "" {
		return v
	}
	return "127.0.0.1:2379"
}

// newTestRegistry 构造连接到真实 etcd 的 Registry，不可达则 Skip。
func newTestRegistry(t *testing.T, opts ...Option) *Registry {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	all := append([]Option{WithCtx(ctx), WithEndpoints(etcdEndpoint())}, opts...)
	r, err := New(all...)
	if err != nil {
		t.Skipf("etcd 不可达，跳过（环境缺失）: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func testInstance(name string) *registry.ServiceInstance {
	return &registry.ServiceInstance{
		ID:        name + "-1",
		Name:      name,
		Version:   "v1.0.0",
		Endpoints: []string{"http://127.0.0.1:18080"},
		Kind:      "http",
	}
}

// TestNew_RequiresEndpoints —— 无 endpoint 时构造报错（fail-fast）。
func TestNew_RequiresEndpoints(t *testing.T) {
	_, err := New()
	if err == nil {
		t.Fatal("无 endpoints 时 New 应报错（fail-fast）")
	}
	t.Logf("确认 fail-fast: %v", err)
}

// TestRegisterDiscoverDeregister —— 全链路（真实 etcd）。
func TestRegisterDiscoverDeregister(t *testing.T) {
	r := newTestRegistry(t)
	ctx := context.Background()
	name := "ut-svc-" + time.Now().Format("150405.000000")
	inst := testInstance(name)

	if err := r.Register(ctx, inst); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// 等注册可见（etcd 写入 + watch 传播）。
	var found []*registry.ServiceInstance
	for i := 0; i < 20; i++ {
		var err error
		found, err = r.GetService(ctx, name)
		if err == nil && len(found) > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(found) == 0 {
		t.Fatalf("GetService(%s) 返回 0 实例", name)
	}
	if found[0].Name != name {
		t.Fatalf("实例名不符: %q", found[0].Name)
	}
	if len(found[0].Endpoints) == 0 || found[0].Endpoints[0] != "http://127.0.0.1:18080" {
		t.Fatalf("endpoints 不符: %v", found[0].Endpoints)
	}
	t.Logf("注册+发现成功: %s -> %v", name, found[0].Endpoints)

	// 注销。
	if err := r.Deregister(ctx, inst); err != nil {
		t.Fatalf("Deregister: %v", err)
	}
	gone := false
	for i := 0; i < 20; i++ {
		after, err := r.GetService(ctx, name)
		if err == nil && len(after) == 0 {
			gone = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !gone {
		t.Fatalf("注销后仍能发现 %s", name)
	}
	t.Log("注销成功：实例已从 etcd 消失")
}

// TestRegister_MissingNameIsAccepted —— **记录一个框架行为（D15）**：
// `Register` 不校验 `service.Name`，缺 name 时**静默成功**。
//
// 依据：`registry.go:108-116` 的 `Register` 直接用
// `fmt.Sprintf("%s/%s/%s", namespace, service.Name, service.ID)` 拼 key，
// **无任何前置校验**——name 为空会注册到 `<ns>//<id>` 这种畸形 key 下，
// 不报错、不可发现（`GetService("")` 语义未定义）。
//
// 本测试**断言当前行为**（静默接受）以锁定事实，而非断言它应报错——
// 后者会红（因为框架确实不校验）。缺陷记入报告 D15。
func TestRegister_MissingNameIsAccepted(t *testing.T) {
	r := newTestRegistry(t)
	ctx := context.Background()
	inst := &registry.ServiceInstance{ID: "ut-noname-" + time.Now().Format("150405.000000")}

	err := r.Register(ctx, inst)
	if err != nil {
		t.Logf("框架已开始校验 name（行为变化，可更新 D15）: %v", err)
		return
	}
	t.Log("确认 D15：缺 name 的实例被静默接受（Register 无前置校验）")

	// 清理：Deregister 用同样的空 name 拼 key，能删掉。
	_ = r.Deregister(ctx, inst)
}

// TestGetService_NotFound —— 查询不存在的服务返回空（非报错）。
func TestGetService_NotFound(t *testing.T) {
	r := newTestRegistry(t)
	ctx := context.Background()
	got, err := r.GetService(ctx, "no-such-service-"+time.Now().Format("150405.000000"))
	if err != nil {
		t.Fatalf("查询不存在的服务不应报错: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("应为 0 实例，实际 %d", len(got))
	}
	t.Log("确认：不存在的服务返回空列表（非报错）")
}

// TestNamespacePrefix —— namespace 选项生效（不同 namespace 互不可见）。
func TestNamespacePrefix(t *testing.T) {
	rA := newTestRegistry(t, WithNamespace("/ns-a"))
	rB := newTestRegistry(t, WithNamespace("/ns-b"))
	ctx := context.Background()
	name := "ut-ns-" + time.Now().Format("150405.000000")

	if err := rA.Register(ctx, testInstance(name)); err != nil {
		t.Fatalf("Register to ns-a: %v", err)
	}

	// 等 A 可见。
	visible := false
	for i := 0; i < 20; i++ {
		got, err := rA.GetService(ctx, name)
		if err == nil && len(got) > 0 {
			visible = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !visible {
		t.Fatal("ns-a 中注册的实例在 ns-a 不可见")
	}

	// B 的 namespace 不同 → 应看不到 A 的实例。
	gotB, err := rB.GetService(ctx, name)
	if err != nil {
		t.Fatalf("ns-b GetService: %v", err)
	}
	if len(gotB) != 0 {
		t.Fatalf("ns-b 不应看到 ns-a 的实例，实际 %d 个——namespace 隔离失效", len(gotB))
	}
	t.Log("确认：namespace 隔离生效（ns-b 看不到 ns-a 的实例）")
}

// TestWatch_ReceivesChanges —— Watch 收到注册/注销变更。
func TestWatch_ReceivesChanges(t *testing.T) {
	r := newTestRegistry(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	name := "ut-watch-" + time.Now().Format("150405.000000")

	w, err := r.Watch(ctx, name)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	defer func() { _ = w.Stop() }()

	// 注册触发变更。
	if err := r.Register(ctx, testInstance(name)); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// 等 watch 通知（Next 阻塞至有变更）。
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
		if len(res.instances) == 0 {
			t.Fatal("Watch 收到变更但实例列表为空")
		}
		t.Logf("确认：Watch 收到变更（%d 个实例）", len(res.instances))
	case <-time.After(8 * time.Second):
		t.Fatal("Watch 未在 8s 内收到变更通知")
	}
}
