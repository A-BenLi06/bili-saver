package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bili-saver/internal/biliapi"
	"bili-saver/internal/config"
	"bili-saver/internal/store"
)

func scanOffset(r *http.Request) string {
	var p struct {
		Offset string `json:"offset"`
	}
	_ = json.Unmarshal([]byte(r.URL.Query().Get("pagination_str")), &p)
	return p.Offset
}
func scanMain(w http.ResponseWriter, replies []map[string]any, end bool, next string) {
	writeJSON(w, map[string]any{"code": 0, "data": map[string]any{"cursor": map[string]any{"is_end": end, "pagination_reply": map[string]any{"next_offset": next}}, "replies": replies}})
}
func scanSub(w http.ResponseWriter, root int64, pn, total int) {
	replies := []map[string]any{}
	for i := (pn-1)*20 + 1; i <= min(pn*20, total); i++ {
		replies = append(replies, map[string]any{"rpid": root*100 + int64(i), "root": root})
	}
	writeJSON(w, map[string]any{"code": 0, "data": map[string]any{"page": map[string]any{"count": total}, "replies": replies}})
}

func TestCommentPipelineSequentialHeadAndOverlappingRoots(t *testing.T) {
	var active, peak atomic.Int32
	var mu sync.Mutex
	var offsets []string
	headAdvanced := make(chan struct{})
	secondRoot := make(chan struct{})
	c := scanCollector(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		if r.URL.Path == "/x/v2/reply/wbi/main" {
			o := scanOffset(r)
			mu.Lock()
			offsets = append(offsets, o)
			mu.Unlock()
			if o == "" {
				scanMain(w, []map[string]any{{"rpid": 1, "rcount": 21}, {"rpid": 10, "rcount": 21}}, false, "second")
			} else {
				close(headAdvanced)
				scanMain(w, []map[string]any{{"rpid": 50}}, true, "")
			}
			return
		}
		root, _ := strconv.ParseInt(r.URL.Query().Get("root"), 10, 64)
		pn, _ := strconv.Atoi(r.URL.Query().Get("pn"))
		if pn == 1 {
			if root == 10 {
				close(secondRoot)
			}
			select {
			case <-headAdvanced:
			case <-r.Context().Done():
				return
			}
			select {
			case <-secondRoot:
			case <-r.Context().Done():
				return
			}
		}
		scanSub(w, root, pn, 21)
	}))
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	seen, complete, err := c.collectComments(ctx, 111, "BVPipe", 100, true)
	if err != nil || !complete || seen != 45 {
		t.Fatalf("seen=%d complete=%v err=%v", seen, complete, err)
	}
	if peak.Load() != 2 {
		t.Fatalf("global in-flight cap/overlap: %d", peak.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	if fmt.Sprint(offsets) != "[ second]" {
		t.Fatal(offsets)
	}
}

func TestCommentStableCursorRequiresActualPageProgress(t *testing.T) {
	for _, stalled := range []bool{false, true} {
		t.Run(fmt.Sprint(stalled), func(t *testing.T) {
			var calls atomic.Int32
			c := scanCollector(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				id := n
				if stalled {
					id = 1
				}
				scanMain(w, []map[string]any{{"rpid": id}}, !stalled && n == 3, "stable")
			}))
			r, err := c.scanComments(t.Context(), 111, "BVStable", 100, true)
			if err != nil || !r.Finished || r.Complete == stalled || r.Scan.Capped != stalled {
				t.Fatal(r, err)
			}
			wantCalls, wantSeen := int32(3), 3
			if stalled {
				wantCalls, wantSeen = 2, 1
			}
			if calls.Load() != wantCalls || r.Scan.Seen != wantSeen {
				t.Fatal(calls.Load(), r.Scan.Seen)
			}
		})
	}
}

