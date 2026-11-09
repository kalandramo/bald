package transport

import (
	"errors"
	"net"
	"strconv"
	"testing"
)

type fakeAddr struct{}

func (fakeAddr) Network() string { return "tcp" }
func (fakeAddr) String() string  { return "1.2.3.4:9999" }

type fakeListener struct{ addr net.Addr }

func (fakeListener) Accept() (net.Conn, error) { return nil, errors.New("not implemented") }
func (fakeListener) Close() error              { return nil }
func (l fakeListener) Addr() net.Addr          { return l.addr }

// TestExtract_BadHostPortNoListener 覆盖 SplitHostPort 失败且无 listener 时直接返回错误。
func TestExtract_BadHostPortNoListener(t *testing.T) {
	if _, err := Extract("bad::addr", nil); err == nil {
		t.Fatal("expected error for malformed hostPort without listener")
	}
}

// TestExtract_DynamicPortNonTCPListener 覆盖端口 0 且 listener 非 TCP 时的错误分支。
func TestExtract_DynamicPortNonTCPListener(t *testing.T) {
	ln := fakeListener{addr: fakeAddr{}}
	if _, err := Extract(":0", ln); err == nil {
		t.Fatal("expected error when listener has no TCP address")
	}
}

// TestExtract_DynamicPortUsesListenerPort 覆盖 ":0" → listener 实际端口。
func TestExtract_DynamicPortUsesListenerPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	got, err := Extract(":0", ln)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	wantPort := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	host, port, err := net.SplitHostPort(got)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", got, err)
	}
	if port != wantPort {
		t.Fatalf("port = %q, want %q", port, wantPort)
	}
	if host == "" || host == "0.0.0.0" {
		t.Fatalf("host = %q, want reachable IP", host)
	}
}

// TestExtract_DynamicPortKeepsExplicitHost 覆盖显式 IP + 端口 0：host 保留、端口取 listener。
func TestExtract_DynamicPortKeepsExplicitHost(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	got, err := Extract("10.0.0.5:0", ln)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	want := "10.0.0.5:" + strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	if got != want {
		t.Fatalf("Extract = %q, want %q", got, want)
	}
}

// TestExtract_WildcardHosts 覆盖空 host 与各种通配符 host 的替换路径。
func TestExtract_WildcardHosts(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	for _, hostPort := range []string{":8080", "0.0.0.0:8080", "[::]:8080"} {
		got, err := Extract(hostPort, ln)
		if err != nil {
			t.Fatalf("Extract(%q): %v", hostPort, err)
		}
		if got == "" {
			continue // 本机无全局单播地址时返回空串，属合法回退。
		}
		host, port, err := net.SplitHostPort(got)
		if err != nil {
			t.Fatalf("Extract(%q) = %q: %v", hostPort, got, err)
		}
		if port != "8080" {
			t.Fatalf("Extract(%q) port = %q, want 8080", hostPort, port)
		}
		if !isValidIP(host) {
			t.Fatalf("Extract(%q) host = %q, want global unicast IP", hostPort, host)
		}
	}
}

func TestPortOf(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	want := ln.Addr().(*net.TCPAddr).Port
	got, ok := portOf(ln)
	if !ok || got != want {
		t.Fatalf("portOf = (%d,%v), want (%d,true)", got, ok, want)
	}
	if _, ok := portOf(fakeListener{addr: fakeAddr{}}); ok {
		t.Fatal("portOf must reject non-TCP listener")
	}
}

