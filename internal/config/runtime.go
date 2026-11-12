package config

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
)

type runtimeConfig struct {
	mu      sync.Mutex
	current atomic.Pointer[Config]
	initial *Config
}

// View returns an immutable configuration snapshot. Update is the only runtime writer.
func (c *Config) View() *Config {
	if c.runtime != nil {
		if v := c.runtime.current.Load(); v != nil {
			return v
		}
	}
	return c
}
func (c *Config) Clone() *Config {
	v := *c.View()
	v.runtime = nil
	v.Download.Qualities = append([]string(nil), v.Download.Qualities...)
	v.Download.AudioQualities = append([]string(nil), v.Download.AudioQualities...)
	v.Media.Servers = append([]MediaServer(nil), v.Media.Servers...)
	return &v
}
func (c *Config) InitRuntime() {
	if c.runtime == nil {
		c.runtime = &runtimeConfig{}
	}
}
func (c *Config) MarkApplied() {
	c.InitRuntime()
	c.runtime.mu.Lock()
	defer c.runtime.mu.Unlock()
	if c.runtime.initial == nil {
		c.runtime.initial = c.Clone()
	}
}
func (c *Config) Update(change func(*Config) error) error {
	c.InitRuntime()
	c.runtime.mu.Lock()
	defer c.runtime.mu.Unlock()
	next := c.Clone()
	if err := change(next); err != nil {
		return err
	}
	c.runtime.current.Store(next)
	return nil
}

func (c *Config) RestartRequired() []string {
	if c.runtime == nil {
		return nil
	}
	c.runtime.mu.Lock()
	initial := c.runtime.initial
	c.runtime.mu.Unlock()
	if initial == nil {
		return nil
	}
	current := c.View()
	out := []string{}
	for _, key := range []string{"download.concurrency", "collector.comment_concurrency", "bilibili.min_req_ms", "teriteri.enabled", "queue.stall_minutes"} {
		a, _ := initial.Field(key)
		b, _ := current.Field(key)
		if !reflect.DeepEqual(a.Interface(), b.Interface()) {
			out = append(out, key)
		}
	}
	return out
}

func (c *Config) EffectiveStartup() map[string]any {
	if c.runtime == nil {
		return nil
	}
	c.runtime.mu.Lock()
	defer c.runtime.mu.Unlock()
	v := c.runtime.initial
	if v == nil {
		return nil
	}
	return map[string]any{"concurrency": v.Download.Concurrency, "comment_concurrency": v.Collector.CommentConcurrency, "min_req_ms": v.Bilibili.MinReqMs, "teriteri_enabled": v.Teriteri.Enabled, "stall_minutes": v.Queue.StallMinutes}
}

func (c *Config) Field(key string) (reflect.Value, error) {
	v := reflect.ValueOf(c).Elem()
	for _, part := range strings.Split(key, ".") {
		found := false
		for i := 0; i < v.NumField(); i++ {
			f := v.Type().Field(i)
			if f.PkgPath != "" {
				continue
			}
			if strings.Split(f.Tag.Get("yaml"), ",")[0] == part {
				v = v.Field(i)
				found = true
				break
			}
		}
		if !found {
			return reflect.Value{}, fmt.Errorf("unknown setting %s", key)
		}
	}
	return v, nil
}
func EnvName(key string) string {
	return "BILI_SAVER_" + strings.ToUpper(strings.ReplaceAll(key, ".", "_"))
}
func EnvControlled(key string) bool { _, ok := os.LookupEnv(EnvName(key)); return ok }
func (c *Config) ApplyOverrides(values map[string]json.RawMessage) error {
	for key, raw := range values {
		if EnvControlled(key) {
			continue
		}
		f, err := c.Field(key)
		if err != nil {
			return err
		}
		if !f.CanAddr() {
			return fmt.Errorf("setting is not writable: %s", key)
		}
		if err = json.Unmarshal(raw, f.Addr().Interface()); err != nil {
			return fmt.Errorf("setting %s: %w", key, err)
		}
	}
	c.Normalize()
	return c.Validate()
}
func (c *Config) Values(keys []string) (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	for _, key := range keys {
		if EnvControlled(key) {
			return nil, fmt.Errorf("%s is controlled by %s", key, EnvName(key))
		}
		v, err := c.Field(key)
		if err != nil {
			return nil, err
		}
		body, err := json.Marshal(v.Interface())
		if err != nil {
			return nil, err
		}
		out[key] = body
	}
	return out, nil
}

// Prepare merges persistence exactly once before components start. The startup
// struct remains immutable after startup; all subsequent changes use Update.
func (c *Config) Prepare(values map[string]json.RawMessage) error {
	c.InitRuntime()
	c.runtime.mu.Lock()
	defer c.runtime.mu.Unlock()
	if c.runtime.initial != nil {
		return nil
	}
	if err := c.ApplyOverrides(values); err != nil {
		return err
	}
	c.runtime.initial = c.Clone()
	return nil
}

func (c *Config) LocalURL() string {
	host, port, err := net.SplitHostPort(c.View().Listen)
	if err != nil {
		return ""
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}
