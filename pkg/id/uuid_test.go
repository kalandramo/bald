package id

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewGUIDv4(t *testing.T) {
	// 带连字符：36 字符、4 个连字符、合法 16 字节十六进制。
	withHyphen := NewGUIDv4(true)
	assert.Equal(t, 36, len(withHyphen))
	assert.Equal(t, 4, strings.Count(withHyphen, "-"))
	b, err := hex.DecodeString(strings.ReplaceAll(withHyphen, "-", ""))
	assert.NoError(t, err)
	assert.Equal(t, 16, len(b))

	// 不带连字符：32 字符纯十六进制。
	withoutHyphen := NewGUIDv4(false)
	assert.Equal(t, 32, len(withoutHyphen))
	assert.Equal(t, 0, strings.Count(withoutHyphen, "-"))
	b2, err := hex.DecodeString(withoutHyphen)
	assert.NoError(t, err)
	assert.Equal(t, 16, len(b2))
}

func TestNewGUIDv4CollisionRate(t *testing.T) {
	const testCount = 10000
	ids := make(map[string]struct{}, testCount)
	for i := 0; i < testCount; i++ {
		id := NewGUIDv4(true)
		_, exists := ids[id]
		assert.False(t, exists, "碰撞发生: %s 已存在", id)
		ids[id] = struct{}{}
	}
}

func TestNewGUIDv7(t *testing.T) {
	withHyphen := NewGUIDv7(true)
	assert.Equal(t, 36, len(withHyphen))
	assert.Equal(t, 4, strings.Count(withHyphen, "-"))
	b, err := hex.DecodeString(strings.ReplaceAll(withHyphen, "-", ""))
	assert.NoError(t, err)
	assert.Equal(t, 16, len(b))

	withoutHyphen := NewGUIDv7(false)
	assert.Equal(t, 32, len(withoutHyphen))
	b2, err := hex.DecodeString(withoutHyphen)
	assert.NoError(t, err)
	assert.Equal(t, 16, len(b2))
}

func TestNewGUIDv7TimeOrdered(t *testing.T) {
	// v7 时间有序：先后生成的 ID 字典序单调不减（时钟回拨场景由 v4 回退兜底，
	// 此处只验证正常路径的有序性）。
	prev := NewGUIDv7(false)
	for i := 0; i < 100; i++ {
		next := NewGUIDv7(false)
		assert.GreaterOrEqual(t, next, prev, "UUIDv7 应时间有序")
		prev = next
	}
}
