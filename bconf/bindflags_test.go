package bconf

import (
	"strings"
	"testing"

	"github.com/spf13/pflag"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// loggerFS 返回绑定了 logger 域（含 repeated 字段 filter_keys）的 FlagSet。
func loggerFS() *pflag.FlagSet {
	fs := pflag.NewFlagSet("app", pflag.ContinueOnError)
	BindFlags(fs, NewBootstrap().GetLogger(), "logger")
	return fs
}

// TestGuideFlagNotPassed 不误用指引 flag 时零感知：解析成功、配置原样。
func TestGuideFlagNotPassed(t *testing.T) {
	fs := loggerFS()
	cfg := NewBootstrap()
	if err := fs.Parse(nil); err != nil {
		t.Fatalf("不传指引 flag 不应有任何报错: %v", err)
	}
	if got := cfg.GetLogger().GetFilterKeys(); len(got) != 0 {
		t.Fatalf("filter_keys 应保持默认（空）: %v", got)
	}
}

// TestGuideFlagHidden 指引 flag 存在但隐藏：--help / VisitAll 零噪音。
func TestGuideFlagHidden(t *testing.T) {
	fs := loggerFS()
	f := fs.Lookup("logger.filter_keys")
	if f == nil {
		t.Fatal("repeated 字段应注册指引 flag")
	}
	if !f.Hidden {
		t.Fatal("指引 flag 应 MarkHidden（--help 不出现）")
	}
}

// TestGuideFlagSet 误用时报「怎么办」指引，而非 unknown flag（只说不行）。
func TestGuideFlagSet(t *testing.T) {
	fs := loggerFS()
	err := fs.Parse([]string{"--logger.filter_keys", "password"})
	if err == nil {
		t.Fatal("误用指引 flag 应报错")
	}
	msg := err.Error()
	if strings.Contains(msg, "unknown flag") {
		t.Fatalf("不应再是 unknown flag: %v", msg)
	}
	for _, want := range []string{"logger.filter_keys", "repeated", "config file", "custom flag binder"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("错误应含指引要素 %q: %v", want, msg)
		}
	}
}

// TestGuideFlagMapField map 字段同款指引（tracer.otlp.headers）。
func TestGuideFlagMapField(t *testing.T) {
	fs := pflag.NewFlagSet("app", pflag.ContinueOnError)
	cfg := NewBootstrap()
	cfg.Tracer = &bootstrapv1.Tracer{Otlp: &bootstrapv1.Tracer_Otlp{Endpoint: "http://otlp:4317"}}
	BindFlags(fs, cfg.GetTracer(), "tracer")

	f := fs.Lookup("tracer.otlp.headers")
	if f == nil || !f.Hidden {
		t.Fatal("map 字段应注册隐藏指引 flag")
	}
	err := fs.Parse([]string{"--tracer.otlp.headers", "x=y"})
	if err == nil || !strings.Contains(err.Error(), "map") {
		t.Fatalf("误用 map 指引 flag 应报带 kind 的错: %v", err)
	}
}

// TestGuideFlagYieldsToCustomBinder 业务自定义同名 flag 时让位，不 panic。
func TestGuideFlagYieldsToCustomBinder(t *testing.T) {
	fs := pflag.NewFlagSet("app", pflag.ContinueOnError)
	var got []string
	fs.StringSliceVar(&got, "logger.filter_keys", nil, "custom binder")
	BindFlags(fs, NewBootstrap().GetLogger(), "logger") // 不应 panic "flag redefined"

	if err := fs.Parse([]string{"--logger.filter_keys", "a,b"}); err != nil {
		t.Fatalf("自定义 binder 应正常工作: %v", err)
	}
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("自定义 binder 结果 = %v", got)
	}
}
