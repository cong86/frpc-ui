//go:build !windows

package installer

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestDeploymentPermissionsWithPrivateUmask(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0700); err != nil {
		t.Fatal(err)
	}
	previous := unix.Umask(0077)
	defer unix.Umask(previous)
	root := filepath.Join(parent, "new-install")
	binary := filepath.Join(root, "bin", "frp-console")
	if err := put(binary, []byte("binary"), 0755); err != nil {
		t.Fatal(err)
	}
	private := filepath.Join(root, "data", "frpc.toml")
	if err := put(private, []byte("private config"), 0600); err != nil {
		t.Fatal(err)
	}
	for path, expected := range map[string]os.FileMode{parent: 0700, root: 0755, filepath.Dir(binary): 0755, binary: 0755, private: 0600} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != expected {
			t.Fatalf("%s mode %o, expected %o", filepath.Base(path), info.Mode().Perm(), expected)
		}
	}
}
