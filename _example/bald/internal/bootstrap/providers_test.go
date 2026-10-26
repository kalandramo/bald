// providers_test.go 钉住 loggerProvider 的掩码行为（Attr 层脱敏演示的行为
// 回归）：password/token 两类键以 slog.Attr 形态流经日志时
// 必须被掩码为 ***，且 service.name 固定属性随行——契约 filter_keys 不处理
// Attr 形态，bslog.WithFilter 是唯一覆盖该来源的机制（bald-admin 实测结论）。
package bootstrap

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/log"
	"github.com/kalandramo/bald/pkg/appkit"
)

func TestLoggerProviderMasksAttrAndTagsServiceName(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "app.log")
	cfg := &bootstrapv1.BootstrapConfig{Logger: &bootstrapv1.Logger{
		Backends: []*bootstrapv1.Logger_Backend{{
			Type: "slog",
			Slog: &bootstrapv1.Logger_Slog{Level: "info", Format: "json", OutputPath: logPath},
		}},
	}}

	p := newLoggerProvider().(loggerProvider)
	product, cleanup, err := p.Build(context.Background(), appkit.BuildScope{Config: cfg, Decl: &appkit.Decl{}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if cleanup == nil {
		t.Fatal("expected non-nil cleanup (file handle release)")
	}
	lg, ok := product.(log.Logger)
	if !ok {
		t.Fatalf("Build product = %T, want log.Logger", product)
	}

	// 三类来源里本机制负责的 slog.Attr 形态：位置参数对与 ctx 属性流由契约
	// filter_keys/外层 FilterLogger 覆盖，不属本 Provider 的增量。
	lg.Error(context.Background(), "login attempt",
		slog.String("password", "s3cret-value"),
		slog.String("token", "tkn-value"),
		slog.String("plain", "visible"))

	// cleanup 兑现文件句柄（缓冲落盘）后再读。
	if err := cleanup(context.Background()); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	out := string(data)
	if strings.Contains(out, "s3cret-value") || strings.Contains(out, "tkn-value") {
		t.Errorf("credentials leaked into log output: %s", out)
	}
	if !strings.Contains(out, "***") {
		t.Errorf("masking not applied: %s", out)
	}
	if !strings.Contains(out, "bald-demo") {
		t.Errorf("service.name attr missing: %s", out)
	}
	if !strings.Contains(out, "visible") {
		t.Errorf("unrelated attr was wrongly masked: %s", out)
	}
}