func TestCommentConcurrencyCapIsSharedAcrossVideos(t *testing.T) {
	var active, peak atomic.Int32
	arrived := make(chan struct{})
	var once sync.Once
	c := scanCollector(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		if n == 2 {
			once.Do(func() { close(arrived) })
		}
		select {
		case <-arrived:
		case <-r.Context().Done():
			return
		}
		if r.URL.Path == "/x/v2/reply/wbi/main" {
			id, _ := strconv.ParseInt(r.URL.Query().Get("oid"), 10, 64)
			scanMain(w, []map[string]any{{"rpid": id * 10, "rcount": 1}}, true, "")
			return
		}
		root, _ := strconv.ParseInt(r.URL.Query().Get("root"), 10, 64)
		scanSub(w, root, 1, 1)
	}))
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	done := make(chan error, 2)
	for id := int64(1); id <= 2; id++ {
		go func(id int64) {
			r, err := c.scanComments(ctx, id, fmt.Sprintf("BVConcurrent%d", id), 100, true)
			if err == nil && !r.Complete {
				err = fmt.Errorf("incomplete")
			}
			done <- err
		}(id)
	}
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if peak.Load() != 2 {
		t.Fatal("global cap", peak.Load())
	}
}

func TestCommentScanReopensDatabaseAndResumesRoot(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	var heads atomic.Int32
	var firstSub atomic.Int32
	c := scanCollector(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/x/v2/reply/wbi/main" {
			heads.Add(1)
			scanMain(w, []map[string]any{{"rpid": 1, "rcount": 23, "replies": []any{map[string]any{"rpid": 101}, map[string]any{"rpid": 102}, map[string]any{"rpid": 103}}}}, true, "")
			return
		}
		pn, _ := strconv.Atoi(r.URL.Query().Get("pn"))
		if pn == 1 {
			firstSub.Add(1)
		}
		if pn == 2 && fail.Load() {
			writeJSON(w, map[string]any{"code": -500, "message": "temporary"})
			return
		}
		scanSub(w, 1, pn, 23)
	}))
	r, err := c.scanComments(t.Context(), 111, "BVResume", 100, true)
	if err != nil || r.Complete || r.Scan.Seen != 21 {
		t.Fatalf("first=%+v err=%v", r, err)
	}
	if err := c.St.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(filepath.Join(c.Cfg.DataDir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	c = New(reopened, c.API, c.Cfg, nil, nil)
	fail.Store(false)
	r, err = c.scanComments(t.Context(), 111, "BVResume", 999, false)
	if err != nil || !r.Complete || r.Scan.Epoch != 100 || !r.Scan.First || r.Scan.Seen != 24 {
		t.Fatalf("resumed=%+v err=%v", r, err)
	}
	if heads.Load() != 1 || firstSub.Load() != 1 {
		t.Fatalf("replayed committed pages: heads=%d sub1=%d", heads.Load(), firstSub.Load())
	}
	rows, err := c.St.ListComments("BVResume", "", 0, 0)
	if err != nil || len(rows) != 24 {
		t.Fatal(len(rows), err)
	}
	for _, row := range rows {
		if row.LastSeenAt != 100 || (row.Rpid != 1 && row.RootRpid != 1) {
			t.Fatalf("bad coverage/hierarchy: %+v", row)
		}
	}
}

func TestCommentScanResumesMainOffset(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	var heads atomic.Int32
	c := scanCollector(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if scanOffset(r) == "" {
			heads.Add(1)
			scanMain(w, []map[string]any{{"rpid": 1}}, false, "second")
			return
		}
		if fail.Load() {
			writeJSON(w, map[string]any{"code": -500})
			return
		}
		scanMain(w, []map[string]any{{"rpid": 2}}, true, "")
	}))
	r, err := c.scanComments(t.Context(), 111, "BVOffset", 100, true)
	if err == nil || r.Scan.Seen != 1 {
		t.Fatal(r, err)
	}
	if err := c.St.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(filepath.Join(c.Cfg.DataDir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	c = New(reopened, c.API, c.Cfg, nil, nil)
	fail.Store(false)
	r, err = c.scanComments(t.Context(), 111, "BVOffset", 200, false)
	if err != nil || !r.Complete || r.Scan.Epoch != 100 || r.Scan.Seen != 2 || heads.Load() != 1 {
		t.Fatal(r, heads.Load(), err)
	}
}

func TestCommentIncrementalFindsOldThreadRepliesAndPeriodicAudit(t *testing.T) {
	var phase atomic.Int32
	var modesMu sync.Mutex
	var modes []string
	now := store.Now()
	c := scanCollector(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/x/v2/reply/wbi/main" {
			modesMu.Lock()
			modes = append(modes, r.URL.Query().Get("mode"))
			modesMu.Unlock()
			if phase.Load() == 1 {
				if scanOffset(r) == "" {
					scanMain(w, []map[string]any{{"rpid": 2, "ctime": now}}, false, "older")
				} else {
					scanMain(w, []map[string]any{{"rpid": 3, "ctime": now - 20*86400}}, false, "must-not-fetch")
				}
				return
			}
			total := 40
			if phase.Load() == 2 {
				total = 41
			}
			scanMain(w, []map[string]any{{"rpid": 1, "rcount": total, "ctime": now - 10*86400}}, true, "")
			return
		}
		pn, _ := strconv.Atoi(r.URL.Query().Get("pn"))
		total := 40
		if phase.Load() > 0 {
			total = 41
		}
		scanSub(w, 1, pn, total)
	}))
	const bvid = "BV1Test00001"
	initial, err := c.ArchiveVideo(t.Context(), bvid)
	if err != nil || !initial.Complete {
		t.Fatal(initial, err)
	}
	phase.Store(1)
	inc, err := c.ArchiveVideo(t.Context(), bvid)
	if err != nil || inc.Complete || inc.CommentsDeleted != 0 {
		t.Fatal(inc, err)
	}
	var got int
	if err := c.St.DB.QueryRow(`SELECT COUNT(*) FROM comments WHERE bvid=? AND rpid=141 AND root_rpid=1 AND status='visible'`, bvid).Scan(&got); err != nil || got != 1 {
		t.Fatal("old thread new reply missed", got, err)
	}
	history, err := c.St.CommentSyncState(bvid)
	if err != nil || history.LastFullAt == 0 {
		t.Fatal(history, err)
	}
	if _, err := c.St.DB.Exec(`UPDATE comment_sync_state SET last_full_at=? WHERE bvid=?`, now-168*3600-1, bvid); err != nil {
		t.Fatal(err)
	}
	phase.Store(2)
	full, err := c.ArchiveVideo(t.Context(), bvid)
	if err != nil || !full.Complete || full.CommentsDeleted != 2 {
		t.Fatal(full, err)
	}
	modesMu.Lock()
	defer modesMu.Unlock()
	if fmt.Sprint(modes) != "[3 2 2 3]" {
		t.Fatal(modes)
	}
}

