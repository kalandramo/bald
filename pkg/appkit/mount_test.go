package appkit

import (
	"context"
	"errors"
	"testing"

	"github.com/kalandramo/bald/pkg/audit"
	"github.com/kalandramo/bald/pkg/authn"
)

// ---- Registry[T].Mount/Unmount ----

// TestRegistry_MountUndo 契约：挂载生效 → undo 后失效 → undo 幂等（重复无副作用）。
func TestRegistry_MountUndo(t *testing.T) {
	r := NewRegistry[string]()
	undo, err := r.Mount("tool-a", "impl-a")
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if got, ok := r.Get("tool-a"); !ok || got != "impl-a" {
		t.Fatalf("after mount Get = (%q,%v), want (impl-a,true)", got, ok)
	}

	undo()
	if got, ok := r.Get("tool-a"); ok || got != "" {
		t.Fatalf("after undo Get = (%q,%v), want (\"\",false)", got, ok)
	}
	undo() // 幂等：二次调用无 panic 无副作用
	if _, ok := r.items["tool-a"]; ok {
		t.Fatal("undo must be idempotent")
	}
}

// TestRegistry_MountConflict 契约：与 Register/Mount 互斥冲突——同名已存在
// （无论谁写入）挂载失败且不覆盖。
func TestRegistry_MountConflict(t *testing.T) {
	r := NewRegistry[string]()
	_ = r.Register("tool", "from-register")
	if _, err := r.Mount("tool", "from-mount"); err == nil {
		t.Fatal("Mount over Register name must fail")
	}
	if got, ok := r.Get("tool"); !ok || got != "from-register" {
		t.Fatalf("Mount must not overwrite, got (%q,%v)", got, ok)
	}

	undo, err := r.Mount("tool2", "m1")
	if err != nil {
		t.Fatalf("Mount tool2: %v", err)
	}
	if _, err := r.Mount("tool2", "m2"); err == nil {
		t.Fatal("second Mount same name must fail")
	}
	if got, ok := r.Get("tool2"); !ok || got != "m1" {
		t.Fatalf("second Mount must not overwrite, got (%q,%v)", got, ok)
	}
	undo()
}

// TestRegistry_UnmountMissing 契约：卸载不存在的条目返回零值+false（无 panic）。
func TestRegistry_UnmountMissing(t *testing.T) {
	r := NewRegistry[string]()
	got, ok := r.Unmount("nope")
	if ok || got != "" {
		t.Fatalf("Unmount missing = (%q,%v), want (%q,false)", got, ok, "")
	}
}

// ---- AppKit.MountComponent / UnmountComponent ----

// TestMountComponent_NotRunning 契约：Run 之前调用报 ErrNotRunning。
func TestMountComponent_NotRunning(t *testing.T) {
	app := New()
	err := app.MountComponent(context.Background(), ComponentFunc("x", nil))
	if !errors.Is(err, ErrNotRunning) {
		t.Fatalf("want ErrNotRunning, got %v", err)
	}
	if err := app.UnmountComponent(context.Background(), "x"); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("unmount want ErrNotRunning, got %v", err)
	}
}

// TestMountComponent_Lifecycle 契约：运行期挂载即时 Start 并纳入停机序列；
// 卸载即时 Dispose 且停机不再触碰（LIFO 移除）；ListComponents 反映现状。
func TestMountComponent_Lifecycle(t *testing.T) {
	l := &compLog{}
	dyn := &fakeComp{name: "dyn", log: l}
	var mountErr, unmountErr, reUnmountErr error
	var app *AppKit

	app = New(
		Components(&fakeComp{name: "base", log: l}),
		AfterStart(func(ctx context.Context) error {
			mountErr = app.MountComponent(ctx, dyn)
			names := app.ListComponents()
			if len(names) != 2 || names[0] != "base" || names[1] != "dyn" {
				t.Errorf("ListComponents after mount = %v, want [base dyn]", names)
			}
			unmountErr = app.UnmountComponent(ctx, "dyn")
			reUnmountErr = app.UnmountComponent(ctx, "dyn") // 未挂载 → 报错
			if names := app.ListComponents(); len(names) != 1 || names[0] != "base" {
				t.Errorf("ListComponents after unmount = %v, want [base]", names)
			}
			return nil
		}),
	)

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = app.Run(ctx) }()
	cancel()
	<-app.Done()

	if mountErr != nil {
		t.Fatalf("mount: %v", mountErr)
	}
	if unmountErr != nil {
		t.Fatalf("unmount: %v", unmountErr)
	}
	if reUnmountErr == nil {
		t.Fatal("second unmount should fail with not mounted")
	}

	// 顺序：base 启动 → dyn 挂载启动 → dyn 即时销毁（unmount）→ 停机仅销毁 base。
	want := []string{"start:base", "start:dyn", "dispose:dyn", "dispose:base"}
	if got := l.snapshot(); !equalStrings(got, want) {
		t.Fatalf("lifecycle order = %v, want %v", got, want)
	}
}

