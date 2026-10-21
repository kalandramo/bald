// client.go —— 底层原生 go-redis client 的加法式暴露。业务（TokenStore / 验证码 / MFA / 审计流 / cachemonitor 等）需要
// 原生 client 能力（INCR 原子计数、SCAN、SUBSCRIBE、定制 Pipeline 等），
// 既有产物却只暴露 cache.Cache 抽象，拿不到原生 client。纯加法：只新增
// 一个只读访问器，既有构造逻辑与既有调用点零改动。
//
// 形态取舍（评估候选三选一，取「实现暴露 Client()」）：
//   - 构造钩子 WithClientHook(func(*redis.Client))：契约轨自建的 client 是
//     goredis.UniversalClient（单节点 / 集群 / 哨兵三形态），*redis.Client 型
//     钩子在集群与哨兵部署下永不触发，按形态拆钩子则 API 面翻倍——不取；
//   - AppProvider 产物 map 追加键：只惠及段驱动装配一条路径，且为同一
//     client 引入第二个事实源（键名契约）——不取；
//   - Cache 暴露 Client()：单方法覆盖全部消费路径（直连 New / contract
//     Provider / AppProvider 产物——产物里的 "redis" 值本身就是 *Cache），
//     UniversalClient 一型对齐三种部署形态——取之。
//
// 用法（AppProvider 产物取原生 client）：
//
//	rc := app.Resource("cache.redis").(*rediscache.Cache) // 精确子段路径直返实例（map 展开仅发生在顶层段聚合）
//	rdb := rc.Client() // goredis.UniversalClient，与适配器同一实例
//	n, err := rdb.Incr(ctx, "captcha:attempts").Result()

package redis

import (
	goredis "github.com/redis/go-redis/v9"
)

// Client 返回底层原生 go-redis client——与 Cache 持有的是**同一实例**
// （非副本、非代理）：适配器方法与业务经本句柄发起的操作共用同一连接池、
// 同一键空间。注意适配器的 keyPrefix 只对适配器方法生效，经本句柄读写
// 裸键不经过前缀。
//
// 所有权不转移：client 归创建方管理——直连 New 的场景归调用方（Cache.Close
// 不会关闭它，见 New 文档）；contract 轨（AppProvider / Provider）归构造期
// 注册的 cleanup（停机关闭连接池），业务侧使用本句柄时勿再 Close。
// 集群 / 哨兵部署下动态类型为 *goredis.ClusterClient / *goredis.FailoverClient，
// 需要具体形态时自行断言。
func (c *Cache) Client() goredis.UniversalClient {
	return c.client
}
