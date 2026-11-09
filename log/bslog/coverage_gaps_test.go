package bslog

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	log "github.com/kalandramo/bald/log"
	"github.com/spf13/pflag"
)

// TestWithHandlerAndOTelAlias 覆盖外部 handler 注入与 OTel 语义别名。
func TestWithHandlerAndOTelAlias(t *testing.T) {
	var buf bytes.Buffer
	l := New(NewOptions(), WithHandler(slog.NewTextHandler(&buf, nil)))
	l.Info(context.Background(), "direct-handler")

	l2 := New(NewOptions(), WithOTelHandler(slog.NewTextHandler(&buf, nil)))
	l2.Info(context.Background(), "otel-alias")

	out := buf.String()
	if !strings.Contains(out, "direct-handler") || !strings.Contains(out, "otel-alias") {
		t.Fatalf("injected handler output missing: %s", out)
	}
}

// TestNewLevelFallbackAndJSONFormat 覆盖非法级别回退 info 与 json 格式分支。
func TestNewLevelFallbackAndJSONFormat(t *testing.T) {
	var buf bytes.Buffer
	l := New(&Options{Level: "nope", Format: "json"}, withWriter(&buf))

	if l.Enabled(log.LevelDebug) {
		t.Fatal("invalid level must fall back to info")
	}
	l.Info(context.Background(), "json-msg", "k", "v")
	if !strings.HasPrefix(strings.TrimSpace(buf.String()), "{") {
		t.Fatalf("expected json output, got %q", buf.String())
	}
}

func TestOpenWriterStdTargets(t *testing.T) {
	cases := []struct {
		paths []string
		want  io.Writer
	}{
		{[]string{"stdout"}, os.Stdout},
		{[]string{"stderr"}, os.Stderr},
		{nil, os.Stdout},
	}
	for _, tc := range cases {
		w, closers := openWriter(&Options{OutputPaths: tc.paths})
		if w != tc.want {
			t.Fatalf("openWriter(%v) = %T, want %T", tc.paths, w, tc.want)
		}
		if len(closers) != 0 {
			t.Fatalf("std targets must not be collected, got %d closers", len(closers))
		}
	}
}

// TestOpenWriterFileErrorFallsBackToStdout 覆盖文件打开失败回退 stdout，
// 以及相对路径（Dir == "."）不尝试 MkdirAll。
func TestOpenWriterFileErrorFallsBackToStdout(t *testing.T) {
	o := &Options{OutputPaths: []string{t.TempDir()}} // 目录不可写为文件
	w, closers := openWriter(o)
	if w != os.Stdout {
		t.Fatalf("failed open must fall back to stdout, got %T", w)
	}
	if len(closers) != 0 {
		t.Fatalf("no handles expected on fallback, got %d", len(closers))
	}

	if w, _ := openWriter(&Options{OutputPaths: []string{"./"}}); w != os.Stdout {
		t.Fatalf("relative dir path must fall back to stdout, got %T", w)
	}
}

// TestOpenWriterMultipleFiles 覆盖多目标聚合写入。
func TestOpenWriterMultipleFiles(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.log")
	b := filepath.Join(dir, "b.log")

	w, closers := openWriter(&Options{OutputPaths: []string{a, b}})
	if _, err := w.Write([]byte("both\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	for _, c := range closers {
		_ = c.Close()
	}
	for _, p := range []string{a, b} {
		data, err := os.ReadFile(p)
		if err != nil || string(data) != "both\n" {
			t.Fatalf("%s: (%q, %v)", p, data, err)
		}
	}
}

type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, errors.New("write boom") }

func TestMultiWriter(t *testing.T) {
	var a, b bytes.Buffer
	n, err := multiWriter([]io.Writer{&a, &b}).Write([]byte("x"))
	if err != nil || n != 1 || a.String() != "x" || b.String() != "x" {
		t.Fatalf("multi write = (%d,%v), a=%q b=%q", n, err, a.String(), b.String())
	}

	if _, err := multiWriter([]io.Writer{&a, errWriter{}}).Write([]byte("y")); err == nil {
		t.Fatal("expected propagated write error")
	}
}

