package filelock

import (
	"errors"
	"os"
)

// Acquire is nonblocking. The OS releases the lock if the owner crashes.
func Acquire(path string) (func(), error) {
	if st, e := os.Lstat(path); e == nil && !st.Mode().IsRegular() {
		return nil, errors.New("锁路径必须是普通文件")
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = lock(f); e != nil {
		f.Close()
		return nil, errors.New("资源正在被其他进程锁定")
	}
	return func() { unlock(f); _ = f.Close() }, nil
}
