package authz

import "testing"

func TestDefaultGRPCObject(t *testing.T) {
	cases := map[string]string{
		"/go.bald.admin.v1.SecretService/GetSecret":    "secret",
		"/go.bald.admin.v1.SecretService/DeleteSecret": "secret",
		"/go.bald.admin.v1.SecretService/ListUsers":    "secret",
		"/go.bald.admin.v1.AuthService/WhoAmI":         "auth",
	}
	for in, want := range cases {
		if got := DefaultGRPCObject(in); got != want {
			t.Errorf("DefaultGRPCObject(%q)=%q, want %q", in, got, want)
		}
	}
}

func TestDefaultGRPCAction(t *testing.T) {
	cases := map[string]string{
		"/go.bald.admin.v1.SecretService/GetSecret":    "get",
		"/go.bald.admin.v1.SecretService/DeleteSecret": "delete",
		"/go.bald.admin.v1.SecretService/ListUsers":    "list",
		"/go.bald.admin.v1.SecretService/CreateX":      "write",
		"/go.bald.admin.v1.SecretService/UpdateX":      "write",
		"/go.bald.admin.v1.SecretService/SetX":         "write",
		"/go.bald.admin.v1.SecretService/PutX":         "write",
		"/go.bald.admin.v1.AuthService/WhoAmI":         "get",
	}
	for in, want := range cases {
		if got := DefaultGRPCAction(in); got != want {
			t.Errorf("DefaultGRPCAction(%q)=%q, want %q", in, got, want)
		}
	}
}

func TestDefaultHTTPObject(t *testing.T) {
	cases := map[string]string{
		"/v1/secret/123":  "secret",
		"/v1/auth/whoami": "auth",
		"/v1":             "",
		"/":               "",
	}
	for in, want := range cases {
		if got := DefaultHTTPObject(in); got != want {
			t.Errorf("DefaultHTTPObject(%q)=%q, want %q", in, got, want)
		}
	}
}

func TestDefaultHTTPAction(t *testing.T) {
	if got := DefaultHTTPAction("GET"); got != "get" {
		t.Errorf("DefaultHTTPAction(GET)=%q, want get", got)
	}
	if got := DefaultHTTPAction("DELETE"); got != "delete" {
		t.Errorf("DefaultHTTPAction(DELETE)=%q, want delete", got)
	}
}
