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
		"server.kafka":       "transport/kafka/contract",
		"server.websocket":   "transport/websocket/contract",
		"broker.kafka":       "broker/kafka/contract",
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
	// 内置面判据按名单派生（S3）：builtinServerSegments 是唯一真相源，
	// providerHint 里不许再藏第二份 http/grpc 特判。
	for face := range builtinServerSegments {
		if got := providerHint("server." + face); got != "" {
			t.Errorf("内置面 server.%s 不该出 contract 提示（名单派生失效），got %q", face, got)
		}
	}
	if got := hintSuffix("broker.kafka"); !strings.Contains(got, "broker/kafka/contract") {
		t.Fatalf("hintSuffix = %q", got)
	}
	if got := hintSuffix("registry.nacos"); got != "" {
		t.Fatalf("hintSuffix for unmapped = %q", got)
	}
}
