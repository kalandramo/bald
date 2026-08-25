package web

import (
	"fmt"
	"net/http"
	"reflect"
	"strconv"
	"strings"

	berrors "github.com/kalandramo/bald/pkg/errors"
)

// bindErr 包装绑定阶段的错误，携带来源便于调用方定位（如 "invalid query value for 'page': not an integer"）。
type bindErr struct {
	source string
	err    error
}

func (e *bindErr) Error() string {
	return fmt.Sprintf("bind %s: %v", e.source, e.err)
}

func (e *bindErr) Unwrap() error { return e.err }

// newBindErr 把绑定错误封装为 berrors.BadRequest（HTTP 400）。绑定失败属于非法请求，
// 按"非法即拒绝"原则显式返回 400 + 结构化 ErrorResponse，而不是落到 500 明文。
func newBindErr(source string, err error) error {
	return berrors.BadRequest("BIND_ERROR").
		WithMessage("bind %s: %v", source, err)
}

// fieldSetter 把字符串值按字段类型写入结构体（支持 int/uint/bool/string）。
// 字段匹配规则：json tag（小写首字母）优先；否则直接用字段名（小写首字母）。
// 找不到匹配字段的键被静默跳过——URI/Query 的键可能对应路径段而非结构体字段。
type fieldSetter struct {
	rv   reflect.Value
	keys map[string]int // 小写字段名/小写 json tag → 字段索引
}

func newFieldSetter(obj any) *fieldSetter {
	rv := reflect.ValueOf(obj)
	for rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}
	keys := map[string]int{}
	if rv.Kind() == reflect.Struct {
		t := rv.Type()
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			name := strings.ToLower(f.Name)
			if tag := f.Tag.Get("json"); tag != "" {
				if comma := strings.IndexByte(tag, ','); comma >= 0 {
					tag = tag[:comma]
				}
				if tag != "" && tag != "-" {
					name = strings.ToLower(tag)
				}
			}
			keys[name] = i
		}
	}
	return &fieldSetter{rv: rv, keys: keys}
}

func (s *fieldSetter) set(key, value string) {
	idx, ok := s.keys[strings.ToLower(key)]
	if !ok {
		return
	}
	f := s.rv.Field(idx)
	if !f.CanSet() {
		return
	}
	switch f.Kind() {
	case reflect.String:
		f.SetString(value)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return
		}
		f.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return
		}
		f.SetUint(n)
	case reflect.Bool:
		b, err := strconv.ParseBool(value)
		if err != nil {
			return
		}
		f.SetBool(b)
	default:
		// 复杂类型（嵌套结构、slice 等）不走 URI/Query 简单绑定，交给 JSON。
		return
	}
}

// pathValueKeys 返回当前请求的 pattern 通配符变量名（由 Router 注入 ctx），
// 供 URI 绑定器按字段名自动绑定。若未注入（如直接调用 ServeMux），返回空。
func pathValueKeys(r *http.Request) []string {
	return pathVarsFromContext(r.Context())
}
