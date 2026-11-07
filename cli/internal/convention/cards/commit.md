---
id: commit
title: 提交与发布
level: 强制
category: ops
sources:
  - docs/devel/zh-CN/多模块发布与变更日志.md
checks: []
---
# 提交与发布

提交信息写给未来排障的人；发布交给工具链，不手工操作。

- [commit-r01] [强制] 提交信息遵循 Conventional Commits：`<type>(<scope>): <subject>`，冒号后面必须有空格。
- [commit-r02] [强制] type 用 feat/fix/docs/chore/refactor/test/build/ci；scope 是受影响模块或包的名词（跨模块逗号并列，如 `feat(bconf,pkg/appkit): …`），不切得太细。
- [commit-r03] [建议] subject 用中文直述改了什么，现在时，结尾不加句号；正文说明「为什么改」和「改前改后有什么不同」。
- [commit-r04] [强制] 不相关的改动不混在一个提交里；AppSpec 声明变更与其生成物是同一逻辑变更，一起提交。
- [commit-r05] [强制] 发布走 `bald release plan → sync --write → changelog → tag`（从 git 提交历史推导待发布模块与下一版本），不手工打 tag、不手写 CHANGELOG 条目。
- [commit-r06] [建议] 多 module 仓按模块独立 tag 发布；主版本策略见 scripts/multi-major.sh 与母文档《多模块发布与变更日志》。
