package config

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cong86/frpc-ui/internal/filelock"
	"github.com/cong86/frpc-ui/internal/state"
)

type Instance struct {
	ID      string `json:"id"`
	Role    string `json:"role"`
	Managed bool   `json:"managed"`
	Path    string `json:"-"`
	Binary  string `json:"-"`
}
type Manager struct {
	State     *state.Store
	Instances map[string]Instance
	mu        sync.Mutex
}
type Plan struct {
	ID         string `json:"id"`
	Instance   string `json:"instance"`
	Before     string `json:"before"`
	After      string `json:"after"`
	Revision   string `json:"revision"`
	Validation string `json:"validation"`
	Impact     string `json:"impact"`
}

func (m *Manager) Read(id string) (*Document, error) {
	i, ok := m.Instances[id]
	if !ok {
		return nil, errors.New("unknown instance")
	}
	st, e := os.Lstat(i.Path)
	if e != nil {
		return nil, errors.New("configuration unavailable")
	}
	if !st.Mode().IsRegular() || st.Size() > MaxSize {
		return nil, errors.New("configuration must be a regular file below 1 MiB")
	}
	b, e := os.ReadFile(i.Path)
	if e != nil {
		return nil, e
	}
	return Parse(string(b), i.Role)
}
func Verify(binary, role, raw, dir string) error {
	if binary == "" {
		return errors.New("official FRP binary not configured; write plan unavailable")
	}
	f, e := os.CreateTemp(dir, "verify-*.toml")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	_ = f.Chmod(0600)
	if _, e = f.WriteString(raw); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// Only verify is invoked. This never starts a tunnel or contacts a server.
	cmd := exec.CommandContext(ctx, binary, "verify", "-c", name)
	if e = cmd.Run(); e != nil {
		return fmt.Errorf("official %s verify failed; review configuration (raw diagnostic suppressed)", role)
	}
	return nil
}
func binaryRevision(path string) (string, error) {
	if path == "" {
		return "", errors.New("official FRP binary not configured; write plan unavailable")
	}
	st, e := os.Lstat(path)
	if e != nil || !st.Mode().IsRegular() {
		return "", errors.New("official FRP binary must be an available regular file")
	}
	f, e := os.Open(path)
	if e != nil {
		return "", errors.New("official FRP binary unavailable")
	}
	defer f.Close()
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil {
		return "", e
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func (m *Manager) NewPlan(id, actor, revision, candidate string) (Plan, error) {
	i, ok := m.Instances[id]
	if !ok {
		return Plan{}, errors.New("unknown instance")
	}
	if !i.Managed {
		return Plan{}, errors.New("adopted deployments are read-only in this milestone")
	}
	d, e := m.Read(id)
	if e != nil {
		return Plan{}, e
	}
	if Revision(d.Raw) != revision {
		return Plan{}, ErrConflict
	}
	raw, e := d.RestoreMasks(candidate)
	if e != nil {
		return Plan{}, e
	}
	return m.rawPlan(id, actor, d, raw)
}
func (m *Manager) ProxyPlan(id, actor, revision, name string, fields map[string]any, remove bool) (Plan, error) {
	i, ok := m.Instances[id]
	if !ok || !i.Managed {
		return Plan{}, errors.New("instance is read-only")
	}
	d, e := m.Read(id)
	if e != nil {
		return Plan{}, e
	}
	if Revision(d.Raw) != revision {
		return Plan{}, ErrConflict
	}
	raw, e := d.PatchProxy(name, fields, remove)
	if e != nil {
		return Plan{}, e
	}
	return m.rawPlan(id, actor, d, raw)
}
func (m *Manager) rawPlan(id, actor string, d *Document, raw string) (Plan, error) {
	i := m.Instances[id]
	binaryRev, e := binaryRevision(i.Binary)
	if e != nil {
		return Plan{}, e
	}
	next, e := Parse(raw, i.Role)
	if e != nil {
		return Plan{}, e
	}
	if r := next.Editability(); r != "" {
		return Plan{}, errors.New(r)
	}
	if e = Verify(i.Binary, i.Role, raw, m.State.Root); e != nil {
		return Plan{}, e
	}
	if now, e := binaryRevision(i.Binary); e != nil || now != binaryRev {
		return Plan{}, errors.New("FRP binary changed during validation")
	}
	p := Plan{ID: state.ID(), Instance: id, Before: d.Snapshot().Text, After: next.Snapshot().Text, Revision: Revision(d.Raw), Validation: "official_binary_verified", Impact: "Save isolated configuration only. FRP is not started or reloaded; runtime and business remain unverified."}
	_, e = m.State.DB.Exec("INSERT INTO plans(id,instance,actor,revision,candidate,created,state,binary_revision) VALUES(?,?,?,?,?,?,?,?)", p.ID, id, actor, p.Revision, m.State.Seal(raw), time.Now().Unix(), "preview", binaryRev)
	return p, e
}
func atomicWrite(path, raw string) error {
	f, e := os.CreateTemp(filepath.Dir(path), ".console-*")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	_ = f.Chmod(0600)
	if _, e = f.WriteString(raw); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(tmp, path)
}
func (m *Manager) Apply(id, actor string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var inst, owner, revision, status, expectedBinary string
	var enc []byte
	var created int64
	e := m.State.DB.QueryRow("SELECT instance,actor,revision,candidate,created,state,binary_revision FROM plans WHERE id=?", id).Scan(&inst, &owner, &revision, &enc, &created, &status, &expectedBinary)
	if e != nil || owner != actor {
		return "", errors.New("plan unavailable")
	}
	if status == "saved_offline" {
		return status, nil
	}
	if status != "preview" {
		return "", errors.New("operation requires recovery or was canceled")
	}
	if time.Now().Unix()-created > 900 {
		return "", errors.New("plan expired")
	}
	i, ok := m.Instances[inst]
	if !ok || !i.Managed {
		return "", errors.New("instance is read-only")
	}
	lock := i.Path + ".console-lock"
	release, e := filelock.Acquire(lock)
	if e != nil {
		return "", errors.New("configuration is locked by another writer")
	}
	defer release()
	d, e := m.Read(inst)
	if e != nil {
		return "", e
	}
	if Revision(d.Raw) != revision {
		return "", ErrConflict
	}
	if now, e := binaryRevision(i.Binary); e != nil || now != expectedBinary {
		return "", errors.New("FRP binary changed since preview; create a new plan")
	}
	raw, e := m.State.Unseal(enc)
	if e != nil {
		return "", e
	}
	if e = Verify(i.Binary, i.Role, raw, m.State.Root); e != nil {
		return "", e
	}
	if now, e := binaryRevision(i.Binary); e != nil || now != expectedBinary {
		return "", errors.New("FRP binary changed during validation")
	}
	// Last check after validation. Exclusively-owned managed files only.
	again, e := m.Read(inst)
	if e != nil {
		return "", e
	}
	if Revision(again.Raw) != revision {
		return "", ErrConflict
	}
	tx, e := m.State.DB.Begin()
	if e != nil {
		return "", e
	}
	defer tx.Rollback()
	if _, e = tx.Exec("UPDATE plans SET backup=?,state='writing' WHERE id=? AND state='preview'", m.State.Seal(d.Raw), id); e != nil {
		return "", e
	}
	if _, e = tx.Exec("INSERT INTO audit(at,actor,action,instance,result) VALUES(?,?,?,?,?)", time.Now().Unix(), actor, "save", inst, "writing"); e != nil {
		return "", e
	}
	if e = tx.Commit(); e != nil {
		return "", e
	}
	if e = atomicWrite(i.Path, raw); e != nil {
		_, _ = m.State.DB.Exec("UPDATE plans SET state='recovery_required',result='file write failed' WHERE id=?", id)
		return "", errors.New("file write failed; recovery required")
	}
	_, e = m.State.DB.Exec("UPDATE plans SET state='saved_offline',result='runtime unverified' WHERE id=?", id)
	if e != nil {
		return "", errors.New("configuration saved but journal unfinished; recovery required")
	}
	_ = m.State.Audit(actor, "save", inst, "saved_offline")
	return "saved_offline", nil
}
func (m *Manager) Cancel(id, actor string) error {
	res, e := m.State.DB.Exec("UPDATE plans SET state='canceled',candidate=? WHERE id=? AND actor=? AND state='preview'", []byte{}, id, actor)
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errors.New("preview unavailable")
	}
	return nil
}
func (m *Manager) RestorePlan(operation, actor string) (Plan, error) {
	var inst, status string
	var enc []byte
	e := m.State.DB.QueryRow("SELECT instance,state,backup FROM plans WHERE id=?", operation).Scan(&inst, &status, &enc)
	if e != nil || len(enc) == 0 {
		return Plan{}, errors.New("backup unavailable")
	}
	if status != "saved_offline" && status != "recovery_required" {
		return Plan{}, errors.New("operation is not restorable")
	}
	raw, e := m.State.Unseal(enc)
	if e != nil {
		return Plan{}, e
	}
	d, e := m.Read(inst)
	if e != nil {
		return Plan{}, e
	}
	return m.newRestoredPlan(inst, actor, d, raw)
}
func (m *Manager) newRestoredPlan(inst, actor string, d *Document, raw string) (Plan, error) {
	if !m.Instances[inst].Managed {
		return Plan{}, errors.New("instance read-only")
	}
	return m.rawPlan(inst, actor, d, raw)
}
func (m *Manager) Recover() error {
	rows, e := m.State.DB.Query("SELECT id,instance,revision,candidate FROM plans WHERE state='writing'")
	if e != nil {
		return e
	}
	type pending struct {
		id, inst, rev string
		enc           []byte
	}
	list := []pending{}
	for rows.Next() {
		var p pending
		if e = rows.Scan(&p.id, &p.inst, &p.rev, &p.enc); e != nil {
			rows.Close()
			return e
		}
		list = append(list, p)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, p := range list {
		status := "recovery_required"
		d, e := m.Read(p.inst)
		raw, de := m.State.Unseal(p.enc)
		if e == nil && de == nil {
			switch Revision(d.Raw) {
			case Revision(raw):
				status = "saved_offline"
			case p.rev:
				status = "aborted_before_write"
			}
		}
		if _, e = m.State.DB.Exec("UPDATE plans SET state=?,result='startup reconciliation' WHERE id=?", status, p.id); e != nil {
			return e
		}
	}
	return nil
}
func MaskError(e error) string {
	if errors.Is(e, ErrConflict) {
		return ErrConflict.Error()
	}
	s := e.Error()
	if strings.Contains(s, "verify") {
		return s
	}
	return s
}
