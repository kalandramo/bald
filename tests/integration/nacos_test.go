//go:build integration

// 集成测试：使用真实 nacos 配置中心验证 config 包与 kratos 后端的桥接。
//
// 本文件位于独立子模块（tests/integration），其 nacos 依赖不进入框架核心
// go.mod，从而保证「核心不拉取远程配置源 SDK」（见 P5 架构改进路线图）。
//
// 运行前置（你本地已起 nacos 时）：
//
//	export NACOS_ADDR=127.0.0.1:8848        # 可选，默认 127.0.0.1:8848
//	export NACOS_NAMESPACE=                 # 可选，默认 public（空串）
//	export NACOS_GROUP=DEFAULT_GROUP        # 可选
//	export NACOS_DATA_ID=bald-demo.yaml      # 可选；需带扩展名以便识别格式
//
// 然后进入本目录运行：
//
//	cd tests/integration && go test -tags integration -run Nacos -v
//
// 若 nacos 不可达或远程配置缺失，相关用例会以 t.Logf+return 优雅跳过。
package integration_test

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	nacosclients "github.com/nacos-group/nacos-sdk-go/clients"
	"github.com/nacos-group/nacos-sdk-go/clients/config_client"
	"github.com/nacos-group/nacos-sdk-go/common/constant"
	"github.com/nacos-group/nacos-sdk-go/vo"

	nacosconfig "github.com/go-kratos/kratos/contrib/config/nacos/v3"
	"github.com/spf13/viper"

	"github.com/kalandramo/bald/pkg/config"
)

func getEnvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func nacosEnv() (addr, namespace, group, dataID string) {
	addr = getEnvDefault("NACOS_ADDR", "127.0.0.1:8848")
	namespace = os.Getenv("NACOS_NAMESPACE") // 默认 public（空串）
	group = getEnvDefault("NACOS_GROUP", "DEFAULT_GROUP")
	dataID = getEnvDefault("NACOS_DATA_ID", "bald-demo.yaml")
	return
}

func splitHostPort(addr string) (string, uint64) {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			host := addr[:i]
			var port uint64
			for _, c := range addr[i+1:] {
				if c < '0' || c > '9' {
					return "127.0.0.1", 8848
				}
				port = port*10 + uint64(c-'0')
			}
			if port == 0 {
				return "127.0.0.1", 8848
			}
			return host, port
		}
	}
	return "127.0.0.1", 8848
}

func newNacosClient(t *testing.T) (config_client.IConfigClient, bool) {
	t.Helper()
	addr, namespace, _, _ := nacosEnv()
	host, port := splitHostPort(addr)
	client, err := nacosclients.NewConfigClient(vo.NacosClientParam{
		ClientConfig: &constant.ClientConfig{
			NamespaceId:         namespace,
			TimeoutMs:           5000,
			LogLevel:            "off",
			NotLoadCacheAtStart: true,
			CacheDir:            t.TempDir(),
			LogDir:              "",
		},
		ServerConfigs: []constant.ServerConfig{
			{IpAddr: host, Port: port, Scheme: "http", ContextPath: "/nacos"},
		},
	})
	if err != nil {
		t.Logf("cannot create nacos client (nacos reachable?): %v", err)
		return nil, false
	}
	return client, true
}

func closeNacos(client config_client.IConfigClient) {
	if c, ok := client.(interface{ Close() error }); ok {
		_ = c.Close()
	}
}

func publishRemote(t *testing.T, client config_client.IConfigClient, dataID, group, content string) bool {
	t.Helper()
	_, err := client.PublishConfig(vo.ConfigParam{
		DataId:  dataID,
		Group:   group,
		Content: content,
	})
	if err != nil {
		t.Logf("publishRemote failed (need write permission?): %v", err)
		return false
	}
	return true
}

func TestIntegration_Nacos_RemoteBaselineThenLocalOverride(t *testing.T) {
	_, _, group, dataID := nacosEnv()
	client, ok := newNacosClient(t)
	if !ok {
		t.Logf("integration skipped: nacos client unavailable (set NACOS_ADDR / start nacos to run this test)")
		return
	}
	defer closeNacos(client)

	remoteContent := "http:\n  addr: \":8080\"\n"
	if !publishRemote(t, client, dataID, group, remoteContent) {
		t.Logf("integration skipped: cannot publish to nacos (nacos not reachable or no write permission)")
		return
	}

	dir := t.TempDir()
	localPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(localPath, []byte("http:\n  addr: \":9090\""), 0o644); err != nil {
		t.Fatalf("write local: %v", err)
	}

	src := config.FromKratosSource(nacosconfig.NewConfigSource(client, nacosconfig.WithDataID(dataID), nacosconfig.WithGroup(group)))
	v, err := config.Load(config.Options{
		Name:       "bald-demo",
		ConfigFile: localPath,
		Remote:     src,
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := v.GetString("http.addr"); got != ":9090" {
		t.Fatalf("http.addr = %q, want :9090 (local overrides nacos remote)", got)
	}
}

func TestIntegration_Nacos_Watch(t *testing.T) {
	_, _, group, dataID := nacosEnv()
	client, ok := newNacosClient(t)
	if !ok {
		t.Logf("integration skipped: nacos client unavailable (set NACOS_ADDR / start nacos to run this test)")
		return
	}
	defer closeNacos(client)

	if !publishRemote(t, client, dataID, group, "http:\n  addr: \":8080\"\n") {
		t.Logf("integration skipped: nacos write permission required for watch test")
		return
	}

	dir := t.TempDir()
	localPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(localPath, []byte("http:\n  addr: \":9090\""), 0o644); err != nil {
		t.Fatalf("write local: %v", err)
	}

	var mu sync.Mutex
	changed := 0

	src := config.FromKratosSource(nacosconfig.NewConfigSource(client, nacosconfig.WithDataID(dataID), nacosconfig.WithGroup(group)))
	_, err := config.Load(config.Options{
		Name:       "bald-demo",
		ConfigFile: localPath,
		Remote:     src,
		OnChange: func(vv *viper.Viper) {
			mu.Lock()
			changed++
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if !publishRemote(t, client, dataID, group, "http:\n  addr: \":7777\"\n") {
		t.Skip("skip: nacos write permission required for watch test")
	}

	deadline := time.After(8 * time.Second)
	for {
		mu.Lock()
		c := changed
		mu.Unlock()
		if c > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("nacos remote OnChange not triggered within timeout")
		case <-time.After(50 * time.Millisecond):
		}
	}
}
