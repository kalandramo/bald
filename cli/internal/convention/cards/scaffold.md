---
id: scaffold
title: 脚手架后置步骤
level: 强制
category: core
sources:
  - docs/devel/zh-CN/代码生成工具设计.md
  - docs/devel/zh-CN/框架契约总览.md
checks: []
---
# 脚手架后置步骤

`bald gen app` / `bald gen domain` 产出之后、写业务代码之前的必做步骤（onexai scaffold 卡的 bald 对应）。生成树「能编译」不等于「已就绪」——声明验证、规范通道与门禁接线都在这几步里。

- [scaffold-r01] [强制] gen app 后先 `bald gen app --check` 验证零差异，再进入业务实现；`--adopt` 只在确认声明无误时收敛差异，禁止用 adopt 掩盖手改生成树的事实。
- [scaffold-r02] [强制] 新项目接线 AI 会话引导物：`bald ide init --ide=<你的工具>`——没有引导物，AI 不知道规范去哪里查（「先查规范」的接缝依赖 AGENTS.md 与 MCP 配置）。
- [scaffold-r03] [强制] 提交前跑完整门禁：`bald convention check --fail-on-findings` + 各 module `task lint`/测试 + 消费者模块的 acceptance（`task acceptance`）。门禁表见仓库根 AGENTS.md；check 通过不代表代码符合规范——它只是机械化底线，评审不可替代。
- [scaffold-r04] [强制] 声明字段必须有消费者：appspec 新增的契约字段要么被生成模板消费、要么有显式注释落点（机器守卫 fieldConsumers 会红）——声明了却不生效是最坏结果（J7 同形）。
- [scaffold-r05] [建议] gen app 产出的 configs 示例按需裁剪后随项目提交；`docs/prd/` 的 PRD 工作流产物（prompt/prd/translate-prompt）与代码一起提交——新人拉下代码即看到需求基准。
