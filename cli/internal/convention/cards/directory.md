---
id: directory
title: 目录与填充点
level: 强制
category: codegen
sources:
  - docs/devel/zh-CN/代码生成工具设计.md
checks: []
---
# 目录与填充点

生成树里「什么在哪、谁能写谁不能写」。归属三态以 appspec.manifest.json 清单为准（codegen 卡管生成纪律）。

- [directory-r01] [强制] 声明真相源在根：appspec.json（应用装配）与 domains 清单（域声明）——改目录行为先改声明，不改生成物。
- [directory-r02] [强制] owned 文件（每次重生成，文件头 DO NOT EDIT）：cmd/<app>/{main.go,app/command.go}、internal/bootstrap/{assemble.gen.go,domains.gen.go,...}、internal/apiserver/layering_invariant_test.go、configs/<app>.yaml 等——禁止手改。
- [directory-r03] [强制] user 文件生成器永不读写：appspec.json、biz/model 等业务声明与业务代码——业务只写这里与 seeded 文件。
- [directory-r04] [强制] seeded 文件生成一次归用户维护（kept:）：internal/bootstrap/custom.go（customOptions）、cmd/<app>/app/custom.go（customVariantOpts/customSubcommands）、seed/ 等——AppSpec 表达不了的接线只写这三个钩子。
- [directory-r05] [强制] 新业务域的落点：gen domain 生成域骨架（handler/biz/store 分层），业务实现在域包的填充点补齐，不在装配目录里写业务。
- [directory-r06] [强制] 契约产物进 api/（buf.yaml 扇出配置 + genproto 由业务 module 按需 require）；proto 模板全仓唯一（gen proto），不双轨。