func TestCommentIncrementalBudgetContinuesInsteadOfSkippingBacklog(t *testing.T) {
	var calls atomic.Int32
	c := scanCollector(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		page := 1
		if offset := scanOffset(r); offset != "" {
			page, _ = strconv.Atoi(offset)
		}
		scanMain(w, []map[string]any{{"rpid": page, "ctime": int64(page)}}, page == 7, strconv.Itoa(page+1))
	}))
	c.Cfg.Collector.FullResyncComments = false
	r, err := c.scanComments(t.Context(), 111, "BVBacklog", 100, false)
	if err != nil || r.Finished || r.Scan.MainPages != 5 || calls.Load() != 5 {
		t.Fatal(r, calls.Load(), err)
	}
	r, err = c.scanComments(t.Context(), 111, "BVBacklog", 200, false)
	if err != nil || !r.Finished || r.Complete || r.Scan.Epoch != 100 || r.Scan.Seen != 7 || calls.Load() != 7 {
		t.Fatal(r, calls.Load(), err)
	}
}

func TestCommentCheckpointInvalidation(t *testing.T) {
	for _, kind := range []string{"session", "age", "policy", "aid"} {
		t.Run(kind, func(t *testing.T) {
			var fail atomic.Bool
			fail.Store(true)
			var heads atomic.Int32
			c := scanCollector(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if scanOffset(r) == "" {
					heads.Add(1)
					scanMain(w, []map[string]any{{"rpid": 1}}, !fail.Load(), "second")
					return
				}
				writeJSON(w, map[string]any{"code": -500})
			}))
			r, err := c.scanComments(t.Context(), 111, "BVReset", 100, true)
			if err == nil {
				t.Fatal("expected interrupted scan")
			}
			switch kind {
			case "session":
				r.Scan.Session = "different"
			case "age":
				r.Scan.StartedAt = store.Now() - int64(commentResumeTTL/time.Second) - 1
			case "policy":
				r.Scan.Policy = "different"
			case "aid":
				r.Scan.Aid = 222
			}
			if err := c.St.StartCommentScan(r.Scan, nil); err != nil {
				t.Fatal(err)
			}
			fail.Store(false)
			r, err = c.scanComments(t.Context(), 111, "BVReset", 200, false)
			if err != nil || !r.Complete || r.Scan.Epoch != 200 || heads.Load() != 2 {
				t.Fatal(r, heads.Load(), err)
			}
		})
	}
}

