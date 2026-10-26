// Command bald-demo 是示例的 api 角色进程入口
// （对照生成物 cmd/<app>/main.go：唯一的 package main，装配知识为零）。
package main

import (
	"os"

	"github.com/kalandramo/bald/example/bald/cmd/bald-demo/app"
)

func main() {
	if err := app.Execute(); err != nil {
		os.Exit(1)
	}
}
