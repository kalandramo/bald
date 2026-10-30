// Package bootstrap 提供两类装配组件：
//
//   - 契约段 → 组件的构造器：配置源（Nacos/Env/File/Etcd/Consul/Apollo/HTTP/
//     Kubernetes/Vault）、日志 Options、协议服务器 Provider；
//   - 契约无关的薄适配：把 bconf 契约字段映射为后端包的 Option，使后端包
//     保持零契约依赖。
//
// 装配编排不在这里：段的消费、阶段、依赖与停机序由 pkg/appkit 的声明式
// 引擎负责（Segment Provider 模型，见《Provider 接入》）。本包只回答
// 「给定契约的某一段，怎么造出对应的组件」。
//
// 设计原则：显式构造，不用 init() + blank import——调用方在代码里显式选择
// 组件，依赖图全程可见。
package bootstrap

import (
	"context"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/bootstrap/config"
)

// ConfigProvider 是配置源工厂：从契约读取一个子配置，构造并返回一个命名配置层。
//
// 返回值语义：
//   - layer 为 nil 表示「该源未在契约中配置」，调用方跳过（非错误）；
//   - layer.Reader 应为整文档源（Load(ctx, "") 返回整份配置文档），实现
//     bconfig.ValueWatcher 时可参与 Store 热更新（layer.Watch 置 true）；
//   - cleanup 释放 provider 持有的资源（含 reader 本身，Store 不重复关闭），可为 nil；
//   - 出错返回 error，调用方短路并回滚已构造的源。
//
// 典型用法是自己实现 config 段 Provider，按需组合这些工厂（示例：
// _example/bald 与 bald-admin 的 remoteConfigProvider），而不是交给框架隐式装配。
type ConfigProvider func(ctx context.Context, cfg *bootstrapv1.BootstrapConfig) (*config.Layer, func(), error)
