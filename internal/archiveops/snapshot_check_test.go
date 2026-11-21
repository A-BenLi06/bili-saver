package archiveops

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestSnapshotVerifierResetsAfterCorruptionAndCancellation(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "raw"), 0700); err != nil {
		t.Fatal(err)
	}
	body := []byte("原始记录")
	var good bytes.Buffer
	z := gzip.NewWriter(&good)
	if _, err := z.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	bad := bytes.Clone(good.Bytes())
	bad[len(bad)-8] ^= 1
	for key, data := range map[string][]byte{"good.gz": good.Bytes(), "crc.gz": bad, "header.gz": []byte("invalid header")} {
		if err := os.WriteFile(filepath.Join(root, "raw", key), data, 0600); err != nil {
			t.Fatal(err)
		}
	}

	ctx := context.Background()
	want := fmt.Sprintf("%x", sha256.Sum256(body))
	var verify snapshotVerifier
	if err := verify.verify(ctx, root, "raw/header.gz", want); !errors.Is(err, gzip.ErrHeader) {
		t.Fatalf("bad header accepted: %v", err)
	}
	if err := verify.verify(ctx, root, "raw/crc.gz", want); !errors.Is(err, gzip.ErrChecksum) {
		t.Fatalf("bad gzip trailer accepted: %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := verify.verify(canceled, root, "raw/good.gz", want); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation ignored: %v", err)
	}
	if err := verify.verify(ctx, root, "raw/good.gz", want); err != nil {
		t.Fatalf("valid snapshot after failures: %v", err)
	}
}
