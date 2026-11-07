---
description: 按 bald 工程规范实现需求（先查规范，再写码，后自检）
---

按以下步骤执行任务（任务描述见末尾）：

1. 先运行 `bald convention overview`，按任务取对应规范卡（`bald convention get <卡id>` 或 MCP 工具 `bald_convention_get`）：写业务域/改装配取 codegen 与 judgments，接中间件/找能力包取 catalog 与 reuse，提交/发布取 commit 与 gates；
2. 按规范实现；AppSpec 相关改动只改声明后用 `bald gen app --adopt` 重新生成，不手改生成树；
3. 实现完成后运行 `bald convention check`，修复所有 [强制] 问题；
4. 最后给出改动摘要与检查结果。

$ARGUMENTS
