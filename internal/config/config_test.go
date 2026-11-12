package config

// SaveConfigFile 写盘-回读 round-trip 测试（设置页持久化路径）。

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveConfigFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	// 首次保存（无原文件，不应尝试备份）
	c1 := Defaults()
	c1.Listen = ":9999"
	c1.Collector.SyncDefaultMinutes = 120
	c1.Collector.AdaptiveSync = false
	c1.Download.Qualities = []string{"1080p", "720p"}
	c1.Media.Layout = "show"
	if err := SaveConfigFile(path, c1); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("保存的配置应可回读: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Listen != ":9999" || got.Collector.SyncDefaultMinutes != 120 ||
		got.Collector.AdaptiveSync || got.Media.Layout != "show" {
		t.Errorf("round-trip 字段不一致: listen=%s sync=%d adaptive=%v layout=%s",
			got.Listen, got.Collector.SyncDefaultMinutes, got.Collector.AdaptiveSync, got.Media.Layout)
	}
	if len(got.Download.Qualities) != 2 || got.Download.Qualities[0] != "1080p" {
		t.Errorf("qualities 列表不一致: %v", got.Download.Qualities)
	}

	// 二次保存：原文件应备份为 .bak 且内容非空
	c2 := Defaults()
	c2.Listen = ":8080"
	if err := SaveConfigFile(path, c2); err != nil {
		t.Fatal(err)
	}
	bak, err := Load(path + ".bak")
	if err != nil {
		t.Fatalf(".bak 备份应可回读: %v", err)
	}
	if bak.Listen != ":9999" {
		t.Errorf(".bak 应保存上一版内容 listen=:9999，实际 %s", bak.Listen)
	}
}

func TestStartupPathsFollowConfigDirectory(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(path, []byte("data_dir: ./archive # literal data path\nmedia:\n  root: ./exports\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DataDir != filepath.Join(root, "archive") || cfg.Media.Root != filepath.Join(root, "exports") || cfg.CookiePath() != filepath.Join(root, "archive", "cookies.json") {
		t.Fatalf("paths not anchored: %+v", cfg)
	}
}
