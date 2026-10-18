package codegen

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestTree_AuthzClosedLoop 生成物 authz 链路闭环（装配层评估 §11 第 6 条缺口
// 的收口证明）：gen app + gen domain 的测试工程里，桥接真实消费两个生成物——
// internal/bootstrap/seed/policies.go 的默认策略行（经 contrib/authz-casbin
// 的 NewRows 装载）与 internal/<apiserver_dir>/authz/alias.gen.go 的
// ObjectAliases（经 NewAlias 双向等价解析）——受保护端点三态：
//
//	POST /v1/greets  无 token → 401（authn 层拒绝）
//	POST /v1/greets  viewer   → 403（策略行 viewer 只有 get/list）
//	POST /v1/greets  admin    → 200（策略行 admin 六元组含 post）
//
// 三态全部由「生成物数据 + contrib 行为」产生：HTTP 面归一化 object 是
// greets（注解路径首段），策略行走 gRPC 命名 greet——403→200 的放行必经
// 别名反查（greets → greet），别名表的消费由此被端到端钉住。
// 接线源文（testdata/authz/*.go.tmpl）即「生成一次、归你维护」用户槽位的
// 消费入口示范：bridge.go（seed.Rows → NewRows + ObjectAliases → NewAlias）、
// demo_authn.go（认证桩）、apiserver.go（bundle.Authn + bundle.Authz 链）、
// greets_routes.go（域自持受保护端点）。
func TestTree_AuthzClosedLoop(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not in PATH")
	}

	// 1. gen app + gen domain（参考夹具；端口换空闲值，避免并发会话冲突）。
	baldRoot := repoRoot(t) // Chdir 前取仓库根（Chdir 后 Getwd 是临时产出根）
	tplDir := filepath.Join(baldRoot, "internal", "codegen", "testdata", "authz")
	as := guardFixture()
	port := freePort(t)
	as.Bootstrap.Server.Http.Addr = ":" + port
	as.Server.HttpAddr = ":" + port
	dir, _ := runGenAppInTemp(t, as)

	// 2. 用户槽位接线（testdata 模板落盘；{{MODULE}} 换成生成 module 名）。
	writeAuthzGlue(t, dir, tplDir, baldRoot, as.GetMeta().GetModule())

	// 3. go mod tidy（contrib/authz-casbin 的 require+replace 已由 glue 落入）。
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = dir
	var tidyOut bytes.Buffer
	tidy.Stdout, tidy.Stderr = &tidyOut, &tidyOut
	if err := tidy.Run(); err != nil {
		t.Fatalf("go mod tidy: %v\n%s", err, tidyOut.String())
	}

	// 4. go build -o（产物落文件，不在源码目录落裸二进制）。
	bin := filepath.Join(dir, "authzapp.bin")
	build := exec.Command("go", "build", "-o", bin, "./cmd/demoapp")
	build.Dir = dir
	var buildOut bytes.Buffer
	build.Stdout, build.Stderr = &buildOut, &buildOut
	if err := build.Run(); err != nil {
		t.Fatalf("go build: %v\n%s", err, buildOut.String())
	}

	// 5. 起服务（configs/demoapp.yaml 相对运行目录）。
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

	// 6. 就绪后打三态（顺序无关，逐态断言）。
	if err := pollOK("http://127.0.0.1:"+port+"/readyz", 90*time.Second); err != nil {
		stopAndDump("readyz never turned 200: %v", err)
	}
	base := "http://127.0.0.1:" + port
	// readyz 200 与路由可服务之间的窗口属预期：小重试后再判失败。
	wantStatus := func(desc, method, path, token string, want int) {
		t.Helper()
		var last string
		for i := 0; i < 5; i++ {
			got, err := requestStatus(method, base+path, token)
			if err != nil {
				last = err.Error()
			} else if got == want {
				return
			} else {
				last = strconv.Itoa(got)
			}
			time.Sleep(300 * time.Millisecond)
		}
		stopAndDump("%s: want %d, last %s", desc, want, last)
	}
	wantStatus("no token", http.MethodPost, "/v1/greets", "", 401)
	wantStatus("viewer", http.MethodPost, "/v1/greets", "tok-viewer", 403)
	wantStatus("admin", http.MethodPost, "/v1/greets", "tok-admin", 200)
	// 只读面：viewer 经别名反查命中 viewer 的 get 策略行 → 200。
	wantStatus("viewer read", http.MethodGet, "/v1/greets/world", "tok-viewer", 200)

	// 7. 优雅停机（与计时脚本同口径）。
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
}

// requestStatus 发一次带可选 Bearer token 的请求，只回状态码。
func requestStatus(method, url, token string) (int, error) {
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return 0, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

// writeAuthzGlue 把 testdata/authz 的四处接线源文落进生成树（{{MODULE}}
// 换成生成 module 名），并向 go.mod 追加 contrib/authz-casbin 的
// require+replace（_example 同款占位版本 + 本地路径指向，离线可构建）。
func writeAuthzGlue(t *testing.T, dir, tplDir, baldRoot, module string) {
	t.Helper()
	plant := func(rel, tmplName string) {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(tplDir, tmplName))
		if err != nil {
			t.Fatalf("read %s: %v", tmplName, err)
		}
		src := strings.ReplaceAll(string(raw), "{{MODULE}}", module)
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	plant(filepath.Join("internal", "apiserver", "authz", "bridge.go"), "bridge.go.tmpl")
	plant(filepath.Join("internal", "apiserver", "demo_authn.go"), "demo_authn.go.tmpl")
	plant(filepath.Join("internal", "apiserver", "apiserver.go"), "apiserver.go.tmpl")
	plant(filepath.Join("internal", "apiserver", "greetv1", "greets_routes.go"), "greets_routes.go.tmpl")

	gomod := filepath.Join(dir, "go.mod")
	content, err := os.ReadFile(gomod)
	if err != nil {
		t.Fatalf("read generated go.mod: %v", err)
	}
	line := "\nrequire github.com/kalandramo/bald/contrib/authz-casbin v0.0.0-00010101000000-000000000000\n\n" +
		"replace github.com/kalandramo/bald/contrib/authz-casbin => " + filepath.Join(baldRoot, "contrib", "authz-casbin") + "\n"
	if err := os.WriteFile(gomod, append(content, []byte(line)...), 0o644); err != nil {
		t.Fatalf("append go.mod: %v", err)
	}
}
