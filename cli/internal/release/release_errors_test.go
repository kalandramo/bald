package release

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 本文件补齐 release 各步骤的错误半边：git 调用失败、go.mod 缺失/非法、计划
// 过期/损坏、工作区不干净、推送无远端等。全部用本机 git 与临时目录夹具，离线
// 可跑；复用 release_test.go 的 newRepo/writeFile/commit/runGit 与
// release_coverage_test.go 的 driveRelease。结构性不可达分支见文件尾注释。

// --- repo.go ---

// TestOpenRepo_NotGit 非 git 目录里 OpenRepo fail-fast（repo.go:48）。
func TestOpenRepo_NotGit(t *testing.T) {
	t.Chdir(t.TempDir())
	if _, err := OpenRepo(); err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("OpenRepo in non-git dir: err=%v, want not-a-git-repository", err)
	}
}

// TestDiscover_NoGoMod 无 go.mod 的目录 Discover 报错（repo.go:96）。
func TestDiscover_NoGoMod(t *testing.T) {
	if _, err := Discover(t.TempDir()); err == nil || !strings.Contains(err.Error(), "no go.mod found") {
		t.Fatalf("Discover empty dir: err=%v, want no-go.mod", err)
	}
}

// TestDiscover_BadGoMod go.mod 缺 module 声明：readModulePath 报错经 WalkDir
// 上抛（repo.go:120 → 74 → 93）。
func TestDiscover_BadGoMod(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "go 1.22\n") // 无 module 行
	if _, err := Discover(root); err == nil || !strings.Contains(err.Error(), "missing module declaration") {
		t.Fatalf("Discover bad go.mod: err=%v, want missing-module-declaration", err)
	}
}

// TestDiscover_NotGit go.mod 合法但目录非 git：latestTag 的 git 调用失败上抛
// （repo.go:148 → 102）。
func TestDiscover_NotGit(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.com/m\n\ngo 1.22\n")
	if _, err := Discover(root); err == nil {
		t.Fatal("Discover non-git with go.mod: want latestTag git error, got nil")
	}
}

// TestReadModulePath_MissingDeclaration 直接驱动 readModulePath 的缺声明半边
// （repo.go:120）。
func TestReadModulePath_MissingDeclaration(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "go 1.22\n")
	if _, err := readModulePath(filepath.Join(root, "go.mod")); err == nil ||
		!strings.Contains(err.Error(), "missing module declaration") {
		t.Fatalf("readModulePath: err=%v", err)
	}
}

// TestRepo_GitErrors 非 git 目录里 head/tagExists/dirty 的 git 调用都失败，
// 覆盖 git() 的 stderr 文案分支（repo.go:178/182）与各调用方错误返回
// （head:163、tagExists:176、dirty:189）。
func TestRepo_GitErrors(t *testing.T) {
	r := &Repo{Root: t.TempDir()}
	if _, err := r.head(); err == nil {
		t.Error("head in non-git dir must error")
	}
	if _, err := r.tagExists("v0.1.0"); err == nil {
		t.Error("tagExists in non-git dir must error")
	}
	if _, err := r.dirty(); err == nil {
		t.Error("dirty in non-git dir must error")
	}
}

// --- tag.go ---

// TestTag_EmptyChanges 无待发布模块时 Tag 打印早退句并返回 nil（tag.go:23）。
func TestTag_EmptyChanges(t *testing.T) {
	var out bytes.Buffer
	if err := Tag(t.TempDir(), &Plan{}, false, false, &out); err != nil {
		t.Fatalf("empty plan tag: %v", err)
	}
	if !strings.Contains(out.String(), "没有待发布模块") {
		t.Errorf("output = %q", out.String())
	}
}

// TestTag_HeadError 非 git 目录里 Tag 的 head 解析失败上抛（tag.go:29）。
func TestTag_HeadError(t *testing.T) {
	plan := &Plan{Changes: []Change{{Tag: "v0.1.0"}}}
	if err := Tag(t.TempDir(), plan, false, false, &bytes.Buffer{}); err == nil {
		t.Fatal("Tag in non-git dir must error on head")
	}
}

