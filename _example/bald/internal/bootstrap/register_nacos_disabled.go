//go:build !nacos

// 默认构建（无 nacos tag）下，nacosBootstrapOptions 返回空：
// 不引入任何 nacos 依赖，assemble.go 可直接调用而不必加构建约束判断。
package bootstrap

import "github.com/kalandramo/bald/pkg/appkit"

func nacosBootstrapOptions() []appkit.Option { return nil }
