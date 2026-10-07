//go:build !windows

package managed

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

func trusted(path string) error {
	st, e := os.Lstat(path)
	if e != nil {
		return e
	}
	s, ok := st.Sys().(*syscall.Stat_t)
	if !ok || s.Uid != 0 || !st.Mode().IsRegular() || st.Mode().Perm()&0022 != 0 {
		return errors.New("管理元数据和程序必须是 root 所有的普通文件，组及其他用户不能写入")
	}
	return TrustedDirectory(filepath.Dir(path))
}

func TrustedDirectory(path string) error {
	for p := path; ; p = filepath.Dir(p) {
		st, e := os.Lstat(p)
		if e != nil {
			return e
		}
		owner, ok := st.Sys().(*syscall.Stat_t)
		if !ok || owner.Uid != 0 || !st.IsDir() || st.Mode().Perm()&0022 != 0 {
			return errors.New("管理路径的父目录必须受保护且归 root 所有")
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	return nil
}
