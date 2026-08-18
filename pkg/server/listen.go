package server

import (
	"net"
)

// listen 在 addr 上创建 TCP 监听器。
func listen(addr string) (net.Listener, error) {
	return net.Listen("tcp", addr)
}
