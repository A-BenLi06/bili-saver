package archiveops

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"bili-saver/internal/archiveguard"
	"bili-saver/internal/archivepath"
	"bili-saver/internal/assets"
	"bili-saver/internal/store"
	sqlite "modernc.org/sqlite"
)

func Backup(ctx context.Context, root, destination string) (*Manifest, error) {
	if _, err := os.Stat(filepath.Join(root, "bili-saver.db")); err != nil {
		return nil, err
	}
	lock, err := archiveguard.Acquire(root)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	return backupLocked(ctx, root, destination)
}

func backupLocked(ctx context.Context, root, destination string) (*Manifest, error) {
	source, err := archivepath.Canonical(root)
	if err != nil {
		return nil, err
	}
	destination, err = archivepath.Canonical(destination)
	if err != nil {
		return nil, err
	}
	if archivepath.Within(source, destination) || archivepath.Within(destination, source) {
		return nil, fmt.Errorf("backup destination must be outside the source archive")
	}
	stage, err := newStage(destination)
	if err != nil {
		return nil, err
	}
	published := false
	defer func() {
		if !published {
			os.RemoveAll(stage)
		}
	}()
	db, err := openReadOnly(ctx, source)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if err = backupDatabase(ctx, db, filepath.Join(stage, "bili-saver.db")); err != nil {
		return nil, err
	}
	m := &Manifest{Version: FormatVersion, Purpose: "private-backup", Sensitive: true, CreatedAt: timestamp(), Files: []File{}}
	if err = db.QueryRowContext(ctx, `SELECT CAST(v AS INTEGER) FROM schema_meta WHERE k='schema'`).Scan(&m.Schema); err != nil {
		return nil, err
	}
	keys := []string{"bili-saver.db"}
	// Full private backups include original/derived bytes and private root state.
	for _, name := range []string{"raw", "media", "derived", "plugins"} {
		start := filepath.Join(source, name)
		if _, e := os.Lstat(start); os.IsNotExist(e) {
			continue
		} else if e != nil {
			return nil, e
		}
		err = filepath.WalkDir(start, func(p string, d os.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if d.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("backup refuses unmanaged symlink: %s", p)
			}
			if d.IsDir() {
				return nil
			}
			info, e := d.Info()
			if e != nil {
				return e
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("unsupported archive file %s", p)
			}
			rel, e := filepath.Rel(source, p)
			if e != nil {
				return e
			}
			keys = append(keys, filepath.ToSlash(rel))
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	for _, name := range []string{"cookies.json", "credentials.json", "bootstrap-credentials.json"} {
		if fi, e := os.Stat(filepath.Join(source, name)); e == nil && fi.Mode().IsRegular() {
			keys = append(keys, name)
		} else if e != nil && !os.IsNotExist(e) {
			return nil, e
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		target := filepath.Join(stage, filepath.FromSlash(key))
		if key != "bili-saver.db" {
			from, e := archivepath.Resolve(source, key)
			if e != nil {
				return nil, e
			}
			if e = copyExclusive(ctx, from, target); e != nil {
				return nil, e
			}
		}
		hash, size, e := assets.Digest(ctx, target)
		if e != nil {
			return nil, e
		}
		m.Files = append(m.Files, File{key, hash, size})
	}
	if err = writeJSON(filepath.Join(stage, "manifest.json"), m); err != nil {
		return nil, err
	}
	if err = verifyBackup(ctx, stage, *m); err != nil {
		return nil, err
	}
	if err = os.Rename(stage, destination); err != nil {
		return nil, err
	}
	published = true
	return m, nil
}

func backupDatabase(ctx context.Context, db *sql.DB, destination string) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	return conn.Raw(func(raw any) error {
		provider, ok := raw.(interface {
			NewBackup(string) (*sqlite.Backup, error)
		})
		if !ok {
			return fmt.Errorf("SQLite driver lacks consistent backup support")
		}
		backup, err := provider.NewBackup(destination)
		if err != nil {
			return err
		}
		finished := false
		defer func() {
			if !finished {
				_ = backup.Finish()
			}
		}()
		for {
			if err = ctx.Err(); err != nil {
				return err
			}
			more, e := backup.Step(256)
			if e != nil {
				return e
			}
			if !more {
				finished = true
				return backup.Finish()
			}
		}
	})
}

func newStage(destination string) (string, error) {
	if _, err := os.Lstat(destination); err == nil {
		return "", fmt.Errorf("destination already exists; choose a new directory")
	} else if !os.IsNotExist(err) {
		return "", err
	}
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(parent, ".bili-restore-*")
	if err != nil {
		return "", err
	}
	if !archivepath.Within(parent, stage) || stage == parent {
		return "", fmt.Errorf("invalid staging path")
	}
	return stage, nil
}

func copyExclusive(ctx context.Context, source, destination string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer out.Close()
	buf := make([]byte, 256*1024)
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		n, e := in.Read(buf)
		if n > 0 {
			if _, err = out.Write(buf[:n]); err != nil {
				return err
			}
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
	}
	return out.Sync()
}

func verifyBackup(ctx context.Context, root string, m Manifest) error {
	seen := map[string]bool{}
	database := false
	for _, entry := range m.Files {
		key, err := archivepath.Key(root, entry.Key)
		if err != nil || key != entry.Key || seen[strings.ToLower(key)] {
			return fmt.Errorf("invalid or duplicate manifest key %q", entry.Key)
		}
		allowed := key == "bili-saver.db" || key == "cookies.json" || key == "credentials.json" || key == "bootstrap-credentials.json"
		for _, prefix := range []string{"raw/", "media/", "derived/", "plugins/"} {
			allowed = allowed || strings.HasPrefix(key, prefix)
		}
		if !allowed {
			return fmt.Errorf("unsupported backup key %q", key)
		}
		seen[strings.ToLower(key)] = true
		if key == "bili-saver.db" {
			database = true
		}
		p, err := archivepath.Resolve(root, key)
		if err != nil {
			return err
		}
		hash, size, err := assets.Digest(ctx, p)
		if err != nil {
			return err
		}
		if hash != entry.SHA256 || size != entry.Size {
			return fmt.Errorf("backup checksum mismatch: %s", key)
		}
	}
	if !database {
		return fmt.Errorf("backup has no database")
	}
	db, err := openReadOnly(ctx, root)
	if err != nil {
		return err
	}
	defer db.Close()
	var schema int
	if err = db.QueryRowContext(ctx, `SELECT CAST(v AS INTEGER) FROM schema_meta WHERE k='schema'`).Scan(&schema); err != nil {
		return err
	}
	if schema > store.SchemaVersion || schema != m.Schema {
		return fmt.Errorf("unsupported or inconsistent backup schema %d", schema)
	}
	var result string
	if err = db.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("backup database integrity check: %s", result)
	}
	return nil
}

func Restore(ctx context.Context, backupRoot, destination string) error {
	m, err := readManifest(backupRoot)
	if err != nil {
		return err
	}
	if err = verifyBackup(ctx, backupRoot, m); err != nil {
		return err
	}
	destination, err = archivepath.Canonical(destination)
	if err != nil {
		return err
	}
	source, err := archivepath.Canonical(backupRoot)
	if err != nil {
		return err
	}
	if archivepath.Within(source, destination) || archivepath.Within(destination, source) {
		return fmt.Errorf("restore destination overlaps backup")
	}
	stage, err := newStage(destination)
	if err != nil {
		return err
	}
	published := false
	defer func() {
		if !published {
			os.RemoveAll(stage)
		}
	}()
	for _, entry := range m.Files {
		from, err := archivepath.Resolve(source, entry.Key)
		if err != nil {
			return err
		}
		if err = copyExclusive(ctx, from, filepath.Join(stage, filepath.FromSlash(entry.Key))); err != nil {
			return err
		}
	}
	if err = verifyBackup(ctx, stage, m); err != nil {
		return err
	}
	if err = relocatePaths(ctx, stage, source, destination); err != nil {
		return err
	}
	if err = os.Rename(stage, destination); err != nil {
		return err
	}
	published = true
	return nil
}

// Relocate creates and verifies a portable copy. The source is deliberately retained.
func Relocate(ctx context.Context, root, destination string) error {
	lock, err := archiveguard.Acquire(root)
	if err != nil {
		return err
	}
	defer lock.Close()
	parent, err := os.MkdirTemp("", "bili-relocate-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(parent)
	backupRoot := filepath.Join(parent, "backup")
	if _, err = backupLocked(ctx, root, backupRoot); err != nil {
		return err
	}
	return Restore(ctx, backupRoot, destination)
}
