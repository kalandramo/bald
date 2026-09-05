// Package config 提供 bald 的配置装载器（merge-store 内核，viper 已退役）。
//
// 定位（2026-09-05 收编自独立 module bald/config）：配置装载的事实方向是
// bootstrap 根包的 Registry（契约 Config 段 → bconfig 源装配）；本包是
// appkit 现行依赖的统一装载内核，基于「命名层（Layer）+ map 深合并」实现，
// 类型规范化交由 bconf.UnmarshalMap。
//
// 归一模型（2026-09-05）：Store 只有一种动态源机制——命名层。远程桥、
// 契约源（Registry 装配）、本地文件都是 layerM 中的一层；各层 reader 实现
// bconfig.ValueWatcher 即参与热更新（变更 → decode → 层缓存 → 全量重合并）。
//
// 加载优先级（高 → 低）：
//
//	flag > 环境变量 > 本地文件 > 契约源层（列表首最高）> 远程桥（基准）
package config

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/kalandramo/bald/bconf"
	"github.com/kalandramo/bald/bconfig"
	"github.com/kalandramo/bald/bconfig/file"
	"github.com/spf13/pflag"
	"google.golang.org/protobuf/proto"
)

// Layer 一个命名的配置源层：由 Registry/调用方装配的 bconfig 源。
//
// Reader 语义为整文档源：Load(ctx, "") 返回整份配置文档字节；
// 若 Reader 同时实现 bconfig.ValueWatcher 且 Watch 为 true，则该层参与热更新。
type Layer struct {
	// Name 层名（日志/错误信息标识，如 "nacos" / "kubernetes" / "file"）。
	Name string
	// Reader 整文档源（bconfig.Reader，key 固定传空串）。
	Reader bconfig.Reader
	// Format 文档格式（yaml/yml/json）。为空时：若 Reader 实现内部
	// formatAware 接口则动态取（远程桥适配器）；否则回退按 yaml 解析
	//（yaml.v3 是 JSON 超集，JSON 文档天然兼容）。
	Format string
	// Watch 是否订阅该层热更新（Reader 须实现 bconfig.ValueWatcher 才生效）。
	// Registry 装配的契约源默认 true；本地文件层由 Options.WatchLocalFile 控制。
	Watch bool
}

// formatAware 允许源动态声明文档格式（远程桥适配器每次 Read/Watch 返回格式）。
type formatAware interface{ currentFormat() string }

// Options 配置加载参数。
type Options struct {
	// Name 应用配置名，用于按规则查找本地文件（如 config.yaml / config-prod.yaml）
	// 与环境变量前缀（NAME_）。
	Name string
	// ConfigFile 显式指定的本地配置文件路径；为空时按 Name + Env 规则查找。
	ConfigFile string
	// Env 运行环境（dev/test/prod...）。非空时：
	//   - 本地：尝试 Name-Env.yaml/json 作为默认文件（多环境路线 1）
	//   - 远程：path 已由后端构造时拼接（如 /config/demo/prod.yaml），Load 不感知
	Env string
	// Flags 业务命令行参数集合（最高优先级）；仅「用户显式传入」的 flag 参与合并。
	Flags *pflag.FlagSet
	// Layers 命名配置源层（契约 Config 段 → bconfig 源，Registry.Build 产出）。
	// 列表首元素优先级最高（对齐 Registry 注册序），合并时从列表尾向头叠加；
	// 整体位于本地文件/env/flag 之下、远程桥之上。
	Layers []Layer
	// Remote 远程配置源（kratos 桥便捷入口）；为空表示不启用。
	// 内部被适配为最低优先级的基准层；契约源层由 Layers 声明（推荐）。
	Remote RemoteSource
	// WatchLocalFile 是否监听本地配置文件变更（fsnotify，经 bconfig/file 源）。
	WatchLocalFile bool
	// OnChange 配置变更回调（任一层变更均触发）。形参为合并后的最新配置
	// 快照（各层重新合并后的整体树），业务在其中重新 Unmarshal 完成热重载。
	OnChange func(map[string]any)
}

// layerState 一个层的运行期状态：声明 + 当前文档缓存。
// m 一律在 Store.mu 写锁内访问。
type layerState struct {
	spec Layer
	m    map[string]any
}

