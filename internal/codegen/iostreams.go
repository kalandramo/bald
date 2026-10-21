package codegen

import (
	"fmt"
	"go/format"
	"io"
	"os"
	"path/filepath"
)

// IOStreams 是命令的标准出入流（kubectl genericiooptions.IOStreams 的迷你版，
// 不为单个结构体引入 k8s 依赖树）。命令不直接打印 os.Stdout，而是写 streams.Out——
// 生产路径绑定进程标准流，测试路径注入 bytes.Buffer 即可断言输出。
//
// 落地依据 golang-cobra-cli-design 规范：叶子命令 Options 三阶段 + IOStreams。
type IOStreams struct {
	In     io.Reader
	Out    io.Writer // 信息性输出（生成结果、next 提示）
	ErrOut io.Writer // 诊断输出
}

// NewDefaultIOStreams 返回绑定进程标准流的 IOStreams（生产路径）。
func NewDefaultIOStreams() IOStreams {
	return IOStreams{In: os.Stdin, Out: os.Stdout, ErrOut: os.Stderr}
}

// CheckErr 是 CLI 的唯一错误出口：非 nil 时打印 stderr 并以码 1 退出。
// 业务代码只管 return err，打印格式与退出码集中在此。
func CheckErr(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// Ownership 是生成物的归属三态（D2-target-codegen.md §5.1）：回归生成不再
// 只有「存在即报错」一种口径。
type Ownership int

const (
	// Owned 生成物：每次 gen 重写（文件头 DO NOT EDIT，内容以生成器为准）。
	// 已存在时不加 --force 一律拒绝（防覆盖底线不变——无 --force 不重写已存在
	// 文件），--force 显式放行。
	Owned Ownership = iota
	// Seeded 生成一次：不存在才落盘；已存在即跳过并输出 kept:（不算错误）。
	// --force 也不覆盖——生成一次之后归用户维护（seed/、apiserver.go 等）。
	Seeded
	// User 用户物：生成器永不读写，仅输出 skip: 提示（appspec.json、biz/model
	// 等声明真相源与业务代码）。
	User
)

// writeGuarded 是全部生成命令共用的落盘通道（按归属决定覆盖/跳过/拒绝）：
//   - Owned：不存在则写；已存在须 --force（错误信息自带指引）。
//   - Seeded：不存在则写；已存在输出 kept: 并返回 nil。
//   - User：永不落盘，输出 skip:。
//
// 成功/跳过提示走 streams.Out（信息性输出不污染 stderr）。
func writeGuarded(path string, content []byte, own Ownership, force bool, streams IOStreams) error {
	switch own {
	case User:
		fmt.Fprintln(streams.Out, "skip:", path, "(user file)")
		return nil
	case Seeded:
		if _, err := os.Stat(path); err == nil {
			fmt.Fprintln(streams.Out, "kept:", path)
			return nil
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("stat %s: %w", path, err)
		}
	case Owned:
		if !force {
			if _, err := os.Stat(path); err == nil {
				return fmt.Errorf("%s already exists, refusing to overwrite (pass --force to allow)", path)
			} else if !os.IsNotExist(err) {
				return fmt.Errorf("stat %s: %w", path, err)
			}
		}
	default:
		return fmt.Errorf("unknown ownership %d for %s", own, path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return err
	}
	fmt.Fprintln(streams.Out, "generated:", path)
	return nil
}

// writeFileFormatted 是 .go 生成物的落盘通道：先 gofmt 再走 writeGuarded。
//
// gofmt 失败时**绝不触碰目标路径**（修掉原 renderAppSpec 的越权直写缺陷：
// 旧实现在 gofmt 失败时 os.WriteFile 目标路径，无 --force 也截断重写用户
// 文件）；诊断性原文写 <path>.rej 并在错误里附路径，交由用户排查。
func writeFileFormatted(path string, raw []byte, own Ownership, force bool, streams IOStreams) error {
	formatted, err := format.Source(raw)
	if err != nil {
		rej := path + ".rej"
		if mkErr := os.MkdirAll(filepath.Dir(rej), 0o755); mkErr == nil {
			if wErr := os.WriteFile(rej, raw, 0o644); wErr == nil {
				err = fmt.Errorf("gofmt generated file: %w (raw template output saved to %s; target not touched)", err, rej)
			}
		}
		return err
	}
	return writeGuarded(path, formatted, own, force, streams)
}
