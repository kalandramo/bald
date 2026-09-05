package bootstrap

import (
	"context"
	"fmt"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/bconfig/http"
	"github.com/kalandramo/bald/bootstrap/config"
)

// HttpProvider 返回 HTTP 配置源的初始化器。
//
// 它知道「契约里 Config.GetHttp() 返回什么字段」与「http.New 接受什么 Option」，
// 因此 bconfig/http 包无需 import bconf，保持源层零契约依赖。
//
// 层语义：HTTP 源无推送能力，以 ETag 条件轮询模拟 watch，层默认参与热更新
// （Watch=true，轮询间隔契约 poll_interval 毫秒，缺省 30s）。层格式取契约
// format 字段；自建 client 的空闲连接归层 cleanup 释放。
func HttpProvider() Provider {
	return func(_ context.Context, cfg *bootstrapv1.BootstrapConfig) (*config.Layer, func(), error) {
		c := cfg.GetConfig().GetHttp()
		if c == nil {
			return nil, nil, nil // 未配置 http 源，跳过
		}
		var opts []http.Option
		if u := c.GetUrl(); u != "" {
			opts = append(opts, http.WithURL(u))
		}
		if m := c.GetMethod(); m != "" {
			opts = append(opts, http.WithMethod(m))
		}
		for k, v := range c.GetHeaders() {
			opts = append(opts, http.WithHeader(k, v))
		}
		if ms := c.GetPollInterval(); ms > 0 {
			opts = append(opts, http.WithPollInterval(time.Duration(ms)*time.Millisecond))
		}
		src, err := http.New(opts...)
		if err != nil {
			return nil, nil, fmt.Errorf("bootstrap: build http source: %w", err)
		}
		return &config.Layer{
			Reader: src,
			Format: c.GetFormat(),
			Watch:  true,
		}, func() { _ = src.Close() }, nil
	}
}
