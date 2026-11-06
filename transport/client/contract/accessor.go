package contract

import (
	"google.golang.org/grpc"

	"github.com/kalandramo/bald/pkg/appkit"
	client "github.com/kalandramo/bald/transport/client"
)

// GRPC 取出 `client.grpc.<name>` 的连接；未装配或名字不存在返回 (nil, false)。
//
//	conn, ok := clientcontract.GRPC(app, "user")
func GRPC(a *appkit.App, name string) (*grpc.ClientConn, bool) {
	return appkit.NewKey[*grpc.ClientConn]("client." + grpcKey(name)).Resource(a)
}

// HTTP 取出 `client.http.<name>` 的客户端；未装配或名字不存在返回
// (nil, false)。
func HTTP(a *appkit.App, name string) (*client.HTTPClient, bool) {
	return appkit.NewKey[*client.HTTPClient]("client." + httpKey(name)).Resource(a)
}

// grpcKey/httpKey 是产物键的唯一拼法（Provider 落库与访问器共用，避免两处
// 各写一遍面前缀而漂移）。引擎按 "<顶层段>.<产物键>" 展开成资源面键。
func grpcKey(name string) string { return "grpc." + name }

func httpKey(name string) string { return "http." + name }
