---
id: bootstrap
title: 配置装载
level: 强制
category: contract
sources:
  - docs/devel/zh-CN/Bald Bootstrap 设计.md
  - docs/devel/zh-CN/Bald 配置系统设计.md
checks: []
---
# 配置装载

bootstrap/config 层模型 + bconfig 多源的装载纪律。契约形状见 bconf 卡。

- [bootstrap-r01] [强制] 配置装配禁止 init() + blank import 隐式全局注册；只有工厂签名 bootstrap.ConfigProvider，层列表由调用方按序拼装。
- [bootstrap-r02] [强制] ConfigProvider 返回值语义：layer==nil = 该源未配置（跳过非错误）；Reader 为 nil、Watch=true 而 Reader 未实现 ValueWatcher 均为装配错误 fail-fast，禁止静默降级。
- [bootstrap-r03] [强制] 层优先级 = 列表序（首元素最高），完整链 flag > env > 本地文件 > 契约源层 > 远程桥（基准）；禁止给 Provider 加 Priority 字段或维护第二优先级真相。
- [bootstrap-r04] [强制] flag 只取显式传入（pflag.Changed == true），零值 flag 禁止压过 env/文件；引导 flag --config 不落配置树。
- [bootstrap-r05] [强制] env 前缀派生自 ConfigNamespace 而非 app.name（消解「env 前缀←配置名←env」自指）；两遍合并用 DeferEnv + Store.MountEnv(name)。
- [bootstrap-r06] [强制] StrictContract 开启后未知键报错（捕获 server.htpp 这类笔误）；业务自持键必须经 AllowBusinessSections 声明独立顶层段——禁止让框架猜。
- [bootstrap-r07] [强制] 热更新全量重合并（结构上保证上层覆盖不污染底层基准）；Store.Settings() 返回深拷贝；解码失败保留旧层缓存但仍触发 OnChange——坏状态可观测而非静默。
- [bootstrap-r08] [强制] 资源释放「谁构造谁释放」：契约源 reader 归 ConfigProvider 返回的 cleanup，Store.Close 只停自建源，禁止重复关闭。
- [bootstrap-r09] [强制] 契约字段→provider Option 的翻译收口在 bootstrap 适配器；bconfig/* 源包零契约依赖（不 import bconf），禁止把适配器下沉进源包。
- [bootstrap-r10] [建议] map→proto 桥接唯一入口 bconf.UnmarshalMap（bconf-r04/r05）；单层装配超时 DefaultLoadTimeout = 10s（远端不可达快速失败）；协议服务器分工「地址/TLS/超时来自契约，路由/拦截器/探针来自代码」——契约配了 grpc 段而代码无 WithGRPC 是配置错误，不静默启动空端口。
