package sysres

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestArchiveSizesScope(t *testing.T) {
	dir := t.TempDir()
	for name, size := range map[string]int{
		"media/BV1/p1/video.mp4": 100, "media/BV1/p2/audio.m4s.part": 30,
		"media/BV1/cover.jpg": 7, "raw/BV1/snapshot.json.gz": 19,
		"derived/note.txt": 11, "bili-saver.db": 13,
		"library/copied-video.mp4": 100, "teriteri/node_modules/code.js": 111,
		"plugins/plugin.py": 108, "logs/run.log": 8,
	} {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, make([]byte, size), 0644); err != nil {
			t.Fatal(err)
		}
	}
	s := scanArchiveSizes(dir)
	if s.VideoBytes != 130 || s.OtherBytes != 50 || s.TotalBytes != 180 || s.Files != 6 || s.Partial || s.Provisional || s.UnixMs == 0 {
		t.Fatalf("archive scope/classification incorrect: %+v", s)
	}
	if s := scanArchiveSizes(filepath.Join(dir, "missing")); !s.Partial {
		t.Fatal("missing directory disguised as complete empty archive")
	}
}

func TestArchiveSizesDoNotFollowLinks(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "media"), 0755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "outside.mp4")
	if err := os.WriteFile(target, make([]byte, 100), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "media", "linked.mp4")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := os.Symlink(dir, filepath.Join(dir, "media", "cycle")); err != nil {
		t.Fatal(err)
	}
	if s := scanArchiveSizes(dir); s.TotalBytes != 0 || s.Partial {
		t.Fatalf("followed linked file or cycle: %+v", s)
	}
}

func TestArchiveStorageBackgroundCache(t *testing.T) {
	var c archiveSizeCache
	var calls, clock atomic.Int64
	clock.Store(1000)
	now := func() time.Time { return time.Unix(clock.Load(), 0) }
	started, release := make(chan struct{}, 2), make(chan struct{}, 2)
	scan := func(path string, publish func(ArchiveSizes)) ArchiveSizes {
		calls.Add(1)
		publish(ArchiveSizes{VideoBytes: 1, TotalBytes: 1, Provisional: true, Files: 1})
		started <- struct{}{}
		<-release
		return ArchiveSizes{TotalBytes: uint64(len(path))}
	}
	first := c.get("a", scan, now)
	if !first.Scanning || first.Sizes != nil {
		t.Fatal("initial request must return pending immediately")
	}
	<-started
	if r := c.get("a", scan, now); !r.Scanning || r.Sizes == nil || !r.Sizes.Provisional || r.Sizes.Files != 1 {
		t.Fatal("initial traversal progress not published")
	}
	for i := 0; i < 20; i++ {
		c.get("a", scan, now)
	}
	if calls.Load() != 1 {
		t.Fatal("parallel poll started duplicate traversal")
	}
	// 路径切换时，不发布已过期目录的扫描结果，也不同时开第二条扫描。
	if r := c.get("longer", scan, now); r.Sizes != nil || !r.Scanning {
		t.Fatal("directory change reused stale data")
	}
	release <- struct{}{}
	deadline := time.Now().Add(2 * time.Second)
	for {
		c.mu.Lock()
		busy := c.busy
		c.mu.Unlock()
		if !busy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("scan did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	c.get("longer", scan, now)
	<-started
	release <- struct{}{}
	var done ArchiveStorage
	for {
		done = c.get("longer", scan, now)
		if done.Sizes != nil && !done.Scanning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("new directory scan did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	if done.Sizes.TotalBytes != 6 {
		t.Fatal("wrong directory result published")
	}
	done.Sizes.TotalBytes = 99
	if c.get("longer", scan, now).Sizes.TotalBytes != 6 || calls.Load() != 2 {
		t.Fatal("cache mutated or refreshed too early")
	}
	clock.Add(600)
	if r := c.get("longer", scan, now); !r.Scanning || r.Sizes.TotalBytes != 6 {
		t.Fatal("refresh must retain previous result")
	}
	<-started
	release <- struct{}{}
}
