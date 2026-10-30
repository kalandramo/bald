// Package seed 承载授权策略行（bald gen app 生成一次，此后归你维护）。
package seed

// PolicyRow 一条默认授权策略行（role/object/action）。
type PolicyRow struct {
	Role   string
	Object string
	Action string
}

// Rows 是默认授权策略行（object 由域 service 派生；与 authz/alias.gen.go
// 的 DefaultActions 对账）。
var Rows = []PolicyRow{
	{Role: "admin", Object: "greet", Action: "get"},
	{Role: "admin", Object: "greet", Action: "list"},
	{Role: "admin", Object: "greet", Action: "write"},
	{Role: "admin", Object: "greet", Action: "delete"},
	{Role: "admin", Object: "greet", Action: "post"},
	{Role: "admin", Object: "greet", Action: "put"},
	{Role: "viewer", Object: "greet", Action: "get"},
	{Role: "viewer", Object: "greet", Action: "list"},
}