func TestCommentInvalidCursorAndShortSubPageNeverReconcile(t *testing.T) {
	for _, kind := range []string{"cursor", "short"} {
		t.Run(kind, func(t *testing.T) {
			c := scanCollector(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/x/v2/reply/wbi/main" {
					if kind == "cursor" && scanOffset(r) != "" {
						writeJSON(w, map[string]any{"code": -400})
						return
					}
					n := 0
					if kind == "short" {
						n = 25
					}
					scanMain(w, []map[string]any{{"rpid": 1, "rcount": n}}, kind == "short", "second")
					return
				}
				writeJSON(w, map[string]any{"code": 0, "data": map[string]any{"page": map[string]any{"count": 25}, "replies": []any{map[string]any{"rpid": 101, "root": 1}}}})
			}))
			r, err := c.scanComments(t.Context(), 111, "BVIncomplete", 100, true)
			if r.Complete || !r.Scan.Capped || !r.Finished {
				t.Fatal(r, err)
			}
			if err := c.finishCommentScan(r, 0); err != nil {
				t.Fatal(err)
			}
			checkpoint, err := c.St.LoadCommentScan("BVIncomplete")
			if err != nil || checkpoint != nil {
				t.Fatal(checkpoint, err)
			}
		})
	}
}

// Worker cancellation must leave committed cursor coverage, without leaked requests.
func TestCommentCancelKeepsCommittedCheckpoint(t *testing.T) {
	started := make(chan struct{})
	c := scanCollector(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if scanOffset(r) == "" {
			scanMain(w, []map[string]any{{"rpid": 1}}, false, "second")
			return
		}
		close(started)
		<-r.Context().Done()
	}))
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, err := c.scanComments(ctx, 111, "BVCancel", 100, true); done <- err }()
	select {
	case <-started:
		cancel()
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("second page not started")
	}
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancel leaked workers")
	}
	scan, err := c.St.LoadCommentScan("BVCancel")
	if err != nil || scan.Offset != "second" || scan.Seen != 1 {
		t.Fatal(scan, err)
	}
}

func TestCommentCompletionRequiresActualLogin(t *testing.T) {
	c := scanCollector(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { scanMain(w, []map[string]any{{"rpid": 1}}, true, "") }))
	// A separate client retains SESSDATA but receives a logged-out nav response.
	api, err := biliapi.New(writeLoginCookie(t), 1, "test")
	if err != nil {
		t.Fatal(err)
	}
	srv := newMockBili(t)
	base := srv.srv.Config.Handler
	srv.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/x/v2/reply/wbi/main" {
			scanMain(w, []map[string]any{{"rpid": 1}}, true, "")
			return
		}
		if r.URL.Path == "/x/web-interface/nav" {
			writeJSON(w, map[string]any{"code": 0, "data": map[string]any{"isLogin": false, "wbi_img": map[string]string{"img_url": "https://i/123.png", "sub_url": "https://i/456.png"}}})
			return
		}
		base.ServeHTTP(w, r)
	})
	api.SetBase(srv.srv.URL, srv.srv.URL)
	c.API = api
	r, err := c.scanComments(t.Context(), 111, "BVExpired", 100, true)
	if err != nil || r.Complete || !r.Finished {
		t.Fatal(r, err)
	}
}

