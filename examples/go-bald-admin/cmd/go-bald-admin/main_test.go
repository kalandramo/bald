package main

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestGatewayAddr_M6 验证 M6 生产化：gateway 地址可经 env BALD_GATEWAY_ADDR 配置。
func TestGatewayAddr_M6(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		os.Unsetenv("BALD_GATEWAY_ADDR")
		assert.Equal(t, ":8081", gatewayAddr())
	})
	t.Run("env override", func(t *testing.T) {
		t.Setenv("BALD_GATEWAY_ADDR", ":18081")
		assert.Equal(t, ":18081", gatewayAddr())
	})
}
