---
id: doctor
title: 装配计划与 doctor
level: 强制
category: ops
sources:
  - docs/guide/zh-CN/Bald 装配计划与 doctor.md
checks: []
---
# 装配计划与 doctor

doctor 是装配的只读诊断面：报告与真实启动同源。使用细节见母文档《Bald 装配计划与 doctor》（guide 层）。

- [doctor-r01] [强制] doctor 子命令必须与 serve 共用同一份装配 Options：appcli.WithDoctor(func(ctx) error { return appkit.Doctor(ctx, bootstrap.Options(role)...) })——报告与真实启动不同源的诊断是假的。
- [doctor-r02] [强制] doctor 只读：不执行 Provider.Build、不跑钩子、不写文件、不联网、不写计划缓存（写基线只发生在真实启动）。
- [doctor-r03] [强制] --plan 在任何副作用之前打印计划并以 0 退出、不进入 Run（打印了但服务没起来是设计行为）；计划编译失败（依赖环、重复 Key 无 Override）非 0 退出。
- [doctor-r04] [强制] 读 segments 分组：unconsumed 非空即启动失败（契约有段无消费者，判据 J7）；豁免出口只有 WithUnimplementedSegments 且条目必须是真实契约字段路径。
- [doctor-r05] [强制] opaque: 行逐名列出逃逸舱节点；域应迁到 Domain.Declare/ApplyNode 使 domains: 行 declared=全部、legacy=0。
- [doctor-r06] [强制] CI 稳定判据用 --plan --plan-format=json 的 plan hash 与提交基线比较 + 生成树 gen app/domain --check；本地 .bald/plan-cache.json 含 env 覆盖值，不是 CI 判据。
- [doctor-r07] [强制] Provider 的 Plan 里禁止读环境变量/随机数/时间（破坏确定性、plan hash 每次不同）；不稳定值放 Build 阶段。
- [doctor-r08] [强制] doctor 的 warnings（override/whitelisted/ambient/opaque-budget/programmatic-takeover）逐条处置：显式覆盖要有 Reason、白名单要有豁免理由、build tag 分叉要么声明变体要么 WithAmbient 登记。
- [doctor-r09] [建议] .bald/ 目录进 .gitignore（本地计划缓存与发布计划文件）；多角色应用必须 WithRole，否则计划缓存无法按角色区分基线。
