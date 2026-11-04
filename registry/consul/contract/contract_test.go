package contract

// contract_test.go —— consul registry 契约装配测试。
//
// ## 覆盖
//
//  1. `Type` 常量值（契约 `registry.proto` 的 CONSUL 枚举对应）；
//  2. **fail-fast**：`type=consul` 但缺 consul 段 → 明确报错；
//  3. 契约字段（address/scheme/token/datacenter/interval 等）走通映射路径；
//  4. 正常构造（真实 consul）返回可用 Registrar，并真的注册/发现/注销得动。
//
// `consul.New` 只建 SDK client（不触网），故 1–3 离线可测、-short 下也运行；
// 第 4 条要真后端（本地 :8500，CI 由 ci.yml 的 `integration` 作业注入并断言
// 判据真的 PASS），-short 下跳过、不可达同样 Skip。
//
// 与 registry/consul 根包用例同一口径：这里显式关掉 TCP 健康检查——检查由
// agent 侧发起，CI 的 service 容器探不到 runner 上的端口（见 registry_test.go 文件头）。

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	registry "github.com/kalandramo/bald/registry"
)

func consulAddr() string {
	v := os.Getenv("BALD_E2E_CONSUL_ADDR")
	if v == "" {
		return "127.0.0.1:8500"
	}
	return v
}

// probeConsul 探一次活：SDK 建 client 不触网，Provider 成功不代表后端在。
func probeConsul(t *testing.T) {
	t.Helper()
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + consulAddr() + "/v1/status/leader")
	if err != nil {
		t.Skipf("consul 不可达，跳过（环境缺失）: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Skipf("consul 未就绪（HTTP %d），跳过", resp.StatusCode)
	}
}

// TestType_Constant —— Type 常量与契约段名一致。
func TestType_Constant(t *testing.T) {
	if Type != "consul" {
		t.Fatalf("Type=%q, want \"consul\"（契约 registry.proto 的 CONSUL 段名）", Type)
	}
	t.Logf("确认 Type=%q", Type)
}

// TestProvider_MissingSectionFailsFast —— type=consul 但缺 consul 段 → 报错。
func TestProvider_MissingSectionFailsFast(t *testing.T) {
	_, _, err := Provider(context.Background(), &bootstrapv1.Registry{Type: Type})
	if err == nil {
		t.Fatal("缺 consul 段应报错（fail-fast，不静默退回默认配置）")
	}
	t.Logf("确认 fail-fast: %v", err)
}

// TestProvider_FieldMappingRuns —— 契约字段全部非空时映射路径走通。
//
// 离线判据：SDK 建 client 不触网，所以这条只判「各字段都被接住、构造不报错」，
// 字段是否真的生效由 TestProvider_BuildsUsableRegistrar 用真后端判 address 一项，
// 其余项（scheme/token/datacenter/interval）属 SDK 侧行为，不在本仓承诺范围。
func TestProvider_FieldMappingRuns(t *testing.T) {
	cfg := &bootstrapv1.Registry{
		Type: Type,
		Consul: &bootstrapv1.Registry_Consul{
			Address:                        consulAddr(),
			Scheme:                         "http",
			Token:                          "dummy-token",
			Datacenter:                     "MULTI",
			EnableHealthCheck:              false,
			Heartbeat:                      false,
			HealthCheckInterval:            5,
			HealthCheckTimeout:             10,
			DeregisterCriticalServiceAfter: 60,
		},
	}
	reg, cleanup, err := Provider(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	if reg == nil {
		t.Fatal("Provider 返回 nil Registrar")
	}
	if cleanup == nil {
		t.Fatal("Provider 应返回 cleanup（client 生命周期）")
	}
	cleanup()
	cleanup() // Close 幂等：停机 Effect 与测试 cleanup 双路径都可能重复调用
}

// TestProvider_BuildsUsableRegistrar —— 真 consul 上注册/发现/注销都动得了。
//
// 集成测试：-short 下跳过（consul 由 ci.yml 的 `integration` 作业注入）。
// 这里刻意把 enable_health_check 留空（=false）：TCP 检查在 agent 的网络
// 命名空间里执行，runner 上的端点探不到，会被判 critical 而从发现结果里消失
// ——那与契约装配无关，健康检查语义在根包用例里判。
func TestProvider_BuildsUsableRegistrar(t *testing.T) {
	if testing.Short() {
		t.Skip("集成测试：依赖真实 consul（:8500），-short 下跳过")
	}
	probeConsul(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg := &bootstrapv1.Registry{
		Type: Type,
		Consul: &bootstrapv1.Registry_Consul{
			Address: consulAddr(),
		},
	}
	reg, cleanup, err := Provider(ctx, cfg)
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	if cleanup == nil {
		t.Fatal("Provider 应返回 cleanup（client 生命周期）")
	}
	defer func() { cleanup() }()

	// 契约面只承诺 Registrar，发现能力是同一实现带上的（出站网关要用它）。
	disc, ok := reg.(registry.Discovery)
	if !ok {
		t.Fatalf("契约产物 %T 不满足 registry.Discovery——装配出来的后端只能注册不能发现", reg)
	}

	name := "ut-contract-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	inst := &registry.ServiceInstance{
		ID:        name + "-1",
		Name:      name,
		Version:   "v1.0.0",
		Endpoints: []string{"http://127.0.0.1:19090"},
	}
	if err := reg.Register(ctx, inst); err != nil {
		t.Fatalf("Register: %v", err)
	}

	var got []*registry.ServiceInstance
	for i := 0; i < 100; i++ {
		got, err = disc.GetService(ctx, name)
		if err == nil && len(got) == 1 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(got) != 1 || got[0].ID != inst.ID {
		t.Fatalf("契约装配出的 Registrar 注册后查不到：GetService 返回 %d 个实例（err=%v）", len(got), err)
	}

	if err := reg.Deregister(ctx, inst); err != nil {
		t.Fatalf("Deregister: %v", err)
	}
	t.Log("确认：契约 registry.consul 段装配出可用 Registrar（注册→发现→注销）")
}

// TestAppProvider_* 系列见 provider_test.go（段路径、Option 嵌入面、
// 段缺失的 bug 级报错）。
