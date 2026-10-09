package appkit

import (
	"net/http"
	"strings"
	"testing"

	bconf "github.com/kalandramo/bald/bconf"
)

// testConfigNamespace 是本包测试统一的配置命名空间。
//
// 取 "bald-app" 的理由：与 bconf.NewBootstrap() 的契约默认 App.Name 同值，
// 使迁移前后 config.Load 的 Name 实参完全一致——48 处调用的迁移是**纯粹**
// 的「补声明」，不夹带任何行为变更，测试若变红即说明迁移出错。
//
// 注意它不等于任何测试的 a.name：服务身份仍由各契约 app.name 驱动。
const testConfigNamespace = "bald-app"

// 本文件钉住「配置命名空间与 app.name 解耦」的不变量。
//
// 背景：AppKit.name 曾双肩挑——既是服务身份（日志/注册中心），又是配置
// 命名空间（env 前缀 + 多环境文件名）。FromBootstrap 路径下 name 取自契约
// app.name，而 app.name 可被配置文件/env 供给，于是「配置里的 app.name」
// 反过来决定「能读到哪些环境变量」——自指循环，症状是 env 覆盖静默失效。
//
// 解耦后：命名空间走独立字段 cfgNamespace，FromBootstrap 要求显式声明。
// 以下测试把这条边界钉死，防止将来有人「顺手」恢复回退。

// TestFromBootstrap_NamespaceRequired 未声明命名空间时构造期 fail-fast。
//
// RED 意义：修复前该调用会成功（静默用契约 app.name 作前缀），
// 本测试在修复前必红——这正是自指 footgun 的入口。
func TestFromBootstrap_NamespaceRequired(t *testing.T) {
	cfg := bconf.NewBootstrap()
	_, err := FromBootstrap(cfg, WithHTTP(new(http.ServeMux)))
	if err == nil {
		t.Fatal("FromBootstrap without WithConfigNamespace succeeded; want fail-fast error")
	}
	// 文案必须能一步定位解法：给出可复制的 Option 名。
	if !strings.Contains(err.Error(), "WithConfigNamespace") {
		t.Fatalf("error message must name the fix (WithConfigNamespace), got: %v", err)
	}
}

// TestFromBootstrap_NamespaceFromOption 显式声明后命名空间就位，
// 且**不受契约 app.name 影响**——这是解耦的核心断言。
func TestFromBootstrap_NamespaceFromOption(t *testing.T) {
	cfg := bconf.NewBootstrap()
	cfg.App.Name = "service-identity" // 服务身份，与命名空间无关

	a, err := FromBootstrap(cfg,
		WithHTTP(new(http.ServeMux)),
		WithConfigNamespace("cfg-ns"),
	)
	if err != nil {
		t.Fatalf("FromBootstrap: %v", err)
	}

	if a.cfgNamespace != "cfg-ns" {
		t.Fatalf("cfgNamespace = %q, want cfg-ns", a.cfgNamespace)
	}
	// 服务身份仍走契约 app.name。
	if a.name != "service-identity" {
		t.Fatalf("name = %q, want service-identity", a.name)
	}
	// 关键：两者已解耦——命名空间不等于服务身份。
	if a.cfgNamespace == a.name {
		t.Fatal("cfgNamespace and name must be independent fields")
	}
}

// TestConfigNamespace_OverridesName New 路径下 ConfigNamespace 优先于 Name。
//
// 同时覆盖**书写顺序无关**：无论 ConfigNamespace 在 Name 之前还是之后，
// 结果都必须是 ConfigNamespace 的值（回退实现放在 opts 循环之后保证这点）。
func TestConfigNamespace_OverridesName(t *testing.T) {
	cases := []struct {
		name string
		opts []Option
		want string
	}{
		{"ns-after-name", []Option{Name("svc"), ConfigNamespace("ns")}, "ns"},
		{"ns-before-name", []Option{ConfigNamespace("ns"), Name("svc")}, "ns"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := New(tc.opts...)
			if a.cfgNamespace != tc.want {
				t.Fatalf("cfgNamespace = %q, want %q", a.cfgNamespace, tc.want)
			}
		})
	}
}

// TestConfigNamespace_FallsBackToName New 路径未声明时回退 Name——
// 该值来自调用方源码（代码层稳定源），不构成自指。
func TestConfigNamespace_FallsBackToName(t *testing.T) {
	a := New(Name("code-layer-ns"))
	if a.cfgNamespace != "code-layer-ns" {
		t.Fatalf("cfgNamespace = %q, want fallback to Name value", a.cfgNamespace)
	}
}

// TestConfigNamespace_DefaultsWithoutName 两个都不传时走 New 的内置默认，
// 不得为空（config.Load 要求 Name 非空，空即启动失败）。
func TestConfigNamespace_DefaultsWithoutName(t *testing.T) {
	a := New()
	if a.cfgNamespace == "" {
		t.Fatal("cfgNamespace must never be empty (config.Load requires non-empty Name)")
	}
}
