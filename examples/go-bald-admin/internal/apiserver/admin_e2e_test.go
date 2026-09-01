package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	gingonic "github.com/gin-gonic/gin"

	authbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/auth"
	secretbiz "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/biz/v1/secret"
	bootstrappkg "github.com/kalandramo/bald/examples/go-bald-admin/internal/bootstrap"
	"github.com/kalandramo/bald/pkg/appkit"
)

// tinyServer 是 e2e 用最小 Server（让 AppKit.Run 走完整生命周期，使 running=true
// 从而 MountComponent 可用）；Endpoint 返回确定地址以通过 waitForEndpoints。
type tinyServer struct{}

func (tinyServer) Start(context.Context) error { return nil }
func (tinyServer) Stop(context.Context) error  { return nil }
func (tinyServer) Endpoint() string            { return "http://127.0.0.1:18099" }

// stubComp 可断言启停的组件（验证经管理面挂载的组件真实走了 Start/Dispose）。
type stubComp struct {
	name              string
	mu                sync.Mutex
	started, disposed bool
}

func (c *stubComp) Name() string { return c.name }
func (c *stubComp) Start(context.Context) error {
	c.mu.Lock()
	c.started = true
	c.mu.Unlock()
	return nil
}
func (c *stubComp) Dispose(context.Context) error {
	c.mu.Lock()
	c.disposed = true
	c.mu.Unlock()
	return nil
}
func (c *stubComp) state() (bool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.started, c.disposed
}

// setupAdmin 构造带管理面路由的 engine + 真实运行的 AppKit（A1 原语要求 Run 存活期）。
// 路由注册先于 app 回填（迟到绑定，与 main 装配路径同构）。
func setupAdmin(t *testing.T) (*gingonic.Engine, *stubComp) {
	t.Helper()
	gingonic.SetMode(gingonic.TestMode)
	if err := bootstrappkg.InitBridges(context.Background()); err != nil {
		t.Fatalf("InitBridges: %v", err)
	}

	var app *appkit.AppKit
	comp := &stubComp{name: "e2e.comp"}

	e := gingonic.New()
	RegisterRoutes(e, authbiz.New(bootstrappkg.Signer), secretbiz.New(nil))
	RegisterAdmin(e, func() *appkit.AppKit { return app }, map[string]ComponentFactory{
		"e2e.comp": func() appkit.Component { return comp },
	})

	app = appkit.New(appkit.Name("admin-e2e"), appkit.Servers(tinyServer{}))

	// Cleanup 顺序关键：必须先 cancel 触发 Run 停机，再等 Done——分开注册会因
	// t.Cleanup 的 LIFO 语义变成「先等 Done、后 cancel」互等死锁（曾卡满 timeout）。
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = app.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-app.Done()
	})
	return e, comp
}

// TestAdmin_ComponentsLifecycle 管理面端到端：挂载（组件真实 Start）→观测→卸载
// （组件真实 Dispose）→重复卸载 404→列表不再含该组件。
func TestAdmin_ComponentsLifecycle(t *testing.T) {
	e, comp := setupAdmin(t)
	tok := login(t, e, "admin", "admin123")

	// 挂载。
	body, _ := json.Marshal(map[string]string{"name": "e2e.comp"})
	req := httptest.NewRequest(http.MethodPost, "/admin/components", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST mount: want 201 got %d (%s)", w.Code, w.Body.String())
	}
	if started, _ := comp.state(); !started {
		t.Fatal("mounted component should be started")
	}

	// 挂载后：组件出现在列表。
	w = do(e, http.MethodGet, "/admin/components", tok)
	if !bytes.Contains(w.Body.Bytes(), []byte("e2e.comp")) {
		t.Fatalf("GET after mount should contain e2e.comp: %s", w.Body.String())
	}

	// 卸载。
	w = do(e, http.MethodDelete, "/admin/components/e2e.comp", tok)
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE unmount: want 200 got %d (%s)", w.Code, w.Body.String())
	}
	if _, disposed := comp.state(); !disposed {
		t.Fatal("unmounted component should be disposed")
	}
	// 重复卸载：404（未挂载）。
	if w = do(e, http.MethodDelete, "/admin/components/e2e.comp", tok); w.Code != http.StatusNotFound {
		t.Fatalf("DELETE again: want 404 got %d (%s)", w.Code, w.Body.String())
	}
	// 列表不再含该组件。
	if w = do(e, http.MethodGet, "/admin/components", tok); bytes.Contains(w.Body.Bytes(), []byte("e2e.comp")) {
		t.Fatalf("GET after unmount should not contain e2e.comp: %s", w.Body.String())
	}
}

// TestAdmin_Forbidden viewer 角色无 admin 资源权限 → 403。
func TestAdmin_Forbidden(t *testing.T) {
	e, _ := setupAdmin(t)
	tok := login(t, e, "alice", "alice123")
	if w := do(e, http.MethodGet, "/admin/components", tok); w.Code != http.StatusForbidden {
		t.Fatalf("viewer GET admin: want 403 got %d (%s)", w.Code, w.Body.String())
	}
}

// TestAdmin_Unauthenticated 未认证 → 401（同时验证 M10.1 lazy 修复：main 装配路径
// 同款 lazyAuthenticator 传递，认证真实生效而非空操作）。
func TestAdmin_Unauthenticated(t *testing.T) {
	e, _ := setupAdmin(t)
	if w := do(e, http.MethodGet, "/admin/components", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("no token: want 401 got %d (%s)", w.Code, w.Body.String())
	}
}

// TestAdmin_UnknownFactory 未知工厂名 → 404。
func TestAdmin_UnknownFactory(t *testing.T) {
	e, _ := setupAdmin(t)
	tok := login(t, e, "admin", "admin123")
	body, _ := json.Marshal(map[string]string{"name": "nope"})
	req := httptest.NewRequest(http.MethodPost, "/admin/components", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown factory: want 404 got %d (%s)", w.Code, w.Body.String())
	}
}
