package codegen

// spec_discovery.go 集中「不给 --spec 时怎么办」这一条 DX 决策。
//
// 只认当前目录下**精确名** appspec.json：不猜 *.json 通配——多候选时猜错比报错
// 更糟（生成器一旦读错声明，产出的是另一棵树的骨架）。两个入口（gen app / gen
// resource）用同一份实现，避免「某个命令支持自动发现、另一个不支持」的分裂。

import (
	"fmt"
	"io"
	"os"
)

// discoverSpec 在 dst 为空时尝试从 cwd 的 appspec.json 取路径。
// 声明不存在时返回 false（由调用方决定报什么错——各命令的必填说法不同）。
func discoverSpec(dst *string, out io.Writer) bool {
	if *dst != "" {
		return true
	}
	if _, err := os.Stat("appspec.json"); err != nil {
		return false
	}
	*dst = "appspec.json"
	fmt.Fprintln(out, "using appspec.json from current directory")
	return true
}
