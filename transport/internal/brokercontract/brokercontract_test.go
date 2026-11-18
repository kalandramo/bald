package brokercontract

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/transport"
)

// ---- 测试家族桩：OPT=string 的最小装配面 ----

type fakeServer struct{ kind string }

func (f *fakeServer) Start(context.Context) error { return nil }
func (f *fakeServer) Stop(context.Context) error  { return nil }
func (f *fakeServer) Endpoint() string            { return "" }

var _ transport.Server = &fakeServer{}

// stubSegment 家族子段桩（bootstrapv1.Server_Kafka/Rabbitmq 的同形状最小面）。
type stubSegment struct {
	addrs []string
	codec string
}

func (s *stubSegment) GetAddrs() []string { return s.addrs }
func (s *stubSegment) GetCodec() string   { return s.codec }

// testFactory 家族桩工厂：Section 闭包直返注入的桩段（seg=nil 模拟段缺失——
// 生产路径经 cfg.GetKafka() 等取段，typed-nil 守卫由各家族闭包承担，已另测）。
func testFactory(kind, key string, seg *stubSegment) Factory[string] {
	return Factory[string]{
		Kind: kind,
		Key:  key,
		Section: func(*bootstrapv1.Server) Segment {
			if seg == nil {
				return nil
			}
			return seg
		},
		EmptyAddrErr: func(i int) error {
			return errors.New(kind + ": " + key + ".addrs[" + itoa(i) + "] is empty (family-specific wording)")
		},
		WithAddrs: func(addrs []string) string { return "addrs=" + join(addrs) },
		WithCodec: func(codec string) string { return "codec=" + codec },
		NewServer: func(opts ...string) transport.Server { return &fakeServer{kind: kind + "|" + join(opts)} },
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	digits := ""
	for ; i > 0; i /= 10 {
		digits = string(rune('0'+i%10)) + digits
	}
	return digits
}

func join(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ","
		}
		out += s
	}
	return out
}

// TestBuild_SectionMissingSkips 段 nil → nil server（合法跳过，非错误）。
func TestBuild_SectionMissingSkips(t *testing.T) {
	f := testFactory("stub", "server.stub", nil)
	srv, cleanup, err := f.Build(context.Background(), &bootstrapv1.Server{}, nil, nil)
	if err != nil || srv != nil || cleanup != nil {
		t.Fatalf("srv=%v cleanup 非 nil=%v err=%v, want 全 nil（跳过）", srv, cleanup != nil, err)
	}
}

// TestBuild_EmptyAddrsFailFast 配段不给地址 → addrs-required 文案（段键与
// 家族短名各就各位——审阅曾抓出第三动词被 Key 污染成 "remove the server.stub
// section"，此处钉死原文形状）。
func TestBuild_EmptyAddrsFailFast(t *testing.T) {
	f := testFactory("stub", "server.stub", &stubSegment{})
	_, _, err := f.Build(context.Background(), &bootstrapv1.Server{}, nil, nil)
	if err == nil {
		t.Fatal("空 addrs 必须 fail-fast")
	}
	want := "stub: server.stub.addrs is required (remove the stub section to disable)"
	if err.Error() != want {
		t.Fatalf("err = %q, want %q（括号内是家族短名 Kind，不是段键）", err.Error(), want)
	}
}

// TestBuild_EmptyElementFamilyWording 空元素 → 家族文案钩子（下标参数可达）。
func TestBuild_EmptyElementFamilyWording(t *testing.T) {
	f := testFactory("stub", "server.stub", &stubSegment{addrs: []string{"a:1", ""}})
	_, _, err := f.Build(context.Background(), &bootstrapv1.Server{}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "addrs[1] is empty") {
		t.Fatalf("err = %v, want 家族文案含下标 [1]", err)
	}
}

// TestBuild_CodecUnregisteredFailFast codec 契约值非空但未注册 → 装配期报错，
// 文案带注册指引（比首包诚实）。
func TestBuild_CodecUnregisteredFailFast(t *testing.T) {
	f := testFactory("stub", "server.stub", &stubSegment{addrs: []string{"a:1"}, codec: "nope"})
	_, _, err := f.Build(context.Background(), &bootstrapv1.Server{}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), `codec "nope" is not registered`) {
		t.Fatalf("err = %v, want 未注册 codec 装配期报错", err)
	}
}

// TestBuild_OptionOrderAndAfterBuildWrap 契约字段在前业务在后（fakeServer.kind
// 携带选项序）+ afterBuild 错误包裹 Kind 前缀且丢弃已构造的 server（与原实现
// 同语义：装配失败不返回半成品）。
func TestBuild_OptionOrderAndAfterBuildWrap(t *testing.T) {
	f := testFactory("stub", "server.stub", &stubSegment{addrs: []string{"a:1"}})
	boom := errors.New("hook boom")
	after := []func(context.Context, transport.Server) error{
		func(context.Context, transport.Server) error { return boom },
	}
	srv, _, err := f.Build(context.Background(),
		&bootstrapv1.Server{},
		[]string{"biz-opt"}, after)
	if !errors.Is(err, boom) || !strings.HasPrefix(err.Error(), "stub: after-build hook: ") {
		t.Fatalf("err = %v, want Kind 前缀包裹的业务错误", err)
	}
	if srv != nil {
		t.Fatal("afterBuild 失败必须丢弃 server（装配失败不返回半成品）")
	}

	// 成功路径的选项序见证。
	ok := []func(context.Context, transport.Server) error{
		func(context.Context, transport.Server) error { return nil },
	}
	srv, _, err = f.Build(context.Background(), &bootstrapv1.Server{}, []string{"biz-opt"}, ok)
	if err != nil {
		t.Fatalf("成功路径不应报错: %v", err)
	}
	if got := srv.(*fakeServer).kind; got != "stub|addrs=a:1,biz-opt" {
		t.Fatalf("选项序 = %q, want 契约字段在前业务在后", got)
	}
}
