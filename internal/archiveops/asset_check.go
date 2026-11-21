package archiveops

import (
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"os"
	"slices"

	"bili-saver/internal/archivepath"
	"bili-saver/internal/assets"
)

// Group identical keys so a shared file is read once during the locked check.
// Size and checksum expectations still apply to each asset row independently.
func checkAssets(ctx context.Context, db *sql.DB, root string, report *Report) error {
	rows, err := db.QueryContext(ctx, `SELECT id,storage_key,sha256,size FROM archive_assets ORDER BY storage_key,id`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type assetIssue struct {
		id    int64
		issue Issue
	}
	var issues []assetIssue
	var current checkedAssetFile
	var digester assets.Digester
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var id, wantSize int64
		var key, wantHash string
		if err := rows.Scan(&id, &key, &wantHash, &wantSize); err != nil {
			return err
		}
		if current.key != key || !current.initialized {
			current = inspectAssetFile(root, key)
		}
		if err := current.verify(ctx, &digester, wantHash, wantSize); err != nil {
			issues = append(issues, assetIssue{id, Issue{"asset", key, err.Error()}})
		}
		report.FilesChecked++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	// Preserve the original order of asset issues even though reads are grouped.
	slices.SortFunc(issues, func(a, b assetIssue) int { return cmp.Compare(a.id, b.id) })
	for _, entry := range issues {
		report.Issues = append(report.Issues, entry.issue)
	}
	return nil
}

type checkedAssetFile struct {
	key, path, digest   string
	info                os.FileInfo
	digestSize          int64
	statErr, digestErr  error
	initialized, hashed bool
}

func inspectAssetFile(root, key string) checkedAssetFile {
	f := checkedAssetFile{key: key, initialized: true}
	f.path, f.statErr = archivepath.Resolve(root, key)
	if f.statErr == nil {
		f.info, f.statErr = os.Stat(f.path)
	}
	return f
}

func (f *checkedAssetFile) verify(ctx context.Context, digester *assets.Digester, wantHash string, wantSize int64) error {
	if f.statErr != nil {
		return f.statErr
	}
	if !f.info.Mode().IsRegular() || f.info.Size() != wantSize {
		return fmt.Errorf("asset size mismatch: %s", f.key)
	}
	if !f.hashed {
		f.digest, f.digestSize, f.digestErr = digester.Digest(ctx, f.path)
		f.hashed = true
	}
	if f.digestErr != nil {
		return f.digestErr
	}
	if f.digest != wantHash || f.digestSize != wantSize {
		return fmt.Errorf("asset checksum mismatch: %s", f.key)
	}
	return nil
}
