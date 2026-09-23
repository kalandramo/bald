# bald-store-mongo

bald 存储层的 MongoDB 桥接子模块（独立 go.mod）。把 mongo-driver v2 的
`*mongo.Database` 适配为 bald 核心的 `store.DBProvider[T]` + `store.Queryable[T]`，
使业务以统一的泛型 `Store[T]` 姿态访问 MongoDB，而 bald 核心不引入任何 MongoDB 依赖
（遵循 P5 零后端耦合）。

## 用法

```go
import (
    baldmongo "github.com/kalandramo/bald/contrib/store-mongo"
    "github.com/kalandramo/bald/pkg/store"
    "go.mongodb.org/mongo-driver/v2/mongo"
    "go.mongodb.org/mongo-driver/v2/mongo/options"
)

client, _ := mongo.Connect(options.Client().ApplyURI("mongodb://localhost:27017"))
db := client.Database("demo")

provider := baldmongo.NewMongoProvider[User](db, "users", func(u *User) string { return u.ID })
_ = provider.Migrate(ctx) // 建集合 + 主键唯一索引（可选）
repo := store.NewStore[User](provider)

_ = repo.Create(ctx, &User{ID: "1", Name: "alice", Age: 30})
got, _ := repo.Get(ctx, &store.Where{Filters: []*storev1.FilterCondition{store.Eq("id", "1")}})
```

业务代码与 GORM/内存后端完全一致——切换后端只换 `NewMongoProvider` 一行。

## 字段映射与类型语义

- **字段名**：`Where.Filters` 的 `Field` 是 Go 字段名，经 `toField` 翻译成 BSON 字段名
  （默认 snake_case，与 `store-gorm` 的 `toColumn` 约定一致）——同一 `Where` 语义跨三后端一致。
- **值类型（重要差异）**：`FilterCondition.value` 是字符串，MongoDB 强类型、**无 SQL
  隐式转换**。本桥接层按**实体 schema**（反射 T 的字段类型）把字符串转为字段的 Go 类型
  ——如 `Gte("age", "25")` 中 `age` 是 `int` 时 `"25"` 转成 `25`，保证与 gorm/inmemory
  后端语义一致。需要显式类型时用 `json_value`（typed），它优先且不做转换。
- **主键字段**：默认 `"id"`（Go 字段 `ID` 的 snake_case）。实体主键字段名非 `ID` 时用
  `baldmongo.WithKeyField[T]("your_key")` 覆盖，避免 Get/Update/Delete 定位到不存在的字段。

## 与 store-gorm 的语义差异

| 行为 | store-gorm（SQL） | store-mongo |
|---|---|---|
| `Update` 返回值 | `RowsAffected`（**受影响**行数）——MySQL 下「更新到相同值」返回 0 | `MatchedCount`（**匹配**行数）——「更新到相同值」返回 1 |
| `rows == 0` 判「记录不存在」 | 不可靠（MySQL 误判） | **可靠**（匹配即算） |
| 未支持的操作符 | 生成对应 SQL（或报错） | 返回恒真条件（保守放行，避免"配了条件却查空"） |

> `Update` 返回值的跨后端差异详见 `pkg/store` 的 `Store.Update` 注释。

## 测试（依赖真实 MongoDB）

本模块测试需**真实 mongod**（MongoDB 无嵌入式等价物，不同于 store-gorm 的纯 Go
SQLite 内存库）。默认连 `mongodb://127.0.0.1:27017`，可用 `BALD_MONGO_URI` 覆盖：

```bash
# 本地起 mongod（Docker）
docker run -d --name bald-store-mongo -p 27017:27017 mongo:7

cd contrib/store-mongo && go test ./...
```

测试用每次调用唯一命名的 database（`baldmongo_test_<TestName>`），结束自动 Drop，
无跨测试串扰。mongod 不可达时 `t.Fatal` 并提示。
