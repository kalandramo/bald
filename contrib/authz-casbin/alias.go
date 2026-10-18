package authzcasbin

import (
	"context"
	"sort"
	"sync"

	"github.com/kalandramo/bald/pkg/authz"
)

// AliasAuthorizer 是 object 别名装饰器：内层明确拒绝后，用别名表里的等价名
// 重试一次授权——gen domain 产物 internal/<apiserver_dir>/authz/alias.gen.go
// 的 ObjectAliases 的消费入口。
//
// 背景（bald-admin 教训 + gen 同源派生）：同一业务资源在 gRPC 面
// （DefaultGRPCObject：Service 段去 Service 后缀转小写，如 "greet"）与 HTTP 面
// （DefaultHTTPObject：注解路径首段，如 "greets"）归一化结果可能不同。生成
// 别名表只列两侧不一致的域（gRPC 名 → HTTP 名）；casbin 模型是精确匹配
// （r.obj == p.obj），请求面命名与策略行命名空间不一致时恒拒绝。
//
// 策略行的 object 命名空间由业务自选（gen 产物默认走 gRPC 命名；bald-admin
// 的 DB 策略走 HTTP 命名），别名解析因此**双向**：内层明确拒绝后依次用等价
// 名重试——先查 aliases[object]（正向），再把 object 当值反查等价键（反向，
// 键序确定性）。任一等价名放行即放行；全部拒绝（或无等价名）保持拒绝。
// 双向=等价关系语义：别名表是业务对「两个名字指同一资源」的声明，任一名字
// 授权即该资源授权。
//
// 语义边界（安全相关，勿放宽；与 bald-admin 内部装饰器同纪律）：
//   - 只在**内层明确返回 (false, nil)** 后重试——「拒绝」才换名再问，不是
//     「拒绝就放行」；别名命中仍需内层放行，本装饰器不放宽任何语义边界；
//   - 内层返回 error（引擎故障）时不重试，原样透传（避免把故障误判成命名
//     不匹配，也避免把部分故障放大成整体放行）；
//   - 别名指向自身或空串的条目跳过（不产生自旋）；
//   - aliases 为 nil/空时等价于直接委托内层。
type AliasAuthorizer struct {
	inner authz.Authorizer

	// mu 保护 aliases/reverse 的快照读取（构造后只读；保留字段位以支持
	// 将来可变别名表，当前无写入者）。
	mu sync.RWMutex
	// aliases 正向表（object → 等价名）；reverse 反向索引（等价名 → 键集，
	// 键序确定性）。
	aliases map[string]string
	reverse map[string][]string
}

// NewAlias 用内层授权器与别名表构造装饰器（防御性拷贝入参）。
func NewAlias(inner authz.Authorizer, aliases map[string]string) *AliasAuthorizer {
	fwd := make(map[string]string, len(aliases))
	revSet := map[string]map[string]struct{}{}
	for k, v := range aliases {
		if v == "" || v == k {
			continue // 自旋/空条目不进表
		}
		fwd[k] = v
		revSet[v] = appendSet(revSet[v], k)
	}
	reverse := make(map[string][]string, len(revSet))
	for v, keys := range revSet {
		ks := make([]string, 0, len(keys))
		for k := range keys {
			ks = append(ks, k)
		}
		sort.Strings(ks) // 反查候选确定性
		reverse[v] = ks
	}
	return &AliasAuthorizer{inner: inner, aliases: fwd, reverse: reverse}
}

func appendSet(m map[string]struct{}, k string) map[string]struct{} {
	if m == nil {
		m = map[string]struct{}{}
	}
	m[k] = struct{}{}
	return m
}

// Authorize 实现 authz.Authorizer：内层允许/出错直接透传；明确拒绝后按
// 等价名（正向一条 + 反向键序）逐个重试，任一放行即放行。
func (a *AliasAuthorizer) Authorize(ctx context.Context, subject, object, action string) (bool, error) {
	if a.inner == nil {
		// 无内层授权器：fail-closed（授权是安全边界，缺失时不能放行）。
		return false, nil
	}
	allowed, err := a.inner.Authorize(ctx, subject, object, action)
	if err != nil || allowed {
		// 放行，或引擎出错——都不进入别名重试。
		return allowed, err
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	cands := make([]string, 0, 2)
	if v, ok := a.aliases[object]; ok {
		cands = append(cands, v)
	}
	cands = append(cands, a.reverse[object]...)
	for _, alias := range cands {
		if alias == object {
			continue
		}
		ok2, err2 := a.inner.Authorize(ctx, subject, alias, action)
		if err2 != nil {
			// 重试路径的引擎错误如实上报（fail-closed：不让故障变成放行）。
			return false, err2
		}
		if ok2 {
			return true, nil
		}
	}
	return false, nil
}

// Reload 透传内层的策略重载能力（实现 authz.ReloadableAuthorizer 时）。
//
// 装饰器必须转发能力发现：业务若经 authz.Reload(authorizer) 热重载策略，
// 包一层别名装饰器不应让 Reload 静默失效。内层不支持重载时为合法无操作
// （与 authz.Reload 对不可重载实现的 (false, nil) 语义一致）。
func (a *AliasAuthorizer) Reload() error {
	a.mu.RLock()
	inner := a.inner
	a.mu.RUnlock()
	if rl, ok := inner.(authz.ReloadableAuthorizer); ok {
		return rl.Reload()
	}
	return nil
}

// 编译期契约。
var (
	_ authz.Authorizer           = (*AliasAuthorizer)(nil)
	_ authz.ReloadableAuthorizer = (*AliasAuthorizer)(nil)
)
