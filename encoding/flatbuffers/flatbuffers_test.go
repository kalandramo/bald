package flatbuffers

import (
	"testing"

	fb "github.com/google/flatbuffers/go"
)

// stubBuffer 是 fb.FlatBuffer 的最小实现：Init 记录传入的字节与位置，
// 不解析任何字段，因此可以单独测试 Unmarshal 的 buffer 校验逻辑。
type stubBuffer struct {
	inited bool
	buf    []byte
	pos    fb.UOffsetT
	table  fb.Table
}

func (s *stubBuffer) Init(buf []byte, i fb.UOffsetT) {
	s.inited = true
	s.buf = buf
	s.pos = i
}

func (s *stubBuffer) Table() fb.Table { return s.table }

// 合法 buffer：4 字节根偏移（小端），指向紧随其后的位置。
func validFlatBuffer() []byte {
	return []byte{
		0x04, 0x00, 0x00, 0x00, // 根偏移 = 4
		0x00, 0x00, 0x00, 0x00, // 根位置占位
	}
}

func TestUnmarshal_ValidBuffer(t *testing.T) {
	var target stubBuffer
	err := New().Unmarshal(validFlatBuffer(), &target)
	if err != nil {
		t.Fatalf("Unmarshal(valid) error = %v, want nil", err)
	}
	if !target.inited {
		t.Fatal("Unmarshal(valid) did not call Init on the target")
	}
}

func TestUnmarshal_RejectsTooShortBuffer(t *testing.T) {
	var target stubBuffer
	// 少于 4 字节：GetRootAs 读 buf[0:4] 会越界，必须在进入前拦下。
	err := New().Unmarshal([]byte{0x01, 0x02}, &target)
	if err == nil {
		t.Fatal("Unmarshal(2-byte buffer) = nil, want error（畸形 buffer 必须被拒绝）")
	}
	if target.inited {
		t.Error("Unmarshal must not Init the target when the buffer is rejected")
	}
}

func TestUnmarshal_RejectsEmptyBuffer(t *testing.T) {
	var target stubBuffer
	if err := New().Unmarshal(nil, &target); err == nil {
		t.Fatal("Unmarshal(nil) = nil, want error")
	}
	if err := New().Unmarshal([]byte{}, &target); err == nil {
		t.Fatal("Unmarshal(empty) = nil, want error")
	}
}

func TestUnmarshal_RejectsOutOfRangeRootOffset(t *testing.T) {
	var target stubBuffer
	// 根偏移 = 0xFFFFFFFF，远超 buffer 长度——解析字段时必然越界 panic。
	bad := []byte{0xFF, 0xFF, 0xFF, 0xFF, 0x00, 0x00, 0x00, 0x00}
	if err := New().Unmarshal(bad, &target); err == nil {
		t.Fatal("Unmarshal(out-of-range root offset) = nil, want error")
	}
	if target.inited {
		t.Error("Unmarshal must not Init the target when the buffer is rejected")
	}
}

func TestUnmarshal_RejectsWrongTargetType(t *testing.T) {
	// 目标未实现 fb.FlatBuffer（此处传一个普通 struct 指针）。
	type notABuffer struct{}
	var target notABuffer
	if err := New().Unmarshal(validFlatBuffer(), &target); err == nil {
		t.Fatal("Unmarshal(non-FlatBuffer target) = nil, want error")
	}
}

var _ fb.FlatBuffer = (*stubBuffer)(nil)
