package archiveops

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"bili-saver/internal/archivepath"
)

// A verifier belongs to one offline check. Reset the decoders for each file
// and read through EOF so gzip trailers and the SHA-256 are both checked.
type snapshotVerifier struct {
	input bufio.Reader
	gzip  gzip.Reader
	copy  []byte
}

func (v *snapshotVerifier) verify(ctx context.Context, root, key, want string) error {
	p, err := archivepath.Resolve(root, key)
	if err != nil {
		return err
	}
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	v.input.Reset(f)
	if err := v.gzip.Reset(&v.input); err != nil {
		return err
	}
	defer v.gzip.Close()
	if v.copy == nil {
		v.copy = make([]byte, 32*1024)
	}
	h := sha256.New()
	if _, err := io.CopyBuffer(h, contextReader{ctx, &v.gzip}, v.copy); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != want {
		return fmt.Errorf("raw response checksum mismatch")
	}
	return nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
