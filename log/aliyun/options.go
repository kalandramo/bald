package aliyun

import (
	"fmt"
	"strings"
)

type options struct {
	accessKey     string
	accessSecret  string
	securityToken string

	endpoint string
	project  string
	logstore string
}

// validateRequiredOptions 校验五项必给配置（endpoint/project/logstore/access_key/
// access_secret）与 endpoint 的 scheme。理由与实测后果见 NewAliyunLogger 的注释。
// security_token 不校验：它只在 STS 临时凭证下有意义，长期 AK/SK 留空是正常配置。
func validateRequiredOptions(o *options) error {
	switch {
	case o.endpoint == "":
		return fmt.Errorf("log/aliyun: endpoint is required (empty target makes every send fail silently)")
	case !strings.HasPrefix(o.endpoint, "https://") && !strings.HasPrefix(o.endpoint, "http://"):
		return fmt.Errorf("log/aliyun: endpoint %q must start with https:// or http:// (a scheme-less endpoint goes to plaintext HTTP by the SDK's default)", o.endpoint)
	case o.project == "":
		return fmt.Errorf("log/aliyun: project is required (it becomes the <project>. host prefix; empty yields an invalid target)")
	case o.logstore == "":
		return fmt.Errorf("log/aliyun: logstore is required (empty yields the /logstores/ path)")
	case o.accessKey == "":
		return fmt.Errorf("log/aliyun: access_key is required (producer has no credential chain; empty AK signs as LOG :<sig>)")
	case o.accessSecret == "":
		return fmt.Errorf("log/aliyun: access_secret is required (empty SK signs with an empty key, so every request is rejected)")
	}
	return nil
}

type Option func(alc *options)

// defaultOptions 刻意不给 project/logstore 造占位默认：占位值会让「漏配」读起来
// 像「配好了」——签名、压缩、host 拼装全都正常工作，日志被投到一个凭空捏造的
// 目标，运维看不出任何异常。必给项由 validateRequiredOptions 统一挡。
func defaultOptions() *options {
	return &options{}
}

func WithEndpoint(endpoint string) Option {
	return func(alc *options) {
		alc.endpoint = endpoint
	}
}

func WithProject(project string) Option {
	return func(alc *options) {
		alc.project = project
	}
}

func WithLogstore(logstore string) Option {
	return func(alc *options) {
		alc.logstore = logstore
	}
}

func WithAccessKey(ak string) Option {
	return func(alc *options) {
		alc.accessKey = ak
	}
}

func WithAccessSecret(as string) Option {
	return func(alc *options) {
		alc.accessSecret = as
	}
}

func WithSecurityToken(st string) Option {
	return func(alc *options) {
		alc.securityToken = st
	}
}
