package bootstrap

import (
	"context"
	stdjson "encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/bconfig"
)

// TestEtcdProvider_Unconfigured 未配置跳过。
func TestEtcdProvider_Unconfigured(t *testing.T) {
	l, closer, err := EtcdProvider()(context.Background(), cfgWith(&bootstrapv1.Config{}))
	if err != nil || l != nil || closer != nil {
		t.Fatalf("EtcdProvider(unconfigured) = (layer=%v, closer!=nil: %t, err=%v), want all nil", l, closer != nil, err)
	}
}

// TestEtcdProvider_ParameterFail 缺 endpoints 报错。
func TestEtcdProvider_ParameterFail(t *testing.T) {
	cfg := cfgWith(&bootstrapv1.Config{Etcd: &bootstrapv1.Config_Etcd{Path: "cfg"}})
	if _, _, err := EtcdProvider()(context.Background(), cfg); err == nil {
		t.Fatal("EtcdProvider(no endpoints) = nil error, want error")
	}
}

// TestEtcdProvider_ContractMapping 契约字段 → options 映射（构造期不建连），
// 层参与热更新（reader 实现 ValueWatcher）。
func TestEtcdProvider_ContractMapping(t *testing.T) {
	cfg := cfgWith(&bootstrapv1.Config{
		Etcd: &bootstrapv1.Config_Etcd{
			Endpoints: []string{"e1:2379", "e2:2379"},
			Path:      "cfg/app.yaml",
			Username:  "u",
			Password:  "p",
			Prefix:    true,
		},
	})
	l, closer, err := EtcdProvider()(context.Background(), cfg)
	if err != nil {
		t.Fatalf("EtcdProvider() = %v", err)
	}
	defer closer()

	if _, ok := l.Reader.(bconfig.ValueWatcher); !ok {
		t.Fatal("etcd reader does not implement bconfig.ValueWatcher")
	}
	if !l.Watch {
		t.Fatal("layer Watch = false, want true (etcd native watch)")
	}
}

// TestConsulProvider_Unconfigured 未配置跳过。
func TestConsulProvider_Unconfigured(t *testing.T) {
	l, closer, err := ConsulProvider()(context.Background(), cfgWith(&bootstrapv1.Config{}))
	if err != nil || l != nil || closer != nil {
		t.Fatalf("ConsulProvider(unconfigured) = (layer=%v, closer!=nil: %t, err=%v), want all nil", l, closer != nil, err)
	}
}

// TestConsulProvider_Build 自建构造成功（懒连接）+ Watch 语义。
func TestConsulProvider_Build(t *testing.T) {
	cfg := cfgWith(&bootstrapv1.Config{
		Consul: &bootstrapv1.Config_Consul{
			Address: "127.0.0.1:1",
			Path:    "cfg/app.yaml",
			Token:   "tok",
			Scheme:  "http",
		},
	})
	l, _, err := ConsulProvider()(context.Background(), cfg)
	if err != nil {
		t.Fatalf("ConsulProvider() = %v", err)
	}
	if !l.Watch {
		t.Fatal("layer Watch = false, want true (consul watch plan)")
	}
}

// TestApolloProvider_Unconfigured 未配置跳过。
func TestApolloProvider_Unconfigured(t *testing.T) {
	l, closer, err := ApolloProvider()(context.Background(), cfgWith(&bootstrapv1.Config{}))
	if err != nil || l != nil || closer != nil {
		t.Fatalf("ApolloProvider(unconfigured) = (layer=%v, closer!=nil: %t, err=%v), want all nil", l, closer != nil, err)
	}
}

// TestApolloProvider_ParameterFail 缺 appid 报错（不触网）。
func TestApolloProvider_ParameterFail(t *testing.T) {
	cfg := cfgWith(&bootstrapv1.Config{
		Apollo: &bootstrapv1.Config_Apollo{Endpoint: "http://apollo:8080", Namespace: "app"},
	})
	if _, _, err := ApolloProvider()(context.Background(), cfg); err == nil {
		t.Fatal("ApolloProvider(no appid) = nil error, want error")
	}
}

// TestHttpProvider_Unconfigured 未配置跳过。
func TestHttpProvider_Unconfigured(t *testing.T) {
	l, closer, err := HttpProvider()(context.Background(), cfgWith(&bootstrapv1.Config{}))
	if err != nil || l != nil || closer != nil {
		t.Fatalf("HttpProvider(unconfigured) = (layer=%v, closer!=nil: %t, err=%v), want all nil", l, closer != nil, err)
	}
}

// TestHttpProvider_EndToEnd httptest 端到端：字段映射 + 真实拉取 + cleanup。
func TestHttpProvider_EndToEnd(t *testing.T) {
	var gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Token")
		_, _ = w.Write([]byte("log:\n  level: debug"))
	}))
	defer srv.Close()

	cfg := cfgWith(&bootstrapv1.Config{
		Http: &bootstrapv1.Config_Http{
			Url:          srv.URL,
			Headers:      map[string]string{"X-Token": "t1"},
			Format:       "yaml",
			PollInterval: 500,
		},
	})
	l, closer, err := HttpProvider()(context.Background(), cfg)
	if err != nil {
		t.Fatalf("HttpProvider() = %v", err)
	}
	defer closer()

	if l.Format != "yaml" {
		t.Errorf("layer Format = %q, want yaml", l.Format)
	}
	if !l.Watch {
		t.Fatal("layer Watch = false, want true (etag polling)")
	}

	data, err := l.Reader.Load(context.Background(), "")
	if err != nil || string(data) != "log:\n  level: debug" {
		t.Fatalf("Load() = (%q, %v)", data, err)
	}
	if gotHeader != "t1" {
		t.Errorf("X-Token = %q", gotHeader)
	}
}

