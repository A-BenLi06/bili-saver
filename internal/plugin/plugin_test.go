package plugin

// 插件系统测试：manifest 加载 + 进程插件协议（用测试二进制自身充当插件进程）。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"bili-saver/internal/store"
)

func TestMain(m *testing.M) {
	if os.Getenv("BILI_SAVER_TEST_PLUGIN") == "1" {
		runHelperPlugin()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// runHelperPlugin 模拟一个外部插件：读 stdin 事件，回传 save_summary 等 op。
func runHelperPlugin() {
	dec := json.NewDecoder(os.Stdin)
	var ev Event
	if err := dec.Decode(&ev); err != nil {
		os.Exit(1)
	}
	resp := map[string]any{
		"op":     "save_summary",
		"params": map[string]any{"bvid": ev.Payload["bvid"], "content": "插件生成的总结", "model": "test-model"},
	}
	_ = json.NewEncoder(os.Stdout).Encode(resp)
	resp2 := map[string]any{
		"op":     "save_tag",
		"params": map[string]any{"bvid": ev.Payload["bvid"], "tags": []string{"AI生成", "测试"}},
	}
	_ = json.NewEncoder(os.Stdout).Encode(resp2)
}

func TestManifestLoad(t *testing.T) {
	dir := t.TempDir()
	mf := `id: test-plugin
version: 1.0.0
description: 测试插件
type: process
entry: [echo, hi]
events: [video.downloaded]
timeout_sec: 30
`
	if err := os.WriteFile(filepath.Join(dir, "manifest.yaml"), []byte(mf), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "test-plugin" || len(m.Events) != 1 || m.Events[0] != "video.downloaded" {
		t.Fatalf("manifest 解析错误: %+v", m)
	}
	// 缺 id 应报错
	bad := filepath.Join(dir, "bad")
	_ = os.MkdirAll(bad, 0o755)
	_ = os.WriteFile(filepath.Join(bad, "manifest.yaml"), []byte("version: 1\nentry: [x]"), 0o644)
	if _, err := LoadManifest(bad); err == nil {
		t.Fatal("缺 id 的 manifest 应报错")
	}
}

func TestBusEmitWritesDerivedOnly(t *testing.T) {
	tmp := t.TempDir()
	st, err := store.Open(filepath.Join(tmp, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	// 插件目录：entry 指向测试二进制自身 + 环境变量触发 helper
	plugDir := filepath.Join(tmp, "plugins", "helper")
	_ = os.MkdirAll(plugDir, 0o755)
	mf := "id: helper\nversion: 1\ntype: process\nentry: [" + strconv.Quote(os.Args[0]) + "]\nevents: [video.downloaded]\ntimeout_sec: 60\n"
	mf += "env:\n  BILI_SAVER_TEST_PLUGIN: \"1\"\n"
	if err := os.WriteFile(filepath.Join(plugDir, "manifest.yaml"), []byte(mf), 0o644); err != nil {
		t.Fatal(err)
	}

	bus := NewBus(st, st, filepath.Join(tmp, "plugins"), nil, Context{DataDir: tmp})
	if err := bus.Reload(); err != nil {
		t.Fatal(err)
	}
	if len(bus.Manifests()) != 1 {
		t.Fatalf("应加载 1 个插件: %d", len(bus.Manifests()))
	}

	bus.Emit("video.downloaded", map[string]any{"bvid": "BV1Test00001"})

	// 派生数据应写入 derived 表
	sums, err := st.ListSummaries("BV1Test00001")
	if err != nil || len(sums) != 1 {
		t.Fatalf("插件总结未入库: %v %d", err, len(sums))
	}
	if sums[0].Origin != "plugin:helper" || sums[0].Content != "插件生成的总结" || sums[0].Model != "test-model" {
		t.Fatalf("派生数据溯源错误: %+v", sums[0])
	}
	tags, _ := st.VideoTags("BV1Test00001")
	if len(tags["plugin:helper"]) != 2 {
		t.Fatalf("插件标签未入库（应与 bilibili 标签分离）: %+v", tags)
	}
	// 审计记录
	runs, err := st.ListPluginRuns()
	if err != nil || len(runs) != 1 || !runs[0].Ok {
		t.Fatalf("插件审计记录错误: %v %+v", err, runs)
	}
}
