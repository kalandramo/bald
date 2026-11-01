// serve_failure_test.go —— 启动失败的可见性契约：日志后端未注入时（仍是
// nop），错误必须出现在 stderr，而不是只留下一个退出码。
package appkit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	log "github.com/kalandramo/bald/log"
)

// recordingLogger 是可切换 Enabled 的日志桩，只记录 Error 消息。
type recordingLogger struct {
	enabled bool
	errors  []string
}

func (*recordingLogger) Debug(context.Context, string, ...any) {}
func (*recordingLogger) Info(context.Context, string, ...any)  {}
func (*recordingLogger) Warn(context.Context, string, ...any)  {}
func (l *recordingLogger) Error(_ context.Context, msg string, _ ...any) {
	l.errors = append(l.errors, msg)
}
func (l *recordingLogger) Enabled(log.Level) bool { return l.enabled }
func (l *recordingLogger) With(...any) log.Logger { return l }

// captureStderr 在 fn 执行期间把 os.Stderr 重定向到临时文件并返回其内容。
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stderr")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create stderr file: %v", err)
	}
	old := os.Stderr
	os.Stderr = f
	defer func() { os.Stderr = old }()

	fn()

	if err := f.Close(); err != nil {
		t.Fatalf("close stderr file: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read stderr file: %v", err)
	}
	return string(data)
}

// TestReportServeFailure_FallsBackToStderr 默认 nop 后端下必须兜底打印。
func TestReportServeFailure_FallsBackToStderr(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })
	log.SetLogger(nil)

	out := captureStderr(t, func() {
		reportServeFailure(context.Background(), errors.New("appkit: preload config: open missing.yaml: no such file"))
	})

	want := "appkit: serve failed: appkit: preload config: open missing.yaml: no such file"
	if !strings.Contains(out, want) {
		t.Fatalf("stderr = %q, want it to contain %q", out, want)
	}
}

// TestReportServeFailure_LoggerEnabledNoStderr 日志后端接管后不重复写 stderr。
func TestReportServeFailure_LoggerEnabledNoStderr(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })
	rec := &recordingLogger{enabled: true}
	log.SetLogger(rec)

	out := captureStderr(t, func() {
		reportServeFailure(context.Background(), errors.New("boom"))
	})

	if out != "" {
		t.Fatalf("stderr = %q, want empty when logger handles errors", out)
	}
	if len(rec.errors) != 1 || rec.errors[0] != "appkit: serve failed" {
		t.Fatalf("logger errors = %v, want one serve-failed entry", rec.errors)
	}
}
