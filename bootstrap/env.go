package bootstrap

import (
	"context"
	"fmt"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/bconfig/env"
	"github.com/kalandramo/bald/bootstrap/config"
)

// EnvProvider 返回环境变量配置源的初始化器。
//
// 它知道「契约里 Config.GetEnv() 返回什么字段」与「env.New 接受什么 Option」，
// 因此 bconfig/env 包无需 import bconf，保持源层零契约依赖。
//
// 层语义：env 源是「单变量装整份文档」的整文档层（K8s ConfigMap 摘要注入
// 环境变量的惯例用法），变量值须为 yaml/json 文档；env 源无 watch 能力，
// 层为静态。env 源无格式声明字段，Store 回退按 yaml 解析（JSON 兼容）。
//
// 契约 Env.separator 字段暂无对应 provider Option（env 源只按整变量名读取），
// 该字段留给上层的 Decoder / key 归一化职责，此处忽略。
func EnvProvider() Provider {
	return func(_ context.Context, cfg *bootstrapv1.BootstrapConfig) (*config.Layer, func(), error) {
		c := cfg.GetConfig().GetEnv()
		if c == nil {
			return nil, nil, nil // 未配置 env 源，跳过
		}
		var opts []env.Option
		if p := c.GetPrefix(); p != "" {
			opts = append(opts, env.WithPrefix(p))
		}
		if k := c.GetKey(); k != "" {
			opts = append(opts, env.WithKey(k))
		}
		src, err := env.New(opts...)
		if err != nil {
			return nil, nil, fmt.Errorf("bootstrap: build env source: %w", err)
		}
		// env 无资源需释放；静态层（无 ValueWatcher，Watch=false）。
		return &config.Layer{Reader: src}, nil, nil
	}
}
