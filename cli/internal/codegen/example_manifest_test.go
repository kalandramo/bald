package codegen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestExampleManifestsMatchSpecAndDisk 是 _example/bald 两份生成清单的「不需要 proto」判据：
// 清单里的 spec 摘要要等于当前 appspec.json 的字节 sha256，每条 owned 记录的 hash 要等于磁盘
// 字节的 sha256。seeded 只判存在——播种后归用户，内容差异合法（与 checkTree 同口径）。
//
// 为什么要单独有这条：完整的 gen domain --check 按当前声明**重算**生成物，而 bind 文件的前置是
// proto 的 Go 产物；_example/bald/api/gen 是 gitignore 的生成目录，干净检出里没有，那条在 CI
// 只能红（--no-bind 不是替代：它改的是期望文件集合，实测把已入库的 bind/路由判成 stale、清单
// spec 也对不上）。这条吃的全在版本库里（清单 + 生成物字节），于是 CI 就能抓住「改了声明忘了
// 重生成」与「手改生成物」两类漂移——前者正是 domains.manifest.json 的旧摘要潜伏 13 个提交
// 那一类（风险评审 §8）。重算级校验（含 proto 派生的 bind 内容）留在需要 buf 的
// task acceptance-example-domain-check。
func TestExampleManifestsMatchSpecAndDisk(t *testing.T) {
	example := exampleModule(t)
	spec, err := os.ReadFile(filepath.Join(example, "appspec.json"))
	if err != nil {
		t.Fatalf("read appspec.json: %v", err)
	}
	specHash := sha256Hex(spec)

	for _, name := range []string{"appspec.manifest.json", "domains.manifest.json"} {
		raw, err := os.ReadFile(filepath.Join(example, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		var m genManifest
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("unmarshal %s: %v", name, err)
		}
		// 防空转：清单为空时下面的循环什么都不查，判据就成了恒真。
		if len(m.Files) == 0 {
			t.Fatalf("%s 里没有任何生成物记录——判据在空转", name)
		}
		if m.Spec != specHash {
			t.Errorf("%s: 清单的 spec 摘要 %s 与当前 appspec.json 的 %s 不符——声明改过但没重生成", name, m.Spec, specHash)
		}
		for _, e := range m.Files {
			p := filepath.Join(example, filepath.FromSlash(e.Path))
			content, err := os.ReadFile(p)
			if err != nil {
				t.Errorf("%s: 清单记录 %s 在磁盘上不存在: %v", name, e.Path, err)
				continue
			}
			if e.Ownership == "seeded" {
				continue
			}
			if got := sha256Hex(content); got != e.Hash {
				t.Errorf("%s: 生成物 %s 的磁盘摘要 %s 与清单记录 %s 不符——生成物被手改，或声明变过没重生成", name, e.Path, got, e.Hash)
			}
		}
	}
}
