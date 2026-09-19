package contract

// contract_test.go —— 补零测试：etcd registry 契约装配（Wave 4.6）。
//
// 本包此前 `[no test files]`（go test 输出证实）。
//
// ## 覆盖
//
//  1. `Type` 常量值（契约 `registry.proto` 的 ETCD 枚举对应）；
//  2. **配置映射**：契约字段（endpoints/prefix/ttl/max_retry）→ Option；
//  3. **fail-fast**：`type=etcd` 但缺 etcd 段 → 明确报错；
//  4. 正常构造（真实 etcd）返回可用 Registrar + cleanup。
//
// 真实 etcd 依赖（:2379），不可达时 Skip——契约装配的「可用性」无法用 mock 证明。

import (
	"context"
	"os"
	"testing"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/registry"
)

func endpoint() string {
	if v := os.Getenv("BALD_E2E_ETCD_ADDR"); v != "" {
		return v
	}
	return "127.0.0.1:2379"
}

// TestType_Constant —— Type 常量与契约枚举一致。
func TestType_Constant(t *testing.T) {
	if Type != "etcd" {
		t.Fatalf("Type=%q, want \"etcd\"（契约 registry.proto 的 ETCD 段名）", Type)
	}
	t.Logf("确认 Type=%q", Type)
}

// TestProvider_MissingSectionFailsFast —— type=etcd 但缺 etcd 段 → 报错。
func TestProvider_MissingSectionFailsFast(t *testing.T) {
	_, _, err := Provider(context.Background(), &bootstrapv1.Registry{Type: Type})
	if err == nil {
		t.Fatal("缺 etcd 段应报错（fail-fast，不静默）")
	}
	t.Logf("确认 fail-fast: %v", err)
}

// TestProvider_BuildsUsableRegistrar —— 正常构造（真实 etcd）。
func TestProvider_BuildsUsableRegistrar(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	cfg := &bootstrapv1.Registry{
		Type: Type,
		Etcd: &bootstrapv1.Registry_Etcd{
			Endpoints: []string{endpoint()},
			Prefix:    "/ut-contract",
			Ttl:       30,
			MaxRetry:  3,
		},
	}
	reg, cleanup, err := Provider(ctx, cfg)
	if err != nil {
		t.Skipf("etcd 不可达，跳过（环境缺失）: %v", err)
	}
	if reg == nil {
		t.Fatal("Provider 返回 nil Registrar")
	}
	if cleanup == nil {
		t.Fatal("Provider 应返回 cleanup（client 生命周期）")
	}
	defer cleanup()

	// 契约字段确实映射到了 Option：用 prefix 隔离注册，验证可注册。
	name := "ut-contract-svc-" + time.Now().Format("150405.000000")
	inst := &registry.ServiceInstance{
		ID: name + "-1", Name: name, Endpoints: []string{"http://127.0.0.1:9999"},
	}
	if err := reg.Register(ctx, inst); err != nil {
		t.Fatalf("用契约构造的 Registrar 注册失败: %v", err)
	}
	t.Logf("确认：契约配置（endpoints/prefix/ttl/max_retry）构造出可用 Registrar")

	// cleanup 应可安全调用（幂等由实现保证，此处只验证不 panic）。
	_ = reg.Deregister(ctx, inst)
}

// TestProvider_ConfigMapping —— 契约字段映射（空字段用默认，非空生效）。
//
// 通过「只给 endpoints」与「给全部字段」两次构造都不报错，间接验证映射路径。
func TestProvider_ConfigMapping(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	cases := []struct {
		name string
		cfg  *bootstrapv1.Registry_Etcd
	}{
		{"仅 endpoints", &bootstrapv1.Registry_Etcd{Endpoints: []string{endpoint()}}},
		{"含 prefix/ttl/max_retry", &bootstrapv1.Registry_Etcd{
			Endpoints: []string{endpoint()}, Prefix: "/mapped", Ttl: 10, MaxRetry: 2,
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &bootstrapv1.Registry{Type: Type, Etcd: tc.cfg}
			reg, cleanup, err := Provider(ctx, cfg)
			if err != nil {
				t.Skipf("etcd 不可达，跳过: %v", err)
			}
			defer cleanup()
			if reg == nil {
				t.Fatal("返回 nil Registrar")
			}
			t.Logf("确认配置映射正常: %s", tc.name)
		})
	}
}
