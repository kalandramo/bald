---
id: codegen
title: 代码生成纪律
level: 强制
category: codegen
sources:
  - docs/devel/zh-CN/代码生成工具设计.md
  - cli/cmd/bald/main.go
checks:
  - gen-app-drift
---
# 代码生成纪律

AppSpec 声明驱动：骨架是声明的函数。改声明 → 重新生成 → --check 验证；生成树不是手写代码的画布。

- [codegen-r01] [强制] AppSpec（appspec.json，protojson）是应用装配的单一真相源：改装配先改声明，再 `bald gen app --adopt` 重新生成；不手改生成树。
- [codegen-r02] [强制] owned 生成物（文件头 DO NOT EDIT）禁止手改；业务只写 user 文件（appspec.json、biz/model 等声明与业务代码）与 seeded 文件（seed/ 等，生成一次归你维护）。
- [codegen-r03] [强制] 生成树的门禁是零差异：`bald gen app --check` 与声明或清单不一致即失败（本卡声明的 gen-app-drift checker 就是这道门），差异时用 --adopt 收敛。
- [codegen-r04] [强制] 域产物（REST 路由绑定、授权投影、proto 契约）用 `bald gen domain --spec` 生成，不手写绑定面；--check 同样零差异。
- [codegen-r05] [强制] proto 是 API 的单一真相源（CRUD 六件套 + buf.validate 注解驱动 REST 与校验），gRPC/REST/前端共享同一份规则，不在多入口各写一遍。
- [codegen-r06] [建议] 生成器行为用版本钉住：`go install github.com/kalandramo/bald/cli/cmd/bald@vX.Y.Z`；@latest 只在确认要跟随生成器演进时用。
