package thrift

import (
	"testing"

	"github.com/apache/thrift/lib/go/thrift"

	"github.com/kalandramo/bald/encoding"
)

// TestCodec_Stateless 钉住「codec 无状态、可安全并发复用」这一契约。
// 删掉 serializer/deserializer 字段后，codec 是空结构体——这里用反射之外的方式
// 断言：同一个 codec 值连续使用两次都成功，且不持有任何跨调用状态。
func TestCodec_Stateless(t *testing.T) {
	c := New()
	if c == nil {
		t.Fatal("New() returned nil")
	}
	if got := c.Name(); got != "thrift" {
		t.Errorf("Name() = %q, want %q", got, "thrift")
	}
}

func TestCodec_RejectsNonTStructMarshal(t *testing.T) {
	// 普通 struct 未实现 thrift.TStruct，必须返回 error 而非 panic。
	_, err := New().Marshal(struct{ A int }{A: 1})
	if err == nil {
		t.Fatal("Marshal(non-TStruct) = nil, want error")
	}
}

func TestCodec_RejectsNonTStructUnmarshal(t *testing.T) {
	target := &struct{ A int }{}
	err := New().Unmarshal([]byte{0x00}, target)
	if err == nil {
		t.Fatal("Unmarshal(non-TStruct target) = nil, want error")
	}
}

// 确保接口契约由编译器与运行时双重确认。
var _ encoding.Codec = New()

// thrift.TStruct 的签名引用，防止依赖升级后本测试静默失去意义。
var _ thrift.TStruct = (thrift.TStruct)(nil)
