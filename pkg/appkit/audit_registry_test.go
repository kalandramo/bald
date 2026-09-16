package appkit

import (
	"context"
	"errors"
	"strings"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/pkg/audit"
)

// taggedAuditor 带名字标记的桩后端（验证 MultiAuditor 组装顺序=配置列表序）。
type taggedAuditor struct {
	audit.Auditor
	tag string
}

// stubAuditProvider 构造桩 Provider（记录调用）。
func stubAuditProvider(called *bool) AuditProvider {
	return func(context.Context, *bootstrapv1.Audit) (audit.Auditor, func(context.Context) error, error) {
		*called = true
		return audit.NopAuditor(), nil, nil
	}
}

// backendProvider 构造带 tag 与事件记录的 Provider（顺序/回滚/聚合验证用）。
// buildErr 非 nil 时构造失败；cleanupErr 成为 cleanup 返回值。
func backendProvider(tag string, events *[]string, buildErr, cleanupErr error) AuditProvider {
	return func(context.Context, *bootstrapv1.Audit) (audit.Auditor, func(context.Context) error, error) {
		*events = append(*events, "build:"+tag)
		if buildErr != nil {
			return nil, nil, buildErr
		}
		cleanup := func(context.Context) error {
			*events = append(*events, "cleanup:"+tag)
			return cleanupErr
		}
		return taggedAuditor{Auditor: audit.NopAuditor(), tag: tag}, cleanup, nil
	}
}

// auditCfg 构造带 backends 列表的 audit 契约段。
func auditCfg(backends ...string) *bootstrapv1.Audit {
	return &bootstrapv1.Audit{Backends: backends}
}

// TestAuditRegistry_RegisterValidation 契约：空名/nil/重名 fail-fast。
func TestAuditRegistry_RegisterValidation(t *testing.T) {
	r := NewAuditRegistry()
	if err := r.Register("", stubAuditProvider(new(bool))); err == nil {
		t.Error("empty type should fail")
	}
	if err := r.Register("store", nil); err == nil {
		t.Error("nil provider should fail")
	}
	if err := r.Register("store", stubAuditProvider(new(bool))); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if err := r.Register("store", stubAuditProvider(new(bool))); err == nil {
		t.Error("duplicate register should fail")
	}
}

// TestAuditRegistry_Build 契约：段缺省不装配；backends 空/重复/未注册
// fail-fast；log 内置零注册；多后端组装 MultiAuditor（顺序=配置序）。
func TestAuditRegistry_Build(t *testing.T) {
	r := NewAuditRegistry()

	// 段缺省：不装配。
	a, cleanup, err := r.Build(context.Background(), nil)
	if a != nil || cleanup != nil || err != nil {
		t.Fatalf("nil section: a=%v cleanup-set=%v err=%v", a, cleanup != nil, err)
	}

	// 空列表：fail-fast。
	if _, _, err := r.Build(context.Background(), &bootstrapv1.Audit{}); err == nil {
		t.Fatal("empty backends should fail")
	}

	// log 内置：无需注册即得 LoggerAuditor。
	a, cleanup, err = r.Build(context.Background(), auditCfg("log"))
	if err != nil || a == nil || cleanup != nil {
		t.Fatalf("log builtin: a=%v cleanup-set=%v err=%v", a, cleanup != nil, err)
	}
	if _, ok := a.(audit.LoggerAuditor); !ok {
		t.Fatalf("log builtin should return LoggerAuditor, got %T", a)
	}

	// 未注册：fail-fast。
	if _, _, err := r.Build(context.Background(), auditCfg("store")); err == nil {
		t.Fatal("unregistered store should fail")
	}

	// 重复项：fail-fast。
	if _, _, err := r.Build(context.Background(), auditCfg("store", "store")); err == nil {
		t.Fatal("duplicate backend should fail")
	}

	// 多后端：注册后组装 MultiAuditor，顺序=配置列表序。
	var events []string
	r.MustRegister("store", backendProvider("store", &events, nil, nil))
	r.MustRegister("stream", backendProvider("stream", &events, nil, nil))
	a, _, err = r.Build(context.Background(), auditCfg("log", "store", "stream"))
	if err != nil || a == nil {
		t.Fatalf("multi backends: a=%v err=%v", a, err)
	}
	m, ok := a.(audit.MultiAuditor)
	if !ok {
		t.Fatalf("multi backends should return MultiAuditor, got %T", a)
	}
	if len(m) != 3 {
		t.Fatalf("MultiAuditor should have 3 members, got %d", len(m))
	}
	// 成员顺序：log 内置在前（无 tag），store/stream 按配置序。
	if _, isLog := m[0].(audit.LoggerAuditor); !isLog {
		t.Fatalf("member[0] should be LoggerAuditor, got %T", m[0])
	}
	for i, want := range []string{"store", "stream"} {
		tagged, ok := m[i+1].(taggedAuditor)
		if !ok || tagged.tag != want {
			t.Fatalf("member[%d] should be tagged %q, got %T(%+v)", i+1, want, m[i+1], m[i+1])
		}
	}
}

