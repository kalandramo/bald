//go:build !unix

package fsatomic

import "os"

// applyUmask 在无 umask 概念的平台（Windows/plan9 等）原样返回：Windows 的
// 文件权限由 ACL 管辖，os.Chmod 只影响只读位，不存在「绕过 umask」问题。
func applyUmask(perm os.FileMode) os.FileMode {
	return perm
}
