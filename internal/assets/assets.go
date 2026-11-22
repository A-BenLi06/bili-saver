// Package assets owns immutable media objects and their portable identities.
package assets

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"bili-saver/internal/archivepath"
	"bili-saver/internal/fileio"
	"bili-saver/internal/store"
)

type Service struct {
	Store *store.Store
	Root  string
}

func Digest(ctx context.Context, p string) (string, int64, error) {
	var digester Digester
	return digester.Digest(ctx, p)
}

// Digester computes file checksums sequentially using a reusable read buffer.
// Its zero value is ready to use. A Digester is not safe for concurrent calls.
type Digester struct {
	buf []byte
}

func (d *Digester) Digest(ctx context.Context, p string) (string, int64, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	if d.buf == nil {
		d.buf = make([]byte, 256*1024)
	}
	var size int64
	for {
		if err = ctx.Err(); err != nil {
			return "", 0, err
		}
		n, e := f.Read(d.buf)
		if n > 0 {
			h.Write(d.buf[:n])
			size += int64(n)
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return "", 0, e
		}
	}
	return hex.EncodeToString(h.Sum(nil)), size, nil
}

func (s Service) Resolve(a *store.Asset) (string, error) {
	p, err := archivepath.Resolve(s.Root, a.StorageKey)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() || fi.Size() != a.Size {
		return "", fmt.Errorf("asset size mismatch: %s", a.StorageKey)
	}
	return p, nil
}

func (s Service) Verify(ctx context.Context, a *store.Asset) error {
	p, err := s.Resolve(a)
	if err != nil {
		return err
	}
	hash, size, err := Digest(ctx, p)
	if err != nil {
		return err
	}
	if hash != a.SHA256 || size != a.Size {
		return fmt.Errorf("asset checksum mismatch: %s", a.StorageKey)
	}
	return nil
}

// Import preserves its input and publishes a complete immutable object before indexing.
func (s Service) Import(ctx context.Context, p string, a store.Asset) (*store.Asset, error) {
	hash, size, err := Digest(ctx, p)
	if err != nil {
		return nil, err
	}
	if size == 0 {
		return nil, fmt.Errorf("empty asset")
	}
	ext := filepath.Ext(p)
	if ext == ".merging" {
		ext = ".mp4"
	}
	if ext == "" {
		ext = ".bin"
	}
	key := filepath.ToSlash(filepath.Join("media", "objects", hash[:2], hash[2:4], hash+ext))
	out, err := archivepath.Resolve(s.Root, key)
	if err != nil {
		return nil, err
	}
	if fi, e := os.Stat(out); e == nil {
		if !fi.Mode().IsRegular() || fi.Size() != size {
			return nil, fmt.Errorf("existing object is invalid")
		}
		existingHash, _, e := Digest(ctx, out)
		if e != nil || existingHash != hash {
			return nil, fmt.Errorf("existing object checksum mismatch")
		}
	} else if !os.IsNotExist(e) {
		return nil, e
	} else {
		_, err = fileio.Write(out, func(w io.Writer) error {
			f, e := os.Open(p)
			if e != nil {
				return e
			}
			defer f.Close()
			_, e = io.Copy(w, f)
			return e
		})
		if err != nil {
			return nil, err
		}
	}
	a.SHA256, a.Size, a.StorageKey = hash, size, key
	a.CreatedAt, a.VerifiedAt = store.Now(), store.Now()
	if err = s.Store.AddAsset(&a); err != nil {
		return nil, err
	}
	return &a, nil
}

// IndexLegacy records bytes in place; later downloads never overwrite this legacy file.
func (s Service) IndexLegacy(ctx context.Context, p string, a store.Asset) (*store.Asset, error) {
	key, err := archivepath.Key(s.Root, p)
	if err != nil {
		return nil, err
	}
	full, err := archivepath.Resolve(s.Root, key)
	if err != nil {
		return nil, err
	}
	hash, size, err := Digest(ctx, full)
	if err != nil {
		return nil, err
	}
	if size == 0 {
		return nil, fmt.Errorf("empty legacy asset")
	}
	a.StorageKey, a.SHA256, a.Size = key, hash, size
	a.CreatedAt, a.VerifiedAt = store.Now(), store.Now()
	if err = s.Store.AddAsset(&a); err != nil {
		return nil, err
	}
	return &a, nil
}
