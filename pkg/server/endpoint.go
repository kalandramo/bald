package server

import (
	"fmt"
	"net"
	"strconv"
)

// Extract 从监听地址解析出「对调用方可达」的 host:port。
//
// 当监听绑定在通配符（"" / "0.0.0.0" / "[::]" / "::"）或仅指定端口（如 ":8080"）
// 时，net.Listener.Addr() 返回的是 "0.0.0.0:8080" 这类通配符地址，直接注册到
// 服务发现会导致其他节点无法直连。Extract 会把这些通配符 host 替换为本机第一个
// 全局单播（非环回、非链路本地多播）IPv4/IPv6 地址，使 endpoint 真正可达。
//
// 端口解析规则：
//   - hostPort 端口非 0（如 ":8080" / "10.0.0.5:8080"）→ 保留该端口；
//   - hostPort 端口为 0（如 ":0" / "10.0.0.5:0"）→ 用 listener 实际分配的端口。
//
// host 解析规则：
//   - host 显式且非通配符 → 原样保留（尊重用户指定的发布 IP）；
//   - host 为空或通配符 → 枚举网卡取首个可达 IP 替换。
//
// 该逻辑是服务注册可达性的关键修复点，请勿退回为裸 s.ln.Addr().String()。
func Extract(hostPort string, ln net.Listener) (string, error) {
	addr, portStr, err := net.SplitHostPort(hostPort)
	if err != nil && ln == nil {
		return "", err
	}
	port := portStr
	// 仅当配置端口为 0（动态端口）时，才用 listener 实际分配的端口覆盖。
	if portStr == "0" && ln != nil {
		p, ok := portOf(ln)
		if !ok {
			return "", fmt.Errorf("server: failed to extract port from listener %v", ln.Addr())
		}
		port = strconv.Itoa(p)
	}
	if len(addr) > 0 && addr != "0.0.0.0" && addr != "[::]" && addr != "::" {
		return net.JoinHostPort(addr, port), nil
	}
	return extractReachableIP(port)
}

// portOf 从 listener 取真实端口。
func portOf(ln net.Listener) (int, bool) {
	if addr, ok := ln.Addr().(*net.TCPAddr); ok {
		return addr.Port, true
	}
	return 0, false
}

// isValidIP 判断是否为可达 IP（全局单播且非接口本地多播）。
func isValidIP(addr string) bool {
	ip := net.ParseIP(addr)
	return ip != nil && ip.IsGlobalUnicast() && !ip.IsInterfaceLocalMulticast()
}

// extractReachableIP 枚举本机网卡，返回首个全局单播 IP 与给定端口拼接的地址。
// 优先 IPv4；若只有 IPv6 可达则回退。无任何可达 IP 时返回空字符串。
func extractReachableIP(port string) (string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	var (
		minIndex int
		ips      = make([]net.IP, 0, 1)
	)
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		if iface.Index >= minIndex && len(ips) != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, rawAddr := range addrs {
			var ip net.IP
			switch a := rawAddr.(type) {
			case *net.IPAddr:
				ip = a.IP
			case *net.IPNet:
				ip = a.IP
			default:
				continue
			}
			if isValidIP(ip.String()) {
				minIndex = iface.Index
				ips = append(ips, ip)
				if ip.To4() != nil {
					break
				}
			}
		}
	}
	if len(ips) != 0 {
		return net.JoinHostPort(ips[len(ips)-1].String(), port), nil
	}
	return "", nil
}
