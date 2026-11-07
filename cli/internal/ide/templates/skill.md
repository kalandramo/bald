---
name: bald-conventions
description: bald 服务框架的工程规范查询。写代码、改装配（appspec.json / gen）、接能力包、提交发布之前，先取对应规范卡再动手。
---

bald 的工程规范以「蒸馏卡」组织，先总览后按需取用：

1. `bald convention overview`（或 MCP 工具 `bald_convention_overview`）列出全部卡与级别；
2. 按任务取卡（MCP 工具 `bald_convention_get`，入参 id）：

   | 任务 | 卡 id |
   |---|---|
   | 写业务域、改装配、生成物纪律 | `codegen` → `judgments` |
   | 找能力包、接中间件、复用判定 | `catalog` → `reuse` |
   | 提交信息、发布流程 | `commit` → `gates` |

3. 卡内规则带 ID（如 `codegen-r03`）与级别前缀（[强制]/[建议]/[参考]）：
   [强制] 违反即评审打回；[建议] 偏离需说明理由；[参考] 按情境采纳；
4. 写完跑 `bald convention check`（CI 侧用 `--fail-on-findings`）；
   check 只覆盖可确定性判断的规则，通过不代表符合规范，代码评审不可替代。
