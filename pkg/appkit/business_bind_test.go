package appkit

import (
	"testing"
	"time"

	"github.com/spf13/pflag"

	bconf "github.com/kalandramo/bald/bconf"
	log "github.com/kalandramo/bald/log"
)

// W2：FromBootstrap 必须提供业务配置对象的 flag 绑定通道。
//
// 缺口：FromBootstrap 只硬编码 Bind("server.http"/"server.grpc"/log)，没有业务
// 绑定入口——业务 flag（如 --login.rate_limit.rate）无法进入装载 FlagSet，
// 「flag > env > 文件」优先级链对业务配置项整条失效（静默忽略用户显式传参）。
//
// 断言：经 WithBind 注册的配置对象，其 flag 能覆盖配置文件中的同名键。
func TestFromBootstrap_BusinessBindFlagOverridesFile(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfgFile := writeCfg(t, "login:\n  rate_limit:\n    rate: 1\n")
	withArgs(t, []string{"--config=" + cfgFile, "--login.rate_limit.rate=9"})

	opts := &testBizOptions{}
	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)

	a, err := FromBootstrap(cfg, WithBind("", opts))
	if err != nil {
		t.Fatalf("FromBootstrap: %v", err)
	}
	if err := a.loadConfig(); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	if got := a.Config().GetString("login.rate_limit.rate"); got != "9" {
		t.Fatalf("login.rate_limit.rate = %q, want 9 (业务 flag 应压过文件)", got)
	}
}

// 反向：未传 flag 时文件值仍生效（WithBind 不得破坏既有优先级）。
func TestFromBootstrap_BusinessBindFileStillApplies(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfgFile := writeCfg(t, "login:\n  rate_limit:\n    rate: 3\n")
	withArgs(t, []string{"--config=" + cfgFile})

	opts := &testBizOptions{}
	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)

	a, err := FromBootstrap(cfg, WithBind("", opts))
	if err != nil {
		t.Fatalf("FromBootstrap: %v", err)
	}
	if err := a.loadConfig(); err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if got := a.Config().GetString("login.rate_limit.rate"); got != "3" {
		t.Fatalf("login.rate_limit.rate = %q, want 3 (来自文件)", got)
	}
}

// testBizOptions 模拟业务配置对象（PlainBinder：键前缀内置）。
type testBizOptions struct {
	Rate float64
}

func (o *testBizOptions) AddFlags(fs *pflag.FlagSet) {
	fs.Float64Var(&o.Rate, "login.rate_limit.rate", o.Rate, "login rate limit")
}

var _ = time.Second
