package appkit

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"sort"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// nodeDesiredHash 计算可协调节点的期望态摘要：节点身份 + 各声明段的确定性
// 序列化子树。框架段取自 BootstrapConfig；`biz.<path>` 段取自合并后的
// Settings 快照（Q8——域自有配置因此可收敛）。段缺失按 "absent" 参与摘要，
// 保持与「段被删除」可区分。非 ReloadReconcile 节点无摘要（重启/静态无期望态）。
func nodeDesiredHash(cfg *bootstrapv1.BootstrapConfig, settings map[string]any, n Node) string {
	if n.Reload != ReloadReconcile {
		return ""
	}
	h := sha256.New()
	io.WriteString(h, "key="+string(n.Key)+"\nkind="+n.Kind+"\n")
	for _, seg := range sortedUnique(n.Segments) {
		io.WriteString(h, "segment="+seg+"\n")
		// 解析用完整路径（biz 是合并快照里的顶层段，与 yaml 层级一致）。
		if _, ok := bizSegmentPath(seg); ok {
			if v, ok := settingsAtPath(settings, seg); ok {
				writeSettingsSubtree(h, v)
			} else {
				io.WriteString(h, "absent")
			}
			io.WriteString(h, "\n")
			continue
		}
		if msg := segmentMessage(cfg, seg); msg != nil {
			if b, err := (proto.MarshalOptions{Deterministic: true}).Marshal(msg.Interface()); err == nil {
				_, _ = h.Write(b)
			}
		}
		io.WriteString(h, "\n")
	}
	return hex.EncodeToString(h.Sum(nil))
}

// segmentMessage 按点路径以 protobuf 反射取契约段消息（"server.grpc" →
// cfg.server.grpc）；路径不存在或非消息型字段返回 nil。
func segmentMessage(cfg *bootstrapv1.BootstrapConfig, path string) protoreflect.Message {
	if cfg == nil || path == "" {
		return nil
	}
	m := cfg.ProtoReflect()
	for _, name := range strings.Split(path, ".") {
		if !m.IsValid() {
			return nil
		}
		fd := m.Descriptor().Fields().ByName(protoreflect.Name(name))
		if fd == nil || fd.Kind() != protoreflect.MessageKind {
			return nil
		}
		m = m.Get(fd).Message()
	}
	if !m.IsValid() {
		return nil
	}
	return m
}

// inputHash 对契约做确定性序列化后求摘要（I2 的可检视输入基线）。
func inputHash(cfg *bootstrapv1.BootstrapConfig) string {
	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(cfg)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// nodeKey 由归属路径合成稳定键：单路径直接用路径（"database.sql"），多路径
// 用排序后逗号连接（std server 的 "server.grpc,server.http"）。
func nodeKey(paths []string) ResourceKey {
	switch len(paths) {
	case 0:
		return ""
	case 1:
		return ResourceKey(paths[0])
	default:
		return ResourceKey(strings.Join(paths, ","))
	}
}

// sortedUnique 返回排序去重后的段路径（计划 JSON 的稳定性来源）。
func sortedUnique(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := append([]string(nil), in...)
	sort.Strings(out)
	dup := out[:1]
	for _, s := range out[1:] {
		if s != dup[len(dup)-1] {
			dup = append(dup, s)
		}
	}
	return dup
}

// phaseFor 把 Provider 的构建时机映射为计划阶段。
func phaseFor(ph providerPhase) Phase {
	switch ph {
	case phaseConstruct:
		return PhaseConfigure
	case phaseStageB, phaseLogger:
		return PhaseBootstrap
	case phaseLate:
		return PhaseServe
	default:
		return PhaseBootstrap
	}
}