// TestTag_DirtyWorkspace apply 时工作区不干净被挡（tag.go:52）。
func TestTag_DirtyWorkspace(t *testing.T) {
	root := newRepo(t)
	writeFile(t, root, "root.go", "package m\n\n// fix\n")
	commit(t, root, "fix: root bug", "")
	plan := planFor(t, root)
	writeFile(t, root, "notes.txt", "uncommitted\n") // 弄脏工作区
	err := Tag(root, plan, true, false, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "工作区不干净") {
		t.Fatalf("dirty workspace must be rejected, got %v", err)
	}
}

// TestTag_PushWithoutRemote apply+push 但无 origin：分批推送的 git push 失败
// 上抛（tag.go:85）。
func TestTag_PushWithoutRemote(t *testing.T) {
	root := newRepo(t)
	writeFile(t, root, "root.go", "package m\n\n// fix\n")
	commit(t, root, "fix: root bug", "")
	plan := planFor(t, root)
	if err := Tag(root, plan, true, true, &bytes.Buffer{}); err == nil {
		t.Fatal("push without origin remote must error")
	}
}

// TestCheckZipPaths_DiscoverError checkZipPaths 的 Discover 失败上抛（tag.go:125）。
func TestCheckZipPaths_DiscoverError(t *testing.T) {
	if err := checkZipPaths(t.TempDir(), &Plan{Changes: []Change{{Dir: ".", Tag: "v0.1.0"}}}); err == nil {
		t.Fatal("checkZipPaths on no-go.mod dir must error")
	}
}

// TestOnlyReleaseEdits 两条形状：base 不可用 ⇒ diff 失败包装报错（tag.go:198）；
// base..head 只改 go.mod ⇒ 放行返回 nil（tag.go:202/206）。
func TestOnlyReleaseEdits(t *testing.T) {
	root := newRepo(t)
	r := &Repo{Root: root}
	head := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))
	if err := r.onlyReleaseEdits("0000000000000000000000000000000000000000", head); err == nil ||
		!strings.Contains(err.Error(), "不可用") {
		t.Fatalf("unavailable base must error, got %v", err)
	}
	writeFile(t, root, "go.mod", "module example.com/m\n\ngo 1.22\n\nrequire example.com/m/sub v0.2.0\n")
	commit(t, root, "chore: sync requires", "")
	newHead := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))
	if err := r.onlyReleaseEdits(head, newHead); err != nil {
		t.Fatalf("go.mod-only release edit must pass, got %v", err)
	}
}

// --- sync.go ---

// TestSyncGoMod_ReadError 目标 go.mod 不存在 ⇒ ReadFile 错误包装（sync.go:63）。
func TestSyncGoMod_ReadError(t *testing.T) {
	_, err := syncGoMod(t.TempDir(), "missing/go.mod",
		[]RequireUpdate{{Module: "x", From: "v1", To: "v2"}}, false, &bytes.Buffer{})
	if err == nil {
		t.Fatal("syncGoMod on missing go.mod must error")
	}
}

// TestSyncGoMod_StaleVersion go.mod 里的版本既非 from 也非 to ⇒ setRequireVersion
// 报错上抛（sync.go:70）。
func TestSyncGoMod_StaleVersion(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module m\n\nrequire example.com/dep v0.1.0\n")
	_, err := syncGoMod(root, "go.mod",
		[]RequireUpdate{{Module: "example.com/dep", From: "v0.0.1", To: "v0.2.0"}}, false, &bytes.Buffer{})
	if err == nil {
		t.Fatal("syncGoMod with stale version must error")
	}
}

// TestSync_TargetError 计划 Head 为空（跳过 fresh 守卫）但目标 go.mod 缺失 ⇒
// Sync 上抛 syncGoMod 错误（sync.go:44），同时覆盖 CheckPlanFresh 的空 Head
// 早退（plan.go:526）。
func TestSync_TargetError(t *testing.T) {
	plan := &Plan{Head: "", Changes: []Change{
		{Dir: "missing", Requires: []RequireUpdate{{Module: "x", From: "v1", To: "v2"}}},
	}}
	if err := Sync(t.TempDir(), plan, false, &bytes.Buffer{}); err == nil {
		t.Fatal("Sync with missing target go.mod must error")
	}
}

// --- changelog.go ---