// TestNacosProvider_Unconfigured 未配置跳过。
func TestNacosProvider_Unconfigured(t *testing.T) {
	l, closer, err := NacosProvider()(context.Background(), cfgWith(&bootstrapv1.Config{}))
	if err != nil || l != nil || closer != nil {
		t.Fatalf("NacosProvider(unconfigured) = (layer=%v, closer!=nil: %t, err=%v), want all nil", l, closer != nil, err)
	}
}

// TestNacosProvider_ParameterFail 缺 server_addrs 报错（惰性建连，构造不触网）。
func TestNacosProvider_ParameterFail(t *testing.T) {
	cfg := cfgWith(&bootstrapv1.Config{
		Nacos: &bootstrapv1.Config_Nacos{DataId: "bootstrap.yaml", Group: "g"},
	})
	if _, _, err := NacosProvider()(context.Background(), cfg); err == nil {
		t.Fatal("NacosProvider(no server addrs) = nil error, want error")
	}
}

// TestNacosProvider_ContractMapping 契约字段 → options 映射 + Format 透传。
func TestNacosProvider_ContractMapping(t *testing.T) {
	cfg := cfgWith(&bootstrapv1.Config{
		Nacos: &bootstrapv1.Config_Nacos{
			ServerAddrs: []string{"nacos:8848"},
			Namespace:   "ns1",
			Group:       "g1",
			DataId:      "app.yaml",
			Format:      "yaml",
		},
	})
	l, _, err := NacosProvider()(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NacosProvider() = %v", err)
	}
	if l.Format != "yaml" {
		t.Errorf("layer Format = %q, want yaml", l.Format)
	}
	if _, ok := l.Reader.(bconfig.ValueWatcher); !ok {
		t.Fatal("nacos reader does not implement bconfig.ValueWatcher")
	}
	if !l.Watch {
		t.Fatal("layer Watch = false, want true (nacos ListenConfig)")
	}
}

// TestKubernetesProvider_Unconfigured 未配置跳过。
func TestKubernetesProvider_Unconfigured(t *testing.T) {
	l, closer, err := KubernetesProvider()(context.Background(), cfgWith(&bootstrapv1.Config{}))
	if err != nil || l != nil || closer != nil {
		t.Fatalf("KubernetesProvider(unconfigured) = (layer=%v, closer!=nil: %t, err=%v), want all nil", l, closer != nil, err)
	}
}

// TestKubernetesProvider_ContractMapping 契约字段映射（惰性建连，构造不触网）。
func TestKubernetesProvider_ContractMapping(t *testing.T) {
	cfg := cfgWith(&bootstrapv1.Config{
		Kubernetes: &bootstrapv1.Config_Kubernetes{
			Namespace:     "ns1",
			ConfigMapName: "app-cm",
			Key:           "app.yaml",
		},
	})
	l, _, err := KubernetesProvider()(context.Background(), cfg)
	if err != nil {
		t.Fatalf("KubernetesProvider() = %v", err)
	}
	if _, ok := l.Reader.(bconfig.ValueWatcher); !ok {
		t.Fatal("kubernetes reader does not implement bconfig.ValueWatcher")
	}
	if !l.Watch {
		t.Fatal("layer Watch = false, want true (configmap watch)")
	}
}

// TestVaultProvider_Unconfigured 未配置跳过。
func TestVaultProvider_Unconfigured(t *testing.T) {
	l, closer, err := VaultProvider()(context.Background(), cfgWith(&bootstrapv1.Config{}))
	if err != nil || l != nil || closer != nil {
		t.Fatalf("VaultProvider(unconfigured) = (layer=%v, closer!=nil: %t, err=%v), want all nil", l, closer != nil, err)
	}
}

// TestVaultProvider_ParameterFail 缺 path 报错。
func TestVaultProvider_ParameterFail(t *testing.T) {
	cfg := cfgWith(&bootstrapv1.Config{
		Vault: &bootstrapv1.Config_Vault{Address: "http://127.0.0.1:1"},
	})
	if _, _, err := VaultProvider()(context.Background(), cfg); err == nil {
		t.Fatal("VaultProvider(no path) = nil error, want error")
	}
}

// TestVaultProvider_EndToEnd httptest 端到端：契约 → provider → Load 全链路。
func TestVaultProvider_EndToEnd(t *testing.T) {
	var content atomic.Pointer[[]byte]
	v := []byte("log:\n  level: debug")
	content.Store(&v)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		b, _ := stdjson.Marshal(string(*content.Load()))
		_, _ = w.Write([]byte(`{"data":{"data":{"content":` + string(b) + `},"metadata":{}}}`))
	}))
	defer srv.Close()

	cfg := cfgWith(&bootstrapv1.Config{
		Vault: &bootstrapv1.Config_Vault{
			Address:      srv.URL,
			Token:        "root",
			Path:         "secret/data/app",
			DataKey:      "content",
			PollInterval: 500,
		},
	})
	l, _, err := VaultProvider()(context.Background(), cfg)
	if err != nil {
		t.Fatalf("VaultProvider() = %v", err)
	}

	data, err := l.Reader.Load(context.Background(), "")
	if err != nil || string(data) != "log:\n  level: debug" {
		t.Fatalf("Load() = (%q, %v)", data, err)
	}
	if !l.Watch {
		t.Fatal("layer Watch = false, want true (polling)")
	}
}
