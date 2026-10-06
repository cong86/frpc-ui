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
		return errors.New("managed metadata and binary must be root-owned regular files, not writable by group or others")
	}
	for p := filepath.Dir(path); ; p = filepath.Dir(p) {
		st, e := os.Lstat(p)
		if e != nil {
			return e
		}
		owner, ok := st.Sys().(*syscall.Stat_t)
		if !ok || owner.Uid != 0 || !st.IsDir() || st.Mode().Perm()&0022 != 0 {
			return errors.New("managed path parents must be protected root-owned directories")
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	return nil
}