// TestWriteChangelog_EmptyChanges 新鲜空计划 ⇒ 打印早退句返回 nil（changelog.go:28）。
func TestWriteChangelog_EmptyChanges(t *testing.T) {
	root := newRepo(t)
	writeFile(t, root, "notes.txt", "docs\n")
	commit(t, root, "docs: notes", "") // docs 不触发发版 ⇒ 空计划
	plan := planFor(t, root)
	if len(plan.Changes) != 0 {
		t.Fatalf("fixture: want empty plan, got %d changes", len(plan.Changes))
	}
	var out bytes.Buffer
	if err := WriteChangelog(root, plan, false, &out); err != nil {
		t.Fatalf("empty changelog: %v", err)
	}
	if !strings.Contains(out.String(), "没有待发布模块") {
		t.Errorf("output = %q", out.String())
	}
}

// --- plan.go ---

// TestBuildPlan_HeadError 非 git 目录 BuildPlan 的 head 失败上抛（plan.go:79）。
func TestBuildPlan_HeadError(t *testing.T) {
	r := &Repo{Root: t.TempDir()}
	if _, err := r.BuildPlan(time.Now()); err == nil {
		t.Fatal("BuildPlan in non-git dir must error on head")
	}
}

// TestBuildPlan_CommitsError head 成功但模块 Latest 指向不存在的 tag ⇒ commitsFor
// 的 git log 失败上抛（plan.go:245 → 93）。
func TestBuildPlan_CommitsError(t *testing.T) {
	root := newRepo(t)
	r := &Repo{Root: root, Modules: []*Module{
		{Dir: ".", Path: "example.com/m", Releasable: true, Latest: "v9.9.9"},
	}}
	if _, err := r.BuildPlan(time.Now()); err == nil {
		t.Fatal("BuildPlan with bogus Latest tag must error in commitsFor")
	}
}

// TestCommitsFor_GitError 非 git 目录 commitsFor 的 git log 失败（plan.go:245）。
func TestCommitsFor_GitError(t *testing.T) {
	r := &Repo{Root: t.TempDir()}
	if _, err := r.commitsFor(&Module{Dir: "."}); err == nil {
		t.Fatal("commitsFor in non-git dir must error")
	}
}

// TestCommitBodies_GitError 非 git 目录 commitBodies 的 git log 失败（plan.go:298）。
func TestCommitBodies_GitError(t *testing.T) {
	r := &Repo{Root: t.TempDir()}
	if _, err := r.commitBodies("HEAD", "."); err == nil {
		t.Fatal("commitBodies in non-git dir must error")
	}
}

// TestConsumerUpdates_ReadError 消费者模块目录缺 go.mod ⇒ requireUpdatesFor 的
// ReadFile 失败上抛（plan.go:354 → 382）。
func TestConsumerUpdates_ReadError(t *testing.T) {
	r := &Repo{Root: t.TempDir(), Modules: []*Module{
		{Dir: "missing", Path: "example.com/missing", Releasable: true},
	}}
	if _, err := r.consumerUpdates(map[string]*Change{}); err == nil {
		t.Fatal("consumerUpdates with missing go.mod must error")
	}
}

// TestWritePlan_MkdirError 计划路径的父目录被同名文件挡住 ⇒ MkdirAll 失败
// （plan.go:492）。
func TestWritePlan_MkdirError(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "blocker", "i am a file\n")
	if err := WritePlan(filepath.Join(root, "blocker", "sub", "plan.json"), &Plan{Version: PlanVersion}); err == nil {
		t.Fatal("WritePlan under a file-blocked dir must error")
	}
}

// TestReadPlan_ParseError 非法 JSON ⇒ Unmarshal 报错（plan.go:506）。
func TestReadPlan_ParseError(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "plan.json", "{not json")
	if _, err := ReadPlan(filepath.Join(root, "plan.json")); err == nil || !strings.Contains(err.Error(), "parse plan") {
		t.Fatalf("ReadPlan invalid json: err=%v", err)
	}
}

// TestReadPlan_VersionMismatch schema 版本不符 ⇒ 拒绝（plan.go:509）。
func TestReadPlan_VersionMismatch(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "plan.json", `{"version":1}`)
	if _, err := ReadPlan(filepath.Join(root, "plan.json")); err == nil || !strings.Contains(err.Error(), "schema version") {
		t.Fatalf("ReadPlan wrong version: err=%v", err)
	}
}

