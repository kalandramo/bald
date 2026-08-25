package web

import (
	"encoding/json"
	"net/http"
	"strings"
)

// Default 是可选接口：绑定完成后调用，用于填充默认值。业务结构体可实现它。
type Default interface {
	Default()
}

// Binder 把请求的一部分绑定到 obj。多个 Binder 顺序调用，后者覆盖同名前者。
type Binder func(r *http.Request, obj any) error

// URI 从 ServeMux 的 path 通配符绑定到 obj 的字段（json tag 或字段名）。
// 例如 pattern "/v1/users/{id}" 时，r.PathValue("id") 绑定到字段 id。
// 空通配符值跳过（不覆盖已绑定的值）。
var URI Binder = func(r *http.Request, obj any) error {
	keys := pathValueKeys(r)
	if len(keys) == 0 {
		return nil
	}
	dec := newFieldSetter(obj)
	for _, k := range keys {
		v := r.PathValue(k)
		if v == "" {
			continue
		}
		dec.set(k, v)
	}
	return nil
}

// Query 从 URL 查询参数绑定。每个键的第一个值参与绑定；空值跳过。
var Query Binder = func(r *http.Request, obj any) error {
	q := r.URL.Query()
	if len(q) == 0 {
		return nil
	}
	dec := newFieldSetter(obj)
	for k := range q {
		v := q.Get(k)
		if v == "" {
			continue
		}
		dec.set(k, v)
	}
	return nil
}

// JSON 从请求体（Content-Type: application/json）绑定。非 JSON 内容类型跳过，
// 不报错——允许"无 body 只绑 Query/URI"的请求。
var JSON Binder = func(r *http.Request, obj any) error {
	if r.Body == nil {
		return nil
	}
	ct := r.Header.Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		return nil
	}
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(obj); err != nil {
		return newBindErr("json", err)
	}
	return nil
}

// Bind 执行多源绑定 + 可选统一验证，对应 onexstack binding.Bind 的动机：URI →
// Query → JSON 依次覆盖，全部绑定完成后才跑 validators，绝不中途校验，避免部分
// 填充的结构体触发假的必填校验错误。
func Bind[T any](r *http.Request, obj *T, binders []Binder, validators ...Validator[T]) error {
	for _, b := range binders {
		if b == nil {
			continue
		}
		if err := b(r, obj); err != nil {
			return err
		}
	}
	if d, ok := any(obj).(Default); ok {
		d.Default()
	}
	for _, v := range validators {
		if v == nil {
			continue
		}
		if err := v(r.Context(), obj); err != nil {
			return err
		}
	}
	return nil
}
