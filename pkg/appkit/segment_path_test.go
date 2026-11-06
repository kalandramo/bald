// 段路径反射下钻对 repeated message 字段的姿势（§7.37）。
//
// 契约里唯一的 repeated message 字段是 logger.backends（bconf/proto/
// bootstrap/v1/log.proto:11）。四处下钻原本只判 Kind()==MessageKind 而不判
// IsList，于是 protoreflect.Value.Message() 撞上 list 直接 panic（「type
// mismatch: cannot convert list to message」）——「白名单名字必须是真实段路径，
// 拼错当场报错」这条不变量在 logger.backends 这个名字上被换成一次启动期崩溃。
//
// 口径：repeated 字段不是一个段——segmentPathKnown 判 false（白名单给出可读的
// unknown-path 错误）、segmentPresent 判 false（providerTriggered/L3 覆盖集都不
// 认它）、presentChildSegments 不列它、segmentMessage 返回 nil、variant 路径报错。
// 已知不对称：声明 logger.* 的 Provider 顶层名仍是 logger，因此按日志专用通道
// 构建（phaseLogger 不过 providerTriggered 闸门），本测试把这层也钉住。
package appkit

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/reflect/protoreflect"

	bconf "github.com/kalandramo/bald/bconf"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	log "github.com/kalandramo/bald/log"
)

// callNoPanic 把 panic 转成可读判红：panic 若直接冒泡会炸掉整个测试二进制，
// 把同批其它判据一起带走。
func callNoPanic(t *testing.T, name string, fn func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("%s panicked instead of returning: %v", name, r)
		}
	}()
	fn()
}

func newRepeatedFieldCfg(t *testing.T) *bootstrapv1.BootstrapConfig {
	t.Helper()
	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)
	cfg.Logger = &bootstrapv1.Logger{Backends: []*bootstrapv1.Logger_Backend{{Type: "slog"}}}
	cfg.Server.Asynq = &bootstrapv1.Server_Asynq{RedisAddress: "127.0.0.1:6379"}
	return cfg
}

func TestSegmentPath_RepeatedMessageFieldIsNotASegment(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := newRepeatedFieldCfg(t)

	t.Run("helper 直读：list 字段判成不是段且不 panic", func(t *testing.T) {
		cases := []struct {
			path    string
			known   bool
			present bool
		}{
			{"logger", true, true},
			{"logger.backends", false, false}, // 契约里唯一的 repeated message 字段
			{"server", true, true},
			{"server.asynq", true, true},
		}
		for _, tc := range cases {
			var known, present bool
			callNoPanic(t, "segmentPathKnown("+tc.path+")", func() { known = segmentPathKnown(tc.path) })
			callNoPanic(t, "segmentPresent("+tc.path+")", func() { present = segmentPresent(cfg, tc.path) })
			if known != tc.known {
				t.Errorf("segmentPathKnown(%q) = %v, want %v", tc.path, known, tc.known)
			}
			if present != tc.present {
				t.Errorf("segmentPresent(%q) = %v, want %v", tc.path, present, tc.present)
			}
		}
		var children []string
		callNoPanic(t, "presentChildSegments(Logger)", func() {
			children = presentChildSegments(cfg.GetLogger().ProtoReflect())
		})
		for _, c := range children {
			if c == "backends" {
				t.Errorf("presentChildSegments(Logger) lists the repeated field: %v", children)
			}
		}
	})

	// 白名单在装配期（newProviderPlan）判定，不经 Run——错误在 assemble 返回。
	t.Run("端到端：白名单写 logger.backends 报可读错误而不是崩溃", func(t *testing.T) {
		_, err := assembleForTestErr(newRepeatedFieldCfg(t), WithUnimplementedSegments("logger.backends"))
		if err == nil {
			t.Fatal("WithUnimplementedSegments(\"logger.backends\") accepted, want unknown-path error")
		}
		for _, w := range []string{"unknown bootstrap segment path", "logger.backends"} {
			if !strings.Contains(err.Error(), w) {
				t.Fatalf("error %q does not contain %q", err.Error(), w)
			}
		}
		// 正反对照：真实段名仍进白名单，否则这条判据是被调窄出来的。
		if _, err := assembleForTestErr(newRepeatedFieldCfg(t),
			WithUnimplementedSegments("server.asynq")); err != nil {
			t.Errorf("WithUnimplementedSegments(\"server.asynq\") = %v, want nil", err)
		}
	})

	// 声明 logger.backends 的 Provider 顶层名是 logger，走日志专用通道
	// （phaseLogger 不过 providerTriggered 闸门），因此必须返回 log.Logger。
	// 修复前的红法发生在 Run 的 BeforeStart：L3 的 triggeredPaths 反射下钻
	// 撞上 list 直接 panic 并打崩测试二进制；修复后启动照常走完。
	t.Run("端到端：Provider 声明 logger.backends 不崩溃且按 logger 通道构建", func(t *testing.T) {
		var built bool
		prov := segProvider{segs: []string{"logger.backends"},
			build: func(context.Context, ApplyScope) (any, Cleanup, error) {
				built = true
				return log.GetLogger(), nil, nil
			}}
		a := assembleForTest(t, newRepeatedFieldCfg(t), WithUnimplementedSegments("server.asynq"),
			WithHTTP(new(http.ServeMux)), prov, StandardHTTPProvider(), StandardGRPCProvider())
		runBriefly(t, a, 30*time.Millisecond)
		if !built {
			t.Fatal("logger-phase Provider was not built — the phaseLogger channel no longer reaches repeated paths")
		}
	})

	// 同一条姿势在四处下钻都要判到（只修一处等于把 panic 留在另一条用户可写的
	// 入口上）：白名单校验、段存在性、子段枚举之外，还有计划摘要与 variant 补丁路径。
	t.Run("另两处下钻：segmentMessage 与 variant 路径同样只报错", func(t *testing.T) {
		c := newRepeatedFieldCfg(t)
		var got protoreflect.Message
		callNoPanic(t, "segmentMessage(logger.backends)", func() { got = segmentMessage(c, "logger.backends") })
		if got != nil {
			t.Errorf("segmentMessage(logger.backends) = %v, want nil", got)
		}
		// 正反对照：真实段仍要能取到，否则这条判据是被调窄出来的。
		var srv protoreflect.Message
		callNoPanic(t, "segmentMessage(server.asynq)", func() { srv = segmentMessage(c, "server.asynq") })
		if srv == nil {
			t.Error("segmentMessage(server.asynq) = nil, want the asynq section")
		}

		var err error
		callNoPanic(t, `bootstrapStringField(logger.backends.type)`, func() {
			_, err = bootstrapStringField(c, "logger.backends.type")
		})
		if err == nil {
			t.Error("bootstrapStringField accepted a path through a repeated field, want an error")
		} else if !strings.Contains(err.Error(), "logger.backends") {
			t.Errorf("error %q does not name the offending path", err)
		}
	})
}
