//go:build unix

package fsatomic

import (
	"os"
	"syscall"
)

// umaskVal 在包初始化时读一次进程 umask：init 阶段没有用户 goroutine，
// 「置 0 读回再还原」的窗口不与任何并发建文件竞争。Unix 的 umask 是进程级
// 全局值，运行中反复 set/restore 会污染其他 goroutine 的建文件行为。
var umaskVal = func() int {
	old := syscall.Umask(0)
	syscall.Umask(old)
	return old
}()

// applyUmask 对 Chmod 前的权限位应用 umask，与 os.WriteFile 的 open(2) 路径
// 语义对齐（不绕过用户 umask）。
func applyUmask(perm os.FileMode) os.FileMode {
	return perm &^ os.FileMode(umaskVal)
}
