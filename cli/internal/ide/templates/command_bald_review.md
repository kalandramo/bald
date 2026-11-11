---
description: 按 bald 全量规范评审当前代码并给出修复建议（阶段性再规范化）
---

对当前改动（或 $ARGUMENTS 指定的范围）执行一次规范评审：

1. 先运行 `bald convention overview` 看卡索引；按改动涉及的面向取卡：装配/生成物取 codegen 与 appspec，错误取 errors，日志取 logging，测试取 testing，中间件取 catalog；
2. 逐卡对照改动代码：[强制] 违规逐条列出（引用规则 ID，如「违反 logging-r01：直接 import 标准库 log」）；[建议] 问题汇总为改进建议；
3. 对 [强制] 问题直接修复并说明每处修复对应的规则 ID；[建议] 问题只列不改；
4. 修复后运行 `bald convention check --min-level=recommended --fail-on-findings` 确认清零，给出评审结论（违规数/修复数/遗留建议）。

评审纪律：check 通过不代表代码符合规范（它只是机械化底线）——本次评审补的是 check 覆盖不了的上下文判断项（分层越界、验收可判定性、命名一致性）。

$ARGUMENTS
