package release

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

// PlanVersion 是计划文件的 schema 版本；字段结构变化时递增，旧计划会被拒绝。
const PlanVersion = 1

// Plan 是一次发布计算的结果。Head 记录计算时的提交，tag 阶段用它判断
// HEAD 是否只多了「发布提交」（go.mod 归位 + CHANGELOG）以外的改动。
type Plan struct {
	Version     int      `json:"version"`
	Head        string   `json:"head"`
	GeneratedAt string   `json:"generated_at"`
	Changes     []Change `json:"changes"`
}

// Change 是一个待发布模块：当前版本、下一版本与触发它的提交。
type Change struct {
	Module   string          `json:"module"`
	Dir      string          `json:"dir"`
	Current  string          `json:"current"`
	Next     string          `json:"next"`
	Tag      string          `json:"tag"`
	Bump     string          `json:"bump"` // initial/patch/minor/major
	Commits  []Commit        `json:"commits"`
	Requires []RequireUpdate `json:"requires,omitempty"`
}

// Commit 是变更日志与版本推导的输入。
type Commit struct {
	Hash     string `json:"hash"`
	Subject  string `json:"subject"`
	Breaking bool   `json:"breaking,omitempty"`
	Type     string `json:"-"`
}

// RequireUpdate 是发布前需要归位的一条内部依赖。
type RequireUpdate struct {
	Module string `json:"module"`
	From   string `json:"from"`
	To     string `json:"to"`
}

var conventionalPattern = regexp.MustCompile(`^([A-Za-z]+)(?:\([^)]*\))?(!)?:`)

// nonReleasingTypes 是只改文档/流程、不单独触发发布的提交类型。
var nonReleasingTypes = map[string]bool{
	"docs": true, "chore": true, "test": true, "tests": true,
	"ci": true, "style": true, "build": true,
}

// BuildPlan 计算自上次 tag 以来每个模块的下一版本，并为计划内模块列出
// 需要归位的内部依赖。
func (r *Repo) BuildPlan(now time.Time) (*Plan, error) {
	head, err := r.head()
	if err != nil {
		return nil, err
	}
	plan := &Plan{
		Version:     PlanVersion,
		Head:        head,
		GeneratedAt: now.UTC().Format(time.RFC3339),
	}

	for _, m := range r.Modules {
		if !m.Releasable {
			continue
		}
		commits, err := r.commitsFor(m)
		if err != nil {
			return nil, err
		}
		bump, next := m.nextVersion(commits)
		if bump == "" {
			continue
		}
		change := Change{
			Module:  m.Path,
			Dir:     m.Dir,
			Current: m.Latest,
			Next:    next,
			Tag:     m.tagFor(next),
			Bump:    bump,
			Commits: commits,
		}
		plan.Changes = append(plan.Changes, change)
	}

	byPath := make(map[string]*Change, len(plan.Changes))
	for i := range plan.Changes {
		byPath[plan.Changes[i].Module] = &plan.Changes[i]
	}
	for i := range plan.Changes {
		updates, err := r.requireUpdates(&plan.Changes[i], byPath)
		if err != nil {
			return nil, err
		}
		plan.Changes[i].Requires = updates
	}
	return plan, nil
}

func (m *Module) tagFor(version string) string {
	if m.Dir == "." {
		return version
	}
	return m.Dir + "/" + version
}

// nextVersion 返回 bump 与下一版本；空 bump 表示本轮不发版。
func (m *Module) nextVersion(commits []Commit) (string, string) {
	if m.Latest == "" {
		if len(commits) == 0 {
			return "", ""
		}
		return "initial", "v0.1.0"
	}
	bump := classify(commits, m.Latest)
	if bump == "" {
		return "", ""
	}
	return bump, bumpVersion(m.Latest, bump)
}

// classify 取所有提交里最高的 bump 等级。同一模块只要有一个 feat 就是 minor；
// 纯 docs/chore 之类的提交不触发发布。
func classify(commits []Commit, current string) string {
	major, _, _ := parseVersion(current)
	bump := ""
	for _, c := range commits {
		var candidate string
		switch {
		case c.Breaking:
			if major >= 1 {
				candidate = "major"
			} else {
				candidate = "minor" // 0.x 的 breaking 由 minor 承载
			}
		case c.Type == "feat":
			candidate = "minor"
		case c.Type == "":
			candidate = "patch" // 非 Conventional 提交按普通改动处理
		case nonReleasingTypes[c.Type]:
			candidate = ""
		default:
			candidate = "patch"
		}
		if bumpRank(candidate) > bumpRank(bump) {
			bump = candidate
		}
	}
	return bump
}

