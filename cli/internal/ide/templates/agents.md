# AGENTS.md

本仓库用 bald（github.com/kalandramo/bald）做声明式装配。**写代码前先查规范**：

```bash
bald convention overview        # 先看有哪些规范卡
bald convention get <卡id>      # 取卡全文（MCP 工具：bald_convention_get）
```

## 最小强制集

- **复用优先**：落笔前先查 bald 能力面（pkg/、contrib/、各能力 module），已有的一律复用；
  不复制、不 vendor bald 代码，始终 Go module 依赖引入。
- **装配声明化**：改装配先改 appspec.json，再 `bald gen app --adopt` 重新生成；
  owned 生成物（DO NOT EDIT）禁止手改，业务只写填充点；差异用 `--check` 拦截。
- **装配纪律**：装配知识走 appkit 段驱动 Provider 契约，不进业务目录；
  配置取值只有 bconf 契约一条通道，未实现配置段 fail-fast。
- **错误与日志**：错误统一 berrors（边界转换 grpcerr/httperr）；日志走 log/bslog，
  英文、KV 结构化、脱敏，错误 key 固定用 "error"。
- **提交**：Conventional Commits `<type>(<scope>): <subject>`；发布走 `bald release`，不手工打 tag。
- **门禁**：提交前 `bald convention check --fail-on-findings`，通过后再人工 review。

## 何时查哪张卡

| 任务 | 取卡 |
|---|---|
| 写业务域 / 改装配 / 改生成物 | `bald convention get codegen`，再取 `judgments` |
| 接中间件 / 找能力包 | `bald convention get catalog`，再取 `reuse` |
| 提交 / 发布 | `bald convention get commit`，再取 `gates` |

check 只覆盖可确定性判断的规则：它通过不代表代码一定符合规范，代码评审不可替代。
