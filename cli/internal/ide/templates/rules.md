# bald 工程规范通道

本仓库用 bald（github.com/kalandramo/bald）做声明式装配。**写代码前先查规范**——
规范以蒸馏卡组织，先总览后按需取用：

```bash
bald convention overview        # 先看有哪些规范卡（索引 + 级别语义）
bald convention get <卡id>      # 取卡全文（MCP 工具：bald_convention_get）
```

## 何时查哪张卡

| 任务 | 卡 id |
|---|---|
| 写业务域、改装配（appspec.json / gen）、生成物纪律 | `codegen` → `judgments` |
| 找能力包、接中间件、复用判定 | `catalog` → `reuse` |
| 提交信息、发布流程、门禁 | `commit` → `gates` |

## 最小强制集

- **复用优先**：落笔前先查 bald 能力面（pkg/、contrib/、各能力 module），已有的一律复用；
  不复制、不 vendor bald 代码，始终 Go module 依赖引入。
- **装配声明化**：改装配先改 appspec.json，再 `bald gen app --adopt` 重新生成；
  owned 生成物（DO NOT EDIT）禁止手改，差异用 `--check` 拦截。
- **装配纪律**：装配知识走 appkit 段驱动 Provider 契约；配置取值只有 bconf 契约一条通道，
  未实现配置段 fail-fast。
- **错误与日志**：错误统一 berrors（边界转换 grpcerr/httperr）；日志走 log/bslog，
  英文、KV 结构化、脱敏，错误 key 固定用 "error"。
- **提交**：Conventional Commits `<type>(<scope>): <subject>`；发布走 `bald release`，不手工打 tag。
- **门禁**：提交前 `bald convention check --fail-on-findings`。

卡内规则带 ID（如 `codegen-r03`）与级别前缀：[强制] 违反即评审打回；[建议] 偏离需说明理由；
[参考] 按情境采纳。写完跑 `bald convention check`——check 只覆盖可确定性判断的规则，
通过不代表符合规范，代码评审不可替代。
