// Package config 提供 bald 的配置加载层。
//
// 设计来源与核心思想见 docs/config-center-design.md：
//   - 本地文件 + 环境变量 + 命令行 flag（面向 K8s/容器部署，基于 viper）
//   - 远程配置中心（基于自研 RemoteSource 抽象，绕开 viper 标准 remote 的缺陷）
//
// 加载优先级（高 → 低，viper 默认语义）：
//   flag > 环境变量 > 本地文件 > 远程配置（远程作为基准）
package config

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// Options 配置加载参数。
type Options struct {
	// Name 应用配置名，用于按规则查找本地文件（如 config.yaml / config-prod.yaml）。
	Name string
	// ConfigFile 显式指定的本地配置文件路径；为空时按 Name + Env 规则查找。
	ConfigFile string
	// Env 运行环境（dev/test/prod...）。非空时：
	//   - 本地：尝试 Name-Env.yaml/json 作为默认文件（多环境路线 1）
	//   - 远程：path 已由后端构造时拼接（如 /config/demo/prod.yaml），Load 不感知
	Env string
	// Flags 业务命令行参数集合，用于绑定到 viper（最高优先级）。
	Flags *pflag.FlagSet
	// Remote 远程配置源；为空表示不启用远程配置。
	Remote RemoteSource
	// WatchLocalFile 是否监听本地配置文件变更（fsnotify）。
	WatchLocalFile bool
	// OnChange 配置变更回调（本地或远程变更均触发）。形参为最新 viper 实例，
	// 业务在其中重新 Unmarshal 完成热重载（裸 viper，粒度由业务自定）。
	OnChange func(v *viper.Viper)
}

// Load 加载配置：远程 + 本地 + env + flag，并可选地监听热更新。
//
// 优先级（高 → 低，viper 默认语义，符合 K8s/容器部署运维预期）：
//   flag > 环境变量 > 本地文件 > 远程（远程作为最底层基准）。
//
// 设计要点：
//   - viper 有「override 层」（flag/env 等显式覆盖）与「底层 config map」两层。
//     flag（BindPFlags）与 env（AutomaticEnv，NAME_ 前缀）位于 override 层，
//     天然压过底层 config；其中 flag 优先级高于 env（viper 默认）。
//   - 远程与本地都落在 viper 的「底层 config」：远程先 ReadConfig（基准），
//     本地后 MergeConfigMap（后写赢），同名 key 本地覆盖远程。
//     因此底层内部顺序为「本地 > 远程」，再叠加更上层的 env/flag，
//     最终优先级为：flag > env > 本地 > 远程。
//   - 远程 watch 时 Reset 底层重新拉远程，再叠加缓存的本地 map，本地不被污染；
//     本地 watch 时同样 Reset 底层、重注入远程、再合并本地 map，远程基准也不丢。
func Load(opts Options) (*viper.Viper, error) {
	if opts.Name == "" {
		return nil, fmt.Errorf("config: Name is required")
	}

	v := viper.New()
	bindViperBasics(v, opts)
	if opts.ConfigFile != "" {
		v.SetConfigFile(opts.ConfigFile) // 仅设路径，供 ConfigFileUsed/WatchConfig 使用
	}

	// vMu 保护 viper 底层 config 的并发读写（viper 本身非并发安全，
	// 本地 watch 的 OnConfigChange 与远程 watch 回调可能并发触发）。
	vMu := &sync.RWMutex{}

	ctx := context.Background()

	// 1) 远程基准：写入底层 config（低优先级），并缓存供 watch 复用。
	var remoteData []byte
	var remoteFormat string
	if opts.Remote != nil {
		data, format, err := opts.Remote.Read(ctx)
		if err != nil {
			return nil, fmt.Errorf("config: read remote: %w", err)
		}
		remoteData, remoteFormat = data, format
		vMu.Lock()
		if err := injectRemote(v, data, format); err != nil {
			vMu.Unlock()
			return nil, fmt.Errorf("config: inject remote: %w", err)
		}
		vMu.Unlock()
	}

	// 2) 本地文件覆盖远程（合并进底层，后写赢；低于 flag/env 层）。
	localMap, err := parseLocal(opts)
	if err != nil {
		return nil, err
	}
	if localMap != nil {
		vMu.Lock()
		if err := v.MergeConfigMap(localMap); err != nil {
			vMu.Unlock()
			return nil, fmt.Errorf("config: merge local: %w", err)
		}
		vMu.Unlock()
	}

	// 3) 热更新监听。
	if err := setupWatch(v, vMu, opts, remoteData, remoteFormat, localMap); err != nil {
		return nil, err
	}

	return v, nil
}

// bindViperBasics 绑定命令行 flag 与环境变量（NAME_ 前缀）。
func bindViperBasics(v *viper.Viper, opts Options) {
	if opts.Flags != nil {
		v.BindPFlags(opts.Flags)
	}
	v.SetEnvPrefix(strings.ToUpper(opts.Name))
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))
	v.AutomaticEnv()
}