func TestIsValidIP(t *testing.T) {
	cases := map[string]bool{
		"8.8.8.8":     true,
		"10.0.0.5":    true,
		"127.0.0.1":   false,
		"::1":         false,
		"0.0.0.0":     false,
		"224.0.0.1":   false,
		"fe80::1":     false,
		"169.254.1.1": false,
		"not-an-ip":   false,
		"":            false,
	}
	for in, want := range cases {
		if got := isValidIP(in); got != want {
			t.Errorf("isValidIP(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestExtractReachableIP 验证枚举网卡后的返回形状：要么空串，要么 IP:port。
func TestExtractReachableIP(t *testing.T) {
	got, err := extractReachableIP("8080")
	if err != nil {
		t.Fatalf("extractReachableIP: %v", err)
	}
	if got == "" {
		return
	}
	host, port, err := net.SplitHostPort(got)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", got, err)
	}
	if port != "8080" {
		t.Fatalf("port = %q, want 8080", port)
	}
	if !isValidIP(host) {
		t.Fatalf("host = %q, want global unicast IP", host)
	}
}

// TestExtractReachableIP_NoInterfaces 覆盖无网卡时返回空串。
func TestExtractReachableIP_NoInterfaces(t *testing.T) {
	orig := netInterfaces
	netInterfaces = func() ([]net.Interface, error) { return nil, nil }
	t.Cleanup(func() { netInterfaces = orig })

	got, err := extractReachableIP("8080")
	if err != nil {
		t.Fatalf("extractReachableIP: %v", err)
	}
	if got != "" {
		t.Fatalf("extractReachableIP = %q, want empty", got)
	}
}

// TestExtractReachableIP_EnumerationError 覆盖网卡枚举失败。
func TestExtractReachableIP_EnumerationError(t *testing.T) {
	orig := netInterfaces
	netInterfaces = func() ([]net.Interface, error) { return nil, errors.New("enum boom") }
	t.Cleanup(func() { netInterfaces = orig })

	if _, err := extractReachableIP("8080"); err == nil {
		t.Fatal("expected enumeration error")
	}
}

func TestPickReachableIP(t *testing.T) {
	v4 := func(s string) net.Addr { return &net.IPAddr{IP: net.ParseIP(s)} }
	v6net := func(s string) net.Addr { return &net.IPNet{IP: net.ParseIP(s)} }
	iface := func(index int, up bool, addrs ...net.Addr) ifaceView {
		return ifaceView{index: index, up: up, addrs: func() ([]net.Addr, error) {
			return addrs, nil
		}}
	}
	failing := ifaceView{index: 0, up: true, addrs: func() ([]net.Addr, error) {
		return nil, errors.New("addr boom")
	}}

	cases := []struct {
		name   string
		ifaces []ifaceView
		want   string
	}{
		{"empty", nil, ""},
		{"down interface skipped", []ifaceView{iface(1, false, v4("10.0.0.1"))}, ""},
		{"address error skipped", []ifaceView{failing}, ""},
		{"ipv4 preferred", []ifaceView{iface(1, true, v4("10.0.0.1"))}, "10.0.0.1"},
		{"ipv6 fallback", []ifaceView{iface(1, true, v6net("2001:db8::1"))}, "2001:db8::1"},
		{"ipv6 then ipv4 breaks", []ifaceView{iface(1, true, v6net("2001:db8::1"), v4("10.0.0.2"))}, "10.0.0.2"},
		{"unsupported addr skipped", []ifaceView{iface(1, true, &net.UnixAddr{Name: "s", Net: "unix"})}, ""},
		{"higher index after hit skipped", []ifaceView{
			iface(5, true, v4("10.1.1.1")),
			iface(6, true, v4("10.2.2.2")),
		}, "10.1.1.1"},
		{"lower index after hit wins", []ifaceView{
			iface(5, true, v4("10.1.1.1")),
			iface(2, true, v4("10.2.2.2")),
		}, "10.2.2.2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := pickReachableIP(tc.ifaces)
			if tc.want == "" {
				if got != nil {
					t.Fatalf("pickReachableIP = %v, want nil", got)
				}
				return
			}
			if got == nil || got.String() != tc.want {
				t.Fatalf("pickReachableIP = %v, want %s", got, tc.want)
			}
		})
	}
}
