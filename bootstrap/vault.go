package bootstrap

import (
	"context"
	"fmt"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/bconfig/vault"
	"github.com/kalandramo/bald/bootstrap/config"
)

// VaultProvider 返回 HashiCorp Vault 配置源的初始化器。
//
// 它知道「契约里 Config.GetVault() 返回什么字段」与「vault.New 接受什么
// Option」，因此 bconfig/vault 包无需 import bconf，保持源层零契约依赖。
//
// 层语义：Vault 无原生推送，以轮询模拟 watch（契约 poll_interval 毫秒，
// 缺省 30s），层默认参与热更新（Watch=true）；client 自建模式（契约
// address/token，缺省回退 VAULT_ADDR/VAULT_TOKEN 环境变量），api.Client
// 无显式关闭接口，cleanup 为 nil。
func VaultProvider() Provider {
	return func(_ context.Context, cfg *bootstrapv1.BootstrapConfig) (*config.Layer, func(), error) {
		c := cfg.GetConfig().GetVault()
		if c == nil {
			return nil, nil, nil // 未配置 vault 源，跳过
		}
		var opts []vault.Option
		if a := c.GetAddress(); a != "" {
			opts = append(opts, vault.WithAddress(a))
		}
		if t := c.GetToken(); t != "" {
			opts = append(opts, vault.WithToken(t))
		}
		if p := c.GetPath(); p != "" {
			opts = append(opts, vault.WithPath(p))
		}
		if k := c.GetDataKey(); k != "" {
			opts = append(opts, vault.WithDataKey(k))
		}
		if ms := c.GetPollInterval(); ms > 0 {
			opts = append(opts, vault.WithPollInterval(time.Duration(ms)*time.Millisecond))
		}
		src, err := vault.New(opts...)
		if err != nil {
			return nil, nil, fmt.Errorf("bootstrap: build vault source: %w", err)
		}
		return &config.Layer{
			Reader: src,
			Watch:  true,
		}, nil, nil
	}
}
