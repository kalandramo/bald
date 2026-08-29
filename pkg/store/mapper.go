// Package store 的可选 DTO↔Entity 映射（对照 go-crud gorm/repository.go 的 Mapper）。
//
// bald 默认 Store[T] 直接操作实体 T（含 gorm tag），足够轻量。当业务需要
// 「对外 DTO / 对内 Entity」分离时，可用 Mapper 在 Provider 层桥接：Provider 构造
// 时包一层 Mapper，把 DTO 转成 Entity 再交给引擎。本文件提供接口与基于反射的
// CopierMapper（同名字段复制），可选使用，不强制所有 Store 采用。
package store

import (
	"fmt"
	"reflect"
)

// Mapper 在 DTO 与 Entity 间双向映射。Entity 通常为 Store[T] 的 T（值类型）。
// 业务在 Provider 层组合 Mapper 实现 DTO/Entity 分离。
type Mapper[DTO any, Entity any] interface {
	// ToEntity 把 DTO 转为 Entity（用于写入前）。
	ToEntity(dto DTO) (Entity, error)
	// ToDTO 把 Entity 转为 DTO（用于读出后）。
	ToDTO(entity Entity) (DTO, error)
}

// CopierMapper 基于反射的同名字段复制实现（对齐 go-crud mapper.CopierMapper）。
// 仅复制导出字段中名字相同且可赋值（kind 兼容）的字段；不匹配字段被忽略。
type CopierMapper[DTO any, Entity any] struct{}

// NewCopierMapper 构造 CopierMapper。
func NewCopierMapper[DTO any, Entity any]() *CopierMapper[DTO, Entity] {
	return &CopierMapper[DTO, Entity]{}
}

// ToEntity 反射复制 dto → entity。
func (m *CopierMapper[DTO, Entity]) ToEntity(dto DTO) (Entity, error) {
	var dst Entity
	if err := copyFields(reflect.ValueOf(&dst).Elem(), reflect.ValueOf(dto)); err != nil {
		return dst, err
	}
	return dst, nil
}

// ToDTO 反射复制 entity → dto。
func (m *CopierMapper[DTO, Entity]) ToDTO(entity Entity) (DTO, error) {
	var dst DTO
	if err := copyFields(reflect.ValueOf(&dst).Elem(), reflect.ValueOf(entity)); err != nil {
		return dst, err
	}
	return dst, nil
}

// copyFields 把 src 的同名导出字段复制到 dst（dst 必须可寻址或可分配指针）。
func copyFields(dst, src reflect.Value) error {
	if src.Kind() == reflect.Ptr {
		if src.IsNil() {
			return nil
		}
		src = src.Elem()
	}
	if dst.Kind() == reflect.Ptr {
		if dst.IsNil() {
			// 目标是指针类型但当前为 nil：分配一个新实例再继续。
			dst.Set(reflect.New(dst.Type().Elem()))
		}
		dst = dst.Elem()
	}
	if src.Kind() != reflect.Struct || dst.Kind() != reflect.Struct {
		return fmt.Errorf("store: copyFields expects structs, got %s -> %s", src.Kind(), dst.Kind())
	}
	srcType := src.Type()
	for i := 0; i < srcType.NumField(); i++ {
		sf := srcType.Field(i)
		if sf.PkgPath != "" { // 非导出字段跳过
			continue
		}
		df := dst.FieldByName(sf.Name)
		if !df.IsValid() || !df.CanSet() {
			continue
		}
		sv := src.Field(i)
		if !sv.Type().AssignableTo(df.Type()) {
			// 类型不兼容则跳过（宽松映射，避免 panic）。
			continue
		}
		df.Set(sv)
	}
	return nil
}
