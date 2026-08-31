package authzcasbin

import (
	"context"
	_ "embed"
	"testing"
)

//go:embed testdata/rbac_policy_test.csv
var testPolicy string

// TestAuthorizer_RBAC 锁定 casbin 桥接的授权语义（真实策略引擎，非内存假表）。
func TestAuthorizer_RBAC(t *testing.T) {
	az, err := New(testPolicy)
	if err != nil {
		t.Fatalf("casbin.New: %v", err)
	}
	ctx := context.Background()

	cases := []struct {
		name   string
		sub    string
		object string // 归一化后的资源名（拦截器层已翻译，见 P9 反哺）
		action string // 归一化后的动作（get/delete/list/write）
		want   bool
	}{
		// admin：全权限（HTTP/gRPC 归一化后同源，桥接只做纯 Enforce）
		{"admin get secret(http)", "u-admin", "secret", "get", true},
		{"admin delete secret(http)", "u-admin", "secret", "delete", true},
		{"admin get secret(grpc)", "u-admin", "secret", "get", true},
		{"admin delete secret(grpc)", "u-admin", "secret", "delete", true},
		{"admin list users(grpc)", "u-admin", "secret", "list", true},
		{"admin whoami(grpc)", "u-admin", "auth", "get", true},
		{"admin whoami(http)", "u-admin", "auth", "get", true},

		// viewer：只读 secret + whoami，无 delete
		{"viewer get secret(http)", "u-alice", "secret", "get", true},
		{"viewer get secret(grpc)", "u-alice", "secret", "get", true},
		{"viewer list users(grpc)", "u-alice", "secret", "list", true},
		{"viewer delete secret(http) denied", "u-alice", "secret", "delete", false},
		{"viewer delete secret(grpc) denied", "u-alice", "secret", "delete", false},

		// 未知 subject：默认拒绝
		{"unknown denied", "u-unknown", "/v1/secret/123", "GET", false},
		// 空 subject：明确报错
		{"empty subject error", "", "/v1/secret/123", "GET", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := az.Authorize(ctx, c.sub, c.object, c.action)
			if c.sub == "" {
				if err == nil {
					t.Fatalf("empty subject: want error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("Authorize: %v", err)
			}
			if got != c.want {
				t.Fatalf("Authorize(sub=%q,obj=%q,act=%q)=%v want %v", c.sub, c.object, c.action, got, c.want)
			}
		})
	}
}

// TestAuthorizer_ActionCaseInsensitive 动作大小写不敏感（核心传大写 HTTP 方法也应命中）。
func TestAuthorizer_ActionCaseInsensitive(t *testing.T) {
	az, err := New(testPolicy)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, err := az.Authorize(context.Background(), "u-admin", "secret", "GET")
	if err != nil || !got {
		t.Fatalf("Authorize GET: got=%v err=%v, want true", got, err)
	}
}

// TestNewWithModel_NoMatchingPolicy 自定义模型入口 + 无关策略可构造（默认拒绝一切）。
// 注：casbin string-adapter 不接受空策略文本，用不含任何角色绑定的策略模拟「全拒绝」。
func TestNewWithModel_NoMatchingPolicy(t *testing.T) {
	az, err := NewWithModel(defaultModel, "p, nobody, other, act\n")
	if err != nil {
		t.Fatalf("NewWithModel: %v", err)
	}
	got, err := az.Authorize(context.Background(), "u-admin", "secret", "get")
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if got {
		t.Fatal("policy without matching subject must deny")
	}
}
