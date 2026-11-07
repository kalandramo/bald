---
id: errors
title: 错误模型
level: 强制
category: contract
sources:
  - docs/devel/zh-CN/Bald 错误模型设计.md
checks: []
---
# 错误模型

berrors 是传输中立的错误类型；完整 API 与设计取舍见母文档《Bald 错误模型设计》。

- [errors-r01] [强制] 错误按三段用包：产生（biz 构造）与匹配（Is 分支）只 import 根包 berrors；grpcerr/httperr 只出现在传输投影边界，业务代码不碰桥接子包。
- [errors-r02] [强制] 构造工厂优先：berrors.NotFound("REASON") 等既有工厂；New(code, reason) 只留给工厂覆盖不了的场景。
- [errors-r03] [强制] With* 是不可变 builder：必须接住返回值链式派生；就地调用丢弃返回值 = sentinel 污染（跨请求串数据的真实 bug 源）。
- [errors-r04] [强制] 匹配只按 Reason（berrors.Is）：Reason 是稳定契约、永不本地化、不放可变文本；Code 是易变传输分类，不参与匹配。
- [errors-r05] [强制] Message 给最终用户、Details 是 i18n 模板变量；Error() 输出只给日志（刻意不含 Message），展示取字段不取字符串。
- [errors-r06] [强制] 要在 gRPC 携带 Code/Reason，错误必须在拦截器链内侧被构造成 *berrors.Error；链外返回裸 error 只会得到 codes.Unknown。
- [errors-r07] [强制] ErrorInterceptor 必须放在 gRPC 拦截器链最外层，内层（校验/认证）的错误才能全部被收口。
- [errors-r08] [强制] HTTP 错误响应体 = google.rpc.Status JSON（transport/web 的 StatusBody/StatusOf/ErrorResponse），gRPC/转码/gin 三面一份契约；禁止 {code,data,message} 全 200 信封。
- [errors-r09] [强制] web 层取状态码用包函数 httperr.StatusCode(e)，不是类型方法（挂方法会让根包反向 import httperr 造成循环依赖）。
- [errors-r10] [参考] Code 常量数值与 gRPC 协议 1:1，永不改动；非跨进程投影面（transport/broker/appkit 等内部子系统）允许 fmt.Errorf/errors.New，进程内 errors.Is/As 仍可用。
