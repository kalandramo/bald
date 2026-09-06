package websocket

import (
	"log"
	"net/url"
	"sync"
	"time"

	ws "github.com/gorilla/websocket"

	"github.com/google/uuid"
)

// channelBufSize 是每个 session 发送通道的默认缓冲大小。
var channelBufSize = 256

// SessionID 唯一标识一个 WebSocket 会话。
type SessionID string

// SessionHooks 定义了 Session 与上层 Server 交互所需的回调接口。
type SessionHooks interface {
	// removeSession 从服务器中注销会话。
	removeSession(*Session)
	// handleSocketRawData 处理从 socket 接收到的原始数据。
	handleSocketRawData(SessionID, []byte) error
	// getPayloadType 返回当前服务器的载荷类型（二进制或文本）。
	getPayloadType() PayloadType
}

// Session 封装单个 WebSocket 连接的生命周期。
type Session struct {
	id SessionID

	conn    *ws.Conn
	queries url.Values

	send chan []byte

	hooks SessionHooks

	lastReadMessageTime  time.Time
	lastWriteMessageTime time.Time

	connMu     sync.RWMutex
	done       chan struct{}
	listenOnce sync.Once
	closeOnce  sync.Once
	wg         sync.WaitGroup
}

// NewSession 创建一个 WebSocket 会话实例。
func NewSession(hooks SessionHooks, conn *ws.Conn, vars url.Values) *Session {
	if conn == nil {
		panic("conn cannot be nil")
	}

	return &Session{
		id:      SessionID(uuid.New().String()),
		conn:    conn,
		queries: vars,
		send:    make(chan []byte, channelBufSize),
		hooks:   hooks,
		done:    make(chan struct{}),
	}
}

// Conn 返回底层 WebSocket 连接。
func (s *Session) Conn() *ws.Conn {
	s.connMu.RLock()
	defer s.connMu.RUnlock()
	return s.conn
}

// Queries 返回连接请求的查询参数。
func (s *Session) Queries() url.Values {
	return s.queries
}

// SessionID 返回会话 ID。
func (s *Session) SessionID() SessionID {
	return s.id
}

// SendMessage 将消息加入发送队列。
func (s *Session) SendMessage(message []byte) {
	select {
	case <-s.done:
		return
	case s.send <- message:
	}
}

// Close 关闭会话并注销。仅执行一次。
func (s *Session) Close() {
	s.closeOnce.Do(func() {
		close(s.done)
		s.closeConnect()

		if s.hooks != nil {
			s.hooks.removeSession(s)
		}
	})
}

// Listen 启动读写 goroutine。仅执行一次。
func (s *Session) Listen() {
	s.listenOnce.Do(func() {
		s.wg.Add(2)
		go s.writePump()
		go s.readPump()
	})
}

// Wait 等待读写 goroutine 结束。
func (s *Session) Wait() {
	s.wg.Wait()
}

func (s *Session) closeConnect() {
	s.connMu.Lock()
	conn := s.conn
	s.conn = nil
	s.connMu.Unlock()

	if conn != nil {
		if err := conn.Close(); err != nil {
			log.Printf("[websocket] disconnect error: %s", err.Error())
		}
	}
}

func (s *Session) sendPingMessage(message string) error {
	s.connMu.RLock()
	conn := s.conn
	s.connMu.RUnlock()
	if conn == nil {
		return nil
	}
	return conn.WriteMessage(ws.PingMessage, []byte(message))
}

func (s *Session) sendPongMessage(message string) error {
	s.connMu.RLock()
	conn := s.conn
	s.connMu.RUnlock()
	if conn == nil {
		return nil
	}
	return conn.WriteMessage(ws.PongMessage, []byte(message))
}

func (s *Session) sendTextMessage(message string) error {
	s.connMu.RLock()
	conn := s.conn
	s.connMu.RUnlock()
	if conn == nil {
		return nil
	}
	return conn.WriteMessage(ws.TextMessage, []byte(message))
}

func (s *Session) sendBinaryMessage(message []byte) error {
	s.connMu.RLock()
	conn := s.conn
	s.connMu.RUnlock()
	if conn == nil {
		return nil
	}
	return conn.WriteMessage(ws.BinaryMessage, message)
}

func (s *Session) writePump() {
	defer s.wg.Done()
	defer s.Close()

	for {
		select {
		case <-s.done:
			return

		case msg := <-s.send:
			s.lastWriteMessageTime = time.Now()

			var err error
			switch s.hooks.getPayloadType() {
			case PayloadTypeBinary:
				if err = s.sendBinaryMessage(msg); err != nil {
					log.Printf("[websocket] write binary message error: %v", err)
					return
				}

			case PayloadTypeText:
				if err = s.sendTextMessage(string(msg)); err != nil {
					log.Printf("[websocket] write text message error: %v", err)
					return
				}
			}
		}
	}
}

func (s *Session) readPump() {
	defer s.wg.Done()
	defer s.Close()

	for {
		select {
		case <-s.done:
			return
		default:
		}

		conn := s.Conn()
		if conn == nil {
			return
		}

		messageType, data, err := conn.ReadMessage()
		if err != nil {
			if ws.IsUnexpectedCloseError(err, ws.CloseNormalClosure, ws.CloseGoingAway, ws.CloseAbnormalClosure) {
				log.Printf("[websocket] read message error: %v", err)
			}
			return
		}

		s.lastReadMessageTime = time.Now()

		switch messageType {
		case ws.CloseMessage:
			return

		case ws.BinaryMessage:
			_ = s.hooks.handleSocketRawData(s.SessionID(), data)

		case ws.TextMessage:
			_ = s.hooks.handleSocketRawData(s.SessionID(), data)

		case ws.PingMessage:
			if err = s.sendPongMessage(""); err != nil {
				log.Printf("[websocket] write pong message error: %v", err)
				return
			}

		case ws.PongMessage:
			// no-op
		}
	}
}
