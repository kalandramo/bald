package bootstrap

import (
	"context"
	"fmt"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/bconfig/file"
	"github.com/kalandramo/bald/bootstrap/config"
)

// FileProvider 返回文件配置源的初始化器。
//
// 它知道「契约里 Config.GetFile() 返回什么字段」与「file.New 接受什么 Option」，
// 因此 bconfig/file 包无需 import bconf，保持源层零契约依赖。
//
// 层格式：契约 File.format 优先；为空时按路径扩展名推断（config.FormatOf），
// 再为空则由 Store 回退按 yaml 解析。契约 watch=true 时层订阅热更新
// （file 源实现 bconfig.ValueWatcher，父目录 watch 防编辑器原子重命名丢事件）。
func FileProvider() Provider {
	return func(ctx context.Context, cfg *bootstrapv1.BootstrapConfig) (*config.Layer, func(), error) {
		c := cfg.GetConfig().GetFile()
		if c == nil {
			return nil, nil, nil // 未配置 file 源，跳过
		}
		var opts []file.Option
		if ctx != nil {
			opts = append(opts, file.WithContext(ctx))
		}
		if p := c.GetPath(); p != "" {
			opts = append(opts, file.WithPath(p))
		}
		if c.GetWatch() {
			opts = append(opts, file.WithWatch(true))
		}
		src, err := file.New(opts...)
		if err != nil {
			return nil, nil, fmt.Errorf("bootstrap: build file source: %w", err)
		}
		format := c.GetFormat()
		if format == "" {
			format = config.FormatOf(c.GetPath())
		}
		return &config.Layer{
			Reader: src,
			Format: format,
			Watch:  c.GetWatch(),
		}, func() { _ = src.Close() }, nil
	}
}
