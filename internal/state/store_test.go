package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEncryptedSnapshotTamperAndMissingKey(t *testing.T) {
	root := t.TempDir()
	s, e := Open(root)
	if e != nil {
		t.Fatal(e)
	}
	text := "token=non-production-secret"
	enc := s.Seal(text)
	if strings.Contains(string(enc), text) {
		t.Fatal("snapshot plaintext")
	}
	out, e := s.Unseal(enc)
	if e != nil || out != text {
		t.Fatal(e)
	}
	enc[len(enc)-1] ^= 1
	if _, e = s.Unseal(enc); e == nil {
		t.Fatal("tampered backup accepted")
	}
	s.DB.Close()
	if e = os.Remove(filepath.Join(root, "backup.key")); e != nil {
		t.Fatal(e)
	}
	if next, e := Open(root); e == nil {
		next.DB.Close()
		t.Fatal("missing key silently replaced")
	}
}
