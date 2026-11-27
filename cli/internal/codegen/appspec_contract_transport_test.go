package codegen

import (
	"os"
	"path/filepath"
	"testing"
)

// TestImplementedServerFacesHaveTransportModules 钉住 ADR-6 近期段白名单与
// transport/ 目录的同源约束：白名单里每一项都必须有同名 transport 模块。
//
// 2026-11-15 的 dc22ec49 删掉了 transport/{http3,graphql,tcp,redis,webrtc}
// 五个模块，白名单却停在 68668447 没同步——那五个面在 Gate2 被当作已实现
// 放行。本测试是那次漂移的回归锁：实现下线而白名单未摘除时直接判红。
// 使用相对路径（Go 测试工作目录恒为包目录）而非硬编码绝对路径，兼容 Windows。
func TestImplementedServerFacesHaveTransportModules(t *testing.T) {
	transportDir := filepath.Join("..", "..", "..", "transport")
	st, err := os.Stat(transportDir)
	if err != nil {
		wd, _ := os.Getwd()
		t.Fatalf("transport dir %s not found: %v (working dir = %s)", transportDir, err, wd)
	}
	if !st.IsDir() {
		t.Fatalf("transport dir %s is not a directory", transportDir)
	}
	for face := range implementedServerFaces {
		dir := filepath.Join(transportDir, face)
		st, err := os.Stat(dir)
		if err != nil {
			t.Errorf("implementedServerFaces[%q]: transport/%s missing: %v", face, face, err)
			continue
		}
		if !st.IsDir() {
			t.Errorf("implementedServerFaces[%q]: transport/%s is not a directory", face, face)
		}
	}
}
