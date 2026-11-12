package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewAddFirstConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	for _, tc := range []struct {
		name, yaml string
		want       bool
	}{
		{"old_config", "listen: ':8080'\n", true},
		{"disabled", "queue:\n  new_add_first: false\n", false},
		{"enabled", "queue:\n  new_add_first: true\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(tc.yaml), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Queue.NewAddFirst != tc.want {
				t.Fatalf("new_add_first=%v, want %v", cfg.Queue.NewAddFirst, tc.want)
			}
			if err := SaveConfigFile(path, cfg); err != nil {
				t.Fatal(err)
			}
			reloaded, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if reloaded.Queue.NewAddFirst != tc.want {
				t.Fatal("保存后优先设置丢失")
			}
		})
	}
}
