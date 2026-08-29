module _ // Version lock for protoc-gen-defaults (managed manually, bingo-compatible).
//
// protoc-gen-defaults 是 Protobuf 代码生成插件，依据 proto 字段的
// (defaults.value) 注解生成 Defaulter.Default() 方法，用于消除配置默认值
// 的双真相源（见 docs/devel/zh-CN/架构改进路线图.md P1）。
//
// 该二进制为「开发期/生成期工具」，不进入 bald 核心 go.mod
// （与 P5「核心不承载非运行时依赖」的治理原则一致）。
//
// 版本固定方式（任选其一）：
//   1. bingo 管理：在仓库根执行 `bingo get protoc-gen-defaults`，
//      由本文件 + 兄弟文件（Variables.mk / variables.env）驱动。
//   2. 手动安装：make proto-config 会在 PATH 缺失该二进制时，
//      自动 `go install github.com/onexstack/protoc-gen-defaults@<pin>`。
//
// Pin 版本（升级时同步更新下方 require 与 Makefile 的 PROTOC_GEN_DEFAULTS_VERSION）：
//   建议钉到 onexstack/protoc-gen-defaults 的最新稳定 tag 或具名 commit。

go 1.26.5

require github.com/onexstack/protoc-gen-defaults v0.0.0-20260626125723-668db92c2c00 // protoc-gen-defaults