func TestCommentCrashHelper(t *testing.T) {
	if os.Getenv("BILI_COMMENT_CRASH_HELPER") != "1" {
		return
	}
	cfg := config.Defaults()
	cfg.DataDir = os.Getenv("BILI_COMMENT_TEST_DIR")
	st, err := store.Open(filepath.Join(cfg.DataDir, "test.db"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	defer st.Close()
	api, err := biliapi.New(os.Getenv("BILI_COMMENT_TEST_COOKIE"), 1, "test")
	if err != nil {
		os.Exit(2)
	}
	api.SetBase(os.Getenv("BILI_COMMENT_TEST_BASE"), os.Getenv("BILI_COMMENT_TEST_BASE"))
	c := New(st, api, cfg, nil, nil)
	api.SetSnapshotHook(c.SnapshotHook())
	first := os.Getenv("BILI_COMMENT_TEST_FIRST") == "1"
	epoch := int64(999)
	if first {
		epoch = 100
	}
	r, err := c.scanComments(t.Context(), 111, "BVCrash", epoch, first)
	if err != nil || !r.Complete {
		fmt.Fprintln(os.Stderr, r, err)
		os.Exit(2)
	}
	fmt.Printf("RESUME_EVIDENCE epoch=%d seen=%d first=%t\n", r.Scan.Epoch, r.Scan.Seen, r.Scan.First)
}

func TestCommentScanSurvivesProcessKill(t *testing.T) {
	var phase atomic.Int32
	var heads, sub1 atomic.Int32
	blocked := make(chan struct{})
	baseURL := make(chan string, 1)
	c := scanCollector(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("oid") == "999" {
			baseURL <- "http://" + r.Host
			scanMain(w, nil, true, "")
			return
		}
		if r.URL.Path == "/x/v2/reply/wbi/main" {
			heads.Add(1)
			scanMain(w, []map[string]any{{"rpid": 1, "rcount": 23}}, true, "")
			return
		}
		pn, _ := strconv.Atoi(r.URL.Query().Get("pn"))
		if pn == 1 {
			sub1.Add(1)
		}
		if pn == 2 && phase.Load() == 0 {
			close(blocked)
			<-r.Context().Done()
			return
		}
		scanSub(w, 1, pn, 23)
	}))
	// The fixture's endpoint is already selected on this client.
	if _, err := c.API.ReplyMain(t.Context(), 999, ""); err != nil {
		t.Fatal(err)
	}
	base := <-baseURL
	c.St.Close()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := func(first bool) *exec.Cmd {
		cmd := exec.Command(exe, "-test.run=^TestCommentCrashHelper$", "-test.timeout=20s")
		cmd.Env = append(os.Environ(), "BILI_COMMENT_CRASH_HELPER=1", "BILI_COMMENT_TEST_DIR="+c.Cfg.DataDir, "BILI_COMMENT_TEST_BASE="+base, "BILI_COMMENT_TEST_COOKIE="+writeLoginCookie(t), fmt.Sprintf("BILI_COMMENT_TEST_FIRST=%d", map[bool]int{true: 1, false: 0}[first]))
		return cmd
	}
	first := child(true)
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Process.Kill() })
	select {
	case <-blocked:
	case <-time.After(10 * time.Second):
		_ = first.Process.Kill()
		t.Fatal("child did not reach second sub page")
	}
	if err := first.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = first.Wait()
	phase.Store(1)
	output, err := child(false).CombinedOutput()
	if err != nil {
		t.Fatalf("restart: %v %s", err, output)
	}
	if !strings.Contains(string(output), "RESUME_EVIDENCE epoch=100 seen=24 first=true") || heads.Load() != 1 || sub1.Load() != 1 {
		t.Fatalf("replayed committed pages: heads=%d sub1=%d output=%s", heads.Load(), sub1.Load(), output)
	}
}
