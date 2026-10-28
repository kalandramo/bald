// plan_cache.go 实现设计 §4.9 的计划缓存：启动期把当前计划按 (Role,
// InputHash) 记入缓存文件，doctor 读同一文件报告相对基线的节点级差异。
//
// 缓存**不是执行依据**：它只记录「上一次真实启动看到了什么计划」，供运维与
// CI 判断「这次装配相对上次变了什么」。因此读写失败一律降级——写失败记一条
// 告警、读失败在报告里给一行提示，绝不改变装配结果或启动成败。

package appkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/kalandramo/bald/log"
)

const (
	planCacheVersion = 1
	// planCacheMaxEntries 限制缓存条目数（不同 InputHash 的历次基线）；
	// 超出按记录时间丢弃最旧条目，文件不会无界增长。
	planCacheMaxEntries = 16
)

// planCacheFile 是缓存文件格式（entries 内每个 (Role, InputHash) 一条）。
type planCacheFile struct {
	Version int              `json:"version"`
	Entries []planCacheEntry `json:"entries"`
}

// planCacheEntry 是一次启动的计划基线。Plan 存整份计划：节点级对比需要
// DesiredHash 与依赖集合，只存哈希无法回答「变了什么」。
type planCacheEntry struct {
	Role       string `json:"role,omitempty"`
	InputHash  string `json:"inputHash"`
	PlanHash   string `json:"planHash"`
	RecordedAt string `json:"recordedAt"`
	Plan       *Plan  `json:"plan"`
}

// planCacheBaseline 是 doctor 对基线的对比结果（Err 非 nil 表示缓存不可读，
// 此时 Entry 为 nil——报告只给一行提示）。
type planCacheBaseline struct {
	Path         string
	Err          error
	Entry        *planCacheEntry
	InputChanged bool
	Change       PlanChange
}

// WithPlanCache 声明计划缓存文件路径（如 ".bald/plan-cache.json"）。启动期
// 经 Serve 记录当前计划为基线；doctor 读同一路径报告相对基线的新增/移除/
// 期望态变化。空路径（缺省）= 不落盘。缓存只作对比基线，读写失败不影响装配。
func WithPlanCache(path string) Option {
	return optionFunc(func(d *Decl) { d.PlanCache = path })
}

// recordPlanCache 把启动期计划记入缓存（best-effort：失败只告警）。在装配
// 成功后、Run 之前调用——记录的是即将生效的那份计划，而不是试算结果。
func (a *App) recordPlanCache(ctx context.Context, path string) {
	if path == "" {
		return
	}
	a.planMu.RLock()
	p := a.plan
	a.planMu.RUnlock()
	if p == nil {
		return
	}
	if err := writePlanCacheEntry(path, p, time.Now()); err != nil {
		// 缓存不是执行依据：只读工作目录等环境下写失败属预期，降级为 debug。
		log.Debug(ctx, "appkit: plan cache not written", "path", path, "error", err.Error())
	}
}

// writePlanCacheEntry 写入一条基线：同 (Role, InputHash) 覆盖，条目数超上限
// 丢弃最旧记录。写临时文件后 rename，避免半写文件被下次读取当成基线。
func writePlanCacheEntry(path string, p *Plan, now time.Time) error {
	if path == "" || p == nil {
		return nil
	}
	f, err := readPlanCacheFile(path)
	if err != nil {
		// 损坏/缺失的缓存不是错误源：视为无基线，整体重建。
		f = &planCacheFile{Version: planCacheVersion}
	}
	entry := planCacheEntry{
		Role:       p.Role,
		InputHash:  p.InputHash,
		PlanHash:   p.Hash(),
		RecordedAt: now.UTC().Format(time.RFC3339),
		Plan:       p,
	}
	idx := -1
	for i, e := range f.Entries {
		if e.Role == entry.Role && e.InputHash == entry.InputHash {
			idx = i
			break
		}
	}
	if idx >= 0 {
		f.Entries[idx] = entry
	} else {
		f.Entries = append(f.Entries, entry)
	}
	sort.SliceStable(f.Entries, func(i, j int) bool {
		return f.Entries[i].RecordedAt < f.Entries[j].RecordedAt
	})
	if len(f.Entries) > planCacheMaxEntries {
		f.Entries = f.Entries[len(f.Entries)-planCacheMaxEntries:]
	}
	f.Version = planCacheVersion

	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// readPlanCacheFile 读取缓存文件（不存在的错误原样返回，调用方区分处理）。
func readPlanCacheFile(path string) (*planCacheFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	f := &planCacheFile{}
	if err := json.Unmarshal(b, f); err != nil {
		return nil, fmt.Errorf("appkit: plan cache %s: %w", path, err)
	}
	if f.Version != planCacheVersion {
		return nil, fmt.Errorf("appkit: plan cache %s: unsupported version %d (want %d)",
			path, f.Version, planCacheVersion)
	}
	return f, nil
}

// comparePlanCache 只读对比当前计划与基线：优先同 (Role, InputHash)，否则
// 取同 Role 最近一次记录（契约变化时仍能回答「装配变了什么」）。path 为空
// 或缓存不可读时返回带 Err 的基线——doctor 以一行提示呈现，不报错。
func comparePlanCache(path string, p *Plan) *planCacheBaseline {
	if path == "" {
		return nil
	}
	b := &planCacheBaseline{Path: path}
	f, err := readPlanCacheFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			b.Err = err
			return b
		}
		return b
	}
	var fallback *planCacheEntry
	for i := range f.Entries {
		e := &f.Entries[i]
		if e.Role != p.Role {
			continue
		}
		if e.InputHash == p.InputHash {
			b.Entry = e
			break
		}
		if fallback == nil || e.RecordedAt > fallback.RecordedAt {
			fallback = e
		}
	}
	if b.Entry == nil {
		b.Entry = fallback
	}
	if b.Entry == nil {
		return b
	}
	b.InputChanged = b.Entry.InputHash != p.InputHash
	if b.Entry.Plan != nil {
		b.Change = diffPlan(b.Entry.Plan, p)
	}
	return b
}
