package contract

// appkit_e2e_test.go —— 声明式装配的端到端取用证据（一进程、真配置文本、真装配）。
//
// 前面几层判据各自成立，不等于串起来成立：这段跑的是
//
//	configs/*.yaml（契约文本）→ appkit 加载 + StrictContract 运行期契约门
//	  → 计划（server.rabbitmq 段命中 AppProvider）→ Build（含 fail-fast）
//
// rabbitmq 的离线边界（见 mapping_offline_test.go 文件头）：Connect 是完整 AMQP
// 握手，没有假后端可信任，所以「Serve 到就绪」这一腿在离线必红在 Connect——那红的
// 是环境而不是装配，不能拿来当证据，也不能反过来证明装配断了。因此本层判的是链路
// 里**不依赖后端**的两段：契约门真在跑（段名写错即失败）+ provider 真进了计划并且
// Build 的 fail-fast 一路穿透 Serve（配了段却缺 addrs）。
//
// 「Serve 到端点就绪」的真集群腿归 CI 服务集成（与 §7.24 确立的口径一致），不在
// 这里用假握手冒充。

// 入口必须是 appkit.Serve——assemble（Provider 计划与 server 段构建）只挂在
// Serve 上；直接 NewApp+Run 会跳过整个段驱动装配，provider 不进计划、服务器不
// 构造，且一声不响。同 transport/{tcp,graphql,kafka}/contract 的 e2e 起手式。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"

	"github.com/kalandramo/bald/pkg/appkit"
)

// isolateFlags 把 pflag.CommandLine 换成空的进程内 FlagSet——go test 的
// -test.* flag 会落进全局 FlagSet，Serve 的 --config/--plan 容错扫描与之竞争。
func isolateFlags(t *testing.T) {
	t.Helper()
	old := pflag.CommandLine
	pflag.CommandLine = pflag.NewFlagSet("rabbitmq-contract-e2e", pflag.ContinueOnError)
	t.Cleanup(func() { pflag.CommandLine = old })
}

// writeContractFile 落一份契约 yaml 并返回其路径。
func writeContractFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bootstrap.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// TestAppProviderEndToEnd_ContractFailFastReachesServe 契约文本 → 计划 → Build
// 的 fail-fast 真的把启动掀掉：配了 server.rabbitmq 段却缺 addrs，Serve 必须带着
// 那句归因返回。这条同时证明 provider 进了计划（没进就不会有这句错）。
func TestAppProviderEndToEnd_ContractFailFastReachesServe(t *testing.T) {
	isolateFlags(t)
	cfgPath := writeContractFile(t, ""+
		"app:\n"+
		"    name: rabbitmq-contract-e2e\n"+
		"    version: v0.0.0\n"+
		"server:\n"+
		"    rabbitmq:\n"+
		"        codec: json\n")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	err := appkit.Serve(ctx,
		appkit.WithRole("rabbitmq-e2e"),
		appkit.ConfigFile(cfgPath),
		appkit.StrictContract(),
		appkit.Providers(AppProvider()),
	)
	if err == nil {
		t.Fatal("Serve 成功，缺 addrs 的 rabbitmq 段没被 fail-fast 拦住（provider 没进计划？）")
	}
	if !strings.Contains(err.Error(), "server.rabbitmq.addrs is required") {
		t.Fatalf("err = %v, want attribution to server.rabbitmq.addrs", err)
	}
}

// TestAppProviderEndToEnd_UnknownContractKeyFailsServe 反证：契约门真的在跑——
// 段名写错（server.rbmq）时启动必须失败，而不是静默少起一台服务器。
func TestAppProviderEndToEnd_UnknownContractKeyFailsServe(t *testing.T) {
	isolateFlags(t)
	cfgPath := writeContractFile(t, ""+
		"app:\n"+
		"    name: rabbitmq-contract-e2e\n"+
		"    version: v0.0.0\n"+
		"server:\n"+
		"    rbmq:\n"+
		"        addrs:\n"+
		"            - 127.0.0.1:5672\n")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	err := appkit.Serve(ctx,
		appkit.WithRole("rabbitmq-e2e"),
		appkit.ConfigFile(cfgPath),
		appkit.StrictContract(),
		appkit.Providers(AppProvider()),
	)
	if err == nil {
		t.Fatal("Serve 成功，未知契约键没被契约门挡住（StrictContract 腿失效）")
	}
	if !strings.Contains(err.Error(), "rbmq") {
		t.Fatalf("err = %v, want attribution to the typo'd key rbmq", err)
	}
}
