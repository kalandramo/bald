---
id: bconf
title: 配置契约
level: 强制
category: contract
sources:
  - docs/devel/zh-CN/Bald 配置契约设计.md
checks: []
---
# 配置契约

bconf = 配置形状的单一真相源（proto 契约层）：只依赖 protobuf+pflag，不读配置（bconfig 的事）、不装配（bootstrap 的事）。

- [bconf-r01] [强制] 配置形状单一真相源是 bconf 的 proto（bootstrap/v1）；改 proto 后跑 task proto-contract 重新生成入库，禁止手改 .pb.go（rawDesc 内嵌描述符带长度前缀，改字符串运行期 panic）。
- [bconf-r02] [强制] 契约演进：删除字段号一律 reserved 不复用；新增纯增量；契约只为已实现的后端承诺形状，未实现的段删掉而非注释掉（判据 J8）。
- [bconf-r03] [强制] 默认值只在 bconf.NewBootstrap() 一处维护；形状校验只在 bconf.Validate 一处。
- [bconf-r04] [强制] 合并必须走 bconf.UnmarshalMap（合并语义）；直接 protojson.Unmarshal 是替换语义，会清掉 NewBootstrap 填的默认值。
- [bconf-r05] [强制] Duration 配置值必须是 protojson 形态（十进制秒 + "s"）；yaml/env/flag 的字符串来源（"1500ms"、"1m30s"）必须经 coerce 归一，原样进 protojson 会炸且丢键名。
- [bconf-r06] [强制] 三态语义（未配置/禁用/启用+参数）用显式子消息或 optional，不用标量 bool——proto3 标量没有 presence，「不表态」与「显式 false」在 wire 上不可分。
- [bconf-r07] [强制] repeated 字段是替换语义；段内裸标量 0 一律读成「未设置」取默认。
- [bconf-r08] [强制] bconf.Validate 只校形状（非空/格式/值域）；后端存在性归装配层覆盖检查（fail-closed）；server.*.middleware 段的 presence 就是「挂不挂这层」（没写=不挂），circuit_breaker 是 opt-in 默认不挂。
- [bconf-r09] [强制] 新配置类型适配只改 bconf 的 coerce 层（map 与 proto 间唯一类型适配点），禁止在加载器散落特判；BindFlags 传 FlagSet 不碰 pflag.CommandLine。
- [bconf-r10] [强制] 配置取值只有契约一条通道（判据 J9）：链外输入必须接进契约或登记理由；未实现的契约段 fail-fast（判据 J7）。
