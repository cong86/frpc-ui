package filelock

import (
	"path/filepath"
	"testing"
)

func TestExclusiveAndReleased(t *testing.T) {
	p := filepath.Join(t.TempDir(), "owner.lock")
	release, e := Acquire(p)
	if e != nil {
		t.Fatal(e)
	}
	if other, e := Acquire(p); e == nil {
		other()
		t.Fatal("second owner accepted")
	}
	release()
	release2, e := Acquire(p)
	if e != nil {
		t.Fatal("released lock remained busy", e)
	}
	release2()
}
