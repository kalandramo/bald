package appkit

import (
	"strings"
	"testing"
)

// DX-13：L3 报错的指路分句——框架自有段按静态映射/族派生给出 contract 包，
// 未收录的段不带提示（提示不是契约，缺表不报错）。
func TestProviderHint(t *testing.T) {
	for path, want := range map[string]string{
		"database.sql":       "contrib/database/gorm/contract",
		"cache.redis":        "cache/redis/contract",
		"oss.minio":          "oss/minio/contract",
		"server.kafka":       "transport/kafka/contract",
		"server.webrtc":      "transport/webrtc/contract",
		"broker.redis":       "broker/redis/contract",
		"broker.rabbitmq":    "broker/rabbitmq/contract",
		"client.http.x":      "transport/client/contract",
		"log.loki":           "log/loki/contract",
		"metrics.prometheus": "metrics/prometheus/contract",
	} {
		if got := providerHint(path); got != want {
			t.Errorf("providerHint(%q) = %q, want %q", path, got, want)
		}
	}
	// 内置面与未收录段：无提示（不编造）。
	for _, path := range []string{"server.http", "server.grpc", "registry.nacos", "unknown.segment"} {
		if got := providerHint(path); got != "" {
			t.Errorf("providerHint(%q) = %q, want empty", path, got)
		}
	}
	if got := hintSuffix("broker.redis"); !strings.Contains(got, "broker/redis/contract") {
		t.Fatalf("hintSuffix = %q", got)
	}
	if got := hintSuffix("registry.nacos"); got != "" {
		t.Fatalf("hintSuffix for unmapped = %q", got)
	}
}
