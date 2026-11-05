package tencent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	cls "github.com/tencentcloud/tencentcloud-cls-sdk-go"
	"google.golang.org/protobuf/proto"

	log "github.com/kalandramo/bald/log"
)

type Logger interface {
	log.Logger

	GetProducer() *cls.AsyncProducerClient
	Close() error
}

type tencentLog struct {
	producer *cls.AsyncProducerClient
	opts     *options
	extra    []any
}

var _ log.Logger = (*tencentLog)(nil)

func (l *tencentLog) GetProducer() *cls.AsyncProducerClient {
	return l.producer
}

func (l *tencentLog) Close() error {
	return l.producer.Close(5000)
}

// Debug 输出 DEBUG 级别日志。
func (l *tencentLog) Debug(ctx context.Context, msg string, keyvals ...any) {
	l.post(ctx, "DEBUG", msg, keyvals)
}

// Info 输出 INFO 级别日志。
func (l *tencentLog) Info(ctx context.Context, msg string, keyvals ...any) {
	l.post(ctx, "INFO", msg, keyvals)
}

// Warn 输出 WARN 级别日志。
func (l *tencentLog) Warn(ctx context.Context, msg string, keyvals ...any) {
	l.post(ctx, "WARN", msg, keyvals)
}

// Error 输出 ERROR 级别日志。
func (l *tencentLog) Error(ctx context.Context, msg string, keyvals ...any) {
	l.post(ctx, "ERROR", msg, keyvals)
}

// With 返回附加了指定 key-value 对的新 Logger 实例。
func (l *tencentLog) With(keyvals ...any) log.Logger {
	return &tencentLog{
		producer: l.producer,
		opts:     l.opts,
		extra:    append(append([]any{}, l.extra...), keyvals...),
	}
}

// Enabled 报告给定级别是否会被输出。云端日志服务默认启用所有级别。
func (l *tencentLog) Enabled(_ log.Level) bool {
	return true
}

// post 发送日志到 Tencent CLS。
// ctx 属性流（log.ContextWithAttrs）在构造条目时同步提取并合并，
// 之后 producer 异步发送不再依赖 ctx。
func (l *tencentLog) post(ctx context.Context, level, msg string, keyvals []any) {
	contents := make([]*cls.Log_Content, 0, 3+len(l.extra)/2+len(keyvals)/2)
	contents = append(contents, &cls.Log_Content{
		Key:   newString("level"),
		Value: newString(level),
	})
	if msg != "" {
		contents = append(contents, &cls.Log_Content{
			Key:   newString("msg"),
			Value: newString(msg),
		})
	}
	all := append(append([]any{}, l.extra...), log.ContextAttrsToArgs(ctx)...)
	all = append(all, keyvals...)
	for i := 0; i+1 < len(all); i += 2 {
		contents = append(contents, &cls.Log_Content{
			Key:   newString(toString(all[i])),
			Value: newString(toString(all[i+1])),
		})
	}

	logInst := &cls.Log{
		Time:     proto.Int64(time.Now().Unix()),
		Contents: contents,
	}
	_ = l.producer.SendLog(l.opts.topicID, logInst, nil)
}

// NewTencentLogger 创建腾讯云 CLS 日志记录器。
//
// endpoint/topic_id/access_key/access_secret 四项必须显式给出，且 endpoint 必须
// 自带 scheme。四项留空都能「构造成功」并一路走到运行期才失败：bald 不消费 producer
// 的失败信号（见 post 里被丢弃的 SendLog 错误），唯一的痕迹是 SDK 自己打到 stderr 的
// 运行期日志。逐项后果：
//   - endpoint 为空：SDK 的 validateOptions 本来就会挡（"host cannot be empty"），
//     但它发生在 NewAsyncProducerClient 内部，错误只回一个字符串，读不出「是配置
//     里哪一项」；这里统一挡，顺便把 scheme 判掉。不带 scheme 时 SDK 按自己的默认
//     走明文 HTTP（NewCLSClient 只在值以 https:// 开头时才上 TLS，且一旦上 TLS
//     就硬编 InsecureSkipVerify: true），签名与日志正文一起裸奔——传输方式不该由
//     SDK 的默认决定。
//   - topic_id 为空：落点退化成 /structuredlog?topic_id=，服务端拒绝而 bald 看不见。
//   - access_key/access_secret 为空：CLS 客户端没有凭证链——validateOptions 里的
//     凭证校验是注释掉的，Send 只在两个密钥都非空时才加 Authorization 头，所以空
//     凭证得到的不是「签名里有空身份」而是「一个完全未签名的请求」（在假线上实测，
//     见 contract 包的 TestSdkAssumption_EmptyCredentialsSendUnsigned）。服务端
//     必然 401，而 post 不消费错误。
func NewTencentLogger(options ...Option) (Logger, error) {
	opts := defaultOptions()
	for _, o := range options {
		o(opts)
	}

	if err := validateRequiredOptions(opts); err != nil {
		return nil, err
	}

	producerConfig := cls.GetDefaultAsyncProducerClientConfig()
	producerConfig.AccessKeyID = opts.accessKey
	producerConfig.AccessKeySecret = opts.accessSecret
	producerConfig.Endpoint = opts.endpoint
	producerInst, err := cls.NewAsyncProducerClient(producerConfig)
	if err != nil {
		return nil, err
	}
	producerInst.Start()
	return &tencentLog{
		producer: producerInst,
		opts:     opts,
	}, nil
}

func newString(s string) *string {
	return &s
}

// toString convert any type to string
func toString(v any) string {
	var key string
	if v == nil {
		return key
	}
	switch v := v.(type) {
	case float64:
		key = strconv.FormatFloat(v, 'f', -1, 64)
	case float32:
		key = strconv.FormatFloat(float64(v), 'f', -1, 32)
	case int:
		key = strconv.Itoa(v)
	case uint:
		key = strconv.FormatUint(uint64(v), 10)
	case int8:
		key = strconv.Itoa(int(v))
	case uint8:
		key = strconv.FormatUint(uint64(v), 10)
	case int16:
		key = strconv.Itoa(int(v))
	case uint16:
		key = strconv.FormatUint(uint64(v), 10)
	case int32:
		key = strconv.Itoa(int(v))
	case uint32:
		key = strconv.FormatUint(uint64(v), 10)
	case int64:
		key = strconv.FormatInt(v, 10)
	case uint64:
		key = strconv.FormatUint(v, 10)
	case string:
		key = v
	case bool:
		key = strconv.FormatBool(v)
	case []byte:
		key = string(v)
	case fmt.Stringer:
		key = v.String()
	default:
		newValue, _ := json.Marshal(v)
		key = string(newValue)
	}
	return key
}
