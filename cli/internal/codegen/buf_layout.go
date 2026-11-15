package codegen

// buf_layout.go 回答一个具体问题：这棵树的 proto 生成物落在哪个 Go 包路径下？
//
// 答案只能从树里读，不能从框架里假设。同一个框架的两代布局都真实存在：
//
//	模板（gen app 今天播种的）：go_package_prefix = <module>/api/gen/go，插件 out: gen/go
//	                             → proto/post/v1/post.proto 生成到 api/gen/go/post/v1/
//	早期树（_example/bald）：    go_package_prefix = <module>/api/gen，     插件 out: gen
//	                             → proto/post/v1/post.proto 生成到 api/gen/post/v1/
//
// gen resource 要给**别人的树**补资源，所以必须先读这份配置再决定写什么
// option go_package。猜错的后果不是「文件多一个 go 段」，而是 buf generate 在 A
// 处产出、绑定在 B 处 import——一个必然编译失败的树。

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// errNoGoPackageSource 是「既没 buf 前缀也没 module」时的错误：两条派生源都
// 缺席，任何猜测都会产出 import 不到自己的树。
var errNoGoPackageSource = errors.New("cannot derive option go_package: no buf managed-mode go_package_prefix found " +
	"and meta.module is empty (set meta.module, or declare the domain with an explicit proto_package)")

// reGoPackagePrefix 抓 buf managed mode 里 go_package_prefix 的取值：
//
//	override:
//	  - file_option: go_package_prefix
//	    value: example.com/mod/api/gen
var reGoPackagePrefix = regexp.MustCompile(`(?m)file_option:\s*go_package_prefix\s*(?:\n|\r)*\s*value:\s*["']?([^\s"']+)`)

// bufGoPackagePrefix 返回树的 managed-mode go 包前缀（读不到时返回空串）。
//
// buf.gen.yaml 相对 proto 根有两个现实位置：api/buf.gen.yaml（gen app 的布局）
// 与 api/proto/buf.gen.yaml（把 buf 根直接放在 proto 目录）。两处都看，先近后远。
func bufGoPackagePrefix(outRoot, protoRoot string) string {
	candidates := []string{
		filepath.Join(protoRoot, "buf.gen.yaml"),
		filepath.Join(filepath.Dir(protoRoot), "buf.gen.yaml"),
	}
	for _, rel := range candidates {
		raw, err := os.ReadFile(filepath.Join(outRoot, rel))
		if err != nil {
			continue
		}
		if m := reGoPackagePrefix.FindStringSubmatch(string(raw)); m != nil {
			return strings.TrimSuffix(strings.TrimSpace(m[1]), "/")
		}
	}
	return ""
}

// protoGoPackage 返回 proto 契约文件应写的 option go_package（"path;alias"）。
//
// 优先沿用树的 managed 前缀（prefix/<domain>/v1）；树里没有 buf 配置时退回
// gen app 的模板约定（<module>/api/gen/go/<domain>/v1）。两者都拿不到就报错——
// 猜一个 module 出来只会生成一棵 import 不到自己的树。
func protoGoPackage(outRoot, protoRoot, module, domain string) (string, error) {
	alias := domain + "v1"
	if prefix := bufGoPackagePrefix(outRoot, protoRoot); prefix != "" {
		return prefix + "/" + domain + "/v1;" + alias, nil
	}
	if module == "" {
		return "", errNoGoPackageSource
	}
	return module + "/api/gen/go/" + domain + "/v1;" + alias, nil
}
