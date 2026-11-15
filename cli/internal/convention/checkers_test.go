package convention

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTree 在临时目录里落一棵文件树（rel → 内容），返回根。
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// runChecker 跑一条 checker 并返回 findings。
func runChecker(t *testing.T, fn checker, dir string) []Finding {
	t.Helper()
	findings, skip, err := fn(dir)
	if err != nil {
		t.Fatalf("checker error: %v", err)
	}
	if skip != "" {
		t.Fatalf("unexpected skip: %q", skip)
	}
	return findings
}

// goMod 是一份最小 go.mod（checker 以它为「这是 Go 项目」的判据）。
const goMod = "module example.com/demo\n"

// TestNewCheckers 是检查器族的形态矩阵：每条 checker 至少覆盖「命中」与
// 「不命中/豁免」两侧。夹具刻意写成真实项目的形状（internal/<app>/<layer>），
// 因为多数 checker 的判定与目录有关。
func TestNewCheckers(t *testing.T) {
	t.Run("berrors-with-discard", func(t *testing.T) {
		hit := writeTree(t, map[string]string{
			"go.mod": goMod,
			"biz.go": `package biz

import "github.com/kalandramo/bald/berrors"

func f() {
	berrors.NotFound("USER").WithDetails(nil)
}
`,
		})
		if got := runChecker(t, checkBerrorsWithDiscard, hit); len(got) != 1 || got[0].RuleID != "errors-r03" {
			t.Fatalf("findings=%v, want 1 errors-r03", got)
		}
		// 链式与实参形态不报（prometheus builder / protobuf 赋值链）。
		miss := writeTree(t, map[string]string{
			"go.mod": goMod,
			"biz.go": `package biz

import "github.com/kalandramo/bald/berrors"

func f() error {
	e := berrors.NotFound("USER")
	return e.WithDetails(nil)
}
`,
		})
		if got := runChecker(t, checkBerrorsWithDiscard, miss); len(got) != 0 {
			t.Fatalf("findings=%v, want clean", got)
		}
	})

	t.Run("errors-bridge-import", func(t *testing.T) {
		hit := writeTree(t, map[string]string{
			"go.mod":                    goMod,
			"internal/demo/biz/post.go": "package biz\n\nimport \"github.com/kalandramo/bald/berrors/httperr\"\n\nvar _ = httperr.StatusCode\n",
		})
		if got := runChecker(t, checkErrorsBridgeImport, hit); len(got) != 1 || got[0].RuleID != "errors-r01" {
			t.Fatalf("findings=%v, want 1 errors-r01", got)
		}
		// handler 是投影边界，不判。
		miss := writeTree(t, map[string]string{
			"go.mod":                        goMod,
			"internal/demo/handler/post.go": "package handler\n\nimport \"github.com/kalandramo/bald/berrors/httperr\"\n\nvar _ = httperr.StatusCode\n",
		})
		if got := runChecker(t, checkErrorsBridgeImport, miss); len(got) != 0 {
			t.Fatalf("findings=%v, want clean (handler 是投影边界)", got)
		}
	})

	t.Run("assembly-import-in-biz", func(t *testing.T) {
		hit := writeTree(t, map[string]string{
			"go.mod":                    goMod,
			"internal/demo/store/s.go":  "package store\n\nimport \"github.com/kalandramo/bald/pkg/appkit\"\n\nvar _ = appkit.Key[int]{}\n",
			"internal/demo/model/m.go":  "package model\n\nimport appkit \"github.com/kalandramo/bald/pkg/appkit\"\n\nvar _ = appkit.Key[int]{}\n",
			"internal/bootstrap/asm.go": "package bootstrap\n\nimport \"github.com/kalandramo/bald/pkg/appkit\"\n\nvar _ appkit.Key[int]\n",
		})
		got := runChecker(t, checkAssemblyImportInBusiness, hit)
		if len(got) != 2 {
			t.Fatalf("findings=%v, want 2（biz/store/model 才判，bootstrap 不判）", got)
		}
	})

	t.Run("ctx-string-key", func(t *testing.T) {
		hit := writeTree(t, map[string]string{
			"go.mod": goMod,
			"a.go": `package a

import "context"

func f(ctx context.Context) context.Context {
	return context.WithValue(ctx, "user_id", 1)
}
`,
		})
		if got := runChecker(t, checkCtxStringKey, hit); len(got) != 1 || got[0].RuleID != "context-r01" {
			t.Fatalf("findings=%v, want 1 context-r01", got)
		}
		miss := writeTree(t, map[string]string{
			"go.mod": goMod,
			"a.go": `package a

import "context"

type key struct{}

func f(ctx context.Context) context.Context {
	return context.WithValue(ctx, key{}, 1)
}
`,
		})
		if got := runChecker(t, checkCtxStringKey, miss); len(got) != 0 {
			t.Fatalf("findings=%v, want clean（类型键交评审）", got)
		}
	})

	t.Run("validate-signature", func(t *testing.T) {
		hit := writeTree(t, map[string]string{
			"go.mod": goMod,
			"v.go": `package v

import "context"

type Req struct{}

type v struct{}

func (v) ValidateCreate(ctx context.Context) error { return nil }
`,
		})
		if got := runChecker(t, checkValidateSignature, hit); len(got) != 1 || got[0].RuleID != "validation-r02" {
			t.Fatalf("findings=%v, want 1 validation-r02", got)
		}
		miss := writeTree(t, map[string]string{
			"go.mod": goMod,
			"v.go": `package v

import "context"

type CreateRequest struct{}

type v struct{}

func (v) ValidateCreate(ctx context.Context, req *CreateRequest) error { return nil }

func (v) Validate() error { return nil }
`,
		})
		if got := runChecker(t, checkValidateSignature, miss); len(got) != 0 {
			t.Fatalf("findings=%v, want clean（合规签名 + 裸 Validate 豁免）", got)
		}
	})

	t.Run("store-tenant-init", func(t *testing.T) {
		hit := writeTree(t, map[string]string{
			"go.mod": goMod,
			"s.go": `package s

import "github.com/kalandramo/bald/pkg/store"

func init() {
	store.RegisterTenant("tenant_id", store.DefaultTenantFunc)
}
`,
		})
		if got := runChecker(t, checkStoreTenantInit, hit); len(got) != 1 || got[0].RuleID != "store-r02" {
			t.Fatalf("findings=%v, want 1 store-r02", got)
		}
	})

	t.Run("assembly-plan-impure", func(t *testing.T) {
		hit := writeTree(t, map[string]string{
			"go.mod": goMod,
			"p.go": `package p

import (
	"context"
	"os"
)

type provider struct{}

func (provider) Plan(ctx context.Context) ([]string, error) {
	if os.Getenv("X") != "" {
		return nil, nil
	}
	return nil, nil
}
`,
		})
		if got := runChecker(t, checkAssemblyPlanImpure, hit); len(got) != 1 || got[0].RuleID != "assembly-r01" {
			t.Fatalf("findings=%v, want 1 assembly-r01", got)
		}
		miss := writeTree(t, map[string]string{
			"go.mod": goMod,
			"p.go": `package p

import "context"

type provider struct{}

func (provider) Plan(ctx context.Context) ([]string, error) {
	return []string{"a"}, nil
}

func build() { _ = context.Background() }
`,
		})
		if got := runChecker(t, checkAssemblyPlanImpure, miss); len(got) != 0 {
			t.Fatalf("findings=%v, want clean（副作用在 Plan 之外）", got)
		}
	})

	t.Run("http-envelope-shape", func(t *testing.T) {
		hit := writeTree(t, map[string]string{
			"go.mod": goMod,
			"r.go": `package r

type Resp struct {
	Code    int         ` + "`json:\"code\"`" + `
	Data    interface{} ` + "`json:\"data\"`" + `
	Message string      ` + "`json:\"message\"`" + `
}
`,
		})
		if got := runChecker(t, checkHTTPEnvelopeShape, hit); len(got) != 1 || got[0].RuleID != "errors-r08" {
			t.Fatalf("findings=%v, want 1 errors-r08", got)
		}
		// google.rpc.Status 形状（code/message/details）不判。
		miss := writeTree(t, map[string]string{
			"go.mod": goMod,
			"r.go": `package r

type StatusBody struct {
	Code    uint32        ` + "`json:\"code\"`" + `
	Message string        ` + "`json:\"message\"`" + `
	Details []interface{} ` + "`json:\"details\"`" + `
}
`,
		})
		if got := runChecker(t, checkHTTPEnvelopeShape, miss); len(got) != 0 {
			t.Fatalf("findings=%v, want clean（StatusBody 是正确形状）", got)
		}
	})

	t.Run("transport-stop-background", func(t *testing.T) {
		hit := writeTree(t, map[string]string{
			"go.mod": goMod,
			"s.go": `package s

import (
	"context"
	"net/http"
)

type server struct{ srv *http.Server }

func (s *server) Stop(ctx context.Context) error {
	return s.srv.Shutdown(context.Background())
}
`,
		})
		if got := runChecker(t, checkTransportStopBackground, hit); len(got) != 1 || got[0].RuleID != "transport-r06" {
			t.Fatalf("findings=%v, want 1 transport-r06", got)
		}
		// Stop 里用 Background 打日志（不调 Shutdown）不判。
		miss := writeTree(t, map[string]string{
			"go.mod": goMod,
			"s.go": `package s

import (
	"context"
	"errors"
	"log"
)

func (s *server) Stop(ctx context.Context) error {
	log.Println("stop", context.Background())
	return errors.New("x")
}
`,
		})
		if got := runChecker(t, checkTransportStopBackground, miss); len(got) != 0 {
			t.Fatalf("findings=%v, want clean（未丢弃 ctx 调 Shutdown）", got)
		}
	})

	t.Run("testing-bare-addr", func(t *testing.T) {
		hit := writeTree(t, map[string]string{
			"go.mod":         goMod,
			"server_test.go": "package s\n\nimport (\n\t\"net\"\n\t\"testing\"\n)\n\nfunc TestX(t *testing.T) { ln, _ := net.Listen(\"tcp\", \":8080\"); _ = ln }\n",
		})
		if got := runChecker(t, checkTestingBareAddr, hit); len(got) != 1 || got[0].RuleID != "testing-r03" {
			t.Fatalf("findings=%v, want 1 testing-r03", got)
		}
		miss := writeTree(t, map[string]string{
			"go.mod":         goMod,
			"server_test.go": "package s\n\nimport (\n\t\"net\"\n\t\"testing\"\n)\n\nfunc TestX(t *testing.T) { ln, _ := net.Listen(\"tcp\", \"127.0.0.1:8080\"); _ = ln }\n",
		})
		if got := runChecker(t, checkTestingBareAddr, miss); len(got) != 0 {
			t.Fatalf("findings=%v, want clean（带 loopback 前缀）", got)
		}
		// 配置声明里的 addr 不是监听调用位——这是第一版判定踩过的误报
		// （cli 的 16 条命中全是 appspec 里的 addr 片段）。
		decl := writeTree(t, map[string]string{
			"go.mod":       goMod,
			"spec_test.go": "package s\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) { _ = `{\"http\":{\"addr\":\":8080\"}}` }\n",
		})
		if got := runChecker(t, checkTestingBareAddr, decl); len(got) != 0 {
			t.Fatalf("findings=%v, want clean（声明值不是监听地址）", got)
		}
		// ":0" 由内核分配端口，客户端回读实际地址，不判。
		ephemeral := writeTree(t, map[string]string{
			"go.mod":         goMod,
			"server_test.go": "package s\n\nimport (\n\t\"net\"\n\t\"testing\"\n)\n\nfunc TestX(t *testing.T) { ln, _ := net.Listen(\"tcp\", \":0\"); _ = ln }\n",
		})
		if got := runChecker(t, checkTestingBareAddr, ephemeral); len(got) != 0 {
			t.Fatalf("findings=%v, want clean（:0 由内核分配）", got)
		}
		// 生产文件里的裸端口不归本规则管（testing-r03 只管测试地址）。
		prod := writeTree(t, map[string]string{
			"go.mod":  goMod,
			"main.go": "package s\n\nimport \"net\"\n\nfunc main() { ln, _ := net.Listen(\"tcp\", \":8080\"); _ = ln }\n",
		})
		if got := runChecker(t, checkTestingBareAddr, prod); len(got) != 0 {
			t.Fatalf("findings=%v, want clean（非测试文件不判）", got)
		}
	})

	t.Run("store-handwritten-tenant", func(t *testing.T) {
		hit := writeTree(t, map[string]string{
			"go.mod":                    goMod,
			"internal/demo/biz/post.go": "package biz\n\ntype post struct{ TenantID string }\n\nfunc f(p *post) { p.TenantID = \"t1\" }\n",
		})
		if got := runChecker(t, checkStoreHandwrittenTenant, hit); len(got) != 1 || got[0].RuleID != "store-r10" {
			t.Fatalf("findings=%v, want 1 store-r10", got)
		}
		// store 层自己写 tenant 是持久化实现的分内事，不判。
		miss := writeTree(t, map[string]string{
			"go.mod":                      goMod,
			"internal/demo/store/post.go": "package store\n\ntype post struct{ TenantID string }\n\nfunc g(p *post) { p.TenantID = \"t1\" }\n",
		})
		if got := runChecker(t, checkStoreHandwrittenTenant, miss); len(got) != 0 {
			t.Fatalf("findings=%v, want clean（store 层豁免）", got)
		}
	})

	t.Run("reuse-vendored-bald", func(t *testing.T) {
		hit := writeTree(t, map[string]string{
			"go.mod":           "module example.com/demo\n",
			"bald-copy/go.mod": "module github.com/kalandramo/bald/log\n",
		})
		if got := runChecker(t, checkReuseVendoredBald, hit); len(got) != 1 || got[0].RuleID != "reuse-r02" {
			t.Fatalf("findings=%v, want 1 reuse-r02", got)
		}
		miss := writeTree(t, map[string]string{
			"go.mod":     "module example.com/demo\n",
			"sub/go.mod": "module example.com/demo/sub\n",
		})
		if got := runChecker(t, checkReuseVendoredBald, miss); len(got) != 0 {
			t.Fatalf("findings=%v, want clean", got)
		}
		// 自身是 bald 仓库 → 整条 skip（96 个 module 都在该路径下）。
		self := writeTree(t, map[string]string{"go.mod": "module github.com/kalandramo/bald\n"})
		if _, skip, err := checkReuseVendoredBald(self); err != nil || skip == "" {
			t.Fatalf("skip=%q err=%v, want skip for bald itself", skip, err)
		}
	})
}

