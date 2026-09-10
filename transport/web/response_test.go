package web

import (
	"encoding/json"
	stderrors "errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/kalandramo/bald/berrors"
)

func init() { gin.SetMode(gin.TestMode) }

// decodeError 把响应体反解为 StatusBody，并顺带校验 JSON 键集完整（decision⑧：
// 空 message/domain/metadata 必须输出 ""/{}/[]，不得被 omitempty 折叠）。
func decodeError(t *testing.T, w *httptest.ResponseRecorder) (int, StatusBody) {
	t.Helper()
	var body StatusBody
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", w.Body.String(), err)
	}
	var raw map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &raw)
	if _, ok := raw["code"]; !ok {
		t.Errorf("code key missing: %s", w.Body.String())
	}
	if _, ok := raw["message"]; !ok {
		t.Errorf("message key missing (empty must emit \"\"): %s", w.Body.String())
	}
	if _, ok := raw["details"]; !ok {
		t.Errorf("details key missing (empty must emit []): %s", w.Body.String())
	}
	return w.Code, body
}

func TestErrorResponseWithReasonAndDetails(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	err := berrors.NotFound("ORDER_NOT_FOUND").
		WithMessage("订单 %s 不存在", "42").
		WithDetails(map[string]string{"id": "42"})

	ErrorResponse(c, err)

	code, body := decodeError(t, w)
	if code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", code)
	}
	if body.Code != berrors.CodeNotFound {
		t.Errorf("body.code = %d, want %d", body.Code, berrors.CodeNotFound)
	}
	if body.Message != "订单 42 不存在" {
		t.Errorf("body.message = %q, want 展示文案而非 Error() 日志串", body.Message)
	}
	if len(body.Details) != 1 {
		t.Fatalf("details len = %d, want 1", len(body.Details))
	}
	d := body.Details[0]
	if d.Type != "type.googleapis.com/google.rpc.ErrorInfo" {
		t.Errorf("details[0].@type = %q", d.Type)
	}
	if d.Reason != "ORDER_NOT_FOUND" {
		t.Errorf("details[0].reason = %q", d.Reason)
	}
	if d.Metadata["id"] != "42" {
		t.Errorf("details[0].metadata = %v", d.Metadata)
	}
}

func TestErrorResponseNoReasonOmitsDetailsEntry(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	ErrorResponse(c, berrors.BadRequest("").WithMessage("%s", "bad input"))

	code, body := decodeError(t, w)
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", code)
	}
	if len(body.Details) != 0 {
		t.Errorf("details = %v, want empty (对齐 gateway：无 ErrorInfo 即无 details 条目)", body.Details)
	}
}

func TestErrorResponseNilMetadataEmitsEmptyObject(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	// 有 reason 无 details：metadata 须输出 {}（protojson EmitUnpopulated 语义），
	// 而非 null。
	ErrorResponse(c, berrors.NotFound("secret"))

	var raw struct {
		Details []struct {
			Metadata map[string]string `json:"metadata"`
		} `json:"details"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw.Details) != 1 || raw.Details[0].Metadata == nil {
		t.Errorf("metadata must be {} not null: %s", w.Body.String())
	}
}

func TestErrorResponsePlainErrorFallsBackInternal(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	ErrorResponse(c, stderrors.New("boom: db down"))

	code, body := decodeError(t, w)
	if code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", code)
	}
	if body.Code != berrors.CodeInternal {
		t.Errorf("body.code = %d, want INTERNAL(13)", body.Code)
	}
	if body.Message != "boom: db down" {
		t.Errorf("body.message = %q, want 原始错误串", body.Message)
	}
	if len(body.Details) != 0 {
		t.Errorf("details = %v, want empty", body.Details)
	}
}

func TestWriteResponseErrorBranchUsesStatusBody(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	WriteResponse[any](c, nil, berrors.NotFound("secret"))

	code, body := decodeError(t, w)
	if code != http.StatusNotFound || body.Details[0].Reason != "secret" {
		t.Errorf("WriteResponse error branch shape: code=%d body=%+v", code, body)
	}
}
