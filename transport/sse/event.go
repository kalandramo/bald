package sse

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"time"
)

// Event represents a single SSE message payload.
type Event struct {
	// timestamp is the server-side creation time for this event.
	timestamp time.Time
	// ID maps to the SSE "id" field for client reconnection.
	ID []byte
	// Data maps to one or more SSE "data" lines.
	Data []byte
	// Event maps to the SSE "event" name.
	Event []byte
	// Retry maps to the SSE "retry" reconnection delay value.
	Retry []byte
	// Comment maps to SSE comment lines prefixed with ':'.
	Comment []byte
}

// EventMetaOption applies optional metadata fields to an Event.
type EventMetaOption func(e *Event)

// WithEventID sets the SSE "id" field on the event.
func WithEventID(id string) EventMetaOption {
	return func(e *Event) {
		if id != "" {
			e.ID = []byte(id)
		}
	}
}

// WithEventName sets the SSE "event" name field on the event.
func WithEventName(name string) EventMetaOption {
	return func(e *Event) {
		if name != "" {
			e.Event = []byte(name)
		}
	}
}

// WithEventRetry sets the SSE "retry" field on the event (value in milliseconds as string).
func WithEventRetry(retry string) EventMetaOption {
	return func(e *Event) {
		if retry != "" {
			e.Retry = []byte(retry)
		}
	}
}

// WithEventComment sets the SSE comment field on the event.
func WithEventComment(comment string) EventMetaOption {
	return func(e *Event) {
		if comment != "" {
			e.Comment = []byte(comment)
		}
	}
}

// hasContent reports whether the event contains any SSE payload fields.
func (e *Event) hasContent() bool {
	return len(e.ID) > 0 || len(e.Data) > 0 || len(e.Event) > 0 || len(e.Retry) > 0
}

// encodeBase64 encodes Data in-place using standard base64.
func (e *Event) encodeBase64() {
	dataLen := len(e.Data)
	if dataLen > 0 {
		output := make([]byte, base64.StdEncoding.EncodedLen(dataLen))
		base64.StdEncoding.Encode(output, e.Data)
		e.Data = output
	}
}

// EventStreamReader incrementally reads raw SSE event blocks from a stream.
type EventStreamReader struct {
	scanner *bufio.Scanner
}

// NewEventStreamReader creates a reader that splits SSE data by blank-line event boundaries.
func NewEventStreamReader(eventStream io.Reader, maxBufferSize int) *EventStreamReader {
	scanner := bufio.NewScanner(eventStream)
	initBufferSize := minPosInt(4096, maxBufferSize)
	scanner.Buffer(make([]byte, initBufferSize), maxBufferSize)

	split := func(data []byte, atEOF bool) (int, []byte, error) {
		if atEOF && len(data) == 0 {
			return 0, nil, nil
		}

		if i, nLen := containsDoubleNewline(data); i >= 0 {
			return i + nLen, data[0:i], nil
		}
		if atEOF {
			return len(data), data, nil
		}
		return 0, nil, nil
	}
	scanner.Split(split)

	return &EventStreamReader{
		scanner: scanner,
	}
}

// ReadEvent reads and returns one raw SSE event block.
func (e *EventStreamReader) ReadEvent() ([]byte, error) {
	if e.scanner.Scan() {
		event := e.scanner.Bytes()
		return event, nil
	}
	if err := e.scanner.Err(); err != nil {
		if errors.Is(err, context.Canceled) {
			return nil, io.EOF
		}
		return nil, err
	}
	return nil, io.EOF
}

func minPosInt(a, b int) int {
	if a < 0 || b < 0 {
		return 0
	}
	if a < b {
		return a
	}
	return b
}

// containsDoubleNewline returns the index and length of the first double newline
// (either \n\n or \r\n\r\n) in data, or (-1, 0) if not found.
func containsDoubleNewline(data []byte) (int, int) {
	// Search for \r\n\r\n first (longer match), then \n\n.
	for i := 0; i < len(data)-3; i++ {
		if data[i] == '\r' && data[i+1] == '\n' && data[i+2] == '\r' && data[i+3] == '\n' {
			return i, 4
		}
	}
	for i := 0; i < len(data)-1; i++ {
		if data[i] == '\n' && data[i+1] == '\n' {
			return i, 2
		}
	}
	return -1, 0
}

// writeData writes an SSE field line: "<field> <data>\n".
// If field is empty, only writes "<data>\n" (used for comments).
func writeData(w io.Writer, field, data []byte) (int, error) {
	if len(field) > 0 {
		n1, err := w.Write(field)
		if err != nil {
			return n1, err
		}
		if len(data) > 0 && (len(field) == 0 || field[len(field)-1] != ' ') {
			if n2, err := w.Write([]byte(" ")); err != nil {
				return n1 + n2, err
			}
		}
	}
	n3, err := w.Write(data)
	if err != nil {
		return n3, err
	}
	n4, err := fmt.Fprint(w, "\n")
	return n3 + n4, err
}
