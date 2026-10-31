# 变更日志

本文件由 `bald release changelog` 从提交历史生成：每个模块一节，版本按发布时间倒序。
条目取自该版本区间内的提交标题（Conventional Commits），不要手工调整顺序。
发布流程见 [多模块发布与变更日志](docs/devel/zh-CN/多模块发布与变更日志.md)。

## 2026-10-31

### bald v0.32.0

- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `c2e57346` docs: 删除历史路线文档，增加文档路径一致性检查
- `15d09e9f` refactor(appkit,codegen): 按关注点拆分三个大文件
- `b12a8ac9` test(fieldmaskutil): 补 nested_mask 关键路径覆盖
- `b0bef5cd` feat(release): 增加多模块发布计划、依赖归位与变更日志工具
- `4bbf28be` feat(example): 示例域改用默认 REST 绑定并补 e2e
- `43ed27e2` test(ci): 增加 -race 门禁并修复并发缺陷
- `33f19e2e` chore(example): 删除废弃的 bald-gin 示例 module 与外层 go.mod
- `6a64c2f9` refactor(codegen)!: 删除 apiserver 侧挂载镜像生成物，去掉无消费者的 domains.model **(breaking)**
- `cea4e63f` docs: README 30 秒路径补最小 AppSpec 样例，路径可复制即跑
- `ed2e4440` docs: 全仓去除来源引用，只留取舍理由与功能设计
- `ac6a36d5` docs: README 去掉三方设计来源清单
- `599b2127` docs: 架构优化路线 §3.7 去除模板生成器描述
- `e0bf54a8` refactor(codegen)!: 删除 gen app 单文件模板模式，只保留 AppSpec 路径 **(breaking)**
- `fe5f0ec7` chore: 删除仓库根目录的 agent 编排临时文件
- `5fba3495` docs: 面向接手者重构文档，删除考古材料
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### ai/eino v0.1.1

- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### ai/langchaingo v0.1.1

- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### ai/openai v0.1.1

- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### bconf v0.14.0

- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `6a64c2f9` refactor(codegen)!: 删除 apiserver 侧挂载镜像生成物，去掉无消费者的 domains.model **(breaking)**
- `ed2e4440` docs: 全仓去除来源引用，只留取舍理由与功能设计
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### bconfig v0.2.1

- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `ed2e4440` docs: 全仓去除来源引用，只留取舍理由与功能设计
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### bootstrap v0.13.2

- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `c2e57346` docs: 删除历史路线文档，增加文档路径一致性检查
- `ed2e4440` docs: 全仓去除来源引用，只留取舍理由与功能设计
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态
- `90eb0dde` chore: W1 卫生波——非导出死代码清零 + internal/ 免费改名 + 依赖面 tidy 修复 + CI tidy 闸门
- `843d3d2a` chore: 清理注释中的批次/排期/迁移痕迹——只留代码逻辑注释

### broker/kafka v0.2.1

- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### broker/rabbitmq v0.2.1

- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### broker/redis v0.4.1

- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### broker/rocketmq v0.2.1

- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### cache/local v0.1.2

- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### cache/redis v0.3.1

- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### circuitbreaker/sres v0.1.3

- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### cobramcp v0.3.4

- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### contrib/audit-gorm v0.2.2

- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态
- `90eb0dde` chore: W1 卫生波——非导出死代码清零 + internal/ 免费改名 + 依赖面 tidy 修复 + CI tidy 闸门

### contrib/audit-stream v0.4.1

- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态
- `90eb0dde` chore: W1 卫生波——非导出死代码清零 + internal/ 免费改名 + 依赖面 tidy 修复 + CI tidy 闸门

### contrib/database/gorm v0.1.1

- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态
- `90eb0dde` chore: W1 卫生波——非导出死代码清零 + internal/ 免费改名 + 依赖面 tidy 修复 + CI tidy 闸门

### contrib/database/mongodb v0.1.1

- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态
- `90eb0dde` chore: W1 卫生波——非导出死代码清零 + internal/ 免费改名 + 依赖面 tidy 修复 + CI tidy 闸门

### contrib/observability-otlp v0.3.2

- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `c2e57346` docs: 删除历史路线文档，增加文档路径一致性检查
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态
- `90eb0dde` chore: W1 卫生波——非导出死代码清零 + internal/ 免费改名 + 依赖面 tidy 修复 + CI tidy 闸门
- `843d3d2a` chore: 清理注释中的批次/排期/迁移痕迹——只留代码逻辑注释

### contrib/store-mongo v0.1.2

- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态
- `90eb0dde` chore: W1 卫生波——非导出死代码清零 + internal/ 免费改名 + 依赖面 tidy 修复 + CI tidy 闸门

### log v0.5.4

- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `ed2e4440` docs: 全仓去除来源引用，只留取舍理由与功能设计
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态
- `843d3d2a` chore: 清理注释中的批次/排期/迁移痕迹——只留代码逻辑注释

### log/aliyun v0.2.2

- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### log/charm v0.2.2

- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### log/loki v0.2.2

- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### log/sentry v0.2.2

- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### log/tencent v0.2.2

- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### metrics v0.1.2

- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### oss/minio v0.3.1

- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### oss/s3 v0.3.1

- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### ratelimit/bbr v0.1.1

- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### registry v0.3.1

- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `ed2e4440` docs: 全仓去除来源引用，只留取舍理由与功能设计
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### registry/consul v0.1.3

- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态
- `90eb0dde` chore: W1 卫生波——非导出死代码清零 + internal/ 免费改名 + 依赖面 tidy 修复 + CI tidy 闸门

### registry/etcd v0.3.1

- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `ed2e4440` docs: 全仓去除来源引用，只留取舍理由与功能设计
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### registry/kubernetes v0.1.4

- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `ed2e4440` docs: 全仓去除来源引用，只留取舍理由与功能设计
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态
- `90eb0dde` chore: W1 卫生波——非导出死代码清零 + internal/ 免费改名 + 依赖面 tidy 修复 + CI tidy 闸门

### registry/nacos v0.3.1

- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `ed2e4440` docs: 全仓去除来源引用，只留取舍理由与功能设计
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### transport/asynq v0.5.1

- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `ed2e4440` docs: 全仓去除来源引用，只留取舍理由与功能设计
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### transport/cron v0.6.1

- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `ed2e4440` docs: 全仓去除来源引用，只留取舍理由与功能设计
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### transport/gateway v0.4.1

- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### transport/http v0.1.3

- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### transport/redis v0.2.1

- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### transport/sse v0.4.1

- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `43ed27e2` test(ci): 增加 -race 门禁并修复并发缺陷
- `ed2e4440` docs: 全仓去除来源引用，只留取舍理由与功能设计
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### transport/tcp v0.1.2

- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `ed2e4440` docs: 全仓去除来源引用，只留取舍理由与功能设计
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态
- `90eb0dde` chore: W1 卫生波——非导出死代码清零 + internal/ 免费改名 + 依赖面 tidy 修复 + CI tidy 闸门

### transport/websocket v0.1.1

- `43ed27e2` test(ci): 增加 -race 门禁并修复并发缺陷
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态

### workflow/argo v0.2.1

- `b910c0f6` docs(comments): 清理 Go 注释与测试名中的缺陷编号和历史叙事
- `fd1a864c` chore(ci): 引入 golangci-lint 与 govulncheck 全模块门禁
- `76dddabc` refactor: 清理死代码与冗余适配层，Provider 收敛到 v2 单一形态
