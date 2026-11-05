package tencent

import (
	"context"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	log "github.com/kalandramo/bald/log"
)

// stubEndpoint 起一个应答 200 的本机假线并返回它的 URL（天然带 http:// 前缀）。
// 构造期校验要求 endpoint 自带 scheme（见 NewTencentLogger 的注释），这几条
// 只判「投递路径不炸」，落到线上的字节判据在 contract 包的离线闸门里。
// 应答 200 而不是让连接失败：失败会进 SDK 的重试队列，Close 要等到 5s 超时才回。
func stubEndpoint(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		rw.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestWithEndpoint(t *testing.T) {
	opts := new(options)
	endpoint := "eee"
	funcEndpoint := WithEndpoint(endpoint)
	funcEndpoint(opts)
	if opts.endpoint != "eee" {
		t.Errorf("WithEndpoint() = %s, want %s", opts.endpoint, endpoint)
	}
}

func TestWithTopicId(t *testing.T) {
	opts := new(options)
	topicID := "ee"
	funcTopicID := WithTopicID(topicID)
	funcTopicID(opts)
	if opts.topicID != "ee" {
		t.Errorf("WithTopicId() = %s, want %s", opts.endpoint, topicID)
	}
}

func TestWithAccessKey(t *testing.T) {
	opts := new(options)
	accessKey := "ee"
	funcAccessKey := WithAccessKey(accessKey)
	funcAccessKey(opts)
	if opts.accessKey != "ee" {
		t.Errorf("WithAccessKey() = %s, want %s", opts.endpoint, accessKey)
	}
}

func TestWithAccessSecret(t *testing.T) {
	opts := new(options)
	accessSecret := "ee"
	funcAccessSecret := WithAccessSecret(accessSecret)
	funcAccessSecret(opts)
	if opts.accessSecret != "ee" {
		t.Errorf("WithAccessSecret() = %s, want %s", opts.accessSecret, accessSecret)
	}
}

func TestTestLogger(t *testing.T) {
	topicID := "aa"
	logger, err := NewTencentLogger(
		WithTopicID(topicID),
		WithEndpoint(stubEndpoint(t)),
		WithAccessKey("a"),
		WithAccessSecret("b"),
	)
	if err != nil {
		t.Error(err)
		return
	}
	defer logger.Close()
	logger.GetProducer()
	logger.Debug(nil, "log", "test")
	logger.Info(nil, "log", "test")
	logger.Warn(nil, "log", "test")
	logger.Error(nil, "log", "test")
}

func TestLog(t *testing.T) {
	topicID := "foo"
	logger, err := NewTencentLogger(
		WithTopicID(topicID),
		WithEndpoint(stubEndpoint(t)),
		WithAccessKey("a"),
		WithAccessSecret("b"),
	)
	if err != nil {
		t.Error(err)
		return
	}
	defer logger.Close()
	logger.Debug(nil, "test", "a", 0, "b", int8(1), "c", int16(2), "d", int32(3))
	logger.Debug(nil, "test", "a", uint(0), "b", uint8(1), "c", uint16(2), "d", uint32(3))
	logger.Debug(nil, "test", "a", int64(0), "b", uint64(1), "c", float32(2), "d", float64(3))
	logger.Debug(nil, "test", "a", []byte{0, 1, 2, 3}, "b", "foo")
}

func TestNewString(t *testing.T) {
	ptr := newString("")
	if kind := reflect.TypeOf(ptr).Kind(); kind != reflect.Pointer {
		t.Errorf("want type: %v, got type: %v", reflect.Pointer, kind)
	}
}

// 带 ctx 属性流的调用应在构造条目时同步合并属性，且 nil ctx 不 panic。
func TestContextAttrs(t *testing.T) {
	logger, err := NewTencentLogger(
		WithTopicID("foo"),
		WithEndpoint(stubEndpoint(t)),
		WithAccessKey("a"),
		WithAccessSecret("b"),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer logger.Close()

	ctx := log.ContextWithAttrs(context.Background(),
		slog.String("trace_id", "t-ctx-1"),
	)
	logger.Debug(ctx, "with attrs")
	logger.Info(ctx, "with attrs")
	logger.Warn(ctx, "with attrs")
	logger.Error(ctx, "with attrs")

	// nil ctx 与空 ctx：ContextAttrsToArgs 返回 nil，不应 panic。
	logger.Debug(nil, "nil ctx")
	logger.Info(context.Background(), "empty ctx")
}

func TestToString(t *testing.T) {
	tests := []struct {
		name string
		in   any
		out  string
	}{
		{"float64", 6.66, "6.66"},
		{"max float64", math.MaxFloat64, "179769313486231570000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000"}, //nolint:lll
		{"float32", float32(6.66), "6.66"},
		{"max float32", float32(math.MaxFloat32), "340282350000000000000000000000000000000"},
		{"int", math.MaxInt64, "9223372036854775807"},
		{"uint", uint(math.MaxUint64), "18446744073709551615"},
		{"int8", int8(math.MaxInt8), "127"},
		{"uint8", uint8(math.MaxUint8), "255"},
		{"int16", int16(math.MaxInt16), "32767"},
		{"uint16", uint16(math.MaxUint16), "65535"},
		{"int32", int32(math.MaxInt32), "2147483647"},
		{"uint32", uint32(math.MaxUint32), "4294967295"},
		{"int64", int64(math.MaxInt64), "9223372036854775807"},
		{"uint64", uint64(math.MaxUint64), "18446744073709551615"},
		{"string", "abc", "abc"},
		{"bool", false, "false"},
		{"[]byte", []byte("abc"), "abc"},
		{"struct", struct{ Name string }{}, `{"Name":""}`},
		{"nil", nil, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			out := toString(test.in)
			if test.out != out {
				t.Fatalf("want: %s, got: %s", test.out, out)
			}
		})
	}
}

// defaultOptions 刻意不给落点与凭证造占位默认：占位值会让「漏配」读起来像
// 「配好了」——签名、压缩、URL 拼装全都正常工作，日志投到一个凭空捏造的目标。
func TestDefaultOptionsHasNoDefaults(t *testing.T) {
	o := defaultOptions()
	if o.endpoint != "" || o.topicID != "" || o.accessKey != "" || o.accessSecret != "" {
		t.Errorf("defaultOptions() 不应给出任何默认值，得到 endpoint=%q topic_id=%q access_key=%q access_secret=%q",
			o.endpoint, o.topicID, o.accessKey, o.accessSecret)
	}
}

// TestNewTencentLoggerRejectsIncompleteOptions 五项构造期必给判据（四项空值 +
// 裸 host）。每一项留空今天都能「构造成功」并走到运行期才失败，而 post 不消费
// SendLog 的错误——逐项后果见 NewTencentLogger 的注释。
func TestNewTencentLoggerRejectsIncompleteOptions(t *testing.T) {
	cases := []struct {
		name string
		opts []Option
		want string
	}{
		// want 取「那一条校验自己的措辞」而不是字段名：空 endpoint 若被摘掉，
		// scheme 那条也会回一个含 "endpoint" 的错误，只比字段名就判不出是哪一行漂了。
		{
			"endpoint 为空",
			[]Option{WithTopicID("t"), WithAccessKey("ak"), WithAccessSecret("sk")},
			"endpoint is required",
		},
		{
			"endpoint 无 scheme（SDK 默认走明文，签名与正文一起裸奔）",
			[]Option{WithEndpoint("127.0.0.1:1"), WithTopicID("t"), WithAccessKey("ak"), WithAccessSecret("sk")},
			"must start with https://",
		},
		{
			"topic_id 为空（落点退化成 ?topic_id=）",
			[]Option{WithEndpoint("http://127.0.0.1:1"), WithAccessKey("ak"), WithAccessSecret("sk")},
			"topic_id is required",
		},
		{
			"access_key 为空（CLS 客户端无凭证链，得到的是未签名请求）",
			[]Option{WithEndpoint("http://127.0.0.1:1"), WithTopicID("t"), WithAccessSecret("sk")},
			"access_key is required",
		},
		{
			"access_secret 为空（同上：SK 为空就不加 Authorization 头）",
			[]Option{WithEndpoint("http://127.0.0.1:1"), WithTopicID("t"), WithAccessKey("ak")},
			"access_secret is required",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l, err := NewTencentLogger(tc.opts...)
			if err == nil {
				_ = l.Close()
				t.Fatalf("%s: 期望构造期 fail-fast", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("错误 %v 应含 %q", err, tc.want)
			}
		})
	}
}
