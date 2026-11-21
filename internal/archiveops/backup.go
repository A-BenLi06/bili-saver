package archiveops

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"bili-saver/internal/archiveguard"
	"bili-saver/internal/archivepath"
	"bili-saver/internal/assets"
	"bili-saver/internal/config"
	"bili-saver/internal/fileio"
	"bili-saver/internal/store"
	sqlite "modernc.org/sqlite"
)

func Backup(ctx context.Context, root, destination string) (*Manifest, error) {
	return backup(ctx, root, destination, nil)
}
func BackupWithConfig(ctx context.Context, cfg *config.Config, destination string) (*Manifest, error) {
	return backup(ctx, cfg.View().DataDir, destination, cfg)
}
func backup(ctx context.Context, root, destination string, cfg *config.Config) (*Manifest, error) {
	if _, err := os.Stat(filepath.Join(root, "bili-saver.db")); err != nil {
		return nil, err
	}
	lock, err := archiveguard.Acquire(root)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	return backupLocked(ctx, root, destination, cfg)
}

func backupLocked(ctx context.Context, root, destination string, startup ...*config.Config) (*Manifest, error) {
	var cfg *config.Config
	if len(startup) > 0 {
		cfg = startup[0]
	}
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
	if cfg != nil && hasTable(db, "runtime_settings") {
		cfg = cfg.Clone()
		values, _, e := (&store.Store{DB: db}).RuntimeSettings()
		if e != nil {
			return nil, e
		}
		if e = cfg.ApplyOverrides(values); e != nil {
			return nil, e
		}
	}
	if err = backupDatabase(ctx, db, filepath.Join(stage, "bili-saver.db")); err != nil {
		return nil, err
	}
	m := &Manifest{Version: FormatVersion, Purpose: "private-backup", Sensitive: true, CreatedAt: timestamp(), Files: []File{}}
	if err = db.QueryRowContext(ctx, `SELECT CAST(v AS INTEGER) FROM schema_meta WHERE k='schema'`).Scan(&m.Schema); err != nil {
		return nil, err
	}
	keys := []string{"bili-saver.db"}
	sources := map[string]string{}
	prepared := map[string]bool{"bili-saver.db": true}
	if cfg != nil {
		if err = config.SaveConfigFile(filepath.Join(stage, "startup-config.yaml"), cfg); err != nil {
			return nil, err
		}
		keys = append(keys, "startup-config.yaml")
		prepared["startup-config.yaml"] = true
	}

	// Full private backups include original/derived bytes and private root state.
	for _, name := range []string{"raw", "media", "derived", "plugins"} {
		start := filepath.Join(source, name)
		if name == "plugins" && cfg != nil && cfg.View().Plugins.Dir != "" {
			start = cfg.View().Plugins.Dir
		}
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
			rel, e := filepath.Rel(start, p)
			if e != nil {
				return e
			}
			key := name + "/" + filepath.ToSlash(rel)
			keys = append(keys, key)
			sources[key] = p
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	for _, name := range []string{"cookies.json", "credentials.json", "bootstrap-credentials.json"} {
		from := filepath.Join(source, name)
		if name == "cookies.json" && cfg != nil && cfg.View().CookiePath() != "" {
			from = cfg.View().CookiePath()
		}
		if fi, e := os.Lstat(from); e == nil {
			if !fi.Mode().IsRegular() {
				return nil, fmt.Errorf("private root file must be regular: %s", name)
			}
			keys = append(keys, name)
			sources[name] = from
		} else if !os.IsNotExist(e) {
			return nil, e
		}
	}
	sort.Strings(keys)
	copyBuffer := make([]byte, 256*1024)
	digest := sha256.New()
	for _, key := range keys {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		target := filepath.Join(stage, filepath.FromSlash(key))
		var checksum string
		var size int64
		if prepared[key] {
			checksum, size, err = assets.Digest(ctx, target)
		} else {
			from := sources[key]
			if from == "" {
				return nil, fmt.Errorf("missing backup source mapping %s", key)
			}
			// Hash the bytes copied from the source. verifyBackup independently
			// reads the staged file before publication.
			digest.Reset()
			size, err = copyExclusive(ctx, from, target, copyBuffer, digest)
			checksum = hex.EncodeToString(digest.Sum(nil))
		}
		if err != nil {
			return nil, err
		}
		m.Files = append(m.Files, File{key, checksum, size})
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
	if err = fileio.ProtectPrivate(stage); err != nil {
		os.Remove(stage)
		return "", err
	}
	return stage, nil
}

// copyExclusive copies a new private file using the caller's scratch buffer.
func copyExclusive(ctx context.Context, source, destination string, buf []byte, digest hash.Hash) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return 0, err
	}
	in, err := os.Open(source)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return 0, err
	}
	defer out.Close()

	var size int64
	for {
		if err = ctx.Err(); err != nil {
			return 0, err
		}
		n, readErr := in.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			if _, err = out.Write(chunk); err != nil {
				return 0, err
			}
			if digest != nil {
				digest.Write(chunk)
			}
			size += int64(n)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return 0, readErr
		}
	}
	if err := out.Sync(); err != nil {
		return 0, err
	}
	return size, out.Close()
}

func verifyBackup(ctx context.Context, root string, m Manifest) error {
	seen := map[string]bool{}
	database := false
	for _, entry := range m.Files {
		key, err := archivepath.Key(root, entry.Key)
		if err != nil || key != entry.Key || seen[strings.ToLower(key)] {
			return fmt.Errorf("invalid or duplicate manifest key %q", entry.Key)
		}
		allowed := key == "bili-saver.db" || key == "cookies.json" || key == "credentials.json" || key == "bootstrap-credentials.json" || key == "startup-config.yaml"
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
	copyBuffer := make([]byte, 256*1024)
	for _, entry := range m.Files {
		from, err := archivepath.Resolve(source, entry.Key)
		if err != nil {
			return err
		}
		if _, err = copyExclusive(ctx, from, filepath.Join(stage, filepath.FromSlash(entry.Key)), copyBuffer, nil); err != nil {
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
	return relocate(ctx, root, destination, nil)
}
func RelocateWithConfig(ctx context.Context, cfg *config.Config, destination string) error {
	return relocate(ctx, cfg.View().DataDir, destination, cfg)
}
func relocate(ctx context.Context, root, destination string, cfg *config.Config) error {
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
	if _, err = backupLocked(ctx, root, backupRoot, cfg); err != nil {
		return err
	}
	return Restore(ctx, backupRoot, destination)
}