// TestCheckPlanFresh_HeadError 计划绑定提交但目录非 git ⇒ resolve HEAD 失败
// （plan.go:530）。空 Head 早退由 TestSync_TargetError 覆盖。
func TestCheckPlanFresh_HeadError(t *testing.T) {
	if err := CheckPlanFresh(t.TempDir(), &Plan{Head: "abcdef0123456789"}); err == nil ||
		!strings.Contains(err.Error(), "resolve HEAD") {
		t.Fatalf("CheckPlanFresh non-git: err=%v", err)
	}
}

// --- command.go ---

// TestCommand_RootHelp 无子命令时根 RunE 走 Help（command.go:36）。
func TestCommand_RootHelp(t *testing.T) {
	if _, _, err := driveRelease(); err != nil {
		t.Fatalf("root help: %v", err)
	}
}

// TestPlanCmd_OpenRepoError 非 git 目录里 plan 子命令的 OpenRepo 失败上抛
// （command.go:58）。
func TestPlanCmd_OpenRepoError(t *testing.T) {
	t.Chdir(t.TempDir())
	if _, _, err := driveRelease("plan", "--out", filepath.Join(t.TempDir(), "p.json")); err == nil {
		t.Fatal("plan in non-git dir must error")
	}
}

// TestPlanCmd_WritePlanError plan 成功但 --out 路径被文件挡住 ⇒ WritePlan 失败
// 上抛（command.go:65）。
func TestPlanCmd_WritePlanError(t *testing.T) {
	root := newRepo(t)
	t.Chdir(root)
	writeFile(t, root, "blocker", "file\n")
	if _, _, err := driveRelease("plan", "--out", filepath.Join(root, "blocker", "sub", "p.json")); err == nil {
		t.Fatal("plan with blocked --out must error")
	}
}

// TestSubcommands_LoadPlanError sync/changelog/tag 在计划文件缺失时经 loadPlan
// 报错上抛（command.go:101/127/154）。git 仓库内 OpenRepo 成功、ReadPlan 失败。
func TestSubcommands_LoadPlanError(t *testing.T) {
	root := newRepo(t)
	t.Chdir(root)
	missing := filepath.Join(t.TempDir(), "missing.json")
	for _, sub := range []string{"sync", "changelog", "tag"} {
		if _, _, err := driveRelease(sub, "--plan", missing); err == nil {
			t.Errorf("%s with missing plan must error", sub)
		}
	}
}

// TestLoadPlan_OpenRepoError 非 git 目录里 loadPlan 的 OpenRepo 失败上抛
// （command.go:169）。
func TestLoadPlan_OpenRepoError(t *testing.T) {
	t.Chdir(t.TempDir())
	if _, _, err := loadPlan(filepath.Join(t.TempDir(), "p.json")); err == nil {
		t.Fatal("loadPlan in non-git dir must error")
	}
}

// 结构性不可达分支（如实留空，不为绿造假）：
//   - repo.go:61/69/112（WalkDir walkErr / filepath.Rel / ReadFile 失败）：依赖
//     文件系统权限或跨卷路径，root 身份与单卷夹具下不可稳定触发。
//   - repo.go:180（git 失败但 stderr 为空）：git 失败恒写 stderr，空文案半边
//     经真实 git 不可达。
//   - tag.go:35/49/64/135（Tag 内 tagExists/dirty/git-tag/ls-tree 失败）：head 已
//     成功证明 git 可用，同仓库后续 git 调用难以独立失败；checkZipPaths 的
//     ls-tree 又被 Discover 的 latestTag（同样需 HEAD）前置守卫。
//   - tag.go:141（ls-tree -z 输出 TrimSuffix 后不会有空 interior 元素）结构性
//     不可达；tag.go:145（owner==nil 跳过）需「无根模块 + 模块外散文件」的刻意
//     夹具，真实仓库恒有根模块兜底，留空不刻意造形。
//   - plan.go:118/124/261/279/307/449/487、changelog.go:34/51、sync.go:88、
//     command.go:62/70/74：BuildPlan 内 requireUpdates/consumerUpdates 失败需
//     Discover 之后 go.mod 凭空消失；git 输出格式恒含分隔符；tabwriter.Flush 与
//     json.Marshal 作用于已知类型/缓冲恒成功；fs 写失败需权限夹具。
