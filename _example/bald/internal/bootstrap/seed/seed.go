// Package seed 承载业务种子数据（bald gen app 生成一次，此后归你维护）。
package seed

// Item 是演示种子行（形状对齐 bald.store.v1 通用 CRUD 面）。
type Item struct {
	ID    string
	Name  string
	Greet string
}

// Items 是 greet 域的演示种子行。
var Items = []Item{
	{ID: "greet-001", Name: "bald", Greet: "hello"},
	{ID: "greet-002", Name: "world", Greet: "hi"},
	{ID: "greet-003", Name: "demo", Greet: "你好"},
}

// Reserved 是业务保留字种子（greetv1 的 reservedNames 从这里起步）。
var Reserved = map[string]struct{}{
	"root":   {},
	"admin":  {},
	"system": {},
}

// Seed 把种子行装载进业务存储的入口（示例域无存储依赖，空实现）。
func Seed() error {
	return nil
}
