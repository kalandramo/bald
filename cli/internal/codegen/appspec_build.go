package codegen

// appspec_build.go 组装目标目录树的落盘清单并按归属三态写入。

import (
	"fmt"
	"path/filepath"
)

// buildTree 组装目标目录树的落盘项清单（路径 / 归属 / 内容）。apiserver.go
// 仅在声明 HTTP 面时 seed（它是 HTTP 装配钩子，无面则无钩子）。
func buildTree(v *treeView, cfgs map[string][]byte) ([]genFile, error) {
	v.Package = "bootstrap" // 域清单固定落 internal/bootstrap（package 子句同目录名）
	files := []genFile{}
	addIn := func(vv *treeView, path, tmpl string, own ownership, goSrc bool) error {
		raw, err := renderTreeTemplate(filepath.Base(path), tmpl, vv)
		if err != nil {
			return err
		}
		files = append(files, genFile{Path: path, Own: own, Raw: raw, GoSrc: goSrc})
		return nil
	}
	add := func(path, tmpl string, own ownership, goSrc bool) error {
		return addIn(v, path, tmpl, own, goSrc)
	}
	// 角色扇出：每角色落 cmd/<CmdName>/{main.go, app/command.go}。单角色时
	// CmdName == AppName，落盘路径与引入角色前完全相同。
	for _, slot := range v.Roles {
		rv := *v
		rv.CmdName = slot.CmdName
		rv.RoleName = slot.Name
		if err := addIn(&rv, filepath.Join("cmd", rv.CmdName, "main.go"), mainTmpl, owned, true); err != nil {
			return nil, err
		}
		if err := addIn(&rv, filepath.Join("cmd", rv.CmdName, "app", "command.go"), commandTmpl, owned, true); err != nil {
			return nil, err
		}
		if err := addIn(&rv, filepath.Join("cmd", rv.CmdName, "app", "custom.go"), appCustomTmpl, Seeded, true); err != nil {
			return nil, err
		}
	}
	if err := add(filepath.Join("internal", "bootstrap", "assemble.gen.go"), assembleTmpl, owned, true); err != nil {
		return nil, err
	}
	// 变体投影只在声明了 variants 时落盘：未声明的树逐个字节与引入前一致
	// （assemble.gen.go 也不出现 variantOptions 调用）。
	if v.HasVariants {
		if err := add(filepath.Join("internal", "bootstrap", "variants.gen.go"), variantsTmpl, owned, true); err != nil {
			return nil, err
		}
		// 每变体一份 //go:build 实现文件：文件本身就是「编译进来即命中」的
		// 证明，与声明同源（同一 spec 的 when 派生），appkit 计划期对账。
		for _, vs := range v.Variants {
			vv := *v
			vv.Variant = vs
			if err := addIn(&vv, filepath.Join("internal", "bootstrap", "variant_"+vs.Name+".gen.go"), variantImplTmpl, owned, true); err != nil {
				return nil, err
			}
		}
	}
	dv := *v
	dv.Gen = domainsGenTag // 与 gen domain 共写同一文件：标识与调用方无关
	if err := addIn(&dv, filepath.Join("internal", "bootstrap", "domains.gen.go"), bootstrapDomainsTmpl, owned, true); err != nil {
		return nil, err
	}
	if err := add(filepath.Join("internal", "bootstrap", "custom.go"), bootstrapCustomTmpl, Seeded, true); err != nil {
		return nil, err
	}
	if err := add(filepath.Join("internal", "bootstrap", "seed", "seed.go"), seedTmpl, Seeded, true); err != nil {
		return nil, err
	}
	if err := add(filepath.Join("internal", "bootstrap", "seed", "policies.go"), seedPoliciesTmpl, Seeded, true); err != nil {
		return nil, err
	}
	if err := add(filepath.Join("internal", v.APIDir, "layering_invariant_test.go"), layeringTestTmpl, owned, true); err != nil {
		return nil, err
	}
	if v.HasHTTP {
		if err := add(filepath.Join("internal", v.APIDir, "apiserver.go"), apiserverTmpl, Seeded, true); err != nil {
			return nil, err
		}
	}
	// buf 流水线配置 seed 而非 owned：插件集合 / 依赖模块 / 输出路径是树的
	// 属性（第三方插件、registry 模块、托管开关），生成器只给可用的起点；
	// --check 只判存在性，业务改配置不会被判脏。
	if err := add(filepath.Join("api", "buf.yaml"), bufYamlTmpl, Seeded, false); err != nil {
		return nil, err
	}
	if err := add(filepath.Join("api", "buf.gen.yaml"), bufGenYamlTmpl, Seeded, false); err != nil {
		return nil, err
	}
	if err := add("taskfile.yml", taskfileTmpl, owned, false); err != nil {
		return nil, err
	}
	// 每角色一份契约：runner 的 configs 里没有 server 段，
	// 「不装 HTTP」因此不依赖任何运行期豁免。单角色仍落 configs/<app>.yaml。
	for _, slot := range v.Roles {
		cfgYAML, ok := cfgs[slot.Name]
		if !ok {
			return nil, fmt.Errorf("roles: no contract generated for role %q", slot.Name)
		}
		files = append(files, genFile{
			Path: filepath.Join("configs", filepath.Base(slot.ConfigFile)),
			Own:  owned,
			Raw:  cfgYAML,
		})
	}
	return files, nil
}

// emitTree 把目录树落盘（产出根 = layout.root，默认 "."）：.go 经
// writeFileFormatted（gofmt + 失败 .rej）；其余经 writeGuarded。逐文件输出
// generated:/kept:/skip:（三态语义可见）。adopt 放行 owned 覆盖（--adopt）。
func emitTree(root string, files []genFile, adopt bool, streams ioStreams) error {
	for _, f := range files {
		path := filepath.Join(root, f.Path)
		if f.GoSrc {
			if err := writeFileFormatted(path, f.Raw, f.Own, adopt, streams); err != nil {
				return err
			}
			continue
		}
		if err := writeGuarded(path, f.Raw, f.Own, adopt, streams); err != nil {
			return err
		}
	}
	return nil
}