// TestCheckASTSkipSemantics 钉共用外壳的两种「不可判」：没有 go.mod（含 dir
// 本身是普通文件的 ENOTDIR）与服务里没有 Go 源文件。两者都不是违规、也不是通过。
func TestCheckASTSkipSemantics(t *testing.T) {
	empty := t.TempDir()
	if _, skip, err := checkCtxStringKey(empty); err != nil || skip == "" {
		t.Fatalf("空目录 skip=%q err=%v, want skip", skip, err)
	}

	// 有 go.mod 但没有 .go 文件。
	noSrc := writeTree(t, map[string]string{"go.mod": goMod})
	if _, skip, err := checkCtxStringKey(noSrc); err != nil || skip != "no go source files in "+noSrc {
		t.Fatalf("无源文件 skip=%q err=%v", skip, err)
	}

	// dir 是普通文件（ENOTDIR）→ skip 而非错误。
	f := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, skip, err := checkCtxStringKey(f); err != nil || skip == "" {
		t.Fatalf("ENOTDIR skip=%q err=%v, want skip", skip, err)
	}
}

// TestCoverage 钉覆盖口径：只算「这一轮真跑到的 checker」绑定的规则，去重计。
func TestCoverage(t *testing.T) {
	res := &checkResult{
		Rules: 205,
		OK:    []string{"gen-app-drift", "logging-stdlib", "no-panic", "not-a-checker"},
	}
	covered, total := coverage(res)
	if covered != 3 || total != 205 {
		t.Fatalf("coverage=%d/%d, want 3/205", covered, total)
	}
	if got := coverageText(covered, total); got != "3/205 rules (1.5%)" {
		t.Fatalf("coverageText=%q", got)
	}
	if got := coverageText(0, 0); got != "0/0 rules" {
		t.Fatalf("empty coverageText=%q", got)
	}
}
