// T7 冒烟探针：直接验证 nacoscontract.Provider 的注册/注销行为（go run ./cmd/probe）。
// 跳过 main.go 完整启动链（DB/Redis 等桥接约 40-75s），秒级完成契约装配路径验证：
// 注册 → 12s 心跳窗口（期间可经 Nacos 控制台/API 核对实例）→ 注销。
//
// registry 段读取与主服务同源：configs/go-bald-admin.yaml（真实配置不入库，
// 凭证留本地；模板见 configs/go-bald-admin.yaml.example）。
package main

import (
	"context"
	"fmt"
	"time"

	nacoscontract "github.com/kalandramo/bald-registry-nacos/contract"
	bconf "github.com/kalandramo/bald/bconf"
	baldconfig "github.com/kalandramo/bald/bootstrap/config"
	registry "github.com/kalandramo/bald/pkg/registry"
)

func main() {
	// 配置装载与主服务同管线：文件 → 合并树 → 契约 Unmarshal → registry 段。
	store, err := baldconfig.Load(baldconfig.Options{
		Name:       "go-bald-admin",
		ConfigFile: "configs/go-bald-admin.yaml",
	})
	if err != nil {
		fmt.Println("CONFIG_ERR:", err)
		return
	}
	bootstrap := bconf.NewBootstrap()
	if err := baldconfig.Unmarshal(store.Settings(), bootstrap); err != nil {
		fmt.Println("UNMARSHAL_ERR:", err)
		return
	}
	cfg := bootstrap.GetRegistry()
	if cfg == nil || cfg.GetNacos() == nil {
		fmt.Println("CONFIG_ERR: registry.nacos 段缺失（见 configs/go-bald-admin.yaml.example）")
		return
	}
	if cfg.GetType() != nacoscontract.Type {
		cfg.Type = nacoscontract.Type
	}

	ctx := context.Background()
	reg, cleanup, err := nacoscontract.Provider(ctx, cfg)
	if err != nil {
		fmt.Println("PROVIDER_ERR:", err)
		return
	}
	si := &registry.ServiceInstance{
		ID:      "probe-t7",
		Name:    "go-bald-admin",
		Version: "v0.1.0",
		Endpoints: []string{
			"grpc://127.0.0.1:19091",
			"http://127.0.0.1:18080",
			"http://127.0.0.1:18081",
		},
	}
	if err := reg.Register(ctx, si); err != nil {
		fmt.Println("REGISTER_ERR:", err)
		return
	}
	fmt.Println("REGISTERED", time.Now().Format("15:04:05"))

	time.Sleep(12 * time.Second)
	fmt.Println("HEARTBEAT_WINDOW_DONE", time.Now().Format("15:04:05"))
	if err := reg.Deregister(ctx, si); err != nil {
		fmt.Println("DEREGISTER_ERR:", err)
		return
	}
	fmt.Println("DEREGISTERED", time.Now().Format("15:04:05"))
	cleanup()
}
