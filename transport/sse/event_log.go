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

// Replay sends all events with ID >= subscriber's last event ID to the subscriber.
func (e *EventLog) Replay(s *Subscriber) {
	for i := 0; i < len(*e); i++ {
		if string((*e)[i].ID) >= s.eventId {
			s.connection <- (*e)[i]
		}
	}
}

// newEventID generates a new UUID v7 as the event identifier.
// UUID v7 is time-ordered, ensuring lexicographic sort matches chronological order.
func newEventID() string {
	return strings.ReplaceAll(uuid.Must(uuid.NewV7()).String(), "-", "")
}
