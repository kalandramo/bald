---
id: store
title: 存储
level: 强制
category: contract
sources:
  - docs/devel/zh-CN/Bald 存储设计.md
checks: []
---
# 存储

pkg/store 泛型数据访问（零引擎依赖）。完整设计与跨后端语义见母文档《Bald 存储设计》。

- [store-r01] [强制] 核心零引擎依赖：只经 store.NewStore[T](provider) + store.DBProvider[T] 使用；GORM/Mongo 桥是独立 module（contrib/store-gorm、contrib/store-mongo）按需 go get，pkg/store 不 import 任何引擎。
- [store-r02] [强制] 多租户隔离必须显式 store.RegisterTenant("tenant_id", store.DefaultTenantFunc) 开启，禁止 init() 隐式注册。
- [store-r03] [强制] 模型无 TenantID 字段的表必须在构造时显式 WithPlatformLevel() 声明豁免；豁免是 Store 实例级，禁止用空租户值推断（判据 J5）。
- [store-r04] [强制] 身份级平台视图只能经 AuthClaims.Platform → contextx.WithPlatform(ctx) 显式声明（默认 fail-closed），且平台身份仍受数据范围约束——二者正交不可互替。
- [store-r05] [强制] 查询条件走 store.Where 双通道（语义恒为 WHERE = AND(Filters..., Expr)），条件用 store.Eq/In/And/Or/Sort 构造族表达，禁止手写引擎方言字符串、禁止让业务 OR 树吞掉隔离条件。
- [store-r06] [强制] ARRAY_CONTAINS/JSON_CONTAINS/EXISTS/SEARCH 跨后端语义冲突，fail-closed 场景（DataScope）禁止使用；全文检索须业务显式建索引后原生查询。
- [store-r07] [强制] Update/Delete 0 行匹配返回 (0, nil)（幂等、面向最终状态），rows==0 禁止当「记录不存在」判据；存在性判断必须显式 store.Get。
- [store-r08] [强制] Get 未命中返回哨兵 store.ErrNotFound，调用方必须 errors.Is 匹配（biz 层用 berrors.NotFound(...).WithCause(err) 投影），禁止判 nil 区分零值与未命中。
- [store-r09] [强制] 列表统一走 ListWithPaging（四策略自动识别，NextToken 只在确有下页时下发）；页大小经 clampPageSize 归一，上限用 store.WithMaxPageSize 防恶意大页。
- [store-r10] [强制] 写路径租户归属由 injectWriteTenant 无条件覆写，业务禁止手工写 tenant_id；数据范围策略经 RegisterDataScope/RegisterDataScopeExpr 注入，身份缺失 fail-closed（恒假 OR）。
