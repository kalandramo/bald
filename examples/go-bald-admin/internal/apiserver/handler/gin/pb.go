// pb.go 统一 proto 消息的 HTTP 绑定与序列化（T2）。
//
// 为什么不用 c.ShouldBindJSON / c.JSON（encoding/json）：grpc-gateway 按 proto3 JSON
// 规范（protojson）编解码——枚举输出符号名（"FREEZE"）、Timestamp 输出 RFC3339、
// int64/uint64 输出字符串；encoding/json 会把枚举输出数字、Timestamp 输出内部字段、
// 且对 int64 输出裸数字。同一路由经 gateway（:8081）与直连 gin（:8080）会得到不同
// JSON——违反「REST 与 gRPC 同源」的 P9 语义。本文件强制两侧统一 protojson。
package gin

import (
	"io"
	"net/http"

	gingonic "github.com/gin-gonic/gin"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// bindPB 按 proto3 JSON 规范把请求体绑定到 proto 消息（DiscardUnknown 兼容宽松客户端）。
func bindPB(c *gingonic.Context, req proto.Message) error {
	b, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return err
	}
	if len(b) == 0 {
		return nil
	}
	return protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(b, req)
}

// writePB 按 proto3 JSON 规范写出响应（与 grpc-gateway 转码结果一致）。
func writePB(c *gingonic.Context, code int, msg proto.Message) {
	b, err := protojson.Marshal(msg)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gingonic.H{"error": err.Error()})
		return
	}
	c.Data(code, "application/json", b)
}
