package grpc

import (
	"context"
	"testing"

	"google.golang.org/grpc"

	"github.com/kalandramo/bald/pkg/authn"
	"github.com/kalandramo/bald/pkg/authz"
)

func mkCtx(subject string) context.Context {
	return authn.ContextWithAuthClaims(context.Background(), &authn.AuthClaims{Subject: subject})
}

func TestAuthzInterceptor_DefaultActionIsCALL(t *testing.T) {
	// 默认行为（无 Option）向后兼容：object=FullMethod, action="CALL"。
	var gotObj, gotAct, gotSub string
	authorizer := authz.Func(func(_ context.Context, subject, object, action string) (bool, error) {
		gotSub, gotObj, gotAct = subject, object, action
		return true, nil
	})
	ic := AuthzInterceptor(authorizer)
	info := &grpc.UnaryServerInfo{FullMethod: "/go.bald.admin.v1.SecretService/GetSecret"}
	_, err := ic(mkCtx("u-admin"), nil, info, func(context.Context, any) (any, error) { return nil, nil })
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if gotSub != "u-admin" || gotObj != info.FullMethod || gotAct != "CALL" {
		t.Fatalf("default: sub=%q obj=%q act=%q, want u-admin/%s/CALL", gotSub, gotObj, gotAct, info.FullMethod)
	}
}

func TestAuthzInterceptor_ResolverNormalization(t *testing.T) {
	// 接入传输中立归一化：object=secret, action=get（与 HTTP 侧同源）。
	var gotObj, gotAct string
	authorizer := authz.Func(func(_ context.Context, _, object, action string) (bool, error) {
		gotObj, gotAct = object, action
		return true, nil
	})
	ic := AuthzInterceptor(authorizer,
		WithObjectResolver(authz.DefaultGRPCObject),
		WithActionResolver(authz.DefaultGRPCAction),
	)
	info := &grpc.UnaryServerInfo{FullMethod: "/go.bald.admin.v1.SecretService/DeleteSecret"}
	_, err := ic(mkCtx("u-admin"), nil, info, func(context.Context, any) (any, error) { return nil, nil })
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if gotObj != "secret" || gotAct != "delete" {
		t.Fatalf("normalized: obj=%q act=%q, want secret/delete", gotObj, gotAct)
	}
}

func TestAuthzInterceptor_NilAuthorizerPassthrough(t *testing.T) {
	ic := AuthzInterceptor(nil)
	_, err := ic(context.Background(), nil, &grpc.UnaryServerInfo{}, func(context.Context, any) (any, error) { return "ok", nil })
	if err != nil {
		t.Fatalf("nil authorizer should pass through, got %v", err)
	}
}

func TestAuthzInterceptor_Denied(t *testing.T) {
	authorizer := authz.DenyAll()
	ic := AuthzInterceptor(authorizer,
		WithObjectResolver(authz.DefaultGRPCObject),
		WithActionResolver(authz.DefaultGRPCAction),
	)
	_, err := ic(mkCtx("u-alice"), nil,
		&grpc.UnaryServerInfo{FullMethod: "/go.bald.admin.v1.SecretService/DeleteSecret"},
		func(context.Context, any) (any, error) { return nil, nil })
	if err == nil {
		t.Fatal("expected denied error")
	}
}
