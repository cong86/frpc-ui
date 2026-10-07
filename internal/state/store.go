package state

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	DB   *sql.DB
	aead cipher.AEAD
	Root string
}

func ID() string {
	b := make([]byte, 24)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func Open(root string) (*Store, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	keyPath := filepath.Join(root, "backup.key")
	key, err := os.ReadFile(keyPath)
	if os.IsNotExist(err) {
		// Never replace a missing key belonging to an existing database.
		if _, e := os.Stat(filepath.Join(root, "console.db")); e == nil {
			return nil, errors.New("备份密钥缺失；启动前请恢复原密钥")
		}
		key = make([]byte, 32)
		if _, err = rand.Read(key); err != nil {
			return nil, err
		}
		err = os.WriteFile(keyPath, key, 0600)
	}
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, errors.New("备份密钥无效")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(root, "console.db"))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;
 CREATE TABLE IF NOT EXISTS users(name TEXT PRIMARY KEY, password BLOB NOT NULL);
 CREATE TABLE IF NOT EXISTS sessions(hash TEXT PRIMARY KEY, user TEXT NOT NULL, csrf TEXT NOT NULL, expires INTEGER NOT NULL);
 CREATE TABLE IF NOT EXISTS plans(id TEXT PRIMARY KEY, instance TEXT NOT NULL, actor TEXT NOT NULL, revision TEXT NOT NULL, candidate BLOB NOT NULL, created INTEGER NOT NULL, state TEXT NOT NULL, backup BLOB, result TEXT NOT NULL DEFAULT '');
 CREATE TABLE IF NOT EXISTS audit(id INTEGER PRIMARY KEY, at INTEGER NOT NULL, actor TEXT NOT NULL, action TEXT NOT NULL, instance TEXT NOT NULL, result TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS metadata(key TEXT PRIMARY KEY,value TEXT NOT NULL);`)
	if err != nil {
		db.Close()
		return nil, err
	}
	_ = os.Chmod(filepath.Join(root, "console.db"), 0600)
	var hasBinaryRevision int
	if err = db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('plans') WHERE name='binary_revision'").Scan(&hasBinaryRevision); err != nil {
		db.Close()
		return nil, err
	}
	if hasBinaryRevision == 0 {
		if _, err = db.Exec("ALTER TABLE plans ADD COLUMN binary_revision TEXT NOT NULL DEFAULT ''"); err != nil {
			db.Close()
			return nil, err
		}
	}
	return &Store{DB: db, aead: aead, Root: root}, nil
}
func (s *Store) Seal(text string) []byte {
	n := make([]byte, s.aead.NonceSize())
	if _, e := rand.Read(n); e != nil {
		panic(e)
	}
	return s.aead.Seal(n, n, []byte(text), nil)
}
func (s *Store) Unseal(b []byte) (string, error) {
	n := s.aead.NonceSize()
	if len(b) < n {
		return "", errors.New("加密快照无效")
	}
	p, e := s.aead.Open(nil, b[:n], b[n:], nil)
	return string(p), e
}
func (s *Store) Audit(actor, action, instance, result string) error {
	_, e := s.DB.Exec("INSERT INTO audit(at,actor,action,instance,result) VALUES(?,?,?,?,?)", time.Now().Unix(), actor, action, instance, result)
	return e
}
