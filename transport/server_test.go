package transport

import (
	"net"
	"testing"
)

// TestExtract_ExplicitIPKept：显式指定 IP 时 Extract 应原样保留，不被覆盖。
func TestExtract_ExplicitIPKept(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	got, err := Extract("10.0.0.5:8080", ln)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if got != "10.0.0.5:8080" {
		t.Fatalf("Extract = %q, want 10.0.0.5:8080 (explicit IP kept)", got)
	}
}