// Store 配置仓库：持有「动态源层 + env + flag」合并后的配置树，
// 并发安全，支持热更新（任一层 watch 触发全量重合并）。
//
// 快照语义：Settings() 返回深拷贝——已发布的快照不受后续热更新影响；
// 热更新重建 merged 树（copy-on-write 深合并），旧引用天然稳定。
type Store struct {
	mu     sync.RWMutex
	merged map[string]any // 当前合并树（低→高：层序→env→flag）
	layerM []layerState   // 动态源层，索引 0 优先级最低（远程桥 → 契约层逆序 → 本地文件）
	envM   map[string]any // env 层缓存
	flagM  map[string]any // flag 层缓存（flag 层静态：进程运行期不变）

	onChange    func(map[string]any)
	watchCancel context.CancelFunc // 取消所有层 watcher（Close 调用）
	watchWG     sync.WaitGroup     // 转发 goroutine 生命周期
	selfClosers []io.Closer        // Store 自建源（本地文件 watch 源），Close 释放
}

// Load 加载配置：远程桥 + 契约源层 + 本地文件 + env + flag 合并，并可选地监听热更新。
//
// 优先级（高 → 低，符合 K8s/容器部署运维预期）：
//
//	flag > 环境变量 > 本地文件 > 契约源层（Layers 列表首最高）> 远程桥（基准）。
//
// 设计要点：
//   - flag 仅取「用户显式传入」的（Changed==true），未传的 flag 不参与合并，
//     避免零值 flag 压过 env/文件的反直觉行为；
//   - env 为枚举驱动（NAME_ 前缀 → 点路径，下划线/连字符切段）；
//   - 任一层 watch 触发时全量重合并（各层缓存仍在），天然保证「上层覆盖
//     不污染底层基准、底层更新不冲掉上层覆盖」——不存在旧 viper 方案的
//     底层重置问题；
//   - 层初始装配失败即整体报错（fail-fast）；热更新期间解码失败保留旧层
//     缓存，仍触发重合并与 OnChange（编辑半写状态可观测）。
func Load(opts Options) (*Store, error) {
	if opts.Name == "" {
		return nil, fmt.Errorf("config: Name is required")
	}

	s := &Store{onChange: opts.OnChange}

	// 1) 远程桥（kratos 桥便捷入口）→ 最低优先级基准层。
	if opts.Remote != nil {
		ls, err := assembleLayer(context.Background(), Layer{
			Name:   "remote",
			Reader: newRemoteLayer(opts.Remote),
			Watch:  true,
		})
		if err != nil {
			return nil, err
		}
		s.layerM = append(s.layerM, *ls)
	}

	// 2) 契约源层：Layers[0] 优先级最高 → 逆序 append（layerM 索引 0 最低）。
	for i := len(opts.Layers) - 1; i >= 0; i-- {
		l := opts.Layers[i]
		if l.Reader == nil {
			return nil, fmt.Errorf("config: layer %q: Reader is required", l.Name)
		}
		if l.Name == "" {
			l.Name = fmt.Sprintf("layer-%d", i)
		}
		if l.Watch {
			if _, ok := l.Reader.(bconfig.ValueWatcher); !ok {
				return nil, fmt.Errorf("config: layer %q: Watch=true but Reader does not implement bconfig.ValueWatcher", l.Name)
			}
		}
		ls, err := assembleLayer(context.Background(), l)
		if err != nil {
			return nil, err
		}
		s.layerM = append(s.layerM, *ls)
	}

	// 3) 本地文件层（运维显式覆盖，高于全部声明式源）。
	localPath := discoverLocalFile(opts)
	if localPath != "" {
		src, err := file.New(file.WithPath(localPath))
		if err != nil {
			return nil, fmt.Errorf("config: open local %s: %w", localPath, err)
		}
		ls, err := assembleLayer(context.Background(), Layer{
			Name:   "local:" + localPath,
			Reader: src,
			Format: FormatOf(localPath),
			Watch:  opts.WatchLocalFile,
		})
		if err != nil {
			_ = src.Close()
			return nil, err
		}
		s.layerM = append(s.layerM, *ls)
		s.selfClosers = append(s.selfClosers, src)
	}

	// 4) env 与 flag 层（进程运行期静态）。
	s.envM = environMap(opts.Name)
	s.flagM = flattenFlags(opts.Flags)

	s.merged = s.remerge()

	// 5) 热更新监听。
	if err := s.setupWatch(opts); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// assembleLayer 初始装配一层：Load 整文档 → 按格式解码 → 层缓存。
// 文档为空（源返回空字节）视为该层无配置（合法），返回空缓存。
func assembleLayer(ctx context.Context, l Layer) (*layerState, error) {
	data, err := l.Reader.Load(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("config: load layer %q: %w", l.Name, err)
	}
	format := l.Format
	if format == "" {
		if fa, ok := l.Reader.(formatAware); ok {
			format = fa.currentFormat()
		}
	}
	if format == "" {
		// yaml.v3 是 JSON 超集：未声明格式的文档按 yaml 解析，JSON 天然兼容。
		format = "yaml"
	}
	m, err := decodeDocument(data, format)
	if err != nil {
		return nil, fmt.Errorf("config: decode layer %q: %w", l.Name, err)
	}
	return &layerState{spec: l, m: m}, nil
}

// layerFormat 返回层的生效格式（formatAware 源动态取，否则用声明值）。
func layerFormat(l *layerState) string {
	if l.spec.Format != "" {
		return l.spec.Format
	}
	if fa, ok := l.spec.Reader.(formatAware); ok {
		if f := fa.currentFormat(); f != "" {
			return f
		}
	}
	return "yaml"
}

// remerge 按优先级重合并（低 → 高：层序叠加 → env → flag）。
// 调用方须已持有写锁。
func (s *Store) remerge() map[string]any {
	var m map[string]any
	for i := range s.layerM {
		m = deepMerge(m, s.layerM[i].m)
	}
	m = deepMerge(m, s.envM)
	m = deepMerge(m, s.flagM)
	return m
}

// setupWatch 配置热更新监听：对每个 Watch 且实现 ValueWatcher 的层启动
// 转发 goroutine（变更 → decode → 更新层缓存 → 全量重合并 + OnChange）。
// watch 生命周期归 Store：Close 时 cancel ctx 使各源关闭通道、goroutine 退出。
func (s *Store) setupWatch(opts Options) error {
	if s.onChange == nil {
		// 无回调时 watch 无意义（没人消费变更），直接跳过，避免空转 goroutine。
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.watchCancel = cancel

	for i := range s.layerM {
		ls := &s.layerM[i]
		if !ls.spec.Watch {
			continue
		}
		vw, ok := ls.spec.Reader.(bconfig.ValueWatcher)
		if !ok {
			continue // 静态层
		}
		ch, err := vw.WatchValue(ctx, "")
		if err != nil {
			cancel()
			s.watchWG.Wait()
			s.watchCancel = nil
			return fmt.Errorf("config: watch layer %q: %w", ls.spec.Name, err)
		}
		s.watchWG.Add(1)
		go func() {
			defer s.watchWG.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case data, ok := <-ch:
					if !ok {
						return // 源关闭通道（监听器致命错误）
					}
					m, derr := decodeDocument(data, layerFormat(ls))
					s.mu.Lock()
					if derr == nil {
						ls.m = m // 解析成功：更新该层缓存（其余层覆盖保留——重合并叠加）
					}
					// 解析失败（如编辑中途半写状态）：保留旧层缓存，仍通知以便观测。
					s.merged = s.remerge()
					snapshot := deepCopyMap(s.merged)
					s.mu.Unlock()
					s.onChange(snapshot)
				}
			}
		}()
	}
	return nil
}

