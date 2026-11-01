package sse

import (
	"strings"
	"time"

	"github.com/google/uuid"
)

// EventLog stores events for replay to reconnecting clients.
type EventLog []*Event

// Add appends an event to the log, assigning it a new ID and timestamp.
func (e *EventLog) Add(ev *Event) {
	if !ev.hasContent() {
		return
	}
	ev.ID = []byte(newEventID())
	ev.timestamp = time.Now()
	*e = append(*e, ev)
}

// Clear removes all events from the log.
func (e *EventLog) Clear() {
	*e = nil
}

// Since returns the events whose ID is not older than eventID, in append order.
// eventID 为空时返回全部保留事件。
func (e *EventLog) Since(eventID string) []*Event {
	out := make([]*Event, 0, len(*e))
	for i := 0; i < len(*e); i++ {
		if string((*e)[i].ID) >= eventID {
			out = append(out, (*e)[i])
		}
	}
	return out
}

// Replay sends all events with ID >= subscriber's last event ID to the subscriber.
//
// 投递是阻塞的，仅供直接调用；服务器事件循环改用 [Since] + 非阻塞投递，
// 避免慢订阅者卡住整条流。
func (e *EventLog) Replay(s *Subscriber) {
	for _, ev := range e.Since(s.eventId) {
		s.connection <- ev
	}
}

// Trim 把日志裁剪到最近 max 条，超出部分最旧优先丢弃。
// max <= 0 表示不裁剪（显式选择无界保留）。
func (e *EventLog) Trim(max int) {
	if max <= 0 || len(*e) <= max {
		return
	}
	old := *e
	drop := len(old) - max
	copy(old, old[drop:])
	for i := max; i < len(old); i++ {
		old[i] = nil // 释放已丢弃事件的引用
	}
	*e = old[:max]
}

// newEventID generates a new UUID v7 as the event identifier.
// UUID v7 is time-ordered, ensuring lexicographic sort matches chronological order.
func newEventID() string {
	return strings.ReplaceAll(uuid.Must(uuid.NewV7()).String(), "-", "")
}
