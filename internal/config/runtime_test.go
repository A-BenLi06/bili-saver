package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStrictYAMLAndEnvironmentArrays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("download:\n  concurncy: 2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("unknown configuration silently accepted")
	}
	if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BILI_SAVER_DOWNLOAD_QUALITIES", `["720p","1080p"]`)
	cfg, err := Load(path)
	if err != nil || len(cfg.Download.Qualities) != 2 {
		t.Fatal(cfg, err)
	}
	t.Setenv("BILI_SAVER_DOWNLOAD_CONCURRENCY", "broken")
	if _, err = Load(path); err == nil {
		t.Fatal("invalid integer was ignored")
	}
}
