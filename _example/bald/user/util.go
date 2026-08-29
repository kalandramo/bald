package user

import (
	"context"
	"strconv"
)

// contextBackground 返回 context.Background（封装避免在大文件里重复 import）。
func contextBackground() context.Context { return context.Background() }

// itoa 是 int->string 的简短别名。
func itoa(i int) string { return strconv.Itoa(i) }

// uint32Ptr 构造 *uint32（分页请求用）。
func uint32Ptr(v uint32) *uint32 { return &v }
