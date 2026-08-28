// 规则式字段校验工具：当无法用「按类型分发」的 ValidateXxx 方法、只想对单个字段做
// 轻量非空/范围检查时使用。它与 Validator（按类型分发）互补，二者都返回 error（建议
// 封装为 pkg/errors 的语义错误）。
package validation

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// ValidRequired 仅校验必填字段非空。fields 为「字段名 = 值」键值对。
// 返回首个缺失字段的错误；全部通过返回 nil。
func ValidRequired(fields map[string]any) error {
	for field, value := range fields {
		if isEmpty(value) {
			return fmt.Errorf("field %q is required", field)
		}
	}
	return nil
}

// ValidRange 校验数值型字段是否在 [min, max] 区间（支持 int/int64/float64/string 长度）。
func ValidRange(field string, value any, min, max any) error {
	switch v := value.(type) {
	case int:
		if v < toInt(min) || v > toInt(max) {
			return fmt.Errorf("field %q must be in [%v, %v]", field, min, max)
		}
	case int64:
		if v < toInt64(min) || v > toInt64(max) {
			return fmt.Errorf("field %q must be in [%v, %v]", field, min, max)
		}
	case float64:
		if v < toFloat64(min) || v > toFloat64(max) {
			return fmt.Errorf("field %q must be in [%v, %v]", field, min, max)
		}
	case string:
		if len(v) < toInt(min) || len(v) > toInt(max) {
			return fmt.Errorf("field %q length must be in [%v, %v]", field, min, max)
		}
	}
	return nil
}

// ValidPattern 校验字符串是否匹配给定前缀/后缀/包含规则。rules 形如 "prefix:abc"、"suffix:xyz"、
// "contains:def"、"len:min,max"。返回首个不匹配的错误。
func ValidPattern(field, value string, rules ...string) error {
	for _, rule := range rules {
		parts := strings.SplitN(rule, ":", 2)
		if len(parts) != 2 {
			continue
		}
		op, arg := parts[0], parts[1]
		switch op {
		case "prefix":
			if !strings.HasPrefix(value, arg) {
				return fmt.Errorf("field %q must start with %q", field, arg)
			}
		case "suffix":
			if !strings.HasSuffix(value, arg) {
				return fmt.Errorf("field %q must end with %q", field, arg)
			}
		case "contains":
			if !strings.Contains(value, arg) {
				return fmt.Errorf("field %q must contain %q",  field, arg)
			}
		case "len":
			b := strings.SplitN(arg, ",", 2)
			if len(b) == 2 {
				min, _ := strconv.Atoi(b[0])
				max, _ := strconv.Atoi(b[1])
				if len(value) < min || len(value) > max {
					return fmt.Errorf("field %q length must be [%d, %d]", field, min, max)
				}
			}
		}
	}
	return nil
}

func isEmpty(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.String:
		return rv.Len() == 0
	case reflect.Map, reflect.Slice, reflect.Array:
		return rv.Len() == 0
	case reflect.Ptr:
		return rv.IsNil()
	}
	return false
}

func toInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	case string:
		n, _ := strconv.Atoi(x)
		return n
	}
	return 0
}

func toInt64(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		return int64(x)
	}
	return 0
}

func toFloat64(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case string:
		f, _ := strconv.ParseFloat(x, 64)
		return f
	}
	return 0
}
