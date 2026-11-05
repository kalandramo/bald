package aliyun

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	sls "github.com/aliyun/aliyun-log-go-sdk"
	"github.com/aliyun/aliyun-log-go-sdk/producer"
	"google.golang.org/protobuf/proto"

	log "github.com/kalandramo/bald/log"
)

// Logger 扩展了项目 Logger 接口，增加 Aliyun SLS 特有的方法。
type Logger interface {
	log.Logger

	GetProducer() *producer.Producer
	Close() error
}

type aliyunLog struct {
	producer *producer.Producer
	opts     *options
	extra    []any
}

func (a *aliyunLog) GetProducer() *producer.Producer {
	return a.producer
}

func (a *aliyunLog) Close() error {
	return a.producer.Close(5000)
}

// Debug 输出 DEBUG 级别日志。
func (a *aliyunLog) Debug(ctx context.Context, msg string, keyvals ...any) {
	a.post(ctx, "DEBUG", msg, keyvals)
}

// Info 输出 INFO 级别日志。
func (a *aliyunLog) Info(ctx context.Context, msg string, keyvals ...any) {
	a.post(ctx, "INFO", msg, keyvals)
}

// Warn 输出 WARN 级别日志。
func (a *aliyunLog) Warn(ctx context.Context, msg string, keyvals ...any) {
	a.post(ctx, "WARN", msg, keyvals)
}

// Error 输出 ERROR 级别日志。
func (a *aliyunLog) Error(ctx context.Context, msg string, keyvals ...any) {
	a.post(ctx, "ERROR", msg, keyvals)
}

// With 返回附加了指定 key-value 对的新 Logger 实例。
func (a *aliyunLog) With(keyvals ...any) log.Logger {
	return &aliyunLog{
		producer: a.producer,
		opts:     a.opts,
		extra:    append(append([]any{}, a.extra...), keyvals...),
	}
}

// Enabled 报告给定级别是否会被输出。云端日志服务默认启用所有级别。
func (a *aliyunLog) Enabled(_ log.Level) bool {
	return true
}

// post 发送日志到 Aliyun SLS。
// ctx 属性流（log.ContextWithAttrs）在构造条目时同步提取并合并，
// 之后 producer 异步发送不再依赖 ctx。
func (a *aliyunLog) post(ctx context.Context, level, msg string, keyvals []any) {
	contents := make([]*sls.LogContent, 0, 3+len(a.extra)/2+len(keyvals)/2)
	contents = append(contents, &sls.LogContent{
		Key:   newString("level"),
		Value: newString(level),
	})
	if msg != "" {
		contents = append(contents, &sls.LogContent{
			Key:   newString("msg"),
			Value: newString(msg),
		})
	}
	all := append(append([]any{}, a.extra...), log.ContextAttrsToArgs(ctx)...)
	all = append(all, keyvals...)
	for i := 0; i+1 < len(all); i += 2 {
		contents = append(contents, &sls.LogContent{
			Key:   newString(toString(all[i])),
			Value: newString(toString(all[i+1])),
		})
	}

	logInst := &sls.Log{
		Time:     proto.Uint32(uint32(time.Now().Unix())),
		Contents: contents,
	}
	_ = a.producer.SendLog(a.opts.project, a.opts.logstore, "", "", logInst)
}

// NewAliyunLogger 创建 Aliyun SLS 日志记录器。
//
// endpoint/project/logstore/access_key/access_secret 五项必须显式给出，且 endpoint
// 必须自带 scheme。这五项留空都能「构造成功」并一路走到运行期才失败：bald 不消费
// producer 的失败信号（见 post 里被丢弃的 SendLog 错误），唯一的痕迹是 SDK 自己
// 打到 stderr 的运行期日志——启动期不挡，运维通常要等到去 SLS 控制台查不到日志
// 才发现。逐项后果：
//   - endpoint 为空：SDK 把 host 拼成 "<project>."（project 前缀机制见下），
//     域名不成立，请求发不出去；
//   - endpoint 不带 scheme：SDK 只在值以 https:// 开头时才用 TLS，裸 host 走明文
//     HTTP——Authorization 里的 HMAC 签名与日志正文一起不上 TLS；这个默认还受
//     SDK 的进程级变量 GlobalForceUsingHTTP 影响，bald 不该把传输安全交给它。
//     显式写 http:// 仍然放行（内网/代理端点是合法选择），只是要运维自己写出来。
//   - project 与 logstore 决定落点：实测 URL 形如
//     "<scheme>://<project>.<endpoint host>/logstores/<logstore>"。project 留空
//     得到一个以点开头的非法 host，logstore 留空得到 "/logstores/"。这两项若靠
//     默认值兜底等于凭空捏造一个目标——所以 defaultOptions 刻意不留占位值。
//   - access_key/access_secret 为空：producer 侧没有任何环境变量凭证链——SDK 的
//     createClient 只认 CredentialsProvider、UpdateStsToken 回调与两个 plain 字段，
//     最后兜底仍是静态空凭证（此前这里的注释与 bconf 的校验都按「SDK env 凭证链」
//     放行空值，那条通道在本路径不存在）。空 AK 在假线上实测得到
//     "Authorization: LOG :<sig>"（签名主体无身份）；空 SK 用空密钥做 HMAC-SHA1，
//     形状合法但服务端必然判 401。security_token 不校验：长期 AK/SK 下留空是正常
//     配置。
func NewAliyunLogger(options ...Option) (Logger, error) {
	opts := defaultOptions()
	for _, o := range options {
		o(opts)
	}

	if err := validateRequiredOptions(opts); err != nil {
		return nil, err
	}

	producerConfig := producer.GetDefaultProducerConfig()
	producerConfig.Endpoint = opts.endpoint

	// 凭证走 CredentialsProvider：SDK 的 producer 只有这一条与「AccessKeyID/Secret
	// 两个 plain 字段」等价的通道（createClient 末尾的兜底同样是静态凭证），
	// 用 provider 是为了把 security_token 一起带进去。
	producerConfig.CredentialsProvider = sls.NewStaticCredentialsProvider(opts.accessKey, opts.accessSecret, opts.securityToken)

	producerInst, err := producer.NewProducer(producerConfig)
	if err != nil {
		return nil, err
	}
	producerInst.Start()

	return &aliyunLog{
		opts:     opts,
		producer: producerInst,
	}, nil
}

// newString string convert to *string
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