// Close 停止所有层的热更新监听并释放 Store 自建的资源（本地文件 watch 源）。
//
// 生命周期约定：Store 只负责自己的转发 goroutine 与自建源；Registry 装配的
// 契约源 reader 资源由 Registry.Build 返回的 cleanup 释放（构造方职责），
// Store 不重复关闭。
func (s *Store) Close() error {
	s.mu.Lock()
	cancel := s.watchCancel
	s.watchCancel = nil
	closers := s.selfClosers
	s.selfClosers = nil
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	s.watchWG.Wait()

	var firstErr error
	for _, c := range closers {
		if err := c.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Settings 返回合并后的配置快照（深拷贝，只读使用）。
// 供 bconf.UnmarshalMap / baldconfig.Unmarshal 填充契约。
func (s *Store) Settings() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return deepCopyMap(s.merged)
}

// Get 读取点路径的原始值（"server.http.addr" → server.http.addr 节点的值）。
// 路径不存在时返回 (nil, false)。
func (s *Store) Get(key string) (any, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return getAtPath(s.merged, key)
}

// GetString 读取点路径的字符串值。与旧 viper cast.ToString 语义对齐：
// bool → "true"/"false"，整型 → 十进制，nil/缺失 → ""。
func (s *Store) GetString(key string) string {
	v, ok := s.Get(key)
	if !ok {
		return ""
	}
	return toString(v)
}

// GetStringSlice 读取点路径的字符串列表：支持 []string、[]any（yaml 列表）
// 与逗号分隔的字符串（"log,store,stream"）；缺失/空值返回 nil。
func (s *Store) GetStringSlice(key string) []string {
	v, ok := s.Get(key)
	if !ok {
		return nil
	}
	return toStringSlice(v)
}

// Unmarshal 把当前配置快照反序列化为 Protobuf 消息（bconf.UnmarshalMap 桥接）。
func (s *Store) Unmarshal(msg proto.Message) error {
	return Unmarshal(s.Settings(), msg)
}

// discoverLocalFile 按规则查找本地配置文件。
// 查找顺序：显式 ConfigFile > Name-Env.yaml/json > Name.yaml/json；
// 搜索目录：. / ./configs / $HOME/.config/Name（对齐旧 viper AddConfigPath 语义）。
// 找不到返回空串（不报错，onexstack 行为：允许纯远程/纯 flag 配置）。
func discoverLocalFile(opts Options) string {
	if opts.ConfigFile != "" {
		return opts.ConfigFile
	}
	home, _ := os.UserHomeDir()
	dirs := []string{".", "./configs"}
	if home != "" {
		dirs = append(dirs, filepath.Join(home, ".config", opts.Name))
	}
	name := opts.Name
	if opts.Env != "" {
		name = opts.Name + "-" + opts.Env
	}
	for _, ext := range []string{"yaml", "yml", "json"} {
		for _, dir := range dirs {
			p := filepath.Join(dir, name+"."+ext)
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				return p
			}
		}
	}
	return ""
}

