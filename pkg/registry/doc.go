// Package registry 定义 bald 框架的服务注册中心抽象。
//
// 设计理念（对齐 go-lulu 的去耦思路）：自带轻量 Registrar 接口，不绑定任何具体
// 注册中心；通过子包提供内存实现（inmemory）用于开发/测试，并可通过 kratos.go
// 的 FromKratos 桥接适配 go-kratos 生态的 etcd/consul/nacos 后端。
//
// 典型用法：
//
//	reg := inmemory.New()
//	app := appkit.New(appkit.Registrar(reg), appkit.Servers(grpcSrv, httpSrv))
//	app.Run(ctx)
package registry