// readLocalFile 把本地配置文件解析进传入 viper 的底层 config。
// 查找顺序：显式 ConfigFile > Name-Env.yaml/json > Name.yaml/json。
// 注意：本函数仅负责"读文件"，优先级由调用方决定（Load 中由 parseLocal 解析后
// 合并进主 viper 底层，因后写赢而覆盖远程基准，且低于 flag/env 层）。
func readLocalFile(v *viper.Viper, opts Options) error {
	switch {
	case opts.ConfigFile != "":
		v.SetConfigFile(opts.ConfigFile)
	case opts.Env != "":
		v.SetConfigName(opts.Name + "-" + opts.Env)
		v.AddConfigPath(".")
		v.AddConfigPath("./configs")
		v.AddConfigPath("$HOME/.config/" + opts.Name)
	default:
		v.SetConfigName(opts.Name)
		v.AddConfigPath(".")
		v.AddConfigPath("./configs")
		v.AddConfigPath("$HOME/.config/" + opts.Name)
	}

	if err := v.MergeInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			// 本地文件缺失不报错（onexstack 行为）：允许纯远程/纯 flag 配置。
			return nil
		}
		return fmt.Errorf("config: merge local %q: %w", v.ConfigFileUsed(), err)
	}
	return nil
}

// setupWatch 配置热更新监听。localMap 为已解析的本地配置（可能为 nil），
// remoteData/remoteFormat 为已拉取的远程基准（无远程时为空），用于 watch 时
// 重新叠加，保证本地覆盖与远程基准互不污染。vMu 保护 viper 并发读写。
func setupWatch(v *viper.Viper, vMu *sync.RWMutex, opts Options, remoteData []byte, remoteFormat string, localMap map[string]any) error {
	if !opts.WatchLocalFile && opts.Remote == nil {
		return nil
	}

	if opts.OnChange != nil {
		v.OnConfigChange(func(e fsnotify.Event) {
			_ = e
			// 本地文件变更：先重置底层，再依次重注入远程基准 + 合并本地 map。
			// 必须整体重建底层（而非仅 MergeConfigMap），否则：① 磁盘已删除的
			// key 会在 viper 中残留旧值；② 远程基准会在 ReadConfig 时被清掉。
			if _, statErr := os.Stat(v.ConfigFileUsed()); statErr == nil {
				m, perr := parseLocal(opts)
				if perr != nil {
					opts.OnChange(v)
					return
				}
				vMu.Lock()
				if remoteData != nil {
					// 重置底层为远程基准，再叠加本地覆盖。
					if err := injectRemote(v, remoteData, remoteFormat); err != nil {
						vMu.Unlock()
						opts.OnChange(v)
						return
					}
				} else {
					// 无远程：用空文档清空底层（viper v1 无 Reset 方法），
					// 仅保留即将合并的本地 map。
					v.SetConfigType("yaml")
					_ = v.ReadConfig(bytes.NewReader([]byte("{}")))
				}
				if m != nil {
					_ = v.MergeConfigMap(m)
				}
				vMu.Unlock()
			}
			opts.OnChange(v)
		})
	}

	if opts.WatchLocalFile {
		// 仅当本地文件存在时才启用 fsnotify 监听，否则会报错。
		if _, err := os.Stat(v.ConfigFileUsed()); err == nil {
			v.WatchConfig()
		}
	}

	if opts.Remote != nil && opts.OnChange != nil {
		onChange := opts.OnChange
		if err := opts.Remote.Watch(context.Background(), func(data []byte, format string) {
			// 重新注入远程基准（Reset 底层 config），再叠加本地 map。
			// 本地落底层后写赢，因此远程更新不会污染本地覆盖。
			// 加锁保护 viper 底层读写，避免与 OnConfigChange 回调并发竞争。
			vMu.Lock()
			if err := injectRemote(v, data, format); err != nil {
				vMu.Unlock()
				// 远程注入失败：保留旧底层，仍通知业务以便观测（与本地 watch 行为一致）。
				onChange(v)
				return
			}
			if localMap != nil {
				_ = v.MergeConfigMap(localMap) // 重新叠加本地覆盖
			}
			vMu.Unlock()
			onChange(v)
		}); err != nil {
			return fmt.Errorf("config: watch remote: %w", err)
		}
	}
	return nil
}

// parseLocal 把本地文件解析为 map（供合并进底层 config）。
// 无本地文件（纯远程/纯 flag）时返回 nil, nil。
func parseLocal(opts Options) (map[string]any, error) {
	if opts.ConfigFile == "" && opts.Env == "" {
		return nil, nil // 无本地文件可叠加
	}
	tmp := viper.New()
	bindViperBasics(tmp, opts)
	if err := readLocalFile(tmp, opts); err != nil {
		return nil, err
	}
	settings := tmp.AllSettings()
	if len(settings) == 0 {
		return nil, nil
	}
	return settings, nil
}

// Marshal 将当前 viper 配置序列化为指定格式（用于调试/落盘）。
func Marshal(v *viper.Viper, format string) ([]byte, error) {
	buf := &bytes.Buffer{}
	enc := newEncoder(format, buf)
	if enc == nil {
		return nil, fmt.Errorf("config: unsupported format %q", format)
	}
	if err := enc.Encode(v.AllSettings()); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
