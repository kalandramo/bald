// Package id 提供框架内置的 ID 生成能力。
//
// 自 bald-utils/id 吸收 UUID 子集（2026-09-10，transport/{tcp,webrtc} session
// ID 生成的既成依赖）：仅保留纯 google/uuid 依赖的 v4/v7，其余变体
// （ShortUUID/KSUID/XID/MongoObjectID/snowflake/sonyflake）仍留在 bald-utils
// ——按「核心零依赖」纪律，不为无消费方的能力引入第三方库。
package id

import (
	"encoding/hex"

	"github.com/google/uuid"
)

// NewGUIDv4 生成 UUID v4 字符串（随机）。withHyphen 控制是否带连字符。
func NewGUIDv4(withHyphen bool) string {
	u := uuid.New()

	if withHyphen {
		return u.String()
	}

	var buf [32]byte
	hex.Encode(buf[:], u[:])
	return string(buf[:])
}

// NewGUIDv7 生成 UUID v7 字符串（时间有序，利于数据库索引局部性）。
// 系统时钟不可靠导致 v7 生成失败时回退 v4。withHyphen 控制是否带连字符。
func NewGUIDv7(withHyphen bool) string {
	u, err := uuid.NewV7()
	if err != nil {
		// Fallback to v4 if system clock is unreliable
		return NewGUIDv4(withHyphen)
	}

	if withHyphen {
		return u.String()
	}

	var buf [32]byte
	hex.Encode(buf[:], u[:])
	return string(buf[:])
}
