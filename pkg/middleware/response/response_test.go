// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package response_test

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/kalandramo/bald/berrors"
	"github.com/kalandramo/bald/berrors/httperr"
	"github.com/kalandramo/bald/pkg/middleware/response"
)

// TestStatusOfPlainError 覆盖非 berrors 错误兜底分支：INTERNAL + message=err.Error()，
// 无 Reason/Details → details 为空 slice。
func TestStatusOfPlainError(t *testing.T) {
	body := response.StatusOf(errors.New("boom"))
	if body.Code != berrors.CodeInternal {
		t.Fatalf("期望 Code=%d，实际 %d", berrors.CodeInternal, body.Code)
	}
	if body.Message != "boom" {
		t.Fatalf("期望 message=boom，实际 %q", body.Message)
	}
	if len(body.Details) != 0 {
		t.Fatalf("期望空 details，实际 %+v", body.Details)
	}
}

// TestStatusOfWithReason 覆盖 Reason 非空且 Details 为 nil 的分支：单条 ErrorInfo，
// metadata 归一为空对象（protojson 对 nil map 输出 {} 而非 null）。
func TestStatusOfWithReason(t *testing.T) {
	err := berrors.Internal("MY_REASON").WithMessage("内部故障")
	body := response.StatusOf(err)
	if body.Code != berrors.CodeInternal || body.Message != "内部故障" {
		t.Fatalf("Code/Message 不符: %+v", body)
	}
	if len(body.Details) != 1 || body.Details[0].Reason != "MY_REASON" {
		t.Fatalf("期望单条 ErrorInfo(MY_REASON)，实际 %+v", body.Details)
	}
	if len(body.Details[0].Metadata) != 0 {
		t.Fatalf("期望空 metadata，实际 %+v", body.Details[0].Metadata)
	}
}

// TestStatusOfWithDetails 覆盖带 Details 变量的分支：metadata 透传键值对。
func TestStatusOfWithDetails(t *testing.T) {
	err := berrors.NotFound("ORDER_NOT_FOUND").WithDetails(map[string]string{"id": "42"})
	body := response.StatusOf(err)
	if body.Code != berrors.CodeNotFound {
		t.Fatalf("期望 Code=%d，实际 %d", berrors.CodeNotFound, body.Code)
	}
	if len(body.Details) != 1 || body.Details[0].Metadata["id"] != "42" {
		t.Fatalf("期望 metadata.id=42，实际 %+v", body.Details)
	}
}

// TestRenderErrorStatusMapping 覆盖 RenderError：HTTP 状态码 = CodeToHTTP(code)，
// body 为统一 StatusBody。NotFound→404、plain→500 各一例。
func TestRenderErrorStatusMapping(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name string
		err  error
		want int
	}{
		{"not_found", berrors.NotFound("NF"), 404},
		{"internal_plain", errors.New("x"), 500},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			response.RenderError(c, tc.err)
			if w.Code != tc.want {
				t.Fatalf("期望 HTTP %d，实际 %d", tc.want, w.Code)
			}
			var got response.StatusBody
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatalf("响应体非合法 JSON: %v", err)
			}
			if got.Code != httperr.HTTPToCode(tc.want) {
				t.Fatalf("body.Code=%d 与状态码 %d 不一致", got.Code, tc.want)
			}
		})
	}
}
