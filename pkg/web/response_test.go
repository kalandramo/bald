package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	stderrors "errors"
	"testing"

	berrors "github.com/kalandramo/bald/pkg/errors"
)

func TestWriteResponseDirectErrorStatus(t *testing.T) {
	w := httptest.NewRecorder()
	WriteResponse(w, nil, berrors.NotFound("ORDER_NOT_FOUND"))
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
	var body ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Reason != "ORDER_NOT_FOUND" {
		t.Fatalf("expected reason ORDER_NOT_FOUND, got %q", body.Reason)
	}
}

func TestWriteResponseWrappedErrorStatus(t *testing.T) {
	// 关键回归：被 fmt.Errorf("%w") 包裹的 *Error 必须仍映射到正确状态码，
	// 而非兜底 500（WriteResponse 改写前会丢失状态码）。
	w := httptest.NewRecorder()
	wrapped := stderrors.New("db timeout")
	err := berrors.NotFound("ORDER_NOT_FOUND").WithCause(wrapped)
	WriteResponse(w, nil, err)
	if w.Code != http.StatusNotFound {
		t.Fatalf("wrapped error should map to 404, got %d", w.Code)
	}
	var body ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Reason != "ORDER_NOT_FOUND" {
		t.Fatalf("expected reason ORDER_NOT_FOUND, got %q", body.Reason)
	}
}

func TestWriteResponsePlainErrorDefaultsTo500(t *testing.T) {
	w := httptest.NewRecorder()
	WriteResponse(w, nil, errors.New("boom"))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", w.Code)
	}
}

func TestWriteResponseSuccess(t *testing.T) {
	w := httptest.NewRecorder()
	WriteResponse(w, map[string]string{"k": "v"}, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}
