package web

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/kalandramo/bald/pkg/berrors"
)

// Defaulter 由请求结构体可选实现，在所有绑定完成后填充默认值。
type Defaulter interface {
	Default()
}

// ShouldBindAll 依次绑定 URI → Query → JSON，后者覆盖前者。
//
// 动机：沿用 onexstack binding.Bind 的多源合并，但修正其顺序陷阱——先绑定
// 再校验会让部分填充的结构体触发假的必填错误。本实现先完成全部绑定（URI
// 优先级最低、JSON 最高），随后若 obj 实现了 Defaulter 则调用 Default()，
// 最后才交由业务校验器执行。
//
// 仅当请求体非空时才尝试 JSON 绑定，避免无 body 时的 EOF 错误。
func ShouldBindAll(c *gin.Context, obj any) error {
	if err := c.ShouldBindUri(obj); err != nil {
		return errors.BadRequest(err.Error())
	}
	if err := c.ShouldBindQuery(obj); err != nil {
		return errors.BadRequest(err.Error())
	}
	if hasBody(c) {
		if err := c.ShouldBindJSON(obj); err != nil {
			return errors.BadRequest(err.Error())
		}
	}
	if d, ok := obj.(Defaulter); ok {
		d.Default()
	}
	return nil
}

// hasBody 报告请求是否携带非空请求体。
func hasBody(c *gin.Context) bool {
	if c.Request.ContentLength > 0 {
		return true
	}
	return c.Request.Body != nil && c.Request.Body != http.NoBody
}
