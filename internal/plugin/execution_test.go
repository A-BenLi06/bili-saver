package plugin

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"bili-saver/internal/store"
)

func TestPythonPluginUTF8Output(t *testing.T) {
	python, err := exec.LookPath("python")
	if err != nil {
		t.Skip("Python unavailable")
	}
	bus, st, _ := helperBus(t, 1, nil)
	m := bus.manifest["helper-0"]
	m.Entry = []string{python, "-c", `import json,sys; json.load(sys.stdin); print(json.dumps({"op":"save_summary","params":{"bvid":"BVUTF8","content":"中文摘要"}},ensure_ascii=False))`}
	m.Env = map[string]string{"PYTHONUTF8": "0"}
	bus.Emit(EventVideoDownloaded, map[string]any{"bvid": "BVUTF8"})
	summaries, err := st.ListSummaries("BVUTF8")
	if err != nil || len(summaries) != 1 {
		t.Fatalf("summaries: %v %v", summaries, err)
	}
	if summaries[0].Content != "中文摘要" || summaries[0].Origin != "plugin:helper-0" {
		t.Fatalf("invalid derived output: %+v", summaries[0])
	}
	runs, err := st.ListPluginRuns()
	if err != nil || len(runs) != 1 || !runs[0].Ok {
		t.Fatalf("runs: %+v %v", runs, err)
	}
}

type countedRegistry struct {
	PluginStore
	reads atomic.Int32
}

func (s *countedRegistry) ListPlugins() ([]*store.PluginRow, error) {
	s.reads.Add(1)
	return s.PluginStore.ListPlugins()
}

func helperBus(t *testing.T, n int, writer Writer) (*Bus, *store.Store, *countedRegistry) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if writer == nil {
		writer = st
	}
	registry := &countedRegistry{PluginStore: st}
	bus := NewBus(registry, writer, dir, nil, Context{DataDir: dir})
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("helper-%d", i)
		if err := os.MkdirAll(filepath.Join(dir, id), 0755); err != nil {
			t.Fatal(err)
		}
		bus.manifest[id] = &Manifest{ID: id, Entry: []string{os.Args[0]}, Events: []string{EventVideoDownloaded}, TimeoutSec: 60, Env: map[string]string{"BILI_SAVER_TEST_PLUGIN": "1"}, Config: map[string]any{"model": "default"}}
		if err := st.UpsertPlugin(&store.PluginRow{ID: id, Enabled: true, ManifestJSON: "{}", ConfigJSON: `{"model":"persisted"}`}); err != nil {
			t.Fatal(err)
		}
	}
	return bus, st, registry
}

func TestEventReadsRegistryOnceAndRespectsFreshSettings(t *testing.T) {
	bus, st, registry := helperBus(t, 6, nil)
	if err := st.SetPluginEnabled("helper-5", false); err != nil {
		t.Fatal(err)
	}
	bus.Emit(EventVideoDownloaded, map[string]any{"bvid": "BVPlugin"})
	if registry.reads.Load() != 1 {
		t.Fatalf("registry read %d times", registry.reads.Load())
	}
	summaries, err := st.ListSummaries("BVPlugin")
	if err != nil || len(summaries) != 5 {
		t.Fatalf("summaries %d: %v", len(summaries), err)
	}
	for _, s := range summaries {
		if s.Model != "persisted" {
			t.Fatal("persisted config lost")
		}
	}
	if err := st.SetPluginConfig("helper-0", `{"model":"changed"}`); err != nil {
		t.Fatal(err)
	}
	for i := 1; i < 6; i++ {
		st.SetPluginEnabled(fmt.Sprintf("helper-%d", i), false)
	}
	bus.Emit(EventVideoDownloaded, map[string]any{"bvid": "BVFresh"})
	summaries, err = st.ListSummaries("BVFresh")
	if err != nil || len(summaries) != 1 || summaries[0].Model != "changed" {
		t.Fatal("event used stale settings")
	}
	if registry.reads.Load() != 2 {
		t.Fatal("N+1 registry reads")
	}
	bus.EmitAsync("unsubscribed", nil)
	bus.Wait()
	if registry.reads.Load() != 2 {
		t.Fatal("unsubscribed event queried registry")
	}
}

type gatedWriter struct {
	Writer
	entered chan struct{}
	gate    chan struct{}
}

func (w *gatedWriter) AddSummary(s *store.Summary) error {
	w.entered <- struct{}{}
	<-w.gate
	return w.Writer.AddSummary(s)
}

func TestPluginProcessLimitAndAsyncBackpressureWithoutLoss(t *testing.T) {
	w := &gatedWriter{entered: make(chan struct{}, 20), gate: make(chan struct{})}
	bus, st, _ := helperBus(t, 1, w)
	w.Writer = st
	defer bus.Wait()
	for i := 0; i < 16; i++ {
		bus.EmitAsync(EventVideoDownloaded, map[string]any{"bvid": "BVPlugin"})
	}
	for i := 0; i < 4; i++ {
		select {
		case <-w.entered:
		case <-time.After(10 * time.Second):
			close(w.gate)
			t.Fatal("plugins did not start")
		}
	}
	returned := make(chan struct{})
	go func() { bus.EmitAsync(EventVideoDownloaded, map[string]any{"bvid": "BVPlugin"}); close(returned) }()
	select {
	case <-returned:
		close(w.gate)
		t.Fatal("unbounded async events")
	case <-time.After(30 * time.Millisecond):
	}
	if len(bus.processSlots) != 4 || len(bus.asyncSlots) != 16 {
		close(w.gate)
		t.Fatal("process or event limit not applied")
	}
	select {
	case <-w.entered:
		close(w.gate)
		t.Fatal("fifth process ran concurrently")
	default:
	}
	close(w.gate)
	select {
	case <-returned:
	case <-time.After(10 * time.Second):
		t.Fatal("backpressure not released")
	}
	bus.Wait()
	runs, err := st.ListPluginRuns()
	if err != nil || len(runs) != 17 {
		t.Fatalf("events lost: %d %v", len(runs), err)
	}
	for _, r := range runs {
		if !r.Ok {
			t.Fatal(r.Error)
		}
	}
	if len(bus.processSlots)+len(bus.asyncSlots) != 0 {
		t.Fatal("slots leaked")
	}
}
