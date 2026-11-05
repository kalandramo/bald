package consul

import (
	"github.com/hashicorp/consul/api"
	"time"
)

// Option is consul registry option.
type Option func(*Registry)

// WithHealthCheck with registry health check option.
func WithHealthCheck(enable bool) Option {
	return func(o *Registry) {
		o.enableHealthCheck = enable
	}
}

// WithTimeout with get services timeout option.
//
// 它管的是**发现侧**一次 resolve 的截止期（Registry.resolve 里的
// context.WithTimeout），不是注册到 consul 的健康检查超时——后者见
// [WithHealthCheckTimeout]。
func WithTimeout(timeout time.Duration) Option {
	return func(o *Registry) {
		o.timeout = timeout
	}
}

// WithHealthCheckTimeout 设置注册到 consul 的 TCP 健康检查 timeout，
// 它出现在 `/v1/agent/service/register` 请求体的检查里。默认 5s。
func WithHealthCheckTimeout(timeout time.Duration) Option {
	return func(o *Registry) {
		if o.cli != nil {
			o.cli.healthCheckTimeout = timeout
		}
	}
}

// WithDatacenter with registry datacenter option
func WithDatacenter(dc Datacenter) Option {
	return func(o *Registry) {
		o.cli.dc = dc
	}
}

// WithHeartbeat enable or disable heartbeat
func WithHeartbeat(enable bool) Option {
	return func(o *Registry) {
		if o.cli != nil {
			o.cli.heartbeat = enable
		}
	}
}

// WithServiceResolver with endpoint function option.
func WithServiceResolver(fn ServiceResolver) Option {
	return func(o *Registry) {
		if o.cli != nil {
			o.cli.resolver = fn
		}
	}
}

// WithHealthCheckInterval with healthcheck interval in seconds.
func WithHealthCheckInterval(interval int) Option {
	return func(o *Registry) {
		if o.cli != nil {
			o.cli.healthcheckInterval = interval
		}
	}
}

// WithDeregisterCriticalServiceAfter with deregister-critical-service-after in seconds.
func WithDeregisterCriticalServiceAfter(interval int) Option {
	return func(o *Registry) {
		if o.cli != nil {
			o.cli.deregisterCriticalServiceAfter = interval
		}
	}
}

// WithServiceCheck with service checks
func WithServiceCheck(checks ...*api.AgentServiceCheck) Option {
	return func(r *Registry) { r.cli.serviceChecks = append(r.cli.serviceChecks, checks...) }
}

// WithAddress 设置 consul 地址（自建 client 模式；空则走默认配置/环境变量）。
func WithAddress(addr string) Option {
	return func(r *Registry) { r.address = addr }
}

// WithScheme 设置 consul 连接协议（http/https）。
func WithScheme(scheme string) Option {
	return func(r *Registry) { r.scheme = scheme }
}

// WithToken 设置 consul ACL token。
func WithToken(token string) Option {
	return func(r *Registry) { r.token = token }
}
