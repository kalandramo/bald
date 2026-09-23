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

## 操作符覆盖

覆盖 `store.proto` 的 `Operator` 枚举绝大部分，含三个 MongoDB 原生扩展（超出
store-gorm/inmemory 的能力面）：

| 操作符 | 实现 | 说明 |
|---|---|---|
| EQ/NEQ/GT/GTE/LT/LTE | 原生比较 | |
| IN/NIN | `$in`/`$nin` | 值按字段类型转换 |
| LIKE/CONTAINS/STARTS_WITH/ENDS_WITH | `$regex`（转义元字符） | |
| ILIKE/ICONTAINS/ISTARTS_WITH/IENDS_WITH/IEXACT | `$regex` + `$options:"i"` | 大小写不敏感 |
| NOT_LIKE | `$not` + `$regex` | |
| IS_NULL/IS_NOT_NULL | `$in`/`$nin` [nil,""] | |
| BETWEEN | `$gte`+`$lte` | |
| REGEXP/IREGEXP | `$regex` | |
| **ARRAY_CONTAINS** | 数组字段等值匹配 | MongoDB 原生：`{tags:"b"}` 命中 `tags:[a,b]` |
| **JSON_CONTAINS** | 点号路径匹配 | MongoDB 原生：`{meta.role:"admin"}` 匹配嵌套子文档 |
| **EXISTS** | `$exists` | MongoDB 原生，value `"true"/"false"` 控制正反 |
| SEARCH | **未实现**（恒真） | `$text` 需**预先建 text 索引**，否则查询报错；盲目映射会把"条件被忽略"换成运行期硬错误。需全文检索时业务应显式建索引并用原生查询 |

> ARRAY_CONTAINS / JSON_CONTAINS / EXISTS 三者在 store-gorm 与 inmemory 中**均未实现**
> （走 default）。本模块提供原生实现，故同一 `Where` 在 mongo 后端下语义更完整。
> 若业务依赖这三者，注意跨后端行为差异（mongo 精确匹配 vs gorm/inmemory 恒真/恒假）。

## 与 store-gorm 的语义差异

| 行为 | store-gorm（SQL） | store-mongo |
|---|---|---|
| `Update` 返回值 | `RowsAffected`（**受影响**行数）——MySQL 下「更新到相同值」返回 0 | `MatchedCount`（**匹配**行数）——「更新到相同值」返回 1 |
| `rows == 0` 判「记录不存在」 | 不可靠（MySQL 误判） | **可靠**（匹配即算） |
| ARRAY_CONTAINS / JSON_CONTAINS / EXISTS | 未实现（恒真） | **原生实现** |
| 未支持的操作符（SEARCH） | 恒真 | 恒真 |

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
无跨测试串扰。

**集成测试：`-short` 下跳过**（CI 无 mongod 服务，见 `.github/workflows/ci.yml`，
`go test -short` 是 CI 与本地快速回归的约定）；非 `-short` 且 mongod 不可达时同样
`t.Skip`（环境缺失，非实现缺陷）。
