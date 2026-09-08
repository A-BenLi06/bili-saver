// Package plugin 实现事件总线与外部进程插件：
// 核心把事件以一行 JSON 写入插件 stdin；插件以一行一个 JSON op 应答，
// 只能写 derived 层数据（save_summary/save_subtitle/save_tag/save_relation/save_embedding/log）。
// 这从机制上保证“B 站原始数据”与“生成数据”显式分离。
package plugin

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Manifest 插件清单（plugins/<dir>/manifest.yaml）。
type Manifest struct {
	ID          string            `yaml:"id"`
	Version     string            `yaml:"version"`
	Description string            `yaml:"description"`
	Type        string            `yaml:"type"` // process
	Entry       []string          `yaml:"entry"`
	Env         map[string]string `yaml:"env"`
	Events      []string          `yaml:"events"`
	TimeoutSec  int               `yaml:"timeout_sec"`
	Config      map[string]any    `yaml:"config"`
}

// LoadManifest 从目录读取并校验 manifest.yaml。
func LoadManifest(dir string) (*Manifest, error) {
	b, err := os.ReadFile(filepath.Join(dir, "manifest.yaml"))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := yaml.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("解析 %s: %w", filepath.Join(dir, "manifest.yaml"), err)
	}
	if m.ID == "" {
		return nil, fmt.Errorf("%s: 缺少 id", filepath.Join(dir, "manifest.yaml"))
	}
	if len(m.Entry) == 0 {
		return nil, fmt.Errorf("插件 %s: 缺少 entry", m.ID)
	}
	if m.TimeoutSec <= 0 {
		m.TimeoutSec = 600
	}
	if m.Type == "" {
		m.Type = "process"
	}
	return &m, nil
}

// LoadAll 扫描插件根目录下所有含 manifest.yaml 的子目录。
func LoadAll(root string) ([]*Manifest, []error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []error{err}
	}
	var out []*Manifest
	var errs []error
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if _, err := os.Stat(filepath.Join(dir, "manifest.yaml")); err != nil {
			continue
		}
		m, err := LoadManifest(dir)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out = append(out, m)
	}
	return out, errs
}
