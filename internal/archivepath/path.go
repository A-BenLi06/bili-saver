// Package archivepath translates portable archive keys to local filesystem paths.
package archivepath

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Key accepts historical Windows separators and data-root prefixes. All new keys
// are slash-separated and relative to the archive, never to the working directory.
func Key(root, value string) (string, error) {
	v := strings.ReplaceAll(value, "\\", "/")
	if v == "" {
		return "", fmt.Errorf("empty archive path")
	}
	for part := range strings.SplitSeq(v, "/") {
		if part == ".." {
			return "", fmt.Errorf("archive path traversal")
		}
	}
	r := strings.TrimSuffix(strings.ReplaceAll(root, "\\", "/"), "/")
	if strings.HasPrefix(v, r+"/") {
		v = strings.TrimPrefix(v, r+"/")
	}
	v = strings.TrimPrefix(v, "./")
	base := path.Base(r)
	if base != "." && strings.HasPrefix(v, base+"/") {
		v = strings.TrimPrefix(v, base+"/")
	}
	// Historical absolute paths retain their archive subtree after relocation.
	if strings.HasPrefix(v, "/") || len(v) > 2 && v[1] == ':' {
		found := -1
		for _, prefix := range []string{"/media/", "/raw/", "/derived/"} {
			if i := strings.Index(v, prefix); i >= 0 && (found < 0 || i < found) {
				found = i
			}
		}
		if found < 0 {
			return "", fmt.Errorf("path is outside the archive")
		}
		v = v[found+1:]
	}
	if !strings.HasPrefix(v, "media/") && !strings.HasPrefix(v, "raw/") && !strings.HasPrefix(v, "derived/") {
		for _, anchor := range []string{"/media/", "/raw/", "/derived/"} {
			if i := strings.Index(v, anchor); i >= 0 {
				v = v[i+1:]
				break
			}
		}
	}
	v = path.Clean(v)
	if v == "." || strings.HasPrefix(v, "/") || strings.Contains(v, ":") {
		return "", fmt.Errorf("invalid archive key")
	}
	return v, nil
}

func Resolve(root, key string) (string, error) {
	k, err := Key(root, key)
	if err != nil {
		return "", err
	}
	r, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	p := filepath.Join(r, filepath.FromSlash(k))
	if !Within(r, p) {
		return "", fmt.Errorf("path is outside archive root")
	}
	// Check existing ancestors as well, so missing output files cannot escape via a link.
	physicalRoot, err := filepath.EvalSymlinks(r)
	if err != nil {
		return "", err
	}
	ancestor := p
	for {
		if _, err := os.Lstat(ancestor); err == nil {
			physical, e := filepath.EvalSymlinks(ancestor)
			if e != nil || !Within(physicalRoot, physical) {
				return "", fmt.Errorf("archive path crosses a symbolic link")
			}
			break
		} else if !os.IsNotExist(err) {
			return "", err
		}
		next := filepath.Dir(ancestor)
		if next == ancestor {
			return "", fmt.Errorf("archive root not found")
		}
		ancestor = next
	}
	return p, nil
}

func Within(root, value string) bool {
	rel, err := filepath.Rel(root, value)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// Canonical resolves existing ancestors without creating a destination.
func Canonical(value string) (string, error) {
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	ancestor := absolute
	for {
		if _, err = os.Lstat(ancestor); err == nil {
			physical, e := filepath.EvalSymlinks(ancestor)
			if e != nil {
				return "", e
			}
			relative, e := filepath.Rel(ancestor, absolute)
			if e != nil {
				return "", e
			}
			return filepath.Join(physical, relative), nil
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", err
		}
		ancestor = parent
	}
}
