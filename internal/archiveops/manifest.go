// Package archiveops provides offline recovery and portable archive catalogs.
package archiveops

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"bili-saver/internal/store"
)

const FormatVersion = 1

type File struct {
	Key    string `json:"key"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}
type Manifest struct {
	Version   int    `json:"version"`
	Purpose   string `json:"purpose"`
	Sensitive bool   `json:"sensitive"`
	CreatedAt string `json:"created_at"`
	Schema    int    `json:"schema"`
	Files     []File `json:"files"`
}

type CatalogAsset struct {
	Bvid       string `json:"bvid"`
	Cid        int64  `json:"cid"`
	Role       string `json:"role"`
	Origin     string `json:"origin"`
	Variant    string `json:"variant"`
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
	Provenance string `json:"provenance"`
	ObservedAt int64  `json:"observed_at"`
}
type Catalog struct {
	Version int            `json:"version"`
	Kind    string         `json:"kind"`
	Assets  []CatalogAsset `json:"assets"`
}

// Catalog excludes node paths, credentials, users, quotas and operational state.
func ExportCatalog(st *store.Store) (Catalog, error) {
	if !hasTable(st.DB, "archive_assets") {
		return Catalog{}, fmt.Errorf("content catalog requires the asset-index migration; start the upgraded application on a verified archive copy first")
	}
	items, err := st.Assets("")
	if err != nil {
		return Catalog{}, err
	}
	out := Catalog{Version: FormatVersion, Kind: "archive-content-catalog", Assets: []CatalogAsset{}}
	for _, a := range items {
		out.Assets = append(out.Assets, CatalogAsset{a.Bvid, a.Cid, a.Role, a.Origin, a.Variant, a.SHA256, a.Size, a.Provenance, a.CreatedAt})
	}
	return out, nil
}

func openReadOnly(ctx context.Context, root string) (*sql.DB, error) {
	p, err := filepath.Abs(filepath.Join(root, "bili-saver.db"))
	if err != nil {
		return nil, err
	}
	if _, err = os.Stat(p); err != nil {
		return nil, err
	}
	u, err := store.DatabaseURL(p)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("mode", "ro")
	q.Add("_pragma", "query_only(1)")
	q.Add("_pragma", "busy_timeout(10000)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(2)
	if err = db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func writeJSON(path string, value any) error {
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err = f.Write(body); err != nil {
		return err
	}
	return f.Sync()
}
func timestamp() string { return time.Now().UTC().Format(time.RFC3339) }

func readManifest(root string) (Manifest, error) {
	var m Manifest
	f, err := os.Open(filepath.Join(root, "manifest.json"))
	if err != nil {
		return m, err
	}
	defer f.Close()
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	if err = d.Decode(&m); err != nil {
		return m, err
	}
	if m.Version != FormatVersion || m.Purpose != "private-backup" || !m.Sensitive {
		return m, fmt.Errorf("unsupported backup format or purpose")
	}
	return m, nil
}

func ReadCatalog(ctx context.Context, root string) (Catalog, error) {
	db, err := openReadOnly(ctx, root)
	if err != nil {
		return Catalog{}, err
	}
	defer db.Close()
	return ExportCatalog(&store.Store{DB: db})
}
