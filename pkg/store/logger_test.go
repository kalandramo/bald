package store

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

// ---------------------------------------------------------------------------
// Logger 适配器
//
// 补测动机：NopLogger 与 logAdapter 此前 0% 覆盖。logAdapter 有真实的转发
// 逻辑（FromLogger 把任意满足 Error/Info 签名的对象适配为 store.Logger），
// 若转发写错，注入的 logger 会静默不生效——正是本项目高频的「契约声明但
// 实现没接」类缺陷，值得钉住。
// ---------------------------------------------------------------------------

// fakeLogSink 记录收到的日志调用，用于验证适配器确实转发。
// 每种调用各存一份完整记录（msg+kvs）——共享 last* 字段会被后续调用覆盖，
// 导致断言到错误的调用（本测试首版即因此误报两次，均为测试缺陷非生产问题）。
type fakeLogSink struct {
	errCalls  int
	infoCalls int

	errMsg  string
	errKVs  []any
	errVal  error
	infoMsg string
	infoKVs []any
}

func (f *fakeLogSink) Error(_ context.Context, err error, msg string, kvs ...any) {
	f.errCalls++
	f.errVal = err
	f.errMsg = msg
	f.errKVs = kvs
}

func (f *fakeLogSink) Info(_ context.Context, msg string, kvs ...any) {
	f.infoCalls++
	f.infoMsg = msg
	f.infoKVs = kvs
}

// TestFromLogger_Forwards 适配器必须把调用原样转发给被适配对象。
func TestFromLogger_Forwards(t *testing.T) {
	sink := &fakeLogSink{}
	l := FromLogger(sink)

	ctx := context.Background()
	boom := errors.New("db down")
	l.Error(ctx, boom, "query failed", "table", "users")
	l.Info(ctx, "connected", "addr", "127.0.0.1")

	assert.Equal(t, 1, sink.errCalls, "Error 应被转发一次")
	assert.Equal(t, 1, sink.infoCalls, "Info 应被转发一次")
	assert.ErrorIs(t, sink.errVal, boom, "错误对象应原样传递")
	assert.Equal(t, "query failed", sink.errMsg)
	assert.Equal(t, []any{"table", "users"}, sink.errKVs, "Error 的 kvs 应原样传递")
	assert.Equal(t, "connected", sink.infoMsg, "Info 的 msg 应原样传递")
	assert.Equal(t, []any{"addr", "127.0.0.1"}, sink.infoKVs, "Info 的 kvs 应原样传递")
}

// TestNopLogger_NoPanic 空转实现不得 panic（测试与无日志场景依赖它）。
func TestNopLogger_NoPanic(t *testing.T) {
	var l NopLogger
	assert.NotPanics(t, func() {
		l.Error(context.Background(), errors.New("x"), "msg", "k", "v")
		l.Info(context.Background(), "msg", "k", "v")
	})
}

// TestFromLogger_SatisfiesLogger 编译期+运行期双重保证适配结果满足 Logger。
func TestFromLogger_SatisfiesLogger(t *testing.T) {
	var _ Logger = NopLogger{}
	var _ Logger = FromLogger(&fakeLogSink{})

	// 默认构造的 Store 应持有 NopLogger（非 nil），且其方法可安全调用。
	s := NewStore[facadeEntity](&stubProvider{})
	assert.NotNil(t, s.logger)
	assert.NotPanics(t, func() { s.logger.Info(context.Background(), "noop") })
}