// TestWarnAndEnabledLevels 覆盖 Warn 与 toSlogLevel 的全部分支。
func TestWarnAndEnabledLevels(t *testing.T) {
	var buf bytes.Buffer
	l := New(NewOptions(), withWriter(&buf))
	l.Warn(context.Background(), "warn-msg")
	if !strings.Contains(buf.String(), "warn-msg") {
		t.Fatalf("warn output missing: %s", buf.String())
	}

	cases := []struct {
		level log.Level
		want  bool
	}{
		{log.LevelDebug, false},
		{log.LevelInfo, true},
		{log.LevelWarn, true},
		{log.LevelError, true},
		{log.Level(99), true}, // 未知级别按 info 映射
	}
	for _, tc := range cases {
		if got := l.Enabled(tc.level); got != tc.want {
			t.Errorf("Enabled(%v) = %v, want %v", tc.level, got, tc.want)
		}
	}
}

// TestFilterHandlerWithGroup 覆盖 filterHandler.WithGroup，并确认组内属性仍被脱敏。
func TestFilterHandlerWithGroup(t *testing.T) {
	var buf bytes.Buffer
	var h slog.Handler = &filterHandler{
		next:   slog.NewTextHandler(&buf, nil),
		filter: FilterKey("password"),
	}
	h = h.WithGroup("g")

	rec := slog.NewRecord(time.Now(), slog.LevelInfo, "m", 0)
	rec.AddAttrs(slog.String("password", "secret"))
	if err := h.Handle(context.Background(), rec); err != nil {
		t.Fatalf("handle: %v", err)
	}

	out := buf.String()
	if strings.Contains(out, "secret") {
		t.Fatalf("grouped attr must be redacted: %s", out)
	}
	if !strings.Contains(out, "g.password=***") {
		t.Fatalf("expected redacted grouped attr, got %s", out)
	}
}

func TestAddFlags(t *testing.T) {
	o := NewOptions()
	fs := pflag.NewFlagSet("log", pflag.ContinueOnError)
	o.AddFlags(fs)
	if err := fs.Parse([]string{
		"--log.level=debug",
		"--log.format=json",
		"--log.output-paths=stdout,stderr",
		"--log.rotate.enabled=true",
		"--log.rotate.max-size=10",
		"--log.rotate.max-backups=3",
		"--log.rotate.max-age=5",
		"--log.rotate.compress=false",
	}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if o.Level != "debug" || o.Format != "json" || len(o.OutputPaths) != 2 {
		t.Fatalf("string flags not applied: %+v", o)
	}
	r := o.Rotate
	if !r.Enabled || r.MaxSize != 10 || r.MaxBackups != 3 || r.MaxAge != 5 || r.Compress {
		t.Fatalf("rotate flags not applied: %+v", r)
	}

	// Rotate 为 nil 时 AddFlags 负责初始化。
	o2 := &Options{}
	fs2 := pflag.NewFlagSet("log", pflag.ContinueOnError)
	o2.AddFlags(fs2)
	if o2.Rotate == nil {
		t.Fatal("AddFlags must initialize nil Rotate")
	}
	if err := fs2.Parse([]string{"--log.rotate.max-size=7"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if o2.Rotate.MaxSize != 7 {
		t.Fatalf("max-size = %d, want 7", o2.Rotate.MaxSize)
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Options)
		ok   bool
	}{
		{"defaults", func(*Options) {}, true},
		{"bad level", func(o *Options) { o.Level = "trace" }, false},
		{"bad format", func(o *Options) { o.Format = "xml" }, false},
		{"empty paths", func(o *Options) { o.OutputPaths = nil }, false},
		{"rotate enabled zero size", func(o *Options) { o.Rotate.Enabled = true; o.Rotate.MaxSize = 0 }, false},
		{"rotate disabled zero size", func(o *Options) { o.Rotate.MaxSize = 0 }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := NewOptions()
			tc.mut(o)
			err := o.Validate()
			if tc.ok && err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"debug": slog.LevelDebug,
		"info":  slog.LevelInfo,
		"warn":  slog.LevelWarn,
		"error": slog.LevelError,
	}
	for in, want := range cases {
		got, err := parseLevel(in)
		if err != nil || got != want {
			t.Errorf("parseLevel(%q) = (%v,%v), want (%v,nil)", in, got, err, want)
		}
	}
	if _, err := parseLevel("DEBUG"); err == nil {
		t.Fatal("parseLevel must reject unknown level")
	}
}
