package appkit

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// 业务配置段：`biz.<path>` 指向合并后的 Settings 快照
// （app.Settings()），而不是 BootstrapConfig 字段——域自有业务配置由此进入
// 可协调节点的期望态。计划期对子树做确定性序列化算 DesiredHash；热更时同一
// 路径取新快照，摘要变化即触发 Domain.Reconcile。
const bizSegmentPrefix = "biz."

// bizSegmentPath 判定 biz 段并拆出余部做结构校验（"biz.audit.policy" →
// "audit.policy"）。前缀之外的路径归 BootstrapConfig；余部为空或含空段视为
// 拼写错误。摘要解析用**完整路径**——`biz` 是合并快照里的顶层段，与契约
// yaml 的 `biz:` 层级一一对应。
func bizSegmentPath(path string) (string, bool) {
	rest, ok := strings.CutPrefix(path, bizSegmentPrefix)
	if !ok || rest == "" {
		return "", false
	}
	for _, p := range strings.Split(rest, ".") {
		if p == "" {
			return "", false
		}
	}
	return rest, true
}

// settingsAtPath 按点路径读取 Settings 快照子树（与 config.Store.Get 同语义）。
func settingsAtPath(settings map[string]any, path string) (any, bool) {
	if settings == nil || path == "" {
		return nil, false
	}
	parts := strings.Split(path, ".")
	var cur any = settings
	for i, p := range parts {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		v, ok := m[p]
		if !ok {
			return nil, false
		}
		if i == len(parts)-1 {
			return v, true
		}
		cur = v
	}
	return nil, false
}

// writeSettingsSubtree 把业务配置子树写进期望态摘要。json.Marshal 对 map 键
// 排序，同一快照必得同一字节序（I2）；不可序列化的值退回 %#v（fmt 同样对
// map 键排序）。
func writeSettingsSubtree(h io.Writer, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		fmt.Fprintf(h, "%#v", v)
		return
	}
	_, _ = h.Write(b)
}
