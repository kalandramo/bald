package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 业务 DTO（对外，JSON 友好，无 gorm tag）。
type userDTO struct {
	ID   string
	Name string
	Age  int
}

// 持久化 Entity（对内，含 gorm tag）。
type userEntity struct {
	ID   string `gorm:"primaryKey"`
	Name string
	Age  int
	// Entity 独有字段，DTO 无对应 → 复制时忽略。
	Internal string
}

func TestMapper_Copier(t *testing.T) {
	m := NewCopierMapper[userDTO, userEntity]()

	// DTO → Entity
	e, err := m.ToEntity(userDTO{ID: "1", Name: "alice", Age: 30})
	require.NoError(t, err)
	assert.Equal(t, "1", e.ID)
	assert.Equal(t, "alice", e.Name)
	assert.Equal(t, 30, e.Age)

	// Entity → DTO（Entity 独有字段 Internal 不出现在 DTO）
	d, err := m.ToDTO(userEntity{ID: "2", Name: "bob", Age: 25, Internal: "x"})
	require.NoError(t, err)
	assert.Equal(t, "2", d.ID)
	assert.Equal(t, "bob", d.Name)
	assert.Equal(t, 25, d.Age)

	// 指针实体也能复制。
	m2 := NewCopierMapper[userDTO, *userEntity]()
	pe, err := m2.ToEntity(userDTO{ID: "3", Name: "c", Age: 1})
	require.NoError(t, err)
	assert.Equal(t, "3", pe.ID)
}
