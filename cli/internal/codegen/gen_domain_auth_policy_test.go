package codegen

import (
	"strings"
	"testing"
)

// TestScanProto_AuthPolicy 钉住 bald.rest.v1.auth_policy 的扫描与值域校验：
// 三档合法值 + 前导点全限定写法 + 显式 UNSPECIFIED 等价缺省 + 未知值报错。
// 本阶段只登记为可机读契约，不改绑定字节——缺省必须严格等于无声明。
func TestScanProto_AuthPolicy(t *testing.T) {
	src := `syntax = "proto3";

package greet.v1;

option go_package = "demo.example/app/api/gen/go/greet/v1;greetv1";

service GreetService {
  rpc Login(LoginRequest) returns (LoginResponse) {
    option (google.api.http) = { post: "/v1/login" };
    option (.bald.rest.v1.auth_policy) = AUTH_POLICY_PUBLIC;
  }

  rpc Logout(LogoutRequest) returns (LogoutResponse) {
    option (google.api.http) = { post: "/v1/logout" };
    option (bald.rest.v1.auth_policy) = AUTH_POLICY_AUTHN;
  }

  rpc Count(CountRequest) returns (CountResponse) {
    option (google.api.http) = { get: "/v1/count" };
    option (bald.rest.v1.auth_policy) = AUTH_POLICY_AUTHED;
  }

  rpc Get(GetRequest) returns (GetResponse) {
    option (google.api.http) = { get: "/v1/greets/{name}" };
  }

  rpc ExplicitDefault(DefaultRequest) returns (DefaultResponse) {
    option (google.api.http) = { get: "/v1/default" };
    option (bald.rest.v1.auth_policy) = AUTH_POLICY_UNSPECIFIED;
  }
}
`
	f, err := scanProto("greet.proto", []byte(src))
	if err != nil {
		t.Fatalf("scanProto: %v", err)
	}
	methods := f.Services[0].Methods
	if len(methods) != 5 {
		t.Fatalf("methods = %d, want 5", len(methods))
	}
	want := []string{"AUTH_POLICY_PUBLIC", "AUTH_POLICY_AUTHN", "AUTH_POLICY_AUTHED", "", ""}
	for i, w := range want {
		if got := methods[i].AuthPolicy; got != w {
			t.Errorf("methods[%d].AuthPolicy = %q, want %q", i, got, w)
		}
	}
}

func TestScanProto_AuthPolicy_RejectsUnknown(t *testing.T) {
	for _, value := range []string{"PUBLIC", "AUTH_POLICY_ADMIN", "auth_policy_public"} {
		src := `syntax = "proto3";
package greet.v1;
option go_package = "demo.example/app/api/gen/go/greet/v1;greetv1";
service GreetService {
  rpc Get(GetRequest) returns (GetResponse) {
    option (google.api.http) = { get: "/v1/greets" };
    option (bald.rest.v1.auth_policy) = ` + value + `;
  }
}
`
		_, err := scanProto("greet.proto", []byte(src))
		if err == nil || !strings.Contains(err.Error(), "auth_policy") {
			t.Errorf("value %q: want auth_policy error, got %v", value, err)
		}
	}
}

// TestAuthPolicyDeclaredButUnparsed 防止「声明了但正则没认出」静默退化：
// 这类拼写漂移会退回缺省档位，等于悄悄改了认证语义，必须生成期报红。
func TestAuthPolicyDeclaredButUnparsed(t *testing.T) {
	src := `syntax = "proto3";
package greet.v1;
option go_package = "demo.example/app/api/gen/go/greet/v1;greetv1";
service GreetService {
  rpc Get(GetRequest) returns (GetResponse) {
    option (google.api.http) = { get: "/v1/greets" };
    option (.bald.rest.v1.auth_policy) = 123;
  }
}
`
	_, err := scanProto("greet.proto", []byte(src))
	if err == nil || !strings.Contains(err.Error(), "recognizable enum value") {
		t.Fatalf("want unrecognized-value error, got %v", err)
	}
}
