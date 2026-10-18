package codegen

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// TestTree_EndToEndTimed 判据 ④ 端到端计时（装配层评估 §4.6 第 4 条的度量
// 口径）：T0 → gen app（参考夹具，gen domain 随域声明跟进）→ go mod tidy →
// go build → 起服务 → GET /readyz 200 → GET 业务端点 200 → SIGTERM →
// 优雅退出 → T1，要求 T1-T0 ≤ 1800s。
//
// 业务端点是「域自持路由」用户槽位的最小接线（greet 域手写文件实现
// Domain.Routes，挂判据口径的 /v1/demo）——生成物开箱只有探针与空路由表，
// 业务端点本就归手写文件，本测试按生成器的真实使用流程补齐这一步。
//
// SIGTERM 口径的跨平台实现：POSIX 直接投递 SIGTERM；Windows 无此信号，
// 以 CTRL_BREAK 控制台事件代之（Go 运行时转成 os.Interrupt，命中 App.Run
// 的 signal.Notify(SIGINT, SIGTERM) 优雅停机分支——同一代码路径）。
func TestTree_EndToEndTimed(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not in PATH")
	}
	start := time.Now() // T0

	// 1. gen app + gen domain（批 3 参考夹具；端口换成本轮空闲值——避免与
	//    并发会话/其他用例的 :18080 冲突，Gate1 双侧投影同步改）。
	as := guardFixture()
	port := freePort(t)
	as.Bootstrap.Server.Http.Addr = ":" + port
	as.Server.HttpAddr = ":" + port
	dir, _ := runGenAppInTemp(t, as)
	t.Logf("stage gen app+domain: %s", time.Since(start))

	// 2. 用户槽位最小接线：业务端点 /v1/demo（判据口径的 200 探针）。
	writeDemoRoute(t, dir)

	// 3. go mod tidy
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = dir
	var tidyOut bytes.Buffer
	tidy.Stdout, tidy.Stderr = &tidyOut, &tidyOut
	if err := tidy.Run(); err != nil {
		t.Fatalf("go mod tidy: %v\n%s", err, tidyOut.String())
	}
	t.Logf("stage tidy: %s", time.Since(start))

	// 4. go build -o（产物落文件，不在源码目录落裸二进制）
	bin := filepath.Join(dir, "demoapp.bin")
	build := exec.Command("go", "build", "-o", bin, "./cmd/demoapp")
	build.Dir = dir
	var buildOut bytes.Buffer
	build.Stdout, build.Stderr = &buildOut, &buildOut
	if err := build.Run(); err != nil {
		t.Fatalf("go build: %v\n%s", err, buildOut.String())
	}
	t.Logf("stage build: %s", time.Since(start))

	// 5. 起服务（configs/demoapp.yaml 相对运行目录）
	cmd := exec.Command(bin)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	withE2ESysProcAttr(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start generated app: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	stopAndDump := func(format string, args ...any) {
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf(format+"\n--- app output ---\n%s", append(args, out.String())...)
	}

	// 6. GET /readyz 200
	if err := pollOK("http://127.0.0.1:"+port+"/readyz", 90*time.Second); err != nil {
		stopAndDump("readyz never turned 200: %v", err)
	}
	t.Logf("stage readyz=200: %s", time.Since(start))

	// 7. GET /v1/demo 200（业务端点）
	if err := pollOK("http://127.0.0.1:"+port+"/v1/demo", 15*time.Second); err != nil {
		stopAndDump("/v1/demo never turned 200: %v", err)
	}
	t.Logf("stage /v1/demo=200: %s", time.Since(start))

	// 8. SIGTERM → 优雅退出（exit 0）
	if err := shutdownE2EProcess(t, cmd); err != nil {
		stopAndDump("deliver shutdown signal: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("graceful shutdown must exit 0, got %v\n--- app output ---\n%s", err, out.String())
		}
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		t.Fatal("graceful shutdown did not finish within 30s")
	}

	elapsed := time.Since(start) // T1
	t.Logf("T1-T0 = %s（判据 ④ 口径：<= 1800s）", elapsed)
	if elapsed > 1800*time.Second {
		t.Errorf("T1-T0 = %s exceeds the 1800s criterion", elapsed)
	}
}

// freePort 预留一个空闲 TCP 端口（先监听再关闭；计时测试单实例运行，
// 关闭后到服务绑定之间的竞态窗口可接受）。
func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve free port: %v", err)
	}
	defer ln.Close()
	return strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
}

// pollOK 轮询 GET url 直到 200 或超时（启动期服务未监听时连接拒绝属预期）。
func pollOK(url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	var last string
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			last = fmt.Sprintf("status %d", resp.StatusCode)
		} else {
			last = err.Error()
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("last: %s", last)
}

// writeDemoRoute 在生成树的 greet 域包落一个手写文件，实现 Domain.Routes
// （生成一次、此后归你维护的用户槽位）：挂 GET /v1/demo 返回 200——判据 ④
// 计时脚本的业务端点探针，与「域自持路由」惯用法同构。
func writeDemoRoute(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, "internal", "apiserver", "greetv1", "demo_routes.go")
	src := `package greetv1

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
)

// demoRoutes 是判据 ④ 计时脚本的业务端点接线：greet 域手写文件实现
// Domain.Routes（引擎在 Wire 之后、server 构建之前调用，见 pkg/appkit/domain.go）。
func init() {
	Domain.Routes = func(h http.Handler) error {
		r, ok := h.(*gin.Engine)
		if !ok {
			return fmt.Errorf("greetv1: expected *gin.Engine, got %T", h)
		}
		r.GET("/v1/demo", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"domain": "greet", "endpoint": "demo"})
		})
		return nil
	}
}
`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatalf("write demo route: %v", err)
	}
}
