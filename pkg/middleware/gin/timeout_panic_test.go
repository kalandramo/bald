package gin

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// itoa 避免 panic 路径里引入 fmt 的额外依赖面（子进程体保持最小）。
func itoa(n int) string { return strconv.Itoa(n) }

// TestTimeout_HandlerPanicDoesNotKillProcess 复现 AR-4：Timeout 中间件的
// handler 跑在独立 goroutine 里，外层 Recovery 的 defer 在另一条 goroutine
// 上接不到 panic——修复前 handler 一次 panic 整个进程退出（exit status 2）。
// 子进程模式：父进程断言子进程存活、panic 被外层 Recovery 转成 500。
func TestTimeout_HandlerPanicDoesNotKillProcess(t *testing.T) {
	if os.Getenv("BALD_GIN_TIMEOUT_PANIC_CHILD") == "1" {
		runTimeoutPanicChild()
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestTimeout_HandlerPanicDoesNotKillProcess", "-test.v")
	cmd.Env = append(os.Environ(), "BALD_GIN_TIMEOUT_PANIC_CHILD=1")
	out := &bytes.Buffer{}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		t.Fatalf("子进程崩溃（panic 逃出 Timeout goroutine，AR-4）: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "SURVIVED") {
		t.Fatalf("子进程未走到存活标记:\n%s", out.String())
	}
}

// runTimeoutPanicChild 是子进程体：Recovery + Timeout + panic handler，
// 同进程内打请求。修复前 panic 逃逸杀死进程（看不到 SURVIVED）；修复后
// panic 被带回主 goroutine 重抛、外层 Recovery 转 500。
func runTimeoutPanicChild() {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(Recovery())
	r.Use(Timeout(TimeoutConfig{DefaultTimeoutMs: 1000}))
	r.GET("/boom", func(c *gin.Context) {
		panic("boom")
	})

	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != 500 {
		panic("expect recovery 500, got " + itoa(rec.Code))
	}
	if !strings.Contains(rec.Body.String(), "boom") {
		panic("expect panic value in recovery body: " + rec.Body.String())
	}
	// 存活标记：走到这里说明进程没有被 panic 杀死。
	print("SURVIVED\n")
}
