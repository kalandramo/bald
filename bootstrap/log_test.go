package bootstrap

import (
	"reflect"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// TestLogOptions_MultiOutputAndRotate 契约多输出 + 轮转段 → Options 映射：
// output_paths 优先于 output_path；rotate 零值字段回退 bslog 默认（100/7/30/gzip）。
func TestLogOptions_MultiOutputAndRotate(t *testing.T) {
	o := LogOptions(&bootstrapv1.Logger_Backend{
		Type: "slog",
		Slog: &bootstrapv1.Logger_Slog{
			OutputPath:  "/should/be/ignored.log",
			OutputPaths: []string{"stdout", "/var/log/app.log"},
			Rotate: &bootstrapv1.Logger_Slog_Rotate{
				Enabled:  true,
				MaxSize:  50,
				Compress: true,
				// MaxBackups/MaxAge 零值 → 回退默认 7/30。
			},
		},
	})
	if want := []string{"stdout", "/var/log/app.log"}; !reflect.DeepEqual(o.OutputPaths, want) {
		t.Fatalf("OutputPaths = %v, want %v（output_paths 应优先于单值）", o.OutputPaths, want)
	}
	if !o.Rotate.Enabled || o.Rotate.MaxSize != 50 || !o.Rotate.Compress {
		t.Fatalf("rotate 显式字段映射: %+v", o.Rotate)
	}
	if o.Rotate.MaxBackups != 7 || o.Rotate.MaxAge != 30 {
		t.Fatalf("rotate 零值应回退默认（7 份/30 天）, got %+v", o.Rotate)
	}
}

// TestLogOptions_OutputPathBackwardCompat 单值 output_path 仍映射为单元素，
// 既有配置零迁移。
func TestLogOptions_OutputPathBackwardCompat(t *testing.T) {
	o := LogOptions(&bootstrapv1.Logger_Backend{
		Type: "slog",
		Slog: &bootstrapv1.Logger_Slog{OutputPath: "stderr"},
	})
	if !reflect.DeepEqual(o.OutputPaths, []string{"stderr"}) {
		t.Fatalf("OutputPaths = %v, want [stderr]", o.OutputPaths)
	}
}

// TestLogOptions_NoOutputTargetFallsBackToStdout 输出目标整体缺省时回退
// 默认 ["stdout"]——契约（log.proto：「两者都空时装配层回退默认 stdout」）
// 与校验层（bconf.Validate 空串放行）共同依赖的兑现点。
//
// 这是 bald-admin 生产配置的实际形态：slog 段只写 level/format。
func TestLogOptions_NoOutputTargetFallsBackToStdout(t *testing.T) {
	o := LogOptions(&bootstrapv1.Logger_Backend{
		Type: "slog",
		Slog: &bootstrapv1.Logger_Slog{Level: "info", Format: "json"},
	})
	if !reflect.DeepEqual(o.OutputPaths, []string{"stdout"}) {
		t.Fatalf("OutputPaths = %v, want [stdout]（回退默认，不得为空）", o.OutputPaths)
	}
}
