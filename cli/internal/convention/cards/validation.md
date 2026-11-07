---
id: validation
title: 校验
level: 强制
category: contract
sources:
  - docs/devel/zh-CN/校验设计.md
checks: []
---
# 校验

protovalidate 注解 + pkg/validation 命令式校验的分工。母文档《校验设计》很短，读它即读卡。

- [validation-r01] [强制] 声明式字段级规则（必填/范围/格式）一律写 proto buf.validate 注解由 protovalidate 执行；pkg/validation 只承接注解表达不了的命令式校验（查库、权限比对、跨字段动态逻辑），禁止两套字段规则表达并存。
- [validation-r02] [强制] 方法名约定 = Validate + 请求类型名（如 ValidateXxxRequest），签名 func(recv T, ctx context.Context, req *XxxRequest) error；凡以 Validate 开头的方法约定不满足必须显式报错，禁止静默跳过。
- [validation-r03] [强制] 校验器经 validation.NewValidator(validators) / Validator.Register 注册，注册期严格校验拼写与签名（ValidateUserRequst 这类拼错必须报错）。
- [validation-r04] [强制] Validator.Validate(ctx, req) 对未注册的请求类型放行（fail-open 增量能力）——「写了的必须正确，没写的先放行」，不得反向改成未注册即拒绝。
- [validation-r05] [强制] web 流水线先 Bind 后校验：ShouldBindAll 多源绑定完成后 handler 内一次 validator.Validate，禁止在多层中间件分散校验。
- [validation-r06] [建议] 单字段命令式检查用 validation.ValidRequired/ValidRange/ValidPattern，返回的 error 封装为 berrors 语义错误；NewValidator(nil) 合法（无校验器）。
