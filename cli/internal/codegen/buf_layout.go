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
	"strings"
)

// errNoGoPackageSource 是「既没 buf 前缀也没 module」时的错误：两条派生源都
// 缺席，任何猜测都会产出 import 不到自己的树。
var errNoGoPackageSource = errors.New("cannot derive option go_package: no buf managed-mode go_package_prefix found " +
	"and meta.module is empty (set meta.module, or declare the domain with an explicit proto_package)")

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
		if v := firstUnqualifiedGoPackagePrefix(string(raw)); v != "" {
			return strings.TrimSuffix(strings.TrimSpace(v), "/")
		}
	}
	return ""
}

// firstUnqualifiedGoPackagePrefix 扫 buf.gen.yaml 的 managed.override，返回第一个
// **不带 path 限定**的 go_package_prefix 条目取值；没有则返回空串。
//
// 为什么不能一条正则「抓第一个 go_package_prefix」：buf 生态常见的写法是第一条
// 带 path 限定（如把 google/protobuf 的包改到 gofeaturepb），真正的前缀在后面的
// 无限定条目里——抓第一个会把新 proto 的 go_package 写进第三方包路径，绑定
// import 一个不存在的包（adv-codegen Bug3，b4 repro 实测）。
func firstUnqualifiedGoPackagePrefix(src string) string {
	lines := strings.Split(src, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "- ") ||
			!strings.Contains(trimmed, "file_option:") ||
			!strings.Contains(trimmed, "go_package_prefix") {
			continue
		}
		entryIndent := len(line) - len(strings.TrimLeft(line, " \t"))
		value, hasPath := "", false
		for j := i + 1; j < len(lines); j++ {
			l := lines[j]
			if strings.TrimSpace(l) == "" {
				continue
			}
			ind := len(l) - len(strings.TrimLeft(l, " \t"))
			t := strings.TrimSpace(l)
			if ind <= entryIndent || strings.HasPrefix(t, "- ") {
				break // 本条目结束
			}
			if strings.HasPrefix(t, "value:") {
				value = strings.Trim(strings.TrimPrefix(t, "value:"), " \t\"'")
			}
			if strings.HasPrefix(t, "path:") {
				hasPath = true
			}
		}
		if value != "" && !hasPath {
			return value
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
