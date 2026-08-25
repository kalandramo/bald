package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// Default 是可选接口：绑定完成后调用，用于填充默认值。业务结构体可实现它。
type Default interface {
	Default()
}

// Binder 把请求的一部分绑定到 obj。多个 Binder 顺序调用，后者覆盖同名前者。
type Binder func(r *http.Request, obj any) error

// errInvalidContentType 表示客户端发送了 body 但未声明 application/json（或声明了
// 非 JSON 类型），属非法请求。按"非法即拒绝"原则显式返回，而非静默放过。
var errInvalidContentType = errors.New("Content-Type 必须为 application/json")

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

// JSON 从请求体按 application/json 绑定。Content-Type 契约是硬性要求：
//   - application/json：按 JSON 解码，解码失败（非法 JSON）显式返回 400 绑定错误；
//   - application/x-www-form-urlencoded / multipart/form-data：明确跳过（走 Query
//     binder，不当 JSON 处理）；
//   - 其他类型或缺省 Content-Type 但 body 非空：客户端发了 body 却未声明 JSON，属非法
//     请求，显式返回 400，而不是静默跳过把锅甩给后续业务校验（如误报 EMPTY_NAME）。
var JSON Binder = func(r *http.Request, obj any) error {
	if r.Body == nil {
		return nil
	}
	ct := r.Header.Get("Content-Type")
	ct = strings.TrimSpace(strings.ToLower(ct))
	switch {
	case ct == "":
		// 缺省 Content-Type 却带 body：非法请求，明确拒绝。
		return newBindErr("json", errInvalidContentType)
	case strings.HasPrefix(ct, "application/x-www-form-urlencoded"),
		strings.HasPrefix(ct, "multipart/form-data"):
		return nil // 表单内容由 Query binder 处理，跳过
	case strings.Contains(ct, "application/json"):
		dec := json.NewDecoder(r.Body)
		if err := dec.Decode(obj); err != nil {
			return newBindErr("json", err)
		}
		return nil
	default:
		// 其他类型（如 text/plain）带 body：非 JSON 契约，非法请求。
		return newBindErr("json", errInvalidContentType)
	}
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
