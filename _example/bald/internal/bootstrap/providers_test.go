// providers_test.go 回归 loggerProvider：slog.Attr 形态的 password/token 被
// 掩码，service.name 固定属性随行。
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
	product, cleanup, err := p.Build(context.Background(), appkit.ApplyScope{Config: cfg, Decl: &appkit.Decl{}}, appkit.Node{})
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

	// 本 Provider 只覆盖 slog.Attr 形态；位置参数与 ctx 属性由 filter_keys 覆盖。
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
