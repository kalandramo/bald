// Package options 提供 bald 框架各协议服务器所需的配置选项。
// 设计对齐 onexstack/pkg/options：统一的 IOptions 接口、AddFlags(fs, fullPrefix)
// 前缀式 flag 注册、Validate() []error 校验，以及可复用的地址校验/监听工具。
package options

import (
	"fmt"
	"net"
	"strings"
)

// Join 用 "." 连接多个前缀，并在非空结果末尾补 "."。
// 用于构建嵌套 flag 前缀，例如 Join("bald-demo", "http") -> "bald-demo.http."。
func Join(prefixes ...string) string {
	joined := strings.Join(prefixes, ".")
	if joined != "" {
		joined += "."
	}
	return joined
}

// ValidateAddress 校验地址是否为合法的 :port 或 ip:port 格式。
// 同时检查 host 部分（若非空）是否为合法 IP、port 是否为合法端口号。
func ValidateAddress(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%q is not in a valid format (:port or ip:port): %w", addr, err)
	}
	if host != "" {
		if ip := net.ParseIP(host); ip == nil {
			return fmt.Errorf("%q is not a valid IP address", host)
		}
	}
	// 端口 0 为合法的"动态端口"（:0 由系统分配随机端口），需放行。
	if port == "0" {
		return nil
	}
	if p, err := net.LookupPort("tcp", port); err != nil || p < 1 || p > 65535 {
		// LookupPort 能接受 "0"；失败或越界则报错。
		if err != nil {
			return fmt.Errorf("%q is not a valid port: %w", port, err)
		}
		return fmt.Errorf("%q is not a valid port number", port)
	}
	return nil
}

// CreateListener 按给定地址创建 net.Listener 并返回监听器与端口。
// 公开工具函数（对齐 onexstack helper），供业务在 options 之外手动监听时使用；
// 当前 server 层在 Server.Start 内联 net.Listen，未直接引用本函数。
func CreateListener(addr string) (net.Listener, int, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to listen on %v: %w", addr, err)
	}
	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		_ = ln.Close()
		return nil, 0, fmt.Errorf("invalid listen address: %q", ln.Addr().String())
	}
	return ln, tcpAddr.Port, nil
}
