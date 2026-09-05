package bootstrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	log "github.com/kalandramo/bald/log"
)

// stubLogProvider 返回固定行为的 LoggerProvider。
func stubLogProvider(l log.Logger, cleanup func(), err error) LoggerProvider {
	return func(context.Context, *bootstrapv1.Logger) (log.Logger, func(), error) {
		return l, cleanup, err
	}
}

// stubLogger 最小 Logger 实现。
type stubLogger struct{ enabled bool }

func (s *stubLogger) Debug(context.Context, string, ...any) {}
func (s *stubLogger) Info(context.Context, string, ...any)  {}
func (s *stubLogger) Warn(context.Context, string, ...any)  {}
func (s *stubLogger) Error(context.Context, string, ...any) {}
func (s *stubLogger) Enabled(log.Level) bool                { return s.enabled }
func (s *stubLogger) With(...any) log.Logger                { return s }

func TestLogRegistry_Register(t *testing.T) {
	r := NewLogRegistry()
	if err := r.Register("slog", stubLogProvider(&stubLogger{}, nil, nil)); err != nil {
		t.Fatalf("Register(first) = %v, want nil", err)
	}
	if err := r.Register("slog", stubLogProvider(&stubLogger{}, nil, nil)); err == nil {
		t.Fatal("Register(duplicate) = nil, want error")
	}
	if err := r.Register("", stubLogProvider(&stubLogger{}, nil, nil)); err == nil {
		t.Fatal("Register(empty name) = nil, want error")
	}
	if err := r.Register("nilp", nil); err == nil {
		t.Fatal("Register(nil provider) = nil, want error")
	}
}

func TestBuildLogger_OK(t *testing.T) {
	r := NewLogRegistry()
	want := &stubLogger{enabled: true}
	called := false
	r.MustRegister("slog", func(context.Context, *bootstrapv1.Logger) (log.Logger, func(), error) {
		called = true
		return want, func() { called = false }, nil
	})

	l, cleanup, err := r.BuildLogger(context.Background(), &bootstrapv1.Logger{Type: "slog"})
	if err != nil || l != want {
		t.Fatalf("BuildLogger() = (%v, %v), want (%v, nil)", l, err, want)
	}
	if !called {
		t.Fatal("provider should be invoked")
	}
	cleanup()
	if called {
		t.Fatal("cleanup should run")
	}
}

func TestBuildLogger_Errors(t *testing.T) {
	r := NewLogRegistry()
	r.MustRegister("slog", stubLogProvider(&stubLogger{}, nil, nil))

	if _, _, err := r.BuildLogger(context.Background(), nil); err == nil {
		t.Fatal("nil config should error")
	}
	if _, _, err := r.BuildLogger(context.Background(), &bootstrapv1.Logger{}); err == nil {
		t.Fatal("empty type should error")
	}
	_, _, err := r.BuildLogger(context.Background(), &bootstrapv1.Logger{Type: "zap"})
	if err == nil || !strings.Contains(err.Error(), `not registered`) || !strings.Contains(err.Error(), "slog") {
		t.Fatalf("unregistered type should error with candidates, got: %v", err)
	}

	// provider 返回 nil Logger 视为错误。
	r2 := NewLogRegistry()
	r2.MustRegister("slog", stubLogProvider(nil, nil, nil))
	if _, _, err := r2.BuildLogger(context.Background(), &bootstrapv1.Logger{Type: "slog"}); err == nil {
		t.Fatal("nil logger from provider should error")
	}

	// provider 出错短路并包装。
	r3 := NewLogRegistry()
	r3.MustRegister("slog", stubLogProvider(nil, nil, errors.New("boom")))
	if _, _, err := r3.BuildLogger(context.Background(), &bootstrapv1.Logger{Type: "slog"}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("provider error should be wrapped, got: %v", err)
	}
}

func TestSlogLoggerProvider(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	cfg := &bootstrapv1.Logger{
		Type: "slog",
		Slog: &bootstrapv1.Logger_Slog{
			Level:      "debug",
			Format:     "json",
			OutputPath: path,
		},
	}

	l, cleanup, err := SlogLoggerProvider()(context.Background(), cfg)
	if err != nil {
		t.Fatalf("SlogLoggerProvider() = %v, want nil", err)
	}
	if l == nil {
		t.Fatal("logger should not be nil")
	}
	defer func() {
		if cleanup != nil {
			cleanup()
		}
	}()

	// level=debug 生效。
	if !l.Enabled(log.LevelDebug) {
		t.Fatal("debug should be enabled per contract")
	}
	l.Info(context.Background(), "contract-log", "k", "v")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("log file should be written: %v", err)
	}
	if !strings.Contains(string(data), "contract-log") || !strings.Contains(string(data), `"k":"v"`) {
		t.Fatalf("unexpected log content: %s", data)
	}
}

func TestSlogLoggerProvider_DefaultFallback(t *testing.T) {
	// type=slog 但 Slog 段缺失 → 默认配置（stdout + info），不报错。
	l, cleanup, err := SlogLoggerProvider()(context.Background(), &bootstrapv1.Logger{Type: "slog"})
	if err != nil || l == nil {
		t.Fatalf("missing slog section should fall back to defaults, got (%v, %v)", l, err)
	}
	if cleanup != nil {
		cleanup()
	}
	if l.Enabled(log.LevelDebug) {
		t.Fatal("default level should be info (debug disabled)")
	}
}

// TestBuildLogger_Integration 契约 → 装配 → 全局表 的端到端验证。
func TestBuildLogger_Integration(t *testing.T) {
	defer log.SetLogger(nil)

	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")

	lr := NewLogRegistry()
	lr.MustRegister("slog", SlogLoggerProvider())

	l, cleanup, err := lr.BuildLogger(context.Background(), &bootstrapv1.Logger{
		Type: "slog",
		Slog: &bootstrapv1.Logger_Slog{Format: "text", OutputPath: path},
	})
	if err != nil {
		t.Fatalf("BuildLogger() = %v, want nil", err)
	}
	defer cleanup()

	log.SetLogger(l)
	log.GetLogger().Info(context.Background(), "e2e", "tenant", "t1")

	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "e2e") {
		t.Fatalf("global logger should write through provider backend: (%s, %v)", data, err)
	}
}
