// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/kalandramo/bald/berrors"
	"github.com/kalandramo/bald/pkg/contextx"
)

// 本文件是方案 S 批次 2 的验收面：统一内置件「写一遍，双协议生效」。每件
// 用两座桥各走一遍同一断言——这是统一链的核心承诺。经桥的错误是 gRPC
// status（桥的 grpcerr 收口），协议面断言用 status.Code；直调统一链才持有
// berrors。

// --- Recovery ---

// TestRecoveryUnaryAndGin panic 在两条协议上都转成 Internal（gRPC status /
// 500 状态体），不击穿进程。
func TestRecoveryUnaryAndGin(t *testing.T) {
	boom := func(ctx context.Context, _ interface{}) (interface{}, error) { panic("explode") }

	// gRPC 桥：经 grpcerr 收口后断言协议面。
	_, err := UnaryServerInterceptor(Recovery())(context.Background(), nil,
		&grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/M"}, boom)
	if status.Code(err) != codes.Internal {
		t.Fatalf("panic code = %v, want Internal", status.Code(err))
	}
	if !strings.Contains(err.Error(), "internal panic: explode") {
		t.Errorf("panic message lost: %v", err)
	}

	// 直调统一链（不经桥）：持有 berrors 本体。
	_, direct := Recovery()(func(context.Context, interface{}) (interface{}, error) {
		panic("explode")
	})(context.Background(), nil)
	if werr, ok := berrors.FromError(direct); !ok || werr.Reason != "PANIC" {
		t.Fatalf("direct chain err = %v, want berrors Internal/PANIC", direct)
	}

	// gin 桥：panic 转成 500 + 状态体。
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/v1/x", GinHandler(Recovery()), func(c *gin.Context) { panic("explode") })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/x", nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("gin panic code = %d, want 500", w.Code)
	}
	if !strings.Contains(w.Body.String(), "PANIC") && !strings.Contains(w.Body.String(), "Internal") {
		t.Errorf("body should carry the Internal status body: %s", w.Body.String())
	}
}

// --- RequestID ---

// TestRequestIDUnaryAndGin 同一实现双协议：入站头带 ID 则透传，缺失则生成；
// 响应头回写；context 注入（contextx）。
func TestRequestIDUnaryAndGin(t *testing.T) {
	m := RequestID()

	// gRPC：入站带 ID → 注入 context（大小写归一由 metadataHeader.Get 承担）。
	var gotID string
	in := metadata.NewIncomingContext(context.Background(), metadata.MD{"x-request-id": {"req-42"}})
	_, _ = UnaryServerInterceptor(m)(in, nil, &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/M"},
		func(ctx context.Context, _ interface{}) (interface{}, error) {
			gotID = contextx.RequestIDFromContext(ctx)
			return nil, nil
		})
	if gotID != "req-42" {
		t.Errorf("grpc request id = %q, want req-42", gotID)
	}

	// gin：缺失 → 生成 + 回写响应头。
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/v1/x", GinHandler(m), func(c *gin.Context) {
		gotID = contextx.RequestIDFromContext(c.Request.Context())
		c.Status(http.StatusOK)
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/x", nil))
	if gotID == "" || gotID == "req-42" {
		t.Errorf("generated id missing: %q", gotID)
	}
	if resp := w.Header().Get("X-Request-ID"); resp != gotID {
		t.Errorf("reply header = %q, want %q", resp, gotID)
	}
}

// --- Timeout ---

// TestTimeoutSemantics 钉住统一 Timeout 的三条语义：已有 deadline 原样放过、
// d<=0 不装配、到期体现在 handler 可见的 ctx 上。
func TestTimeoutSemantics(t *testing.T) {
	m := Timeout(30 * time.Millisecond)
	ctx := context.Background()

	// 已有 deadline：不覆盖（不腰斩调用方的预算）。
	clientCtx, clientCancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer clientCancel()
	clientDL, _ := clientCtx.Deadline()
	var seenDL time.Time
	_, _ = m(func(innerCtx context.Context, _ interface{}) (interface{}, error) {
		if dl, ok := innerCtx.Deadline(); ok {
			seenDL = dl
		}
		return nil, nil
	})(clientCtx, nil)
	if !seenDL.Equal(clientDL) {
		t.Error("existing deadline was overridden")
	}

	// 无 deadline：注入 30ms 截止期，到期后 ctx.Err() = DeadlineExceeded。
	err := func() error {
		_, err := m(func(innerCtx context.Context, _ interface{}) (interface{}, error) {
			<-innerCtx.Done()
			return nil, innerCtx.Err()
		})(ctx, nil)
		return err
	}()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("ctx error = %v, want DeadlineExceeded", err)
	}

	// d<=0：直通。
	passed := false
	_, _ = Timeout(0)(func(context.Context, interface{}) (interface{}, error) {
		passed = true
		return nil, nil
	})(ctx, nil)
	if !passed {
		t.Error("d<=0 should pass through")
	}
}

// TestTimeoutViaBridges 统一 Timeout 经两座桥都生效（双协议一份实现的证明）。
// gRPC 面经 grpcerr 收口后是 DeadlineExceeded status；gin 面是 ctx-deadline
// 语义（handler 的 ctx 会到期——与旧 gin 链「响应硬返回」刻意不同）。
func TestTimeoutViaBridges(t *testing.T) {
	m := Timeout(20 * time.Millisecond)
	sleepy := func(ctx context.Context, _ interface{}) (interface{}, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}

	_, err := UnaryServerInterceptor(m)(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/M"}, sleepy)
	if status.Code(err) != codes.DeadlineExceeded {
		t.Errorf("grpc: code = %v, want DeadlineExceeded", status.Code(err))
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	var ginErr error
	r.GET("/v1/slow", GinHandler(m), func(c *gin.Context) {
		<-c.Request.Context().Done()
		ginErr = c.Request.Context().Err()
		c.Status(http.StatusOK)
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/slow", nil))
	if !errors.Is(ginErr, context.DeadlineExceeded) {
		t.Errorf("gin: err = %v, want DeadlineExceeded", ginErr)
	}
}
