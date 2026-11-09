package http3

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

type bindTarget struct {
	Name      string          `json:"name"`
	FormOnly  string          `form:"form_only"`
	QueryOnly string          `query:"query_only"`
	WithOpt   int             `json:"with_opt,omitempty"`
	Skip      string          `json:"-"`
	Count     int             `json:"count"`
	Size      uint            `json:"size"`
	Ratio     float64         `json:"ratio"`
	Enabled   bool            `json:"enabled"`
	Ptr       *int            `json:"ptr"`
	Raw       json.RawMessage `json:"raw"`
	NoTag     string
}

func TestBindQuery_AllKinds(t *testing.T) {
	values := url.Values{
		"name":       {"alice"},
		"form_only":  {"from-form"},
		"query_only": {"from-query"},
		"with_opt":   {"3"},
		"-":          {"ignored"},
		"count":      {"7"},
		"size":       {"42"},
		"ratio":      {"1.5"},
		"enabled":    {"true"},
		"ptr":        {"9"},
		"raw":        {"x"},
		// 空值按“未提供”处理，不覆盖零值
		"name_empty": {""},
	}

	var dst bindTarget
	if err := BindQuery(values, &dst); err != nil {
		t.Fatalf("BindQuery: %v", err)
	}
	if dst.Name != "alice" || dst.FormOnly != "from-form" || dst.QueryOnly != "from-query" {
		t.Errorf("tag 解析结果 = %+v", dst)
	}
	if dst.WithOpt != 3 || dst.Count != 7 || dst.Size != 42 || dst.Ratio != 1.5 || !dst.Enabled {
		t.Errorf("标量绑定结果 = %+v", dst)
	}
	if dst.Ptr == nil || *dst.Ptr != 9 {
		t.Errorf("指针分配结果 = %v", dst.Ptr)
	}
	if string(dst.Raw) != `"x"` {
		t.Errorf("RawMessage = %s, want \"x\"", dst.Raw)
	}
	if dst.Skip != "" || dst.NoTag != "" {
		t.Errorf("跳过字段不应被赋值: %+v", dst)
	}
}

// 不可寻址（不可写）的字段必须静默跳过，不能 panic。
func TestSetField_NonSettable(t *testing.T) {
	field := reflect.ValueOf(bindTarget{}).FieldByName("Name")
	if field.CanSet() {
		t.Fatal("前提不成立：临时值的字段应不可写")
	}
	if err := setField(field, "x"); err != nil {
		t.Errorf("setField(不可写字段) = %v, want nil", err)
	}
}

func TestBindQuery_ExistingPointer(t *testing.T) {
	n := 1
	dst := bindTarget{Ptr: &n}
	if err := BindQuery(url.Values{"ptr": {"5"}}, &dst); err != nil {
		t.Fatalf("BindQuery: %v", err)
	}
	if dst.Ptr != &n || n != 5 {
		t.Errorf("已分配的指针应原地写入: ptr=%v n=%d", dst.Ptr, n)
	}
}

func TestBindQuery_IgnoredTargets(t *testing.T) {
	var nilPtr *bindTarget
	cases := []struct {
		name string
		obj  any
	}{
		{"nil 指针", nilPtr},
		{"非指针", bindTarget{}},
		{"指针非结构体", new(int)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := BindQuery(url.Values{"name": {"x"}}, c.obj); err != nil {
				t.Fatalf("应静默忽略: %v", err)
			}
		})
	}
}

func TestBindQuery_ParseErrors(t *testing.T) {
	cases := []struct {
		name string
		key  string
		val  string
	}{
		{"int", "count", "abc"},
		{"uint", "size", "-1"},
		{"float", "ratio", "abc"},
		{"bool", "enabled", "maybe"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var dst bindTarget
			if err := BindQuery(url.Values{c.key: {c.val}}, &dst); err == nil {
				t.Fatalf("BindQuery(%s=%q) 应报错", c.key, c.val)
			}
		})
	}
}

func TestBindForm(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("name=bob&count=2"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	var dst bindTarget
	if err := BindForm(req, &dst); err != nil {
		t.Fatalf("BindForm: %v", err)
	}
	if dst.Name != "bob" || dst.Count != 2 {
		t.Errorf("表单绑定结果 = %+v", dst)
	}
}

// 非法百分号编码让 ParseForm 失败，错误必须原样上抛。
func TestBindForm_ParseFormError(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("a=%zz"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	var dst bindTarget
	if err := BindForm(req, &dst); err == nil {
		t.Fatal("非法表单应返回错误")
	}
	if err := BindForm(httptest.NewRequest(http.MethodGet, "/?a=%zz", nil), &dst); err == nil {
		t.Fatal("非法 query 编码也应返回错误")
	}
}
