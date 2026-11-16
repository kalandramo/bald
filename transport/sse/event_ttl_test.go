package sse

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestEventTTLWithAutoReplayOff_DoesNotDropEvents 复现 TTL 静默清空整个流：
// eventTTL 过滤读 ev.timestamp，而 timestamp 只由 EventLog.Add 在 autoReplay
// 开启时写入——WithEventTTL + autoReplay=false 时它是零值，zero.Add(ttl) 恒
// 早于现在，每个事件都被 continue 掉，SSE 流静默断供。修复后：无时间戳的
// 事件不参与 TTL 过滤（TTL 只对带投递时间戳的事件有意义）。
func TestEventTTLWithAutoReplayOff_DoesNotDropEvents(t *testing.T) {
	srv := NewServer(":0", WithEventTTL(time.Minute), WithAutoReplay(false))
	body := serveAndDrain(t, srv, "ttl-off", func(stream *Stream) {
		srv.Publish(context.Background(), stream.StreamID(), &Event{Data: []byte("payload")})
	})
	if !strings.Contains(body, "data: payload") {
		t.Fatalf("autoReplay=false + EventTTL 时事件被静默丢弃：\n%s", body)
	}
}

// TestEventTTLStillFiltersExpired 对照组：带真实时间戳且确实过期的事件
// 仍要被 TTL 过滤掉——修零值误伤不能把过滤本身修没了。用负 TTL 稳定触发：
// EventLog.Add 会把 timestamp 覆盖成投递时刻（Publish 路径手填的过去时间戳
// 会被覆盖），所以「确实过期」必须由 TTL 本身构成。
func TestEventTTLStillFiltersExpired(t *testing.T) {
	srv := NewServer(":0", WithEventTTL(-time.Second), WithAutoReplay(true))
	body := serveAndDrain(t, srv, "ttl-expired", func(stream *Stream) {
		srv.Publish(context.Background(), stream.StreamID(), &Event{Data: []byte("stale")})
	})
	if strings.Contains(body, "data: stale") {
		t.Fatalf("过期事件必须被 TTL 过滤：\n%s", body)
	}
}
