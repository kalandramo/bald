package bootstrap

import (
	"sort"
	"strings"
	"testing"

	"github.com/kalandramo/bald/bconf"
	"github.com/kalandramo/bald/log/bslog"
	"github.com/spf13/pflag"
)

// plainBinders 是框架装配面（pkg/appkit 的 Bind 调用）注册的「手写 flag 注册器」清单，
// 键＝来源包目录。闸门只从这些注册器**当场派生** flag 名，不抄名字。
//
// 已知余量（§7.39 登记）：这张清单本身是手抄的——它等于 appkit 里 PlainBinder 分支的
// 实际调用点。当前全仓只有一个 AddFlags 实现（log/bslog/options.go:55），新增第二个
// 注册器时必须同时进这张表，否则它注册的 flag 名不会被本闸门看到。
var plainBinders = map[string]func(fs *pflag.FlagSet){
	"log/bslog": func(fs *pflag.FlagSet) { bslog.NewOptions().AddFlags(fs) },
}

// offContractFlagRoutes 是「落不进契约的 flag 路由」登记表：键＝契约校验报出的未知键原文，
// 值＝为什么它不能进契约 + 现在靠什么生效。
//
// 条目什么时候算陈旧（闸门据此判红）：
//   - 派生不出这个键了（注册器改名/删掉该 flag）⇒ 条目陈旧；
//   - 派生出新的契约外键却没登记 ⇒ 缺登记；
//   - 理由为空 ⇒ 判红（本表不允许「先记个键名占位」）。
//
// 注意：修复不会让条目消失——`.log` 之所以契约外，是因为 flag 名由 bslog 的注册器给出，
// 与契约形状无关。修好的是「它有没有生效路径」，写进理由里的是那条路径。
var offContractFlagRoutes = map[string]string{
	".log": "§7.39 已修（overlay 路由）：契约里日志旋钮的形状是 repeated " +
		"logger.backends[i].slog ⇒ 没有任何标量 flag 名能表达该路径，因此 --log.* 永远进不了契约。" +
		"现在的生效路径：loadConfig 按 LogFlagNames 把这些键摘出合并树（不进严格契约校验），" +
		"再由 ApplyLogFlagOverrides 在 bconf.Validate 之后、计划编译之前按 Changed 写进契约 logger 段" +
		"（冷启动 syncBootstrap 与热更 hotReload 两处）。残余限制：proto3 标量无 presence ⇒ " +
		"--log.rotate.enabled=false / --log.rotate.compress=false 压不过契约里已置真的值；" +
		"真要能置假需契约改成 optional bool（bconf 重出 + 发布裁决）。",
}

// TestHandwrittenFlagRoutesLandInContract 是 §7.39 的闸门：契约驱动的配置取值链里，
// flag 层的每一条路由都必须能在契约树里找到自己的落点。派生方式＝真调注册器
// （AddFlags）拿到 flag 名，再按点分路径拼成合并树喂给契约校验本体（bconf.FindUnknownKeys），
// 与运行期启动路径同一个谓词，不另写一份判定。
func TestHandwrittenFlagRoutesLandInContract(t *testing.T) {
	names := map[string]bool{}
	for dir, register := range plainBinders {
		fs := pflag.NewFlagSet(dir, pflag.ContinueOnError)
		register(fs)
		fs.VisitAll(func(f *pflag.Flag) { names[f.Name] = true })
		if len(names) == 0 {
			t.Fatalf("%s 的注册器一个 flag 都没派生到 ⇒ 本闸门在空转", dir)
		}
	}
	// 防空转之二：谓词本身必须判得出「契约外的键」——用一个人造的假键自证。
	if unknown := bconf.FindUnknownKeys(map[string]any{"definitely_not_a_contract_key": map[string]any{}},
		bconf.NewBootstrap()); len(unknown) == 0 {
		t.Fatal("FindUnknownKeys 对人造假键判绿 ⇒ 谓词失效，本闸门在空转")
	}

	tree := map[string]any{}
	for name := range names {
		node := tree
		parts := strings.Split(name, ".")
		for _, p := range parts[:len(parts)-1] {
			child, ok := node[p].(map[string]any)
			if !ok {
				child = map[string]any{}
				node[p] = child
			}
			node = child
		}
		node[parts[len(parts)-1]] = "sentinel"
	}

	unknown := bconf.FindUnknownKeys(tree, bconf.NewBootstrap())
	have := map[string]bool{}
	for _, k := range unknown {
		have[k] = true
	}
	var missing, stale []string
	for _, k := range unknown {
		if _, ok := offContractFlagRoutes[k]; !ok {
			missing = append(missing, k)
		}
	}
	for k := range offContractFlagRoutes {
		if !have[k] {
			stale = append(stale, k)
		} else if strings.TrimSpace(offContractFlagRoutes[k]) == "" {
			t.Errorf("%s 登记为空理由 ⇒ 每条契约外 flag 路由必须写清为什么允许", k)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	if len(missing) > 0 {
		t.Errorf("这些 flag 路由不在契约里且未登记：%v\n"+
			"要么把 flag 名改成契约路径，要么进 offContractFlagRoutes 写清理由与状态", missing)
	}
	if len(stale) > 0 {
		t.Errorf("offContractFlagRoutes 里这些键已经不再被派生：%v（说明 flag 面已收窄或已修，删掉条目）", stale)
	}
	if len(missing) == 0 {
		t.Logf("派生 flag %d 个，其中契约外路由 %d 条且逐条有理由：%v", len(names), len(unknown), unknown)
	}
}