// getAtPath 按点路径查找嵌套 map 中的值。
func getAtPath(m map[string]any, key string) (any, bool) {
	if m == nil || key == "" {
		return nil, false
	}
	parts := strings.Split(key, ".")
	cur := m
	for i, p := range parts {
		v, ok := cur[p]
		if !ok {
			return nil, false
		}
		if i == len(parts)-1 {
			return v, true
		}
		cur, ok = v.(map[string]any)
		if !ok {
			return nil, false
		}
	}
	return nil, false
}

// toString 标量转字符串（对齐旧 viper cast.ToString 的常见分支）。
func toString(v any) string {
	switch val := v.(type) {
	case nil:
		return ""
	case string:
		return val
	case bool:
		if val {
			return "true"
		}
		return "false"
	case int:
		return fmt.Sprint(val)
	case int64:
		return fmt.Sprint(val)
	case float64:
		return fmt.Sprint(val)
	default:
		return fmt.Sprint(val)
	}
}

// toStringSlice 值转字符串列表：[]string / []any / 逗号分隔字符串。
func toStringSlice(v any) []string {
	switch val := v.(type) {
	case nil:
		return nil
	case []string:
		out := make([]string, 0, len(val))
		for _, s := range val {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		if strings.TrimSpace(val) == "" {
			return nil
		}
		parts := strings.Split(val, ",")
		out := make([]string, 0, len(parts))
		for _, s := range parts {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(val))
		for _, item := range val {
			if s := toString(item); strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	default:
		return nil
	}
}

// Unmarshal 把配置快照（合并后的 map）反序列化为 Protobuf 消息。
//
// 类型规范化与「合并而非替换」的核心实现已下沉至 bconf.UnmarshalMap
// （契约层工具，加载器无关）；本函数只是 map 树模型与契约层之间的薄桥。
//
// 语义细节（合并式、repeated 替换、proto3 标量无 presence 的注意点）
// 见 bconf.UnmarshalMap 的文档注释。
func Unmarshal(m map[string]any, msg proto.Message) error {
	if msg == nil {
		return fmt.Errorf("config: proto message is nil")
	}
	if m == nil {
		m = map[string]any{}
	}
	return bconf.UnmarshalMap(m, msg)
}
