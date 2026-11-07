package appkit

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/health"
)

// 框架默认探针路径（可用 WithProbePaths 覆盖）。
const (
	DefaultHealthPath = "/healthz" // 存活探针（进程在即 200）
	DefaultReadyPath  = "/readyz"  // 就绪探针（依赖检查，Down → 503）
)

// WithProbePaths 覆盖 HTTP 探针路径（默认 /healthz /readyz）；空值忽略。
// 作为 WithHealth 的变参项使用（原 HealthOption 类型已并入唯一 Option 类型）。
//
// 显式表态优先于契约 app.probe_live_path / app.probe_ready_path：空值不算表态，
// 那一位仍由契约填（两个字段的取值互不影响）。
func WithProbePaths(live, ready string) Option {
	return optionFunc(func(s *Decl) {
		if live != "" {
			s.ProbeLivePath = live
			s.probeLiveSet = true
		}
		if ready != "" {
			s.ProbeReadyPath = ready
			s.probeReadySet = true
		}
	})
}

// WithHealthPollInterval 覆盖 gRPC 侧就绪状态同步间隔（默认 2s）。
// 显式表态优先于契约 app.health_poll_interval；非正值忽略（那一位仍由契约填）。
func WithHealthPollInterval(d time.Duration) Option {
	return optionFunc(func(s *Decl) {
		if d > 0 {
			s.HealthInterval = d
			s.healthIntervalSet = true
		}
	})
}

// WithHealth 装配健康检查：HTTP/gateway 双探针 + gRPC 标准健康服务状态联动。
//
// 探针路由由装配层在业务 handler 外层包一层 mux 得到（协议实现不注册任何
// 框架路由）；gRPC 侧由 bootstrap 后台轮询 h 并把结果 SetServingStatus。
// **不声明则不挂任何探针**——业务也可以完全自行组装：把 health.NewHandler(h)
// 挂进自己的 gin/mux 即可。见《Bald 健康检查装配设计》。
//
// 三个运行期值都可由契约 app 段表达（probe_live_path / probe_ready_path /
// health_poll_interval，见 applyContractHealth）；本 Option 与变参项的显式值
// 优先于契约。健康源 h 本身只能代码注入——契约构造不出对象。
func WithHealth(h *health.Health, opts ...Option) Option {
	return optionFunc(func(s *Decl) {
		s.Health = h
		// 默认值不是业务表态：不置 probeLiveSet/probeReadySet，契约才能改路径。
		s.ProbeLivePath = DefaultHealthPath
		s.ProbeReadyPath = DefaultReadyPath
		for _, o := range opts {
			if o != nil {
				o.Apply(s)
			}
		}
	})
}

// applyContractHealth 把契约 app 段的三个健康字段填进声明里**业务未表态**的位。
// 次序与 transport/sse 的 buildOpts 一致：契约先应用、业务 Option 覆盖。
//
// 写回 Decl 而不是只喂内置 Provider：探针路径有两个消费者——appkit 的
// stdHTTPProvider 与 transport/gateway 的 HTTPProvider（后者直读 Decl 字段、
// 自己不兜默认值），落在 Decl 上两面才同源，gateway 无需跟着改。
//
// 纯映射不校验：校验集中在 validateCapabilities，assemble 与 Compile 共用一条，
// 故 `--plan` 预检与真实启动对同一批非法值 fail-fast。热更新不重映射——app 段
// 是 ReloadStatic（探针在 server 构造期挂上 mux），改了要重启。
func applyContractHealth(d *Decl, cfg *bootstrapv1.BootstrapConfig) {
	if d.Health == nil {
		return
	}
	app := cfg.GetApp()
	if !d.probeLiveSet {
		if v := app.GetProbeLivePath(); v != "" {
			d.ProbeLivePath = v
		}
	}
	if !d.probeReadySet {
		if v := app.GetProbeReadyPath(); v != "" {
			d.ProbeReadyPath = v
		}
	}
	if !d.healthIntervalSet {
		if v := app.GetHealthPollInterval(); v != nil {
			d.HealthInterval = v.AsDuration()
		}
	}
}

// validateHealthContract 校验契约 app 段的健康字段与代码声明的对偶关系，以及
// 映射后探针路径的可用性。由 validateCapabilities 调用，故 assemble 与 Compile
// 共用——`--plan` 预检与真实启动对同一批非法值 fail-fast。
//
// 三条判据各自防一个静默/崩溃面：
//   - 字段有值却没有 WithHealth：健康源是注入对象，契约构造不出来，不拦就是
//     「写了配置、探针没挂」的静默失效。
//   - 间隔非正：bootstrap 侧 interval <= 0 会回退默认 2s，契约里写 0s 于是
//     「看着生效其实没生效」；显式拒绝而不是悄悄回退。
//   - 路径相同或不以 / 开头：两者都是 http.ServeMux 的非法注册（同路径重复
//     注册 panic、非 / 开头不是合法 pattern），原先只在 server 构造期崩，
//     现在提前到装配期报错。判的是**映射后**的值，两个来源同一条规则。
func validateHealthContract(d *Decl, cfg *bootstrapv1.BootstrapConfig) error {
	app := cfg.GetApp()
	live, ready := app.GetProbeLivePath(), app.GetProbeReadyPath()
	interval := app.GetHealthPollInterval()
	if d.Health == nil {
		if live != "" || ready != "" || interval != nil {
			return errors.New("appkit: app.probe_live_path/probe_ready_path/health_poll_interval are set " +
				"but no health source is declared: the contract cannot construct one; " +
				"declare appkit.WithHealth(health.New(...)) or remove the fields")
		}
		return nil
	}
	if interval != nil && interval.AsDuration() <= 0 {
		return fmt.Errorf("appkit: app.health_poll_interval must be positive, got %s", interval.AsDuration())
	}
	if d.ProbeLivePath == d.ProbeReadyPath {
		return fmt.Errorf("appkit: WithHealth probe paths must differ, got %q twice "+
			"(app.probe_live_path/app.probe_ready_path or appkit.WithProbePaths)", d.ProbeLivePath)
	}
	for _, p := range []struct{ field, value string }{
		{"probe_live_path", d.ProbeLivePath},
		{"probe_ready_path", d.ProbeReadyPath},
	} {
		if !strings.HasPrefix(p.value, "/") {
			return fmt.Errorf("appkit: probe path %q must start with \"/\" (http.ServeMux pattern); "+
				"fix app.%s or appkit.WithProbePaths", p.value, p.field)
		}
	}
	return nil
}

// WithProbes 在业务 handler 外层包一层探针 mux（精确路径给探针，其余交给业务），
// 导出给手工装配（NewApp + Servers）场景复用：CLI 生成的 app、不走 Serve 的
// 装配代码。live/ready 传空串时用默认路径（/healthz、/readyz）。
func WithProbes(business http.Handler, h *health.Health, live, ready string) http.Handler {
	if live == "" {
		live = DefaultHealthPath
	}
	if ready == "" {
		ready = DefaultReadyPath
	}
	return withProbes(business, h, live, ready)
}

// withProbes 在业务 handler 外层包一层探针 mux：精确路径给探针（存活恒 200、
// 就绪按依赖 503），其余路径交给业务。
func withProbes(business http.Handler, h *health.Health, live, ready string) http.Handler {
	mux := http.NewServeMux()
	mux.Handle(live, health.NewLivenessHandler())
	mux.Handle(ready, health.NewHandler(h))
	if business != nil {
		mux.Handle("/", business)
	}
	return mux
}
