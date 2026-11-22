package archiveops

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"bili-saver/internal/config"
)

func TestPrivateBackupCapturesStartupPreferencesAndExternalState(t *testing.T) {
	root := fixture(t)
	cfg := config.Defaults()
	cfg.DataDir = root
	cfg.Download.AutoDownload = false
	cfg.Download.Quality = "720p"
	cfg.Media.Enabled = false
	cfg.Collector.CommentsMax = 123
	external := t.TempDir()
	cfg.Bilibili.CookieFile = filepath.Join(external, "active-cookies.json")
	cfg.Plugins.Dir = filepath.Join(external, "plugins")
	if err := os.WriteFile(cfg.Bilibili.CookieFile, []byte("external-cookie-fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cfg.Plugins.Dir, "test"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Plugins.Dir, "test", "manifest.json"), []byte(`{"id":"test"}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.Normalize()
	backup := filepath.Join(t.TempDir(), "backup")
	if _, err := BackupWithConfig(context.Background(), cfg, backup); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(t.TempDir(), "restored")
	t.Setenv("BILI_SAVER_DOWNLOAD_AUTO_DOWNLOAD", "true") // Must not alter the archived snapshot during restore.
	if err := Restore(context.Background(), backup, restored); err != nil {
		t.Fatal(err)
	}
	next, err := config.LoadSnapshot(filepath.Join(restored, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if next.Download.AutoDownload || next.Media.Enabled || next.Download.Quality != "720p" || next.Collector.CommentsMax != 123 {
		t.Fatal("startup-only preferences lost")
	}
	assertRestoredPath(t, next.Bilibili.CookieFile, filepath.Join(restored, "cookies.json"))
	assertRestoredPath(t, next.Plugins.Dir, filepath.Join(restored, "plugins"))
	body, err := os.ReadFile(next.Bilibili.CookieFile)
	if err != nil || string(body) != "external-cookie-fixture" {
		t.Fatal("active external cookie not captured", err)
	}
	if _, err = os.Stat(filepath.Join(next.Plugins.Dir, "test", "manifest.json")); err != nil {
		t.Fatal("external plugin not captured", err)
	}
}

func assertRestoredPath(t *testing.T, configured, expected string) {
	t.Helper()
	actualInfo, err := os.Stat(configured)
	if err != nil {
		t.Fatalf("stat configured path %q: %v", configured, err)
	}
	expectedInfo, err := os.Stat(expected)
	if err != nil {
		t.Fatalf("stat expected path %q: %v", expected, err)
	}
	// Restore canonicalizes its destination, so both paths can name the same entry with different spellings.
	if !os.SameFile(actualInfo, expectedInfo) {
		t.Fatalf("configured path %q does not point to restored path %q", configured, expected)
	}
}
