package cobramcp

import (
	"context"
	"log/slog"
	"testing"

	baldlog "github.com/kalandramo/bald/log"
	"github.com/spf13/cobra"
)

// ---------------------------------------------------------------------------
// logger.go：stderrLogger 后端全量（此前整文件 0%）
// ---------------------------------------------------------------------------

func TestToSlogLevel_AllBranches(t *testing.T) {
	cases := map[baldlog.Level]slog.Level{
		baldlog.LevelDebug: slog.LevelDebug,
		baldlog.LevelWarn:  slog.LevelWarn,
		baldlog.LevelError: slog.LevelError,
		baldlog.LevelInfo:  slog.LevelInfo, // default 分支
	}
	for in, want := range cases {
		if got := toSlogLevel(in); got != want {
			t.Errorf("toSlogLevel(%v) = %v, want %v", in, got, want)
		}
	}
}

func TestStderrLogger_MethodsAndFiltering(t *testing.T) {
	l := newStderrLogger(baldlog.LevelError)
	ctx := context.Background()

	// Error 级：Debug/Info/Warn 被过滤（Enabled=false），Error 通过。
	if l.Enabled(baldlog.LevelDebug) {
		t.Error("debug should be disabled at error level")
	}
	if !l.Enabled(baldlog.LevelError) {
		t.Error("error should be enabled at error level")
	}
	l.Debug(ctx, "dbg", "k", 1)
	l.Info(ctx, "info")
	l.Warn(ctx, "warn")
	l.Error(ctx, "err")

	// With 派生带公共属性的新 logger，并在新实例上再次调用各方法。
	child := l.With("component", "test").(*stderrLogger)
	child.Debug(ctx, "child-dbg")
	child.Info(ctx, "child-info")
	child.Warn(ctx, "child-warn")
	child.Error(ctx, "child-err")

	// Info 级后端不应过滤 Info。
	l2 := newStderrLogger(baldlog.LevelInfo)
	if !l2.Enabled(baldlog.LevelInfo) {
		t.Error("info should be enabled at info level")
	}
}

func TestInstallStderrLogger(t *testing.T) {
	old := baldlog.GetLogger()
	t.Cleanup(func() { baldlog.SetLogger(old) })

	installStderrLogger(baldlog.LevelWarn)
	if _, ok := baldlog.GetLogger().(*stderrLogger); !ok {
		t.Errorf("global logger after install = %T, want *stderrLogger", baldlog.GetLogger())
	}
}

// ---------------------------------------------------------------------------
// root.go / start.go / tools.go / stream.go / restcmd.go：命令树构造
// ---------------------------------------------------------------------------

func TestCommand_BuildsSubcommandTree(t *testing.T) {
	// config == nil：commandName 回退默认 "mcp"，各子命令构造体（含 flags 注册）被执行。
	cmd := Command(nil)
	if cmd == nil {
		t.Fatal("Command(nil) returned nil")
	}
	if cmd.Name() != "mcp" {
		t.Errorf("root name = %q, want mcp", cmd.Name())
	}
	want := map[string]bool{"start": false, "tools": false, "stream": false, "rest": false}
	for _, sub := range cmd.Commands() {
		if _, ok := want[sub.Name()]; ok {
			want[sub.Name()] = true
		}
	}
	for n, seen := range want {
		if !seen {
			t.Errorf("subcommand %q not built", n)
		}
	}

	// config != nil：自定义命令名 + DefaultEnv 分支。
	cmd2 := Command(&Config{CommandName: "myapp"})
	if cmd2.Name() != "myapp" {
		t.Errorf("root name = %q, want myapp", cmd2.Name())
	}
}

// ---------------------------------------------------------------------------
// server.go：WithSelectors + Transport
// ---------------------------------------------------------------------------

func TestWithSelectorsAndTransport(t *testing.T) {
	factory := func() *cobra.Command {
		root := &cobra.Command{Use: "demo"}
		root.AddCommand(&cobra.Command{Use: "greet", RunE: func(*cobra.Command, []string) error { return nil }})
		return root
	}

	// 空 selectors：保留默认 catch-all，不覆盖。
	srv, err := NewMCPServer(MCPOptions{Name: "demo", Version: "1"}, factory, WithSelectors())
	if err != nil {
		t.Fatalf("NewMCPServer: %v", err)
	}
	if len(srv.selectors) != 1 {
		t.Errorf("default selectors = %d, want 1 (WithSelectors no-op on empty)", len(srv.selectors))
	}

	// 非空 selectors：覆盖为传入的两条。
	srv2, err := NewMCPServer(MCPOptions{Name: "demo", Version: "1"}, factory, WithSelectors(Selector{}, Selector{}))
	if err != nil {
		t.Fatalf("NewMCPServer: %v", err)
	}
	if len(srv2.selectors) != 2 {
		t.Errorf("selectors = %d, want 2", len(srv2.selectors))
	}

	// Transport 返回底层 mcp server。
	if srv.Transport() == nil {
		t.Error("Transport() returned nil")
	}
}
