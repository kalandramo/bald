// Command bald-demo-worker 是示例的 worker 角色进程入口
// （对照生成物 cmd/<app>/main.go：唯一的 package main，装配知识为零）。
package main

import (
	"os"

	"github.com/kalandramo/bald/example/bald/cmd/bald-demo-worker/app"
)

func main() {
	if err := app.Execute(); err != nil {
		os.Exit(1)
	}
}
