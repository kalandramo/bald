package web

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/gin-gonic/gin"
)

// Default 是可选接口：绑定完成后调用，用于填充默认值。业务结构体可实现它。
type Default interface {
	Default()
}

// Validator 在全部绑定完成后执行统一校验。业务可在路由级或全局注册。
type Validator[T any] func(ctx context.Context, obj *T) error

// Binder 把请求的一部分绑定到 obj。多个 Binder 顺序调用，后者覆盖同名前者。
type Binder func(c *gin.Context, obj any) error

// errInvalidContentType 表示客户端发送了 body 但未声明 application/json（或声明了
// 非 JSON 类型），属非法请求。按"非法即拒绝"原则显式返回，而非静默放过。
var errInvalidContentType = errors.New("Content-Type 必须为 application/json")

// URI 从 gin 路由路径变量绑定到 obj 的字段（json tag 或字段名）。
// 例如 pattern "/v1/users/:id" 时，c.Param("id") 绑定到字段 id。
var URI Binder = func(c *gin.Context, obj any) error {
	params := c.Params
	if len(params) == 0 {
		return nil
	}
	dec := newFieldSetter(obj)
	for _, p := range params {
		if p.Value == "" {
			continue
		}
		dec.set(p.Key, p.Value)
	}
	return nil
}

// Query 从 URL 查询参数绑定。每个键的第一个值参与绑定；空值跳过。
var Query Binder = func(c *gin.Context, obj any) error {
	q := c.Request.URL.Query()
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
var JSON Binder = func(c *gin.Context, obj any) error {
	if c.Request.Body == nil {
		return nil
	}
	ct := c.Request.Header.Get("Content-Type")
	ct = normalizeContentType(ct)
	body := c.Request.Body
	switch {
	case ct == "":
		// 缺省 Content-Type 却带 body：非法请求，明确拒绝。
		return newBindErr("json", errInvalidContentType)
	case ct == "application/x-www-form-urlencoded", ct == "multipart/form-data":
		return nil // 表单内容由 Query binder 处理，跳过
	case ct == "application/json":
		dec := json.NewDecoder(body)
		if err := dec.Decode(obj); err != nil {
			return newBindErr("json", err)
		}
		return nil
	default:
		// 其他类型（如 text/plain）带 body：非 JSON 契约，非法请求。
		return newBindErr("json", errInvalidContentType)
	}
}

// normalizeContentType 提取媒体类型主段并小写，忽略参数（如 charset）。
func normalizeContentType(ct string) string {
	for i := 0; i < len(ct); i++ {
		if ct[i] == ';' || ct[i] == ' ' {
			return ct[:i]
		}
	}
	return ct
}

// Bind 执行多源绑定 + 可选统一验证，对应 onexstack binding.Bind 的动机：URI →
// Query → JSON 依次覆盖，全部绑定完成后才跑 validators，绝不中途校验，避免部分
// 填充的结构体触发假的必填校验错误。
func Bind[T any](c *gin.Context, obj *T, binders []Binder, validators ...Validator[T]) error {
	for _, b := range binders {
		if b == nil {
			continue
		}
		if err := b(c, obj); err != nil {
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
		if err := v(c.Request.Context(), obj); err != nil {
			return err
		}
	}
	return nil
}
