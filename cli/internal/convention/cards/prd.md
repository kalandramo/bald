---
id: prd
title: PRD 方法论
level: 强制
category: core
sources:
  - docs/architecture/onex-ai.md
checks:
  - prd-prompt-seam
  - prd-structure
---
# PRD 方法论

AI 驱动开发的 PRD 格式、工作流与最佳实践（onexai `convention prd` 的 bald 对应卡；格式锚点是存档的工单管理系统样例——prompt 翻译产物 docs/architecture/onex-ai.md §Prompt）。

工作流：人写需求描述（docs/prd/<name>.prompt.md，自然语言 + 能力清单）→ AI 会话翻译成 PRD（docs/prd/<name>.prd.md，翻译前先查规范）→ PRD 的用户故事是后续设计与实现的验收基准。

- [prd-r01] [强制] PRD 文档结构三段齐备：`# PRD: <系统名>` 一级标题、`## Introduction` 概述段（系统用途与能力面一句话）、按用户故事逐节展开——结构是翻译的产物契约，AI 会话与评审共享同一形状。
- [prd-r02] [强制] 用户故事 ID 化且一节一事：`### US-NNN: <能力名>`（三位数递增），一个故事只承载一个能力面。
- [prd-r03] [强制] 每个故事四要素齐备：`**Status:**`（todo/doing/done）、`**Priority:**`（整数，1 最高）、`**Description:**`（As a <角色>, I want <能力> so that <价值> 三段式）、`**Acceptance Criteria:**`（`- [ ]` 复选框逐条）。
- [prd-r04] [强制] 验收标准可判定：每条是一个可测试的陈述（可观察结果——状态码、字段、行为边界、配置面），不写「合理的」「友好的」「良好的」这类不可判定词。
- [prd-r05] [建议] 故事按 Priority 升序排列；同优先级按交付依赖排（被依赖的在前）。
- [prd-r06] [强制] 需求描述（prompt）尾部带「先查规范。」——翻译之前先查 convention（规范通道的接缝：不查规范直接翻译 = 产物脱离工程既有契约面）。
- [prd-r07] [参考] 横切能力各自成故事：认证、审计、后台任务、CLI 管理面不塞进 CRUD 故事——它们有独立的验收标准与实现面（样例口径：US-001 认证、US-004 后台任务、US-005 CLI 各自独立成节）。