func bumpRank(bump string) int {
	switch bump {
	case "major":
		return 3
	case "minor":
		return 2
	case "patch":
		return 1
	default:
		return 0
	}
}

// bumpVersion 只处理正式版本（vX.Y.Z）；模块 tag 约定不含预发布后缀。
func bumpVersion(version, bump string) string {
	major, minor, patch := parseVersion(version)
	switch bump {
	case "major":
		return fmt.Sprintf("v%d.0.0", major+1)
	case "minor":
		return fmt.Sprintf("v%d.%d.0", major, minor+1)
	default:
		return fmt.Sprintf("v%d.%d.%d", major, minor, patch+1)
	}
}

func parseVersion(version string) (major, minor, patch int) {
	parts := strings.Split(strings.TrimPrefix(version, "v"), ".")
	nums := make([]int, 3)
	for i := 0; i < len(parts) && i < 3; i++ {
		n, _ := strconv.Atoi(parts[i])
		nums[i] = n
	}
	return nums[0], nums[1], nums[2]
}

func compareVersion(a, b string) int {
	amaj, amin, apat := parseVersion(a)
	bmaj, bmin, bpat := parseVersion(b)
	switch {
	case amaj != bmaj:
		return amaj - bmaj
	case amin != bmin:
		return amin - bmin
	default:
		return apat - bpat
	}
}

// commitsFor 返回真正改动了本模块文件的提交（新到旧）。
//
// 先按目录取提交与文件列表，再用「最深 module」归属过滤：cobramcp/examples/*
// 这类嵌套 module 的文件不会被算进 cobramcp 的变更。merge 提交没有独立改动，
// 不参与发版判断。
func (r *Repo) commitsFor(m *Module) ([]Commit, error) {
	rangeSpec := "HEAD"
	if m.Latest != "" {
		rangeSpec = m.Latest + "..HEAD"
	}
	path := m.Dir

	out, err := r.git("log", "--no-merges", "--format=%x1e%H%x1f%s", "--name-only", rangeSpec, "--", path)
	if err != nil {
		return nil, err
	}

	type record struct {
		hash    string
		subject string
	}
	var records []record
	for _, chunk := range strings.Split(out, "\x1e") {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		lines := strings.Split(chunk, "\n")
		fields := strings.SplitN(lines[0], "\x1f", 2)
		if len(fields) != 2 {
			continue
		}
		files := make([]string, 0, len(lines)-1)
		for _, line := range lines[1:] {
			if line = strings.TrimSpace(line); line != "" {
				files = append(files, line)
			}
		}
		if r.owns(m, files) {
			records = append(records, record{hash: fields[0], subject: fields[1]})
		}
	}
	if len(records) == 0 {
		return nil, nil
	}

	bodies, err := r.commitBodies(rangeSpec, path)
	if err != nil {
		return nil, err
	}

	commits := make([]Commit, 0, len(records))
	for _, rec := range records {
		typ, bang := parseSubject(rec.subject)
		commits = append(commits, Commit{
			Hash:     rec.hash,
			Subject:  rec.subject,
			Breaking: bang || hasBreakingFooter(bodies[rec.hash]),
			Type:     typ,
		})
	}
	return commits, nil
}

func (r *Repo) commitBodies(rangeSpec, path string) (map[string]string, error) {
	out, err := r.git("log", "--no-merges", "--format=%x1e%H%x1f%b", rangeSpec, "--", path)
	if err != nil {
		return nil, err
	}
	bodies := map[string]string{}
	for _, chunk := range strings.Split(out, "\x1e") {
		if chunk == "" {
			continue
		}
		idx := strings.Index(chunk, "\x1f")
		if idx < 0 {
			continue
		}
		hash := strings.TrimSpace(chunk[:idx])
		bodies[hash] = chunk[idx+1:]
	}
	return bodies, nil
}

func (r *Repo) owns(m *Module, files []string) bool {
	for _, file := range files {
		if owner := r.owner(file); owner != nil && owner.Dir == m.Dir {
			return true
		}
	}
	return false
}

func parseSubject(subject string) (typ string, breaking bool) {
	match := conventionalPattern.FindStringSubmatch(subject)
	if match == nil {
		return "", false
	}
	return strings.ToLower(match[1]), match[2] == "!"
}

func hasBreakingFooter(body string) bool {
	for _, line := range strings.Split(body, "\n") {
		upper := strings.ToUpper(strings.TrimSpace(line))
		if strings.HasPrefix(upper, "BREAKING CHANGE:") || strings.HasPrefix(upper, "BREAKING-CHANGE:") {
			return true
		}
	}
	return false
}

