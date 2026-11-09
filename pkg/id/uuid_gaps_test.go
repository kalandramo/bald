package id

import (
	"crypto/rand"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

// failFirstReader 首次 Read 失败（令 NewV7 出错触发回退），其后委托真实随机源
// （令回退的 NewGUIDv4 成功）。默认未启用 uuid 随机池，NewRandom 每次直读 rander。
type failFirstReader struct{ failed bool }

func (r *failFirstReader) Read(p []byte) (int, error) {
	if !r.failed {
		r.failed = true
		return 0, errors.New("entropy unavailable")
	}
	return rand.Reader.Read(p)
}

// TestNewGUIDv7_FallbackToV4 系统随机源不可靠致 v7 失败时回退 v4，仍返回合法 ID。
func TestNewGUIDv7_FallbackToV4(t *testing.T) {
	t.Cleanup(func() { uuid.SetRand(nil) })

	for _, hyphen := range []bool{true, false} {
		uuid.SetRand(&failFirstReader{}) // 每次新建，确保两种 hyphen 都走回退
		got := NewGUIDv7(hyphen)
		if hyphen {
			assert.Equal(t, 36, len(got))
			assert.Equal(t, 4, strings.Count(got, "-"))
		} else {
			assert.Equal(t, 32, len(got))
			assert.Equal(t, 0, strings.Count(got, "-"))
		}
	}
}
