//go:build !nacos

// 默认构建（无 nacos tag）下，applyNacosBackends 是空操作：
// 不引入任何 nacos 依赖，main.go 可直接调用而不必加构建约束判断。
package main

import "github.com/kalandramo/bald/pkg/appkit"

func applyNacosBackends(_ *appkit.AppKit) {}
