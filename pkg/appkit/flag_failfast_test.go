package appkit

import (
	"strings"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// 本文件钉住 W-4：生产代码过去用 `_ = fs.Parse(os.Args[1:])` 把 flag 解析错误
// 整个吞掉，用户写 `--port=abc` 这类畸形值会被静默忽略、服务带默认配置起来，
// 违背 J4/J5 fail-fast。修复口径：未知 flag 容忍是刻意的，畸形值容错不是。
// 下列测试全部走生产路径本体（parseConfigFlag / parsePlanFlag / App.loadConfig），
// 不再像 log_flags_test.go 那样自建 FlagSet 绕过真实入口。

// TestParsePlanFlag_MalformedBoolValue_FailsFast 钉住畸形值（类型不匹配）：
// 给 bool flag --plan 传 abc，pflag 返回 InvalidValueError，必须被判为错误，
// 不能再被吞掉后按默认配置继续。
func TestParsePlanFlag_MalformedBoolValue_FailsFast(t *testing.T) {
	_, err := parsePlanFlag([]string{"--plan=abc"})
	if err == nil {
		t.Fatal("--plan=abc 是畸形值，parsePlanFlag 必须返回错误（W-4 / J4/J5）")
	}
	if !strings.Contains(err.Error(), "--plan") || !strings.Contains(err.Error(), "abc") {
		t.Fatalf("错误未指明畸形值所在 flag 与取值，不可行动：%v", err)
	}
}

// TestParsePlanFlag_UnknownFlag_StillTolerated 保护既有语义：未知 flag 由
// ParseErrorsAllowlist.UnknownFlags 静默跳过，--plan 仍被正确解析。畸形值
// fail-fast 的修复不得把这条容忍一起改坏。
func TestParsePlanFlag_UnknownFlag_StillTolerated(t *testing.T) {
	out, err := parsePlanFlag([]string{"--plan", "--brand-new-flag=whatever"})
	if err != nil {
		t.Fatalf("未知 flag 应被容忍，parsePlanFlag 却报错：%v", err)
	}
	if !out.enabled || out.format != "text" {
		t.Fatalf("未知 flag 干扰了正常解析：%+v", out)
	}
}

// TestParseConfigFlag_MissingValue_FailsFast 钉住 --config 上的畸形输入：
// --config 缺值（ValueRequiredError）属于畸形输入，必须返回错误。
func TestParseConfigFlag_MissingValue_FailsFast(t *testing.T) {
	_, err := parseConfigFlag("default.yaml", []string{"--config"})
	if err == nil {
		t.Fatal("--config 缺值必须返回错误（W-4 / J4/J5）")
	}
	if !strings.Contains(err.Error(), "config") {
		t.Fatalf("错误未指明缺值的 flag，不可行动：%v", err)
	}
}

// TestParseConfigFlag_UnknownFlag_StillTolerated 保护既有语义：未知 flag 不阻断，
// 同批 args 里的 --config 仍被正确解析。
func TestParseConfigFlag_UnknownFlag_StillTolerated(t *testing.T) {
	got, err := parseConfigFlag("default.yaml", []string{"--brand-new-flag=1", "--config=wanted.yaml"})
	if err != nil {
		t.Fatalf("未知 flag 应被容忍，parseConfigFlag 却报错：%v", err)
	}
	if got != "wanted.yaml" {
		t.Fatalf("--config = %q, want wanted.yaml", got)
	}
}

// TestLoadConfig_MalformedBindingFlagValue_FailsFast 钉住 W-4 在真正装载配置
// 的那一处（appkit.go:552）：给业务 bool flag --grpc.reflection 传非 bool 值，
// loadConfig 必须 fail-fast，而不是继续 config.Load 并带默认配置起来。
func TestLoadConfig_MalformedBindingFlagValue_FailsFast(t *testing.T) {
	withArgs(t, []string{"--grpc.reflection=abc"})
	a := NewApp(Name("bald-test"), Bind("grpc", &bootstrapv1.Server_Grpc{}))
	err := a.loadConfig()
	if err == nil {
		t.Fatal("--grpc.reflection=abc 是畸形值，loadConfig 必须返回错误（W-4 / J4/J5）")
	}
	if !strings.Contains(err.Error(), "grpc.reflection") {
		t.Fatalf("错误未指明具体 flag，不可行动：%v", err)
	}
}

// TestLoadConfig_UnknownFlag_StillTolerated 保护既有语义：loadConfig 仍容忍未知
// flag，未知 flag 不阻断配置装载，文件层配置照常生效。
func TestLoadConfig_UnknownFlag_StillTolerated(t *testing.T) {
	cfgFile := writeCfg(t, "grpc:\n  addr: \":9090\"\n")
	withArgs(t, []string{"--config=" + cfgFile, "--brand-new-flag=1"})
	a := NewApp(Name("bald-test"), Bind("grpc", &bootstrapv1.Server_Grpc{}))
	if err := a.loadConfig(); err != nil {
		t.Fatalf("未知 flag 应被容忍，loadConfig 却报错：%v", err)
	}
	if got := readSetting(a, "grpc.addr"); got != ":9090" {
		t.Errorf("grpc.addr = %q, want :9090（未知 flag 不应改坏正常装载）", got)
	}
}
