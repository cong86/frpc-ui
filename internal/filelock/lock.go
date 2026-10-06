package filelock

import (
	"errors"
	"os"
)

// Acquire is nonblocking. The OS releases the lock if the owner crashes.
func Acquire(path string) (func(), error) {
	if st, e := os.Lstat(path); e == nil && !st.Mode().IsRegular() {
		return nil, errors.New("lock path must be a regular file")
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = lock(f); e != nil {
		f.Close()
		return nil, errors.New("resource is locked by another process")
	}
	return func() { unlock(f); _ = f.Close() }, nil
}
