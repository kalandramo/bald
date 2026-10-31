package appkit

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件钉住 B6 两遍合并的配置命名空间语义：
//
//   - Serve 路径：命名空间由**契约 app.name 派生**——第一遍装载文件/远程层
//     解析 app.name，第二遍以它为前缀挂 env 层。env 前缀 NAME_* 不变是红线
//     （现有用户的 NAME_ 前缀 env 必须原样生效）；
//   - NewApp 路径：命名空间回退 Name（其值来自调用方源码，代码层稳定源）；
//   - 契约无 app.name → fail-fast 且错误可行动（指明 app.name 必填）。
//
// 历史：曾要求显式声明（ConfigNamespace Option）以避开 env 前缀自指；两遍
// 合并落地后该 Option 已删除，本文件的两条 Serve 测试即其替代的回归锁。

// TestServe_AppNameRequired 契约无 app.name 时入口 fail-fast，错误指明必填。
func TestServe_AppNameRequired(t *testing.T) {
	// 工作目录（appkit 包）下无 bald-app.yaml / --config：预装载找不到任何
	// 配置源 → app.name 缺失 → fail-fast。
	err := Serve(context.Background(), WithHTTP(new(http.ServeMux)))
	if err == nil {
		t.Fatal("Serve without app.name succeeded; want fail-fast error")
	}
	if !strings.Contains(err.Error(), "app.name is required") {
		t.Fatalf("error message must state app.name is required, got: %v", err)
	}
}

// TestServe_NamespaceDerivedFromContractAndEnvWorks 核心回归锁：命名空间由
// 契约 app.name 派生，NAME_ 前缀 env 覆盖文件值（env 名不变红线）。
//
// 文件写 app.name: ns-app 与 server.http.addr ":1xxxx"（占位）；env 以
// NS_APP_ 前缀覆盖 server.http.addr 为测试专属空闲端口——服务必须绑定 env
// 值。该断言端到端贯穿：预装载派生命名空间 → Run 期 loadConfig 以同一命名
// 空间重建 env 层 → flag/env/文件四源合并。
func TestServe_NamespaceDerivedFromContractAndEnvWorks(t *testing.T) {
	// 占随机端口（关闭后复用，竞态窗口可忽略）。
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	envAddr := lis.Addr().String()
	_ = lis.Close()

	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "ns-app.yaml")
	yml := "app:\n  name: \"ns-app\"\nserver:\n  http: { addr: \":18443\" }\n"
	if err := os.WriteFile(yamlPath, []byte(yml), 0o644); err != nil {
		t.Fatal(err)
	}
	// env 名不变红线：前缀派生自 app.name（ns-app → NS_APP_）。
	t.Setenv("NS_APP_SERVER_HTTP_ADDR", envAddr)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- Serve(ctx, StandardServerProvider(), ConfigFile(yamlPath), WithHTTP(new(http.ServeMux)))
	}()
	defer func() {
		cancel()
		select {
		case err := <-errCh:
			if err != nil {
				t.Errorf("Serve: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("app did not stop within timeout")
		}
	}()

	// 就绪判据：env 覆盖的端口可连（文件值 :18443 不得被绑定）。
	deadline := time.Now().Add(5 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		c, derr := net.DialTimeout("tcp", envAddr, 200*time.Millisecond)
		if derr == nil {
			_ = c.Close()
			ready = true
			break
		}
		select {
		case runErr := <-errCh:
			t.Fatalf("Serve failed before ready: %v", runErr)
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ready {
		t.Fatalf("env-overridden addr %s not bound within timeout (env prefix derivation broken?)", envAddr)
	}
}

// TestNewApp_NamespaceFallsBackToName New 路径命名空间回退 Name（代码层
// 稳定源，无自指）；缺省非空（config.Load 要求 Name 非空）。
func TestNewApp_NamespaceFallsBackToName(t *testing.T) {
	a := NewApp(Name("code-layer-ns"))
	if a.cfgNamespace != "code-layer-ns" {
		t.Fatalf("cfgNamespace = %q, want fallback to Name value", a.cfgNamespace)
	}
	if b := NewApp(); b.cfgNamespace == "" {
		t.Fatal("cfgNamespace must never be empty (config.Load requires non-empty Name)")
	}
}
