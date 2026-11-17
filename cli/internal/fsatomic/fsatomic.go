// Package fsatomic 提供原子文件写入：同目录临时文件 + rename 覆盖，替代
// os.WriteFile 的原地截断写。
//
// 动机：写盘中断（Ctrl+C / 崩溃 / 磁盘满）此前会留下半截文件——用户手写的
// appspec.json（声明真源）、IDE 的 mcp.json（编辑器配置）、CHANGELOG 被截断
// 即损坏且无恢复点。temp+rename 保证观察者只看到旧内容或新内容，不看到
// 中间态；os.Rename 在 Windows 上带 MOVEFILE_REPLACE_EXISTING 语义，覆盖
// 已存在目标的行为与 Unix 一致，临时文件与目标同目录保证同卷。
//
// 不做 fsync（与 os.WriteFile 的持久化语义保持一致：本包解决的是「截断」，
// 不是「掉电」）；不产生 .bak——git 跟踪的文件自带恢复点，未跟踪文件靠
// rename 原子性。硬崩可能遗留 .<name>.tmp-* 孤儿（可安全删除），好过半截目标。
//
// 与 os.WriteFile 的两处已知行为差异（均为 fail-closed，记录备查）：
//  1. 悬空符号链接：os.WriteFile 会沿链接 O_CREATE 建出目标（父目录存在时），
//     本包解析失败即报错——宁可显式失败，不静默改写用户链接布局；
//  2. Windows 共享冲突：目标被无 FILE_SHARE_DELETE 的句柄占用（watcher/tail）
//     时 rename 报 sharing violation，os.WriteFile（open-for-write）可能成功。
//     两处都返回错误且原文件完好，无静默损坏。
package fsatomic

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// WriteFile 与 os.WriteFile 同签名：原子写（同目录临时文件 + rename 覆盖）。
// 目标所在目录必须已存在（与 os.WriteFile 一致，不代建目录）。
//
// 符号链接语义：目标是符号链接时穿透到真实目的地写（rename 的替换语义会把
// 链接本身变成普通文件，静默改变用户布局）。链接解析在写前取一次快照——
// 写间隙里链接被并发换目标的窗口毫秒级，失败形态是写到旧目的地（孤儿写），
// 不会损坏新目标（对 CLI 场景可忽略，见包注释）。
func WriteFile(path string, data []byte, perm os.FileMode) error {
	if path == "" {
		return errors.New("fsatomic: empty path")
	}
	if fi, err := os.Lstat(path); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		real, err := filepath.EvalSymlinks(path)
		if err != nil {
			return fmt.Errorf("fsatomic: resolve symlink %s: %w", path, err)
		}
		path = real
	}
	dir, base := filepath.Split(path)
	if dir == "" {
		dir = "."
	}
	if base == "" {
		// 尾随分隔符（--out "dir/"）会落到空 base：临时文件名不可构造，
		// 显式报错而不是在 rename 时才意外失败。
		return errors.New("fsatomic: path must name a file, not a directory: " + path)
	}

	// 临时文件与目标同目录（同卷 rename 才原子）、同前缀（遗留孤儿可辨识归主）。
	tmp, err := os.CreateTemp(dir, "."+base+".bald-tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}

	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Chmod(applyUmask(perm)); err != nil {
		// 显式落目标权限：CreateTemp 恒 0600；必须过一遍 umask——Chmod 是
		// 绝对置位，绕过 umask 会把 umask 077 用户的 0600 文件写成 world-readable
		// （mcp.json 里是 API key；os.WriteFile 的 open(2) 路径本就应用 umask）。
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}
