---
id: dev-system
title: 标准化开发体系
level: 强制
category: core
sources:
  - docs/architecture/onexai-gap-review.md
  - docs/devel/zh-CN/设计判据.md
checks: []
---
# 标准化开发体系

bald 的三步方法论（onexai dev-system 卡的 bald 对应）：**规范化 → 标准化 → 工具化**。它回答「AI Coding 时代为什么这套体系有效」——AI 的代码质量问题与传统初中级工程师高度一致，软件工程几十年的团队管控体系就是解法：把规范文档化、把标准工具化、把高质量数据以 AI 友好的方式渐进喂给 AI。

- [dev-system-r01] [强制] 三步各自有载体，不互相替代：规范化 = docs/devel 设计文档与设计判据（J1–J9）；标准化 = 本卡库的蒸馏卡（每条规则带 ID 与母文档锚点）；工具化 = `bald gen` 脚手架 + `bald convention` 查询/check 闸门 + `bald mcp` AI 通道。只有文档没有卡 = 规范不进 AI 上下文；只有卡没有 checker = 靠自觉。
- [dev-system-r02] [强制] 蒸馏卡不是文档复制：卡是「在什么场景下、做什么、做到什么程度」的规则蒸馏（好规则三问），母文档承载完整设计取舍；卡与母文档漂移时以母文档为准并回改卡。
- [dev-system-r03] [强制] 高质量数据四特征约束一切 AI 面产物：结构化（规则带 ID 级别，非散文）、一致性（卡与母文档、卡与生成模板不互相矛盾）、高质量（母文档本身遵循最佳实践）、可扩展（新能力 = 新卡 + 新契约段 + 新 checker 的既有通道）。
- [dev-system-r04] [强制] AI 会话的渐进式喂给：入口是 overview 索引（不是全量规范）；按任务取对应卡（见 AGENTS.md「何时查哪张卡」表）；确定性检查用 check（CI 用 --fail-on-findings）。不要把规范全文塞进 Prompt。
- [dev-system-r05] [建议] 阶段性再规范化优先于频繁优化：功能迭代期间不做规范清扫，按里程碑跑一轮全量 check（--min-level=recommended）一次性收敛——省 token 且减少与业务改动的冲突。
