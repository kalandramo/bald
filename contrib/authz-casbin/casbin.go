// Package authzcasbin 实现基于 casbin 的 bald authz.Authorizer 桥接，
// 自 go-bald-admin 范例 internal/security/casbin 晋升（见 docs/devel/zh-CN/架构优化路线.md）。
//
// 设计约束（对齐 bald P7/P9 原则）：
//   - 实现 bald 核心的 authz.Authorizer 接口，作为业务侧桥接子模块；
//     核心框架保持零策略引擎耦合，casbin 只存在于本 module（不进 bald 核心）。
//   - 传输中立归一化已由核心拦截器完成（反哺）：gin/grpc Authz 中间件经
//     authz.DefaultHTTPObject/DefaultHTTPAction（gin）与 authz.DefaultGRPCObject/
//     DefaultGRPCAction（grpc）在拦截器层把请求翻译为 (资源名, 动作)，本桥接只做纯 Enforce，
//     不再重复归一化（CR Issue4 根因：双命名空间泄漏，已在核心层根治）。
//
// 职责划分：RBAC **模型**（.conf）是框架性声明，由本包内嵌为默认；**策略**（.csv，
// 角色→权限、subject→角色）是业务数据，由调用方注入（通常 go:embed 业务自己的 csv）。
//
// 归一化后的权限点约定（casbin 的 obj:act）：
//
//	GET /v1/secret/123 (HTTP) / SecretService/GetSecret (gRPC) -> obj="secret", act="get"
//	DEL /v1/secret/123            / SecretService/DeleteSecret -> obj="secret", act="delete"
//	                               / SecretService/ListUsers  -> obj="secret", act="list"
package authzcasbin

import (
	"context"
	_ "embed"
	"fmt"
	"strings"
	"sync"

	"github.com/casbin/casbin/v2"
	casbinmodel "github.com/casbin/casbin/v2/model"
	"github.com/casbin/casbin/v2/persist"
	"github.com/casbin/casbin/v2/persist/string-adapter"

	"github.com/kalandramo/bald/pkg/authz"
)

//go:embed rbac_model.conf
var defaultModel string

// Authorizer 是基于 casbin 的 RBAC 授权器，实现 authz.Authorizer。
//
// 自 D7 起额外实现 authz.ReloadableAuthorizer：策略（.csv）可在运行期重载，
// 无需重启进程。策略源是「构造时注入的策略文本/适配器」——Reload 从**同一
// 适配器**重新装载（见 Reload）。
type Authorizer struct {
	enf *casbin.Enforcer

	// mu 串行化 Reload 与 Authorize：casbin Enforcer 的 LoadPolicy 会重建
	// 内部策略状态，与并发 Enforce 交叉会数据竞争。RWMutex 让判定走读锁、
	// 重载走写锁，常态下判定无额外串行。
	mu sync.RWMutex

	// adapter 是策略源；nil 表示构造时无策略（空策略 fail-closed 初始态），
	// 此时 Reload 为合法无操作（无可重载之源）。
	adapter persist.Adapter

	// cache 是 (subject, object, action) → 判定 的有界 LRU 结果缓存（批 6），
	// 吸收 casbin 引擎逐请求分配（命中路径零 Enforce、零分配）。安全语义见
	// cache.go：只缓存无错误判定、Reload 整体失效、容量有界、命中不以任何
	// 方式放宽放行面。锁序：本结构先 a.mu 后 cache.mu（两把锁从不反序嵌套）。
	cache *enforceCache
}

// New 用内嵌的默认 RBAC 模型 + 调用方策略构造 casbin 授权器。
// policyCSV 为 casbin 策略文本（通常是业务 go:embed 的 .csv），形如：
//
//	p, admin, secret, get
//	g, u-admin, admin
func New(policyCSV string) (*Authorizer, error) {
	return NewWithModel(defaultModel, policyCSV)
}

// NewWithModel 用自定义模型与策略构造授权器（默认模型不满足时使用，如 ABAC/域模型）。
// 空策略文本是合法输入（fail-closed 初始态：无任何 p/g 行时 Enforce 一律拒绝）——
// 此时不喂 stringadapter（其 LoadPolicy 对空文本报 invalid line），构造无策略 enforcer。
func NewWithModel(modelConf, policyCSV string) (*Authorizer, error) {
	m, err := casbinmodel.NewModelFromString(modelConf)
	if err != nil {
		return nil, fmt.Errorf("casbin: parse model: %w", err)
	}
	var (
		enf     *casbin.Enforcer
		adapter persist.Adapter
	)
	if strings.TrimSpace(policyCSV) == "" {
		enf, err = casbin.NewEnforcer(m)
	} else {
		adapter = stringadapter.NewAdapter(policyCSV)
		enf, err = casbin.NewEnforcer(m, adapter)
	}
	if err != nil {
		return nil, fmt.Errorf("casbin: new enforcer: %w", err)
	}
	return &Authorizer{enf: enf, adapter: adapter, cache: newEnforceCache(defaultEnforceCacheCapacity)}, nil
}

