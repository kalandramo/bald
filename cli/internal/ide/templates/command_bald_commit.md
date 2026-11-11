---
description: 按 bald 提交规范提交当前改动（Conventional Commits + 门禁前置）
---

按以下步骤提交当前工作树的改动：

1. 先运行 `bald convention get commit` 取提交规范卡（或 MCP 工具 `bald_convention_get`），按其规则写提交信息：Conventional Commits `<type>(<scope>): <中文 subject>`；
2. 运行 `bald convention get gates` 取门禁卡，执行提交前门禁：`bald convention check --fail-on-findings` + 相关 module 的 lint 与测试；
3. 门禁全绿后按规范分组提交（不相关的改动拆分提交），不 push（推送由人工决定）；
4. 最后给出每笔提交的摘要与门禁结果。

$ARGUMENTS
