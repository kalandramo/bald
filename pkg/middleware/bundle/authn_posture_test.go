package bundle

// 认证姿态必须被决定（风险 6.1）：未注入 Authn 时链里没有认证层，与「有意公开」
// 在链上同形——零开销省略是优点，静默开放是风险。这里锁住区分两者的最小面：
// Verify 的三种判定 + 出链时对未决定姿态的告警。

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/kalandramo/bald/log"
	"github.com/kalandramo/bald/log/bslog"
)

func TestBundleVerifyAuthnPosture(t *testing.T) {
	for _, tc := range []struct {
		name    string
		opts    []Option
		wantErr bool
	}{
		{"注入 Authn", []Option{Authn(&stubAuthn{})}, false},
		{"声明 Public", []Option{Public()}, false},
		{"两者都不给", nil, true},
		{"只给 Authz 不算", []Option{Authz(&stubAuthz{})}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := New(tc.opts...).Verify()
			if tc.wantErr && err == nil {
				t.Fatal("Verify = nil, want 未决定姿态错误")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Verify = %v, want nil", err)
			}
			if err != nil {
				msg := err.Error()
				if !strings.Contains(msg, "Authn") || !strings.Contains(msg, "Public") {
					t.Fatalf("错误须指出两条出路，得 %q", msg)
				}
			}
		})
	}
}

// 出链时对未决定的姿态告警（Gin 与 gRPC 两个产出点），已决定的两条路径都不该出声。
func TestBundleUndeclaredPostureWarnsOnChainBuild(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	buf := &bytes.Buffer{}
	log.SetLogger(bslog.New(bslog.NewOptions(),
		bslog.WithHandler(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))))

	for _, tc := range []struct {
		name     string
		opts     []Option
		build    func(*Bundle)
		wantWarn bool
	}{
		{"Gin 未决定", nil, func(b *Bundle) { b.Gin() }, true},
		{"gRPC 未决定", nil, func(b *Bundle) { b.GRPCInterceptors() }, true},
		{"Gin 已 Authn", []Option{Authn(&stubAuthn{})}, func(b *Bundle) { b.Gin() }, false},
		{"Gin 已 Public", []Option{Public()}, func(b *Bundle) { b.Gin() }, false},
		{"gRPC 已 Public", []Option{Public()}, func(b *Bundle) { b.GRPCInterceptors() }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf.Reset()
			tc.build(New(tc.opts...))
			warned := strings.Contains(buf.String(), "AUTHN_UNDECLARED") ||
				strings.Contains(buf.String(), "认证层")
			if warned != tc.wantWarn {
				t.Fatalf("告警 = %v, want %v（日志：%s）", warned, tc.wantWarn, buf.String())
			}
		})
	}
}