// TestMountComponent_MountFailureRollback 契约：挂载的组件 Start 失败 →
// 返回错误、不进入停机序列（后续 stopAll 不会触碰它）。
func TestMountComponent_MountFailureRollback(t *testing.T) {
	l := &compLog{}
	var app *AppKit
	app = New(
		Components(&fakeComp{name: "base", log: l}),
		AfterStart(func(ctx context.Context) error {
			return app.MountComponent(ctx, &fakeComp{name: "boom", log: l, startErr: errors.New("no resource")})
		}),
	)

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = app.Run(ctx) }()
	cancel()
	<-app.Done()

	// boom Start 失败：无 dispose（未入列）；停机仅销毁 base。
	want := []string{"start:base", "start:boom", "dispose:base"}
	if got := l.snapshot(); !equalStrings(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

// ---- 运行时重组审计（§6.2） ----

// mountAuditMem 捕获审计事件的内存 Auditor。
type mountAuditMem struct{ events []audit.AuditEvent }

func (m *mountAuditMem) Record(_ context.Context, ev audit.AuditEvent) {
	m.events = append(m.events, ev)
}

// TestAuditComponent_EventShape 契约：重组审计事件三元组 + Meta + Subject（取 ctx claims）。
func TestAuditComponent_EventShape(t *testing.T) {
	old := audit.GetAuditor()
	am := &mountAuditMem{}
	audit.SetAuditor(am)
	t.Cleanup(func() { audit.SetAuditor(old) })

	app := New()
	ctx := authn.ContextWithAuthClaims(context.Background(),
		&authn.AuthClaims{Subject: "u-admin", TenantID: "t-1"})
	app.auditComponent(ctx, "mount", "tool.echo")
	app.auditComponent(context.Background(), "unmount", "tool.echo") // 匿名：Subject 空

	if len(am.events) != 2 {
		t.Fatalf("want 2 events, got %d", len(am.events))
	}
	ev := am.events[0]
	if ev.Subject != "u-admin" || ev.Object != "component" || ev.Action != "mount" || ev.Result != audit.ResultAllow {
		t.Errorf("mount event mismatch: %+v", ev)
	}
	if ev.Meta["component"] != "tool.echo" {
		t.Errorf("meta component = %v, want tool.echo", ev.Meta["component"])
	}
	if am.events[1].Subject != "" || am.events[1].Action != "unmount" {
		t.Errorf("unmount event mismatch: %+v", am.events[1])
	}
}

// TestAuditComponent_PanicIsolated 契约：Auditor panic 被隔离，不影响调用方。
func TestAuditComponent_PanicIsolated(t *testing.T) {
	old := audit.GetAuditor()
	audit.SetAuditor(auditorFunc(func(context.Context, audit.AuditEvent) { panic("boom") }))
	t.Cleanup(func() { audit.SetAuditor(old) })

	app := New()
	done := make(chan struct{})
	go func() {
		defer close(done)
		app.auditComponent(context.Background(), "mount", "x") // 不应向调用方传播 panic
	}()
	<-done
}

// auditorFunc 函数适配器（测试用）。
type auditorFunc func(context.Context, audit.AuditEvent)

func (f auditorFunc) Record(ctx context.Context, ev audit.AuditEvent) { f(ctx, ev) }
