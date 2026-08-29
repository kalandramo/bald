package conf

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
)

// validateAddress 校验地址是否为合法的 :port 或 ip:port 格式。
func validateAddress(addr string) error {
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
		if err != nil {
			return fmt.Errorf("%q is not a valid port: %w", port, err)
		}
		return fmt.Errorf("%q is not a valid port number", port)
	}
	return nil
}

// durationToString 把 *durationpb.Duration 格式化为 viper/protojson 兼容的 "Ns" 形式。
func durationToString(d *durationpb.Duration) string {
	if d == nil {
		return "0s"
	}
	return d.AsDuration().String()
}

// parseDuration 解析 "10s" / "1m30s" 为 *durationpb.Duration。
func parseDuration(s string) (*durationpb.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return durationpb.New(0), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return nil, fmt.Errorf("invalid duration %q: %w", s, err)
	}
	return durationpb.New(d), nil
}

// --- 标量解析（供 flag 值写入器使用） ---

func parseBool(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "t", "true", "yes", "y", "on":
		return true, nil
	case "0", "f", "false", "no", "n", "off", "":
		return false, nil
	}
	return strconv.ParseBool(s)
}

func parseInt32(s string) (int32, error) {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 32)
	return int32(n), err
}

func parseInt64(s string) (int64, error) {
	return strconv.ParseInt(strings.TrimSpace(s), 10, 64)
}

func parseUint32(s string) (uint32, error) {
	n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 32)
	return uint32(n), err
}

func parseUint64(s string) (uint64, error) {
	return strconv.ParseUint(strings.TrimSpace(s), 10, 64)
}

func parseFloat32(s string) (float32, error) {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 32)
	return float32(f), err
}

func parseFloat64(s string) (float64, error) {
	return strconv.ParseFloat(strings.TrimSpace(s), 64)
}
