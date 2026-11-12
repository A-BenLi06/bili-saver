package media

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"bili-saver/internal/archivepath"
	"bili-saver/internal/assets"
	"bili-saver/internal/store"
)

func ValidateRoot(dataRoot string, lib *store.MediaLibrary, libs []*store.MediaLibrary) error {
	root, err := archivepath.Canonical(lib.RootPath)
	if err != nil {
		return err
	}
	lib.RootPath = root
	data, err := archivepath.Canonical(dataRoot)
	if err != nil {
		return err
	}
	if archivepath.Within(root, data) {
		return fmt.Errorf("library output cannot contain the archive root")
	}
	protected := []string{"media", "raw", "derived", "plugins", "tmp", "teriteri"}
	for _, name := range protected {
		p, err := archivepath.Canonical(filepath.Join(data, name))
		if err != nil {
			return err
		}
		if archivepath.Within(p, root) || archivepath.Within(root, p) {
			return fmt.Errorf("library overlaps protected archive directory %s", name)
		}
	}
	for _, other := range libs {
		if other.ID == lib.ID || !other.Enabled {
			continue
		}
		p, err := archivepath.Canonical(other.RootPath)
		if err != nil {
			return err
		}
		if archivepath.Within(p, root) || archivepath.Within(root, p) {
			return fmt.Errorf("library root overlaps library %s", other.Name)
		}
	}
	return nil
}

// Output paths may be leaf symlinks, but no directory ancestor may escape root.
func outputPath(root, key string) (string, error) {
	if key == "" || filepath.IsAbs(key) || strings.ContainsAny(key, `\:`) {
		return "", fmt.Errorf("invalid output key")
	}
	for _, p := range strings.Split(key, "/") {
		if p == ".." || p == "." || p == "" {
			return "", fmt.Errorf("invalid output component")
		}
	}
	target := filepath.Join(root, filepath.FromSlash(key))
	parent, err := archivepath.Canonical(filepath.Dir(target))
	if err != nil {
		return "", err
	}
	canonical, err := archivepath.Canonical(root)
	if err != nil {
		return "", err
	}
	if !archivepath.Within(canonical, parent) {
		return "", fmt.Errorf("output ancestor escapes library root")
	}
	return target, nil
}
func fingerprint(path, hint string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		return "symlink:" + target, err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("output is not a regular file")
	}
	if strings.HasPrefix(hint, "hard:") {
		target := strings.TrimPrefix(hint, "hard:")
		if original, e := os.Stat(target); e == nil && os.SameFile(info, original) {
			return hint, nil
		}
	}
	hash, _, err := assets.Digest(context.Background(), path)
	return "sha256:" + hash, err
}

func (b *Bridge) publishProjection(lib *store.MediaLibrary, stage string, sources map[string]string) error {
	root, err := archivepath.Canonical(lib.RootPath)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(root, 0755); err != nil {
		return err
	}
	previous, err := b.St.LibraryOutputs(lib.ID, root)
	if err != nil {
		return err
	}
	wanted := map[string]string{}
	paths := map[string]string{}
	err = filepath.WalkDir(stage, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		key, err := filepath.Rel(stage, path)
		if err != nil {
			return err
		}
		key = filepath.ToSlash(key)
		hint := ""
		if source := sources[path]; source != "" {
			hint = "hard:" + source
		}
		var fp string
		if b.projectionCopies[path] {
			hash, _, e := assets.Digest(context.Background(), sources[path])
			fp = "sha256:" + hash
			err = e
		} else {
			fp, err = fingerprint(path, hint)
		}
		if err != nil {
			return err
		}
		wanted[key] = fp
		paths[key] = path
		return nil
	})
	if err != nil {
		return err
	}
	// Check all conflicts before publishing any new files.
	keys := make([]string, 0, len(wanted))
	checked := map[string]string{}
	for key, fp := range wanted {
		target, err := outputPath(root, key)
		if err != nil {
			return err
		}
		old := previous[key]
		prior, e := matchingFingerprint(target, old.Fingerprint, old.Previous, fp)
		checked[key] = prior
		if e != nil && !os.IsNotExist(e) {
			return fmt.Errorf("unmanaged or modified library output %s: %w", target, e)
		}

		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		target, err := outputPath(root, key)
		if err != nil {
			return err
		}
		fp := wanted[key]
		prior := checked[key]
		if err = b.St.OutputIntent(lib.ID, root, key, fp, prior); err != nil {
			return err
		}
		if prior != fp {
			if err = os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			if b.projectionCopies[paths[key]] {
				err = copyFile(sources[paths[key]], target)
			} else {
				err = publishFile(paths[key], target)
			}
			if err != nil {
				return err
			}
		}

		if err = b.St.OutputPublished(lib.ID, root, key); err != nil {
			return err
		}
	}
	// Remove only previously owned leaves that still match their recorded identity.
	for key, old := range previous {
		if _, keep := wanted[key]; keep {
			continue
		}
		target, err := outputPath(root, key)
		if err != nil {
			return err
		}
		fp, e := matchingFingerprint(target, old.Fingerprint, old.Previous)
		if e == nil {
			if fp != old.Fingerprint && fp != old.Previous {
				return fmt.Errorf("modified stale output retained: %s", target)
			}
			if err = os.Remove(target); err != nil {
				return err
			}
		} else if !os.IsNotExist(e) {
			return e
		}
		if err = b.St.ForgetOutput(lib.ID, root, key); err != nil {
			return err
		}
	}
	return nil
}
func publishFile(source, target string) error {
	f, err := os.CreateTemp(filepath.Dir(target), ".publish-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	f.Close()
	defer os.Remove(tmp)
	if err = os.Remove(tmp); err != nil {
		return err
	}
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		link, err := os.Readlink(source)
		if err != nil {
			return err
		}
		if err = os.Symlink(link, tmp); err != nil {
			return err
		}
	} else if err = os.Link(source, tmp); err != nil {
		if err = copyFile(source, tmp); err != nil {
			return err
		}
	}
	return os.Rename(tmp, target)
}

func matchingFingerprint(path string, identities ...string) (string, error) {
	if _, err := os.Lstat(path); err != nil {
		return "", err
	}
	for _, identity := range identities {
		if identity == "" {
			continue
		}
		actual, err := fingerprint(path, identity)
		if err != nil {
			return "", err
		}
		if actual == identity {
			return identity, nil
		}
	}
	return "", fmt.Errorf("file identity does not match ownership record")
}