// TestAuditRegistry_BuildRollback 契约：任一项构造失败，逆序回滚已构造项
// 的 cleanup（对齐 bootstrap 六域 Build 模式）。
func TestAuditRegistry_BuildRollback(t *testing.T) {
	var events []string
	r := NewAuditRegistry()
	r.MustRegister("store", backendProvider("store", &events, nil, nil))
	r.MustRegister("stream", backendProvider("stream", &events, errors.New("boom"), nil))

	a, _, err := r.Build(context.Background(), auditCfg("store", "stream"))
	if err == nil || a != nil {
		t.Fatalf("partial failure should fail: a=%v err=%v", a, err)
	}
	if !strings.Contains(err.Error(), "build audit backend") {
		t.Fatalf("error should carry backend name: %v", err)
	}
	// 事件序：store 构造成功 → stream 构造失败（build 事件先记）→ 逆序
	// 回滚 store 的 cleanup（stream 未成功构造，无 cleanup 可回滚）。
	want := []string{"build:store", "build:stream", "cleanup:store"}
	if len(events) != len(want) {
		t.Fatalf("events=%v want %v", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("events[%d]=%q want %q (full=%v)", i, events[i], want[i], events)
		}
	}
}

// TestAuditRegistry_BuildCleanupAggregate 契约：多后端 cleanup 聚合逆序
// 回放（后构造先释放），错误聚合不吞项。
func TestAuditRegistry_BuildCleanupAggregate(t *testing.T) {
	var events []string
	r := NewAuditRegistry()
	r.MustRegister("store", backendProvider("store", &events, nil, nil))
	r.MustRegister("stream", backendProvider("stream", &events, nil, errors.New("flush failed")))

	a, cleanup, err := r.Build(context.Background(), auditCfg("store", "stream"))
	if err != nil || a == nil || cleanup == nil {
		t.Fatalf("build: a=%v cleanup=%v err=%v", a, cleanup != nil, err)
	}
	if err := cleanup(context.Background()); err == nil || !strings.Contains(err.Error(), "flush failed") {
		t.Fatalf("aggregated cleanup should surface member error, got %v", err)
	}
	want := []string{"build:store", "build:stream", "cleanup:stream", "cleanup:store"}
	if len(events) != len(want) {
		t.Fatalf("events=%v want %v", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("events[%d]=%q want %q (full=%v)", i, events[i], want[i], events)
		}
	}
}

// TestBuildAudit 契约：段缺省 no-op；[log] 无 Registry 也装配；
// [store] 无 Registry fail-fast（provider not registered）；
// [store,stream] 有 Registry 全局生效 MultiAuditor、prev 保留。
func TestBuildAudit(t *testing.T) {
	old := audit.GetAuditor()
	t.Cleanup(func() { audit.SetAuditor(old) })

	// 段缺省：no-op（全局不动）。
	st, err := buildAudit(&bootstrapv1.BootstrapConfig{}, &bootstrapSpec{})
	if err != nil || st != nil {
		t.Fatalf("no section: st=%v err=%v", st, err)
	}

	// 空列表：fail-fast。
	cfg := &bootstrapv1.BootstrapConfig{}
	cfg.Audit = &bootstrapv1.Audit{}
	if _, err := buildAudit(cfg, &bootstrapSpec{}); err == nil {
		t.Fatal("empty backends should fail")
	}

	// [log]：无 Registry 也装配（内置）。
	cfg.Audit = auditCfg("log")
	st, err = buildAudit(cfg, &bootstrapSpec{})
	if err != nil {
		t.Fatalf("log builtin: %v", err)
	}
	if st == nil || st.prev != old {
		t.Fatalf("log state should carry prev: %+v", st)
	}
	if _, ok := audit.GetAuditor().(audit.LoggerAuditor); !ok {
		t.Fatalf("global should be LoggerAuditor, got %T", audit.GetAuditor())
	}

	// [store]：无 Registry fail-fast，错误直指缺失后端（升级前为
	// 「no AuditRegistry wired」）。
	cfg.Audit = auditCfg("store")
	_, err = buildAudit(cfg, &bootstrapSpec{})
	if err == nil || !strings.Contains(err.Error(), `provider "store" not registered`) {
		t.Fatalf("store without registry should fail with provider error, got %v", err)
	}

	// [store,stream]：有 Registry 全局生效 MultiAuditor、prev 保留。
	// 先设哨兵基线（[log] 例已替换全局，old 不再是当前值）。
	marker := taggedAuditor{Auditor: audit.NopAuditor(), tag: "prev-marker"}
	audit.SetAuditor(marker)

	var events []string
	spec := &bootstrapSpec{auditRegistry: NewAuditRegistry()}
	spec.auditRegistry.MustRegister("store", backendProvider("store", &events, nil, nil))
	spec.auditRegistry.MustRegister("stream", backendProvider("stream", &events, nil, nil))
	cfg.Audit = auditCfg("store", "stream")
	st, err = buildAudit(cfg, spec)
	if err != nil || st == nil {
		t.Fatalf("store+stream: st=%v err=%v", st, err)
	}
	if _, ok := audit.GetAuditor().(audit.MultiAuditor); !ok {
		t.Fatalf("global should be MultiAuditor, got %T", audit.GetAuditor())
	}
	if st.prev != marker {
		t.Fatalf("state should carry prev marker, got %#v", st.prev)
	}
}
