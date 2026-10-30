package bootstrap

import (
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/log/bslog"
)

// LogOptions 把契约的日志后端声明项转为 bslog.Options。
//
// 放在装配层：这里已同时依赖 bconf（契约类型）与 log/bslog（Options 形状），
// 零新增依赖。Slog 段缺失时回退 Options 默认值（stdout + info）；b 为 nil
// （阶段 A 默认）同理。
func LogOptions(b *bootstrapv1.Logger_Backend) *bslog.Options {
	c := b.GetSlog()
	if c == nil {
		// type=slog 但未携带 Slog 段：使用默认配置，保证日志开箱即用。
		return bslog.NewOptions()
	}
	o := bslog.NewOptions()
	if c.GetLevel() != "" {
		o.Level = c.GetLevel()
	}
	if c.GetFormat() != "" {
		o.Format = c.GetFormat()
	}
	// 多输出优先：output_paths 非空优先生效，为空回退单值 output_path，
	// 两者都空保留默认 stdout（与契约校验的回退序一致）。
	if ps := c.GetOutputPaths(); len(ps) > 0 {
		o.OutputPaths = ps
	} else if p := c.GetOutputPath(); p != "" {
		o.OutputPaths = []string{p}
	}
	// 轮转：零值字段回退 bslog 默认（100MB / 7 份 / 30 天 / gzip）——
	// 契约 0 = 默认的语义在此兑现，用户只写关心的字段即可。
	if r := c.GetRotate(); r != nil {
		if r.GetEnabled() {
			o.Rotate.Enabled = true
		}
		if v := int(r.GetMaxSize()); v > 0 {
			o.Rotate.MaxSize = v
		}
		if v := int(r.GetMaxBackups()); v > 0 {
			o.Rotate.MaxBackups = v
		}
		if v := int(r.GetMaxAge()); v > 0 {
			o.Rotate.MaxAge = v
		}
		if r.GetCompress() {
			o.Rotate.Compress = true
		}
	}
	return o
}
