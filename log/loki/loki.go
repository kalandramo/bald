package loki

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	log "github.com/kalandramo/bald/log"
)

// Compile-time assertion: lokiLog implements log.Logger.
var _ log.Logger = (*lokiLog)(nil)

// Logger 扩展了项目 Logger 接口，增加 Loki 特有的方法。
type Logger interface {
	log.Logger

	Close() error
}

// lokiLog 是基于 Grafana Loki HTTP Push API 的日志适配器。
//
// Loki 是 Grafana Labs 的水平可扩展、高可用、多租户日志聚合系统。
// 日志通过 HTTP POST 发送到 Loki 的 /loki/api/v1/push 端点。
//
// 缓冲与发送配置（shared）在 With 派生的所有实例间共享：
// 派生实例与原实例写入同一缓冲，任一实例 Close 都会冲刷全部待发日志，
// 不会形成派生实例的孤岛缓冲。
//
// Example:
//
//	logger, _ := loki.NewLogger(
//	    loki.WithEndpoint("http://loki:3100/loki/api/v1/push"),
//	    loki.WithLabel("app", "my-service"),
//	    loki.WithLabel("env", "production"),
//	)
//	defer logger.Close()
type lokiLog struct {
	s     *shared
	extra []any
}

// shared 汇集 With 派生实例间共享的缓冲与发送配置。
type shared struct {
	client       *http.Client
	endpoint     string
	labels       map[string]string
	mu           sync.Mutex
	buffer       []logEntry
	batchSize    int
	flushTimeout time.Duration
}

type logEntry struct {
	ts   string // 纳秒时间戳字符串
	line string // JSON 日志行
}

// NewLogger 创建一个 Grafana Loki 日志记录器。
func NewLogger(opts ...Option) (Logger, error) {
	cfg := defaultOptions()
	for _, opt := range opts {
		opt(cfg)
	}

	if cfg.endpoint == "" {
		return nil, fmt.Errorf("loki: endpoint is required")
	}

	client := cfg.httpClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}

	return &lokiLog{
		s: &shared{
			client:       client,
			endpoint:     cfg.endpoint,
			labels:       cfg.labels,
			batchSize:    cfg.batchSize,
			flushTimeout: cfg.flushInterval,
		},
	}, nil
}

// Debug 输出 DEBUG 级别日志。
func (l *lokiLog) Debug(ctx context.Context, msg string, keyvals ...any) {
	l.post(ctx, "DEBUG", msg, keyvals)
}

// Info 输出 INFO 级别日志。
func (l *lokiLog) Info(ctx context.Context, msg string, keyvals ...any) {
	l.post(ctx, "INFO", msg, keyvals)
}

// Warn 输出 WARN 级别日志。
func (l *lokiLog) Warn(ctx context.Context, msg string, keyvals ...any) {
	l.post(ctx, "WARN", msg, keyvals)
}

// Error 输出 ERROR 级别日志。
func (l *lokiLog) Error(ctx context.Context, msg string, keyvals ...any) {
	l.post(ctx, "ERROR", msg, keyvals)
}

// With 返回附加了指定 key-value 对的新 Logger 实例。
// 派生实例与原实例共享缓冲（shared）：写入同一缓冲，任一 Close 全量冲刷。
func (l *lokiLog) With(keyvals ...any) log.Logger {
	return &lokiLog{
		s:     l.s,
		extra: append(append([]any{}, l.extra...), keyvals...),
	}
}

// Enabled 报告给定级别是否会被输出。云端日志服务默认启用所有级别。
func (l *lokiLog) Enabled(_ log.Level) bool {
	return true
}

// Close 刷新剩余缓冲日志。
func (l *lokiLog) Close() error {
	return l.flush()
}

// post 将日志条目加入缓冲区，达到阈值后刷新到 Loki。
// ctx 属性流（log.ContextWithAttrs）在构建 JSON 行时同步提取并合并，
// 之后缓冲与异步 flush 不再依赖 ctx。
func (l *lokiLog) post(ctx context.Context, level, msg string, keyvals []any) {
	data := make(map[string]string, 3+len(l.extra)/2+len(keyvals)/2)
	data["level"] = level
	data["msg"] = msg

	all := append(append([]any{}, l.extra...), log.ContextAttrsToArgs(ctx)...)
	all = append(all, keyvals...)
	for i := 0; i+1 < len(all); i += 2 {
		data[toString(all[i])] = toString(all[i+1])
	}

	line, _ := json.Marshal(data)

	entry := logEntry{
		ts:   strconv.FormatInt(time.Now().UnixNano(), 10),
		line: string(line),
	}

	l.s.mu.Lock()
	l.s.buffer = append(l.s.buffer, entry)
	shouldFlush := len(l.s.buffer) >= l.s.batchSize
	l.s.mu.Unlock()

	if shouldFlush {
		_ = l.flush()
	}
}

// flush 将共享缓冲区中的日志批量推送到 Loki。
// 所有派生实例共享缓冲，任一实例调用 flush 都会冲刷全部待发日志。
func (l *lokiLog) flush() error {
	s := l.s
	s.mu.Lock()
	if len(s.buffer) == 0 {
		s.mu.Unlock()
		return nil
	}
	entries := s.buffer
	s.buffer = nil
	s.mu.Unlock()

	// 构建 Loki push payload
	// {"streams":[{"stream":{"label":"value"},"values":[["ts","line"], ...]}]}
	values := make([][]string, len(entries))
	for i, e := range entries {
		values[i] = []string{e.ts, e.line}
	}

	payload := map[string]any{
		"streams": []map[string]any{
			{
				"stream": s.labels,
				"values": values,
			},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("loki: marshal payload: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.flushTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("loki: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("loki: push: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("loki: push returned status %d", resp.StatusCode)
	}

	return nil
}

// toString 将任意类型转换为字符串。
func toString(v any) string {
	if v == nil {
		return ""
	}
	switch v := v.(type) {
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(v), 'f', -1, 32)
	case int:
		return strconv.Itoa(v)
	case uint:
		return strconv.FormatUint(uint64(v), 10)
	case int8:
		return strconv.Itoa(int(v))
	case uint8:
		return strconv.FormatUint(uint64(v), 10)
	case int16:
		return strconv.Itoa(int(v))
	case uint16:
		return strconv.FormatUint(uint64(v), 10)
	case int32:
		return strconv.Itoa(int(v))
	case uint32:
		return strconv.FormatUint(uint64(v), 10)
	case int64:
		return strconv.FormatInt(v, 10)
	case uint64:
		return strconv.FormatUint(v, 10)
	case string:
		return v
	case bool:
		return strconv.FormatBool(v)
	case []byte:
		return string(v)
	case fmt.Stringer:
		return v.String()
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}
