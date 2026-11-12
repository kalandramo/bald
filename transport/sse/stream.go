package sse

import (
	"context"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"

	"github.com/kalandramo/bald/log"
)

// StreamID uniquely identifies an SSE stream.
type StreamID string

// defaultSubscriberBuffer 是单个订阅者连接通道的缓冲条数。写满即判定为
// 慢客户端（HTTP 连接消费不过来），该订阅者会被移出并断开，避免其阻塞
// 整条流上的其他订阅者。
const defaultSubscriberBuffer = 64

// SubscriberFunction is invoked when a subscriber is added to or removed from a stream.
type SubscriberFunction func(streamID StreamID, sub *Subscriber)

// Stream holds subscribers and dispatches events for a single stream ID.
type Stream struct {
	id StreamID

	event        chan *Event
	quit         chan struct{}
	quitOnce     sync.Once
	eventLog     EventLog
	eventLogSize int

	autoReplay bool
	autoStream bool

	register   chan *Subscriber
	deregister chan *Subscriber

	subscribers     []*Subscriber
	subscriberCount int32

	onSubscribe   SubscriberFunction
	onUnsubscribe SubscriberFunction
}

// newStream creates a stream instance with subscriber callbacks and buffering settings.
func newStream(id StreamID, buffSize, eventLogSize int, replay, autoStream bool, onSubscribe, onUnsubscribe SubscriberFunction) *Stream {
	return &Stream{
		id:            id,
		autoStream:    autoStream,
		autoReplay:    replay,
		subscribers:   make([]*Subscriber, 0),
		register:      make(chan *Subscriber),
		deregister:    make(chan *Subscriber),
		event:         make(chan *Event, buffSize),
		quit:          make(chan struct{}),
		eventLog:      make(EventLog, 0),
		eventLogSize:  eventLogSize,
		onSubscribe:   onSubscribe,
		onUnsubscribe: onUnsubscribe,
	}
}

// StreamID returns the stream identifier.
func (s *Stream) StreamID() StreamID {
	return s.id
}

// run starts the stream event loop.
func (s *Stream) run() {
	go func(stream *Stream) {
		for {
			select {
			case subscriber := <-stream.register:
				stream.subscribers = append(stream.subscribers, subscriber)
				if stream.autoReplay {
					stream.replay(subscriber)
				}

			case subscriber := <-stream.deregister:
				i := stream.getSubIndex(subscriber)
				if i == -1 {
					// 已被慢客户端策略移除：不再重复触发回调。
					continue
				}
				stream.removeSubscriber(i)
				if stream.onUnsubscribe != nil {
					go stream.onUnsubscribe(stream.id, subscriber)
				}

			case event := <-stream.event:
				if stream.autoReplay {
					stream.eventLog.Add(event)
					stream.eventLog.Trim(stream.eventLogSize)
				}
				stream.dispatch(event)

			case <-stream.quit:
				stream.removeAllSubscribers()
				return
			}
		}
	}(s)
}

// dispatch 非阻塞地把事件投递给所有订阅者。写缓冲已满的订阅者视为慢客户端，
// 移出并断开（客户端可按 Last-Event-ID 重连回放），保证单条慢连接不会阻塞
// 整条流。
func (s *Stream) dispatch(event *Event) {
	for i := 0; i < len(s.subscribers); {
		sub := s.subscribers[i]
		if s.trySend(sub, event) {
			i++
			continue
		}
		log.Warn(context.Background(), "sse disconnect slow subscriber, send buffer full", "stream", s.id)
		s.removeSubscriber(i)
		if s.onUnsubscribe != nil {
			go s.onUnsubscribe(s.id, sub)
		}
	}
}

// replay 按 Last-Event-ID 回放历史事件；缓冲写满时同样按慢客户端断开处理，
// 避免回放期间卡住事件循环。
func (s *Stream) replay(sub *Subscriber) {
	if s.eventLog.TruncatedBefore(sub.eventId) {
		log.Warn(context.Background(), "sse replay gap, Last-Event-ID predates retained events",
			"stream", s.id, "last_event_id", sub.eventId, "oldest", string(s.eventLog[0].ID))
	}
	for _, ev := range s.eventLog.Since(sub.eventId) {
		if s.trySend(sub, ev) {
			continue
		}
		log.Warn(context.Background(), "sse disconnect slow subscriber during replay, send buffer full", "stream", s.id)
		if i := s.getSubIndex(sub); i != -1 {
			s.removeSubscriber(i)
		}
		if s.onUnsubscribe != nil {
			go s.onUnsubscribe(s.id, sub)
		}
		return
	}
}

// trySend 尝试投递一次，订阅者缓冲满时立即返回 false（不阻塞事件循环）。
func (s *Stream) trySend(sub *Subscriber, event *Event) bool {
	select {
	case sub.connection <- event:
		return true
	default:
		return false
	}
}

// close stops the stream event loop once.
func (s *Stream) close() {
	s.quitOnce.Do(func() {
		close(s.quit)
	})
}

func (s *Stream) getSubIndex(sub *Subscriber) int {
	for i := range s.subscribers {
		if s.subscribers[i] == sub {
			return i
		}
	}
	return -1
}

func (s *Stream) addSubscriber(eventId string, req *http.Request) *Subscriber {
	var requestURL *url.URL
	var header http.Header
	if req != nil {
		if req.URL != nil {
			urlCopy := *req.URL
			requestURL = &urlCopy
		}
		header = req.Header.Clone()
	}

	sub := &Subscriber{
		eventId:    eventId,
		quit:       s.deregister,
		streamQuit: s.quit,
		connection: make(chan *Event, defaultSubscriberBuffer),
		URL:        requestURL,
		Header:     header,
	}

	if s.autoStream {
		sub.removed = make(chan struct{}, 1)
	}

	atomic.AddInt32(&s.subscriberCount, 1)
	select {
	case s.register <- sub:
	case <-s.quit:
		// 流已关闭：返回已关闭连接的订阅者，调用方读取即刻结束，不阻塞。
		close(sub.connection)
		atomic.AddInt32(&s.subscriberCount, -1)
		return sub
	}

	if s.onSubscribe != nil {
		go s.onSubscribe(s.id, sub)
	}

	return sub
}

func (s *Stream) removeSubscriber(i int) {
	atomic.AddInt32(&s.subscriberCount, -1)
	close(s.subscribers[i].connection)
	if s.subscribers[i].removed != nil {
		s.subscribers[i].removed <- struct{}{}
		close(s.subscribers[i].removed)
	}
	s.subscribers = append(s.subscribers[:i], s.subscribers[i+1:]...)
}

func (s *Stream) removeAllSubscribers() {
	for i := 0; i < len(s.subscribers); i++ {
		close(s.subscribers[i].connection)
		if s.subscribers[i].removed != nil {
			s.subscribers[i].removed <- struct{}{}
			close(s.subscribers[i].removed)
		}
	}
	atomic.StoreInt32(&s.subscriberCount, 0)
	s.subscribers = s.subscribers[:0]
}

func (s *Stream) getSubscriberCount() int {
	return int(atomic.LoadInt32(&s.subscriberCount))
}
