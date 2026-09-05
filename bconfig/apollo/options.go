package apollo

import (
	"github.com/apolloconfig/agollo/v4/constant"
	"github.com/apolloconfig/agollo/v4/extension"
)

// Option 是 Apollo 配置源选项。
type Option func(*options)

type options struct {
	appid          string
	secret         string
	cluster        string
	endpoint       string
	namespace      string
	isBackupConfig bool
	backupPath     string
	originConfig   bool
}

// WithAppID 设置 Apollo 应用 ID（自建模式必填）。
func WithAppID(appID string) Option {
	return func(o *options) {
		o.appid = appID
	}
}

// WithCluster 设置集群名（默认 default）。
func WithCluster(cluster string) Option {
	return func(o *options) {
		o.cluster = cluster
	}
}

// WithEndpoint 设置 Apollo 配置中心地址（自建模式必填）。
func WithEndpoint(endpoint string) Option {
	return func(o *options) {
		o.endpoint = endpoint
	}
}

// WithEnableBackup 启用本地备份。
func WithEnableBackup() Option {
	return func(o *options) {
		o.isBackupConfig = true
	}
}

// WithDisableBackup 禁用本地备份。
func WithDisableBackup() Option {
	return func(o *options) {
		o.isBackupConfig = false
	}
}

// WithSecret 设置应用密钥。
func WithSecret(secret string) Option {
	return func(o *options) {
		o.secret = secret
	}
}

// WithNamespace 设置命名空间名称（可逗号分隔多个，Load/Watch 用首个；必填）。
func WithNamespace(name string) Option {
	return func(o *options) {
		o.namespace = name
	}
}

// WithBackupPath 设置本地备份路径。
func WithBackupPath(backupPath string) Option {
	return func(o *options) {
		o.backupPath = backupPath
	}
}

// WithOriginalConfig 结构化命名空间（yaml/yml/json）返回原始文档而非
// 展开为扁平 KV 的嵌套 JSON。整文档消费（如 bootstrap/config Store）推荐开启。
func WithOriginalConfig() Option {
	return func(o *options) {
		extension.AddFormatParser(constant.JSON, &jsonExtParser{})
		extension.AddFormatParser(constant.YAML, &yamlExtParser{})
		extension.AddFormatParser(constant.YML, &yamlExtParser{})
		o.originConfig = true
	}
}
