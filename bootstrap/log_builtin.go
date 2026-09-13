package bootstrap

import (
	aliyuncontract "github.com/kalandramo/bald/log/aliyun/contract"
	charmcontract "github.com/kalandramo/bald/log/charm/contract"
	lokicontract "github.com/kalandramo/bald/log/loki/contract"
	sentrycontract "github.com/kalandramo/bald/log/sentry/contract"
	tencentcontract "github.com/kalandramo/bald/log/tencent/contract"
)

// 内置日志后端注册清单：bootstrap 自有 provider（slog/nop，随本包分发）
// + 五个独立后端 module 的契约适配器（经各 log/<backend>/contract 包显式注册）。
// 注册名一律采用契约 logger.type 的小写字符串值。
//
// 依赖账本说明：bootstrap 本就是重聚合 module（经 bconfig 带入 consul/vault/
// nacos/etcd/apollo/k8s client-go 等间接依赖），此处全量捆绑与 bconfig 配置源
// 「多选一、全捆绑」是同一模式先例——应用只用一个后端，但所有后端都在依赖树里。
// 介意二进制体积者可不走内置注册，仍用 NewLogRegistry + MustRegister 精选。
var builtinLogProviders = []struct {
	name     string
	provider LoggerProvider
}{
	{"slog", BslogLoggerProvider()},
	{"nop", NopLoggerProvider()},
	{lokicontract.Type, lokicontract.Provider},
	{aliyuncontract.Type, aliyuncontract.Provider},
	{tencentcontract.Type, tencentcontract.Provider},
	{sentrycontract.Type, sentrycontract.Provider},
	{charmcontract.Type, charmcontract.Provider},
}

// RegisterBuiltinLogProviders 把全部内置日志后端注册进 r：
// slog / nop / loki / aliyun / tencent / sentry / charm。
//
// 可与自定义注册组合——先内置再追加，或反之（重名 fail-fast 由 Register 保证）：
//
//	reg := bootstrap.NewLogRegistry()
//	if err := bootstrap.RegisterBuiltinLogProviders(reg); err != nil { ... }
//	reg.MustRegister("mylog", myProvider)
//
// 注册名单为编译期固定常量，重名只可能是编程错误（如对同一 registry 重复调用），
// 出错即 fail-fast。
func RegisterBuiltinLogProviders(r *LogRegistry) error {
	for _, p := range builtinLogProviders {
		if err := r.Register(p.name, p.provider); err != nil {
			return err
		}
	}
	return nil
}

// NewBuiltinLogRegistry 创建内置全量注册表（空表 + 7 个内置后端）。
// appkit 默认装配路径用它实现「业务侧纯配置声明 logger.type、零注册代码」。
func NewBuiltinLogRegistry() *LogRegistry {
	r := NewLogRegistry()
	if err := RegisterBuiltinLogProviders(r); err != nil {
		// 名单为编译期常量，空表上注册不可能重名——到达此处即内部不变量被破坏。
		panic(err)
	}
	return r
}
