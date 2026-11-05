package tencent

import (
	"fmt"
	"strings"
)

type options struct {
	topicID      string
	accessKey    string
	accessSecret string
	endpoint     string
}

// validateRequiredOptions 校验四项必给配置（endpoint/topic_id/access_key/
// access_secret）与 endpoint 的 scheme。逐项后果见 NewTencentLogger 的注释。
func validateRequiredOptions(o *options) error {
	switch {
	case o.endpoint == "":
		return fmt.Errorf("log/tencent: endpoint is required (empty target makes every send fail silently)")
	case !strings.HasPrefix(o.endpoint, "https://") && !strings.HasPrefix(o.endpoint, "http://"):
		return fmt.Errorf("log/tencent: endpoint %q must start with https:// or http:// (a scheme-less endpoint goes to plaintext HTTP by the SDK's default)", o.endpoint)
	case o.topicID == "":
		return fmt.Errorf("log/tencent: topic_id is required (empty yields /structuredlog?topic_id=)")
	case o.accessKey == "":
		return fmt.Errorf("log/tencent: access_key is required (the CLS client has no credential chain; empty AK sends an unsigned request)")
	case o.accessSecret == "":
		return fmt.Errorf("log/tencent: access_secret is required (empty SK skips the Authorization header entirely)")
	}
	return nil
}

func defaultOptions() *options {
	return &options{}
}

func WithEndpoint(endpoint string) Option {
	return func(cls *options) {
		cls.endpoint = endpoint
	}
}

func WithTopicID(topicID string) Option {
	return func(cls *options) {
		cls.topicID = topicID
	}
}

func WithAccessKey(ak string) Option {
	return func(cls *options) {
		cls.accessKey = ak
	}
}

func WithAccessSecret(as string) Option {
	return func(cls *options) {
		cls.accessSecret = as
	}
}

type Option func(cls *options)
