// Package config 提供 bald 的配置加载层。
//
// 设计来源与核心思想见 docs/config-center-design.md：
//   - 本地文件 + 环境变量 + 命令行 flag（onexstack 风格，基于 viper）
//   - 远程配置中心（基于自研 RemoteSource 抽象，绕开 viper 标准 remote 的缺陷）
//
// 加载优先级（高 → 低）：
//   flag > 本地文件 > 环境变量 > 远程配置（远程作为基准，本地覆盖远程）
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

// Load 加载配置：本地 + 远程 + env + flag，并可选地监听热更新。
//
// 优先级（高 → 低）：flag > 本地文件 > 环境变量 > 远程（底层基准）。
// 设计要点（符合决策①「远程基准 + 本地覆盖」）：
//   - 远程与本地都落在 viper 的"底层 config"（低于 flag/env 层），但加载顺序上
//     远程先 ReadConfig、本地后 MergeConfigMap，同名 key 本地赢，从而本地覆盖远程。
//   - flag（BindPFlags）与 env（AutomaticEnv）位于更上层，天然压过底层，
//     优先级正确：flag > 本地 > env > 远程。
//   - 远程 watch 时只 Reset 底层并重新拉远程，再叠加缓存的本地 map，因此本地不会被污染；
//     本地 watch 时重新解析本地文件并 MergeConfigMap，远程基准保持不变。
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

	// 1) 远程基准：写入底层 config（低优先级）。
	if opts.Remote != nil {
		data, format, err := opts.Remote.Read(ctx)
		if err != nil {
			return nil, fmt.Errorf("config: read remote: %w", err)
		}
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
	if err := setupWatch(v, vMu, opts, localMap); err != nil {
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
// 用于远程 watch 重新叠加，保证本地覆盖不被污染。vMu 保护 viper 并发读写。
func setupWatch(v *viper.Viper, vMu *sync.RWMutex, opts Options, localMap map[string]any) error {
	if !opts.WatchLocalFile && opts.Remote == nil {
		return nil
	}

	if opts.OnChange != nil {
		v.OnConfigChange(func(e fsnotify.Event) {
			_ = e
			// 本地文件变更：重新解析并整体刷新底层（而非 MergeConfigMap 累积合并），
			// 避免磁盘已删除的 key 在 viper 中残留旧值。
			if _, statErr := os.Stat(v.ConfigFileUsed()); statErr == nil {
				if m, perr := parseLocal(opts); perr == nil && m != nil {
					vMu.Lock()
					_ = v.ReadConfig(bytes.NewReader(mustMarshalYAML(m)))
					vMu.Unlock()
				}
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

// mustMarshalYAML 将 map 序列化回 yaml 字节，供 ReadConfig 整体刷新底层 config。
// 解析失败直接 panic：仅在热更新回调内部使用，且 map 来自已成功解析的本地文件，
// 不会失败；若失败属编程错误，应尽早暴露。
func mustMarshalYAML(m map[string]any) []byte {
	buf := &bytes.Buffer{}
	if err := newEncoder("yaml", buf).Encode(m); err != nil {
		panic(fmt.Sprintf("config: marshal local map: %v", err))
	}
	return buf.Bytes()
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
