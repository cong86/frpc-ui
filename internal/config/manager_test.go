package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/cong86/frpc-ui/internal/state"
)

func testManager(t *testing.T) *Manager {
	t.Helper()
	root := t.TempDir()
	st, e := state.Open(root)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { st.DB.Close() })
	p := filepath.Join(root, "frpc.toml")
	raw := strings.ReplaceAll(fixture, "opaque_field = \"keep-me\"\n", "")
	if e = os.WriteFile(p, []byte(raw), 0600); e != nil {
		t.Fatal(e)
	}
	bin := os.Getenv("FRP_TEST_DIR")
	if bin == "" {
		t.Skip("set FRP_TEST_DIR to official FRP executables for integration tests")
	}
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	return &Manager{State: st, Instances: map[string]Instance{"frpc": {ID: "frpc", Role: "frpc", Managed: true, Path: p, Binary: filepath.Join(bin, "frpc"+suffix)}}}
}
func TestOfficialVerifyApplyCancelConflictRestore(t *testing.T) {
	m := testManager(t)
	d, _ := m.Read("frpc")
	candidate := strings.Replace(d.Snapshot().Text, "localPort = 8080", "localPort = 8090", 1)
	cancel, e := m.NewPlan("frpc", "admin", Revision(d.Raw), candidate)
	if e != nil {
		t.Fatal(e)
	}
	if e = m.Cancel(cancel.ID, "admin"); e != nil {
		t.Fatal(e)
	}
	after, _ := m.Read("frpc")
	if after.Raw != d.Raw {
		t.Fatal("preview cancel wrote configuration")
	}
	p, e := m.NewPlan("frpc", "admin", Revision(d.Raw), candidate)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = m.Apply(p.ID, "another-user"); e == nil {
		t.Fatal("plan actor not enforced")
	}
	if s, e := m.Apply(p.ID, "admin"); e != nil || s != "saved_offline" {
		t.Fatal(s, e)
	}
	if _, e = m.Apply(p.ID, "admin"); e != nil {
		t.Fatal("duplicate request should return original result", e)
	}
	after, _ = m.Read("frpc")
	if after.Raw == d.Raw {
		t.Fatal("file not saved")
	}
	restore, e := m.RestorePlan(p.ID, "admin")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = m.Apply(restore.ID, "admin"); e != nil {
		t.Fatal(e)
	}
	after, _ = m.Read("frpc")
	if after.Raw != d.Raw {
		t.Fatal("restore did not reproduce original file")
	}
	conflict, e := m.NewPlan("frpc", "admin", Revision(d.Raw), candidate)
	if e != nil {
		t.Fatal(e)
	}
	external := d.Raw + "\n# external modification\n"
	os.WriteFile(m.Instances["frpc"].Path, []byte(external), 0600)
	if _, e = m.Apply(conflict.ID, "admin"); !errors.Is(e, ErrConflict) {
		t.Fatal("external edit not rejected", e)
	}
	after, _ = m.Read("frpc")
	if after.Raw != external {
		t.Fatal("external edit overwritten")
	}
	var encrypted, backup []byte
	if e = m.State.DB.QueryRow("SELECT candidate,backup FROM plans WHERE id=?", p.ID).Scan(&encrypted, &backup); e != nil {
		t.Fatal(e)
	}
	for _, blob := range [][]byte{encrypted, backup} {
		if strings.Contains(string(blob), "a-long-test-secret") {
			t.Fatal("plaintext secret in database snapshot")
		}
	}
}
func TestAdoptedReadOnlyAndMissingBinary(t *testing.T) {
	m := testManager(t)
	d, _ := m.Read("frpc")
	i := m.Instances["frpc"]
	i.Managed = false
	m.Instances["frpc"] = i
	if _, e := m.NewPlan("frpc", "admin", Revision(d.Raw), d.Snapshot().Text); e == nil {
		t.Fatal("adopted config writable")
	}
	i.Managed = true
	i.Binary = ""
	m.Instances["frpc"] = i
	if _, e := m.NewPlan("frpc", "admin", Revision(d.Raw), d.Snapshot().Text); e == nil {
		t.Fatal("missing binary should not count as verified")
	}
}
func TestStartupReconciliationDoesNotRewrite(t *testing.T) {
	m := testManager(t)
	d, _ := m.Read("frpc")
	p, e := m.NewPlan("frpc", "admin", Revision(d.Raw), strings.Replace(d.Snapshot().Text, "localPort = 8080", "localPort = 8099", 1))
	if e != nil {
		t.Fatal(e)
	}
	m.State.DB.Exec("UPDATE plans SET state='writing',backup=? WHERE id=?", m.State.Seal(d.Raw), p.ID)
	if e = m.Recover(); e != nil {
		t.Fatal(e)
	}
	var status string
	m.State.DB.QueryRow("SELECT state FROM plans WHERE id=?", p.ID).Scan(&status)
	if status != "aborted_before_write" {
		t.Fatal(status)
	}
	after, _ := m.Read("frpc")
	if after.Raw != d.Raw {
		t.Fatal("recovery replayed write")
	}
}
func TestBinaryReplacementRejectsOldPlan(t *testing.T) {
	m := testManager(t)
	d, _ := m.Read("frpc")
	p, e := m.NewPlan("frpc", "admin", Revision(d.Raw), d.Snapshot().Text)
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(m.State.Root, "replacement-binary")
	os.WriteFile(path, []byte("not-the-original-official-binary"), 0600)
	i := m.Instances["frpc"]
	i.Binary = path
	m.Instances["frpc"] = i
	if _, e = m.Apply(p.ID, "admin"); e == nil {
		t.Fatal("binary replacement was accepted")
	}
	after, _ := m.Read("frpc")
	if after.Raw != d.Raw {
		t.Fatal("binary replacement caused a write")
	}
}