// Authorize 判定 subject（用户 ID）是否对 (object, action) 有权限。
//   - subject 为空（未认证）→ 报错拒绝；
//   - casbin Enforce 返回 false → 拒绝（默认拒绝，需显式授权）；
//   - 引擎错误 → 透传 error（拦截器映射为 500）。
//
// 判定结果经有界 LRU 缓存（见 [enforceCache]）：Web 流量对同一 (subject,
// object, action) 高度重入（同一用户反复访问同资源），命中路径不经引擎、
// 零分配。命中返回的是**曾依据当时策略算出的结果**——策略在 Reload 时整体
// 失效（见 [Authorizer.Reload]），错误永不入缓存；未命中照常评估，评估语义
// fail-closed 不变。键含 ToLower 后的 action，大小写变体共享同一判定。
//
// object/action 已由核心拦截器归一化（见包文档），此处直接作为 (obj, act) 喂给 casbin。
func (a *Authorizer) Authorize(ctx context.Context, subject, object, action string) (bool, error) {
	if subject == "" {
		return false, fmt.Errorf("casbin: empty subject (not authenticated)")
	}
	act := strings.ToLower(action)
	key := enforceKey{subject: subject, object: object, action: act}
	if allowed, ok := a.cache.get(key); ok {
		return allowed, nil
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	allowed, err := a.enf.Enforce(subject, object, act)
	if err != nil {
		return false, fmt.Errorf("casbin: enforce: %w", err)
	}
	a.cache.put(key, allowed)
	return allowed, nil
}

// Reload 从策略源重新装载策略，使运行期变更（如新注册用户的角色绑定）立即
// 对后续 Authorize 生效，无需重启进程。
//
// 语义：
//   - 写锁独占，与并发的 Authorize 判定互斥（LoadPolicy 会重建内部策略状态，
//     与 Enforce 交叉构成数据竞争）；
//   - 空策略初始态（构造时无策略文本）下为合法无操作：无源可重载，返回 nil；
//   - 装载失败时 **casbin 的 LoadPolicy 语义保证旧策略仍在**（fail-safe：
//     失败的 LoadPolicy 不改变已加载的策略），错误透传给调用方记录/重试；
//   - 重载触碰策略源，判定缓存（[enforceCache]）随之**整体失效**——装载成败
//     皆失效：成功自不必说；失败时旧策略虽原样保留，多一次冷启动只是性能
//     损失，而「Reload 后缓存必空」的不变量不留例外（安全红线：任何策略
//     变更路径都不得让旧判定越过重载继续生效）。
//
// 实现 authz.ReloadableAuthorizer。
func (a *Authorizer) Reload() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.adapter == nil {
		return nil // 空策略初始态：无源可重载（无策略变更，缓存无需失效）
	}
	err := a.enf.LoadPolicy()
	a.cache.invalidate()
	if err != nil {
		return fmt.Errorf("casbin: reload policy: %w", err)
	}
	return nil
}

// compile-time 断言：*Authorizer 实现 authz.Authorizer 与 authz.ReloadableAuthorizer。
var (
	_ authz.Authorizer           = (*Authorizer)(nil)
	_ authz.ReloadableAuthorizer = (*Authorizer)(nil)
)

// NewWithAdapter 用自定义模型 + 任意 casbin 适配器构造授权器。
//
// 这是 [Reload] 有**真实语义**的构造路径：当 adapter 指向可变策略源（文件
// adapter、DB adapter、自定义实现）时，Reload 会从该源重新读取——运行期策略
// 变更（新增角色绑定等）得以生效。相比之下 [New]/[NewWithModel] 用字符串
// adapter，源是构造时快照的文本，Reload 是合法但内容不变的重载。
//
// 适配器需实现 casbin persist.Adapter（LoadPolicy/SavePolicy）。传入 nil 与
// [NewWithModel] 传空策略等价（fail-closed 初始态，Reload 为无操作）。
func NewWithAdapter(modelConf string, adapter persist.Adapter) (*Authorizer, error) {
	m, err := casbinmodel.NewModelFromString(modelConf)
	if err != nil {
		return nil, fmt.Errorf("casbin: parse model: %w", err)
	}
	var enf *casbin.Enforcer
	if adapter == nil {
		enf, err = casbin.NewEnforcer(m)
	} else {
		enf, err = casbin.NewEnforcer(m, adapter)
	}
	if err != nil {
		return nil, fmt.Errorf("casbin: new enforcer: %w", err)
	}
	return &Authorizer{enf: enf, adapter: adapter, cache: newEnforceCache(defaultEnforceCacheCapacity)}, nil
}