// requireUpdates 列出模块 go.mod 中需要随本轮发布归位的内部依赖。
// 只关心计划内的模块——不发版的依赖即使版本更旧也不动，避免把发布范围
// 无限扩大。
func (r *Repo) requireUpdates(c *Change, byPath map[string]*Change) ([]RequireUpdate, error) {
	data, err := os.ReadFile(filepath.Join(r.Root, c.Dir, "go.mod"))
	if err != nil {
		return nil, err
	}
	var updates []RequireUpdate
	for _, req := range parseRequires(string(data)) {
		dep, ok := byPath[req.Path]
		if !ok || req.Path == c.Module {
			continue
		}
		if compareVersion(req.Version, dep.Next) < 0 {
			updates = append(updates, RequireUpdate{Module: req.Path, From: req.Version, To: dep.Next})
		}
	}
	sort.Slice(updates, func(i, j int) bool { return updates[i].Module < updates[j].Module })
	return updates, nil
}

type requirement struct {
	Path    string
	Version string
}

// parseRequires 只解析 module 路径与版本，不追求完整 go.mod 语法：发布工具
// 只需要改写仓库内部 module 的 require 行。
func parseRequires(src string) []requirement {
	var reqs []requirement
	inBlock := false
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}
		if inBlock {
			if trimmed == ")" {
				inBlock = false
				continue
			}
			fields := strings.Fields(trimmed)
			if len(fields) >= 2 && !strings.HasPrefix(fields[0], "//") {
				reqs = append(reqs, requirement{Path: fields[0], Version: fields[1]})
			}
			continue
		}
		switch {
		case trimmed == "require (":
			inBlock = true
		case strings.HasPrefix(trimmed, "require "):
			fields := strings.Fields(trimmed)
			if len(fields) >= 3 {
				reqs = append(reqs, requirement{Path: fields[1], Version: fields[2]})
			}
		}
	}
	return reqs
}

// PrintPlan 输出给人看的计划表；依赖归位单独列在表后，便于确认 sync 会改什么。
func PrintPlan(w io.Writer, plan *Plan, root string) error {
	if len(plan.Changes) == 0 {
		_, err := fmt.Fprintf(w, "自上次 tag 以来没有需要发版的提交（HEAD %s）。\n", shortHash(plan.Head))
		return err
	}
	_, _ = fmt.Fprintf(w, "发布计划（HEAD %s，%d 个模块）\n\n", shortHash(plan.Head), len(plan.Changes))

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, c := range plan.Changes {
		current := versionOf(c, c.Current)
		if current == "" {
			current = "-"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d 个提交\n",
			changelogLabel(root, c), current, c.Next, c.Bump, len(c.Commits))
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	anyRequires := false
	for _, c := range plan.Changes {
		for _, u := range c.Requires {
			if !anyRequires {
				_, _ = fmt.Fprintln(w, "\n依赖归位（bald release sync --write）：")
				anyRequires = true
			}
			gomod := "go.mod"
			if c.Dir != "." {
				gomod = c.Dir + "/go.mod"
			}
			_, _ = fmt.Fprintf(w, "  %s: require %s %s -> %s\n", gomod, u.Module, u.From, u.To)
		}
	}
	return nil
}

// WritePlan 把计划写成 JSON；目录不存在时自动创建。
func WritePlan(path string, plan *Plan) error {
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, data, 0o644)
}

// ReadPlan 读取并校验计划文件。
func ReadPlan(path string) (*Plan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read plan: %w", err)
	}
	var plan Plan
	if err := json.Unmarshal(data, &plan); err != nil {
		return nil, fmt.Errorf("parse plan %s: %w", path, err)
	}
	if plan.Version != PlanVersion {
		return nil, fmt.Errorf("plan %s has schema version %d, want %d; rerun `bald release plan`", path, plan.Version, PlanVersion)
	}
	return &plan, nil
}

func changelogLabel(root string, c Change) string {
	if c.Dir == "." {
		return filepath.Base(root)
	}
	return c.Dir
}

// versionOf 从完整 tag 里取出纯版本号；根模块 tag 本身就是版本。
func versionOf(c Change, tag string) string {
	if c.Dir == "." {
		return tag
	}
	return strings.TrimPrefix(tag, c.Dir+"/")
}

func shortHash(hash string) string {
	if len(hash) > 8 {
		return hash[:8]
	}
	return hash
}
