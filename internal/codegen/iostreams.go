package codegen

import (
	"fmt"
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

// writeFileGuarded 是全部生成命令共用的落盘通道（「不静默改动」原则）：
// 目标文件已存在即拒绝覆盖——用户可能已手改生成物；--force 显式放行。
// 成功提示走 streams.Out（信息性输出不污染 stderr）。
func writeFileGuarded(path string, content []byte, force bool, streams IOStreams) error {
	if !force {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%s already exists, refusing to overwrite (pass --force to allow)", path)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("stat %s: %w", path, err)
		}
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
