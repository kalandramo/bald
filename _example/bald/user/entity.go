// Package user 演示 bald 存储层的「后端可切换」能力：
// 同一份业务代码（User 实体 + Store[T] 调用）在运行时背后可换用
// 内存实现或 GORM 实现，业务无感。
//
// 运行：
//
//	# 内存版（默认构建，零外部数据库依赖）
//	go run ./_example/bald user
//
//	# GORM + SQLite 版（build tag 隔离 GORM 依赖，默认构建不引入）
//	go run -tags gorm ./_example/bald user
//
// 该命令执行一组固定 CRUD/过滤/排序/分页演示并退出（非长驻服务），
// 用于验证 pkg/store 抽象 + 各后端 Provider 正确性。
package user

import (
	storev1 "github.com/kalandramo/bald/bconf/gen/go/bald/store/v1"
	"github.com/kalandramo/bald/pkg/store"
)

// User 是示例业务实体，同时充当 GORM 模型（含 gorm tag）。
type User struct {
	ID   string `gorm:"primaryKey" json:"id"`
	Name string `json:"name"`
	Age  int    `json:"age"`
}

// keyOf 提取主键，供内存/GORM Provider 唯一定位实体。
func keyOf(u *User) string { return u.ID }

// demoUsers 是预置数据集。
func demoUsers() []*User {
	return []*User{
		{ID: "1", Name: "alice", Age: 30},
		{ID: "2", Name: "bob", Age: 25},
		{ID: "3", Name: "carol", Age: 35},
		{ID: "4", Name: "dave", Age: 25},
	}
}

// runDemo 用给定 provider 执行统一的存储演示，与具体后端无关。
func runDemo(p store.DBProvider[User]) error {
	ctx := contextBackground()
	repo := store.NewStore[User](p)

	// 预置数据
	for _, u := range demoUsers() {
		if err := repo.Create(ctx, u); err != nil {
			return err
		}
	}

	// 读取
	got, err := repo.Get(ctx, &store.Where{Filters: []*storev1.FilterCondition{store.Eq("id", "1")}})
	if err != nil {
		return err
	}
	println("  Get id=1 ->", got.Name, "(age", itoa(got.Age), ")")

	// 过滤 + 排序 + 分页：age>=25 且 name 含 'a'，按 age 降序
	res, err := repo.ListWithPaging(ctx, &storev1.PagingRequest{
		FilterExpr: &storev1.FilterExpr{
			Conditions: []*storev1.FilterCondition{
				store.Gte("age", "25"),
				store.Contains("name", "a"),
			},
		},
		Sorting:  []*storev1.Sorting{store.SortDesc("age")},
		Page:     uint32Ptr(1),
		PageSize: uint32Ptr(10),
	})
	if err != nil {
		return err
	}
	println("  过滤(name 含 a, age>=25) 降序, 共", itoa(int(res.Meta.GetTotal().GetValue())), "条:")
	for _, u := range res.Items {
		println("   -", u.Name, "(age", itoa(u.Age), ")")
	}

	// 更新
	if err := repo.Update(ctx, &User{ID: "1", Name: "alice-updated", Age: 31}); err != nil {
		return err
	}
	got, _ = repo.Get(ctx, &store.Where{Filters: []*storev1.FilterCondition{store.Eq("id", "1")}})
	println("  更新后 Get id=1 ->", got.Name, "(age", itoa(got.Age), ")")

	// 删除
	if err := repo.Delete(ctx, &store.Where{Filters: []*storev1.FilterCondition{store.Eq("id", "2")}}); err != nil {
		return err
	}
	_, err = repo.Get(ctx, &store.Where{Filters: []*storev1.FilterCondition{store.Eq("id", "2")}})
	println("  删除 id=2 后再次 Get -> err == ErrNotFound:", err == store.ErrNotFound)

	// 计数
	n, err := repo.Count(ctx, nil)
	if err != nil {
		return err
	}
	println("  剩余总数:", itoa(int(n)))
	return nil
}
