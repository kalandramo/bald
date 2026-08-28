package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	stderrors "errors"
	"testing"

	"github.com/gin-gonic/gin"

	berrors "github.com/kalandramo/bald/pkg/errors"
)

func newCtx() (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	return c, w
}

func TestWriteResponseDirectErrorStatus(t *testing.T) {
	c, w := newCtx()
	ErrorResponse(c, berrors.NotFound("ORDER_NOT_FOUND"))
	if c.Writer.Status() != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", c.Writer.Status())
	}
	var body errorBody
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Code != "ORDER_NOT_FOUND" {
		t.Fatalf("expected code ORDER_NOT_FOUND, got %q", body.Error.Code)
	}
}

func TestWriteResponseWrappedErrorStatus(t *testing.T) {
	// 关键回归：被 fmt.Errorf("%w") 包裹的 *Error 必须仍映射到正确状态码，
	// 而非兜底 500（WriteResponse 改写前会丢失状态码）。
	c, w := newCtx()
	wrapped := stderrors.New("db timeout")
	err := berrors.NotFound("ORDER_NOT_FOUND").WithCause(wrapped)
	ErrorResponse(c, err)
	if c.Writer.Status() != http.StatusNotFound {
		t.Fatalf("wrapped error should map to 404, got %d", c.Writer.Status())
	}
	var body errorBody
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Code != "ORDER_NOT_FOUND" {
		t.Fatalf("expected code ORDER_NOT_FOUND, got %q", body.Error.Code)
	}
}

func TestWriteResponsePlainErrorDefaultsTo500(t *testing.T) {
	c, _ := newCtx()
	ErrorResponse(c, stderrors.New("boom"))
	if c.Writer.Status() != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", c.Writer.Status())
	}
}

func TestWriteResponseSuccess(t *testing.T) {
	c, w := newCtx()
	WriteResponse(c, map[string]string{"k": "v"})
	if c.Writer.Status() != http.StatusOK {
		t.Fatalf("expected 200, got %d", c.Writer.Status())
	}
	if w.Body.String() == "" {
		t.Fatalf("expected non-empty body")
	}
}

func TestWriteResponseNilDataNoBody(t *testing.T)  {
	c, w := newCtx()
	WriteResponse(c, nil)
	if c.Writer.Status() != http.StatusOK {
		t.Fatalf("expected 200, got %d", c.Writer.Status())
	}
	if w.Body.Len() != 0 {
		t.Fatalf("expected empty body, got %q", w.Body.String())
	}
}
