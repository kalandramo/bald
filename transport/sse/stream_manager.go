package sse

import "sync"

// StreamMap maps stream IDs to their Stream instances.
type StreamMap map[StreamID]*Stream

// StreamManager manages the lifecycle of multiple SSE streams.
type StreamManager struct {
	streams StreamMap
	mtx     sync.RWMutex
}

// NewStreamManager creates a new StreamManager.
func NewStreamManager() *StreamManager {
	return &StreamManager{
		streams: make(StreamMap),
	}
}

// Clean closes and removes all streams.
func (s *StreamManager) Clean() {
	s.mtx.Lock()
	defer s.mtx.Unlock()

	for _, v := range s.streams {
		v.close()
	}
	s.streams = make(StreamMap)
}

// Count returns the number of active streams.
func (s *StreamManager) Count() int {
	s.mtx.Lock()
	defer s.mtx.Unlock()

	return len(s.streams)
}

// Get returns the stream with the given ID, or nil if not found.
func (s *StreamManager) Get(streamId StreamID) *Stream {
	s.mtx.Lock()
	defer s.mtx.Unlock()

	c, _ := s.streams[streamId]
	return c
}

// Exist reports whether a stream with the given ID exists.
func (s *StreamManager) Exist(streamId StreamID) bool {
	stream := s.Get(streamId)
	return stream != nil
}

// Range iterates over all streams.
func (s *StreamManager) Range(fn func(*Stream)) {
	s.mtx.Lock()
	defer s.mtx.Unlock()

	for _, v := range s.streams {
		fn(v)
	}
}

// Add registers a new stream. No-op if a stream with the same ID already exists.
func (s *StreamManager) Add(stream *Stream) {
	if stream == nil {
		return
	}
	if s.Exist(stream.StreamID()) {
		return
	}
	s.mtx.Lock()
	defer s.mtx.Unlock()
	s.streams[stream.StreamID()] = stream
}

// RemoveWithID closes and removes the stream with the given ID.
func (s *StreamManager) RemoveWithID(streamId StreamID) {
	s.mtx.Lock()
	defer s.mtx.Unlock()

	if s.streams[streamId] != nil {
		s.streams[streamId].close()
		delete(s.streams, streamId)
	}
}

// Remove closes and removes the given stream.
func (s *StreamManager) Remove(stream *Stream) {
	s.mtx.Lock()
	defer s.mtx.Unlock()

	for k, v := range s.streams {
		if stream == v {
			s.streams[k].close()
			delete(s.streams, k)
			return
		}
	}
}
