---
id: testing
title: 测试
level: 强制
category: codegen
sources:
  - docs/devel/zh-CN/测试工具设计.md
  - README.md
checks:
  - no-panic
---
# 测试

测试辅助与验收纪律。门禁清单见 gates 卡；本卡管「测试代码怎么写」。

- [testing-r01] [强制] 跨项目复用的测试辅助进 pkg/testkit，不在各包 _test.go 各自复制、不塞进核心包污染生产 API 面。
- [testing-r02] [强制] e2e「先拿地址、再启动监听」用 testkit.FreeAddr(tb)；GatewayServer 的后端 gRPC 地址禁用 :0（转码转发时无从得知真实端口）。
- [testing-r03] [强制] 测试监听地址必须带 127.0.0.1 前缀（FreeAddr 语义）；裸 :port 会被 gRPC 客户端解析成 IPv6 导致 connection refused。
- [testing-r04] [强制] 验收腿真相源是 scripts/acceptance-gate.sh 的 rows()，不手写测试清单表；判据先 go test -list 取真实命中再跑 -v 断言 --- PASS（-run 命中 0 个返回 0，手写正则会假绿）。
- [testing-r05] [建议] 每条 -short 跳过的用例要么被 acceptance 表某条正则命中、要么被某个 CI 作业点名（守卫 TestAcceptanceTableCoversShortSkippedTests 判定）。
- [testing-r06] [参考] 测试基础设施与测试用例分离；testkit 按需收编——只收反复出现且与业务无关的辅助，不做垃圾场。
- [testing-r07] [强制] 库代码（非 cmd 入口 main、非 _test.go）禁止 panic：可预期的失败返回 error（业务语义用 berrors 构造），不可恢复的进程级故障在入口用统一出口处理；panic 让「调用方决定怎么处理」的 error 流失效，且跨包恢复不可测试。
