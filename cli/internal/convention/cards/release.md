---
id: release
title: 发布
level: 强制
category: ops
sources:
  - docs/devel/zh-CN/多模块发布与变更日志.md
checks: []
---
# 发布

多模块发布走 bald release 四步。本卡是 commit-r05 的展开；完整流程与故障案例见母文档《多模块发布与变更日志》。

- [release-r01] [强制] 发布必须走四步：plan → sync → changelog → tag；禁止跳过计划直接 git tag（go.mod 归位与 changelog 会和 tag 脱节）。
- [release-r02] [强制] 所有 release 写操作默认关闭：先跑 dry-run（plan / changelog 不带参数、tag 不带 --apply），确认输出后再 --write/--apply。
- [release-r03] [强制] sync/changelog 前校验计划新鲜度：计划记录的 head ≠ 当前 HEAD 直接拒绝——重跑 plan 放行，旧结论不能用。
- [release-r04] [强制] 打 tag 前确认 HEAD 那轮 ci run 是 completed + success（cancelled 不构成发版依据）；发布提交推上去后、tag 打完前不再往该分支 push。
- [release-r05] [强制] 版本判级按提交最高等级：feat=minor；fix/perf/refactor/revert=patch；type!: 或 BREAKING CHANGE=1.x 升 major/0.x 升 minor；docs/chore/test/ci 不触发发布；无历史 tag 的模块首版固定 v0.1.0。
- [release-r06] [强制] tag --apply 前五道守卫：工作区干净、HEAD 相对计划只多 CHANGELOG.md 与 go.mod、目标 tag 不存在、--push 与 --apply 同用、待发文件路径能进模块 zip（ASCII 合法字符集）。
- [release-r07] [强制] --push 每批最多 3 个 tag：GitHub 对一次推送超 3 个 tag 不产生 push 事件，会漏触发依赖 tag 的流水线（如 publish-bconf）。
- [release-r08] [强制] _example/ 与 cobramcp/examples/* 不发布、不写 changelog，但作为消费者模块参与 require 归位。
- [release-r09] [强制] bconf 与 cli 同批计划时 cli 必须排下一轮（cli 零 replace 无本地兜底）：先出 bconf tag 并确认目标代理解析到（.info 返回 200），再对 cli 归位。
- [release-r10] [建议] sync --write 后逐 module 跑 GOPROXY=off GOFLAGS=-mod=readonly go list ./... 验叠加层零红；诊断一律 -mod=readonly（-mod=mod 会改写 100+ 个 go.mod 搞脏发布面）。
