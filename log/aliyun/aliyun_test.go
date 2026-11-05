package aliyun

import (
	"context"
	"log/slog"
	"math"
	"net"
	"reflect"
	"strconv"
	"strings"
	"testing"

	log "github.com/kalandramo/bald/log"
)

// stubEndpoint 起一个只 accept 不解析的本机监听，返回带 scheme 的 endpoint。
// 构造期校验要求 endpoint 自带 scheme（见 NewAliyunLogger 的注释），这三条
// 冒烟测试因此不能再用裸 host；落点在本机也避免测试碰真实 DNS 与外网。
func stubEndpoint(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return "http://127.0.0.1:" + strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
}

// newTestLogger 用一组完整合法的配置构造 logger，停机交给 t.Cleanup。
func newTestLogger(t *testing.T) Logger {
	t.Helper()
	l, err := NewAliyunLogger(
		WithEndpoint(stubEndpoint(t)),
		WithProject("test-project"),
		WithLogstore("test-logstore"),
		WithAccessKey("test-ak"),
		WithAccessSecret("test-sk"),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func TestWithEndpoint(t *testing.T) {
	opts := new(options)
	endpoint := "foo"
	funcEndpoint := WithEndpoint(endpoint)
	funcEndpoint(opts)
	if opts.endpoint != endpoint {
		t.Errorf("WithEndpoint() = %s, want %s", opts.endpoint, endpoint)
	}
}

func TestWithProject(t *testing.T) {
	opts := new(options)
	project := "foo"
	funcProject := WithProject(project)
	funcProject(opts)
	if opts.project != project {
		t.Errorf("WithProject() = %s, want %s", opts.project, project)
	}
}

func TestWithLogstore(t *testing.T) {
	opts := new(options)
	logstore := "foo"
	funcLogstore := WithLogstore(logstore)
	funcLogstore(opts)
	if opts.logstore != logstore {
		t.Errorf("WithLogatore() = %s, want %s", opts.logstore, logstore)
	}
}

func TestWithAccessKey(t *testing.T) {
	opts := new(options)
	accessKey := "foo"
	funcAccessKey := WithAccessKey(accessKey)
	funcAccessKey(opts)
	if opts.accessKey != accessKey {
		t.Errorf("WithAccessKey() = %s, want %s", opts.accessKey, accessKey)
	}
}

func TestWithAccessSecret(t *testing.T) {
	opts := new(options)
	accessSecret := "foo"
	funcAccessSecret := WithAccessSecret(accessSecret)
	funcAccessSecret(opts)
	if opts.accessSecret != accessSecret {
		t.Errorf("WithAccessSecret() = %s, want %s", opts.accessSecret, accessSecret)
	}
}

func TestLogger(t *testing.T) {
	logger := newTestLogger(t)
	logger.GetProducer()
	logger.Debug(nil, "test")
	logger.Info(nil, "test")
	logger.Warn(nil, "test")
	logger.Error(nil, "test")
}

func TestLog(t *testing.T) {
	logger := newTestLogger(t)
	logger.Debug(nil, "test", "a", 0, "b", int8(1), "c", int16(2), "d", int32(3))
	logger.Debug(nil, "test", "a", uint(0), "b", uint8(1), "c", uint16(2), "d", uint32(3))
	logger.Debug(nil, "test", "a", int64(0), "b", uint64(1), "c", float32(2), "d", float64(3))
	logger.Debug(nil, "test", "a", []byte{0, 1, 2, 3}, "b", "foo")
	logger.Debug(nil, "test", "a", true, "b", 0)
}

// 带 ctx 属性流的调用应在构造条目时同步合并属性，且 nil ctx 不 panic。
func TestContextAttrs(t *testing.T) {
	logger := newTestLogger(t)

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

// defaultOptions 不给 project/logstore 造占位默认：占位值让「漏配」读起来像
// 「配好了」。这条守的是这个不变量本身——一旦有人重新塞进默认目标，这里判红。
func TestDefaultOptionsHasNoPlaceholderTarget(t *testing.T) {
	o := defaultOptions()
	if o.endpoint != "" || o.project != "" || o.logstore != "" {
		t.Errorf("defaultOptions() 不应给出落点默认值，得到 endpoint=%q project=%q logstore=%q",
			o.endpoint, o.project, o.logstore)
	}
}

// 五项必给配置留空时构造必须失败，且错误里点名是哪一项——日志静默消失的四种
// 形状（空 endpoint / 无 scheme 的 endpoint / 空落点 / 空凭证）都由这里挡下。
func TestNewAliyunLogger_RejectsIncompleteOptions(t *testing.T) {
	cases := []struct {
		name string
		opts []Option
		want string
	}{
		// want 取各条校验自身的措辞，理由同 contract/mapping_offline_test.go。
		{
			"endpoint 为空",
			[]Option{WithProject("p"), WithLogstore("s"), WithAccessKey("ak"), WithAccessSecret("sk")},
			"endpoint is required",
		},
		{
			"endpoint 无 scheme（SDK 默认走明文，签名与正文一起裸奔）",
			[]Option{WithEndpoint("127.0.0.1:1"), WithProject("p"), WithLogstore("s"), WithAccessKey("ak"), WithAccessSecret("sk")},
			"must start with https://",
		},
		{
			"project 为空",
			[]Option{WithEndpoint("http://127.0.0.1:1"), WithLogstore("s"), WithAccessKey("ak"), WithAccessSecret("sk")},
			"project is required",
		},
		{
			"logstore 为空",
			[]Option{WithEndpoint("http://127.0.0.1:1"), WithProject("p"), WithAccessKey("ak"), WithAccessSecret("sk")},
			"logstore is required",
		},
		{
			"access_key 为空",
			[]Option{WithEndpoint("http://127.0.0.1:1"), WithProject("p"), WithLogstore("s"), WithAccessSecret("sk")},
			"access_key is required",
		},
		{
			"access_secret 为空",
			[]Option{WithEndpoint("http://127.0.0.1:1"), WithProject("p"), WithLogstore("s"), WithAccessKey("ak")},
			"access_secret is required",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l, err := NewAliyunLogger(tc.opts...)
			if err == nil {
				_ = l.Close()
				t.Fatalf("缺 %s 仍然构造成功，日志会静默消失", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("错误未点名有缺陷的配置项: err=%v, want 包含 %q", err, tc.want)
			}
		})
	}
}

// security_token 是唯一可留空的凭证项（长期 AK/SK 下的正常配置），不能被
// 必给校验顺手挡掉。
func TestNewAliyunLogger_AllowsEmptySecurityToken(t *testing.T) {
	l, err := NewAliyunLogger(
		WithEndpoint(stubEndpoint(t)),
		WithProject("p"),
		WithLogstore("s"),
		WithAccessKey("ak"),
		WithAccessSecret("sk"),
	)
	if err != nil {
		t.Fatalf("security_token 留空是合法配置: %v", err)
	}
	_ = l.Close()
}

func TestNewString(t *testing.T) {
	ptr := newString("")
	if kind := reflect.TypeOf(ptr).Kind(); kind != reflect.Pointer {
		t.Errorf("want type: %v, got type: %v", reflect.Pointer, kind)
	}
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
