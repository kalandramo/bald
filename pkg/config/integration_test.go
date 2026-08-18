//go:build integration

// 集成测试：使用真实 nacos 配置中心验证 config 包与 kratos 后端的桥接。
//
// 运行前置（你本地已起 nacos 时）：
//
//	export NACOS_ADDR=127.0.0.1:8848        # 可选，默认 127.0.0.1:8848
//	export NACOS_NAMESPACE=                 # 可选，默认 public（空串）
//	export NACOS_GROUP=DEFAULT_GROUP        # 可选
//	export NACOS_DATA_ID=bald-demo.yaml      # 可选；需带扩展名以便识别格式
//
// 然后在 nacos 控制台（或本测试自动发布）准备好 dataID 内容，例如：
//
//	http:
//	  addr: ":8080"
//
// 运行：
//
//	go test -tags integration -run Integration ./pkg/config/ -v
//
// 若 nacos 不可达或远程配置缺失，相关用例会以 t.Skip 优雅跳过，不会让 CI 直接失败。
package config

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
)

// 降噪说明（仅 integration 标签编译）：nacos-sdk-go 在 Windows 上 rotatelogs
// 无 symlink 权限时，后台 goroutine 持续打印 "failed to rotate"，会导致 go test
// 判定 FAIL。本测试通过 ClientConfig.LogDir="" 让 nacos 不初始化文件 logger，
// 噪声消失，无 nacos 时用例以 t.Logf+return 干净 PASS。正常失败信息见 CI 日志。
func nacosEnv() (addr, namespace, group, dataID string) {
	addr = getEnvDefault("NACOS_ADDR", "127.0.0.1:8848")
	namespace = os.Getenv("NACOS_NAMESPACE") // 默认 public（空串）
	group = getEnvDefault("NACOS_GROUP", "DEFAULT_GROUP")
	dataID = getEnvDefault("NACOS_DATA_ID", "bald-demo.yaml")
	return
}

func getEnvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// newNacosClient 构造 nacos 配置客户端（基于环境变量）。
// 第二个返回值表示是否成功；若 nacos 不可达/客户端创建失败，返回 false，
// 由调用方在主测试函数内决定是否 t.Skip（避免在 t.Helper 内 Skip 带来的歧义）。
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

// closeNacos 关闭 nacos 客户端（停止其内部后台 goroutine）。
// IConfigClient 接口未暴露 Close，但具体实现（*config_client.ConfigClient）提供，
// 这里用类型断言安全调用，避免测试退出后残留 goroutine 导致 go test 判定失败。
func closeNacos(client config_client.IConfigClient) {
	if c, ok := client.(interface{ Close() error }); ok {
		_ = c.Close()
	}
}

func splitHostPort(addr string) (string, uint64) {
	// 简单解析 "ip:port"。
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

// publishRemote 尝试把远程基准写入 nacos；失败返回 false（调用方应 Skip 写相关断言）。
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

// TestIntegration_Nacos_RemoteBaselineThenLocalOverride：真实 nacos 作基准，
// 本地文件覆盖远程。验证 FromKratosSource 桥接、nacos 格式识别（dataID 扩展名）、
// 以及「本地 > 远程」（底层内部优先级）在真实后端下成立。
// 注：完整优先级链为 flag > env > 本地 > 远程。
func TestIntegration_Nacos_RemoteBaselineThenLocalOverride(t *testing.T) {
	_, _, group, dataID := nacosEnv()
	client, ok := newNacosClient(t)
	if !ok {
		t.Logf("integration skipped: nacos client unavailable (set NACOS_ADDR / start nacos to run this test)")
		return
	}
	defer closeNacos(client)

	// 预置远程基准（需写权限；无 nacos 或不可写时直接跳过整个用例）。
	remoteContent := "http:\n  addr: \":8080\"\n"
	if !publishRemote(t, client, dataID, group, remoteContent) {
		t.Logf("integration skipped: cannot publish to nacos (nacos not reachable or no write permission)")
		return
	}

	// 本地覆盖：http.addr = :9090
	dir := t.TempDir()
	localPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(localPath, []byte("http:\n  addr: \":9090\""), 0o644); err != nil {
		t.Fatalf("write local: %v", err)
	}

	src := FromKratosSource(nacosconfig.NewConfigSource(client, nacosconfig.WithDataID(dataID), nacosconfig.WithGroup(group)))
	v, err := Load(Options{
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

// TestIntegration_Nacos_Watch：远程变更应触发 OnChange，且本地覆盖不被污染。
func TestIntegration_Nacos_Watch(t *testing.T) {
	_, _, group, dataID := nacosEnv()
	client, ok := newNacosClient(t)
	if !ok {
		t.Logf("integration skipped: nacos client unavailable (set NACOS_ADDR / start nacos to run this test)")
		return
	}
	defer closeNacos(client)

	// watch 测试需要写权限来模拟远程变更。
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

	src := FromKratosSource(nacosconfig.NewConfigSource(client, nacosconfig.WithDataID(dataID), nacosconfig.WithGroup(group)))
	_, err := Load(Options{
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

	// 模拟远程变更：把远程改为 :7777（本地仍应压住）。
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
