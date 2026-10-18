// domains.gen.go —— 域清单的承载者（手写的目标形态占位）。
//
// 由 bald gen domain 生成（W3 使其真实——装配层评估 §4.3 接缝裁定 / §4.6
// 新增域触碰清单）。Go 无运行期包发现，域清单必须有承载者：生成物重写本
// 文件聚合全部域声明值，main 的装配清单只展开它——新增业务域 = 手写 3 个
// 文件 + 生成器重写本文件，装配改动 0。init() 自注册禁止（「不做 init()
// 自注册」哲学）：清单显式可 grep——import 树里有什么，这里就列什么。
package main

import (
	"github.com/kalandramo/bald/pkg/appkit"

	greetv1 "github.com/kalandramo/bald/example/bald/greetv1"
)

// domainOptions 是本应用全部业务域的声明值（变参直传 appkit.MustServe；
// 清单序 = 域装配次序）。
var domainOptions = []appkit.Option{
	greetv1.Domain,
}
