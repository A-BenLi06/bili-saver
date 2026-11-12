package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"bili-saver/internal/biliapi"
	"bili-saver/internal/store"
	"bili-saver/internal/taskflow"
)

const commentPageSize = 20
const commentResumeTTL = 24 * time.Hour

type commentScanResult struct {
	Scan               *store.CommentScan
	Complete, Finished bool
}
type commentRequest struct {
	Main   bool
	Offset string
	Root   *store.CommentScanRoot
}
type commentResponse struct {
	Request commentRequest
	Main    *biliapi.ReplyMainData
	Sub     *biliapi.ReplySubData
	Err     error
}

// ArchiveVideoOpts acknowledges the finished scan only after reconciliation.
func (c *Collector) collectComments(ctx context.Context, aid int64, bvid string, epoch int64, first bool) (int, bool, error) {
	r, err := c.scanComments(ctx, aid, bvid, epoch, first)
	if r == nil {
		return 0, false, err
	}
	if r.Finished {
		fullAt := int64(0)
		if r.Complete {
			fullAt = store.Now()
		}
		if ferr := c.finishCommentScan(r, fullAt); ferr != nil && err == nil {
			err = ferr
			r.Complete = false
		}
	}
	return r.Scan.Seen, r.Complete, err
}
func (c *Collector) finishCommentScan(r *commentScanResult, fullAt int64) error {
	if r.Scan.Capped || (r.Scan.Full && !r.Complete) {
		return c.St.DiscardCommentScan(r.Scan.Bvid)
	}
	return c.St.FinishCommentScan(r.Scan, fullAt)
}

func (c *Collector) scanComments(ctx context.Context, aid int64, bvid string, epoch int64, first bool) (*commentScanResult, error) {
	history, err := c.St.CommentSyncState(bvid)
	if err != nil {
		return nil, err
	}
	scan, err := c.St.LoadCommentScan(bvid)
	if err != nil {
		return nil, err
	}
	policy := fmt.Sprintf("v2:%t:%d:%d:%d", c.Cfg.View().Collector.FullResyncComments, c.Cfg.View().Collector.CommentsMax, c.Cfg.View().Collector.SubRepliesMax, c.Cfg.View().Collector.CommentFullHours)
	now := store.Now()
	session := c.API.SessionKey()
	if scan != nil && (scan.Aid != aid || scan.Session != session || scan.Policy != policy || now-scan.StartedAt > int64(commentResumeTTL/time.Second) || scan.StartedAt > now) {
		c.Log.Info("评论断点失效，重新建立采集轮次", "bvid", bvid)
		scan = nil
	}
	if scan == nil {
		hours := c.Cfg.View().Collector.CommentFullHours
		full := first || (c.Cfg.View().Collector.FullResyncComments && (history.LastFullAt == 0 || hours == 0 || now-history.LastFullAt >= int64(hours)*3600))
		scan = &store.CommentScan{Bvid: bvid, Aid: aid, Epoch: epoch, StartedAt: now, Session: session, Policy: policy, Full: full, First: first, Floor: max(0, history.LatestMainCtime-86400), HighWater: history.LatestMainCtime, RootCursor: history.RootCursor}
		var roots []*store.CommentScanRoot
		if !full {
			roots, err = c.St.RotatingCommentRoots(bvid, history.RootCursor, 4)
			if err != nil {
				return nil, err
			}
			if len(roots) > 0 {
				scan.RootCursor = roots[len(roots)-1].Root
			}
		}
		if err := c.St.StartCommentScan(scan, roots); err != nil {
			return nil, err
		}
	} else {
		c.Log.Info("恢复评论采集断点", "bvid", bvid, "轮次", scan.Epoch, "主楼页", scan.MainPages, "楼中楼页", scan.SubPages)
	}
	r := &commentScanResult{Scan: scan}
	n := cap(c.commentSlots)
	if n == 0 {
		return r, fmt.Errorf("评论并发调度器未初始化")
	}
	workCtx, cancel := context.WithCancel(ctx)
	jobs := make(chan commentRequest, n)
	results := make(chan commentResponse, n)
	fullMode := scan.Full
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-workCtx.Done():
					return
				case req := <-jobs:
					select {
					case c.commentSlots <- struct{}{}:
					case <-workCtx.Done():
						return
					}
					out := commentResponse{Request: req}
					if req.Main {
						mode := 3
						if !fullMode {
							mode = 2
						}
						out.Main, out.Err = c.API.ReplyMainMode(workCtx, aid, req.Offset, mode)
					} else {
						out.Sub, out.Err = c.API.ReplySub(workCtx, aid, req.Root.Root, req.Root.NextPN, commentPageSize)
					}
					<-c.commentSlots
					select {
					case results <- out:
					case <-workCtx.Done():
						return
					}
				}
			}
		}()
	}
	defer func() { cancel(); wg.Wait() }()
	inflight := 0
	mainBusy := false
	active := map[int64]bool{}
	attempts, headRequests := 0, 0
	paused := false
	var scanErr error
	for {
		if err := ctx.Err(); err != nil {
			return r, err
		}
		for !paused && inflight < n && (scan.Full || attempts < 10) && !(c.Cfg.View().Collector.CommentsMax > 0 && scan.Seen >= c.Cfg.View().Collector.CommentsMax) {
			var req commentRequest
			if !scan.MainDone && !mainBusy && (scan.Full || headRequests < 5) {
				req = commentRequest{Main: true, Offset: scan.Offset}
			} else {
				roots, err := c.St.PendingCommentRoots(bvid, n+len(active))
				if err != nil {
					return r, err
				}
				for _, root := range roots {
					if !active[root.Root] {
						req.Root = root
						break
					}
				}
				if req.Root == nil {
					break
				}
			}
			// Queue hooks run only on the coordinator, never concurrently in workers.
			if err := taskflow.Yield(ctx); err != nil {
				return r, err
			}
			if req.Main {
				mainBusy = true
				headRequests++
			} else {
				active[req.Root.Root] = true
			}
			jobs <- req
			inflight++
			attempts++
		}
		if inflight == 0 {
			break
		}
		var response commentResponse
		select {
		case response = <-results:
		case <-ctx.Done():
			return r, ctx.Err()
		}
		inflight--
		if response.Request.Main {
			mainBusy = false
		} else {
			delete(active, response.Request.Root.Root)
		}
		if response.Err != nil {
			paused = true
			if response.Request.Main {
				scanErr = response.Err
				var apiErr *biliapi.APIError
				if response.Request.Offset != "" && errors.As(response.Err, &apiErr) && apiErr.Code == -400 {
					scan.Capped = true
				}
			} else {
				c.Log.Warn("楼中楼抓取暂停，保留断点", "bvid", bvid, "root", response.Request.Root.Root, "err", response.Err)
			}
			continue
		}
		var pageErr error
		if response.Request.Main {
			pageErr = c.commitMainComments(scan, response.Main)
		} else {
			pageErr = c.commitSubComments(scan, response.Request.Root, response.Sub)
		}
		if pageErr != nil {
			return r, pageErr
		}
		c.reportCommentScan(ctx, scan)
	}
	roots, err := c.St.PendingCommentRoots(bvid, 1)
	if err != nil {
		return r, err
	}
	r.Finished = scan.Capped || (scan.MainDone && len(roots) == 0)
	r.Complete = r.Finished && scan.Full && !scan.Capped && !paused && c.API.HasCookie("SESSDATA")
	if r.Complete {
		loggedIn, err := c.API.NavIsLogin(ctx)
		if err != nil {
			r.Complete = false
			return r, err
		}
		r.Complete = loggedIn && c.API.SessionKey() == scan.Session
		if !r.Complete {
			c.Log.Warn("评论完整核对未通过登录态校验，不执行消失检测", "bvid", bvid)
		}
	}
	if scan.Capped {
		r.Complete = false
	}
	return r, scanErr
}

func (c *Collector) commitMainComments(scan *store.CommentScan, data *biliapi.ReplyMainData) error {
	next := *scan
	next.Requests++
	next.MainPages++
	next.Total = data.Cursor.AllCount
	var replies []biliapi.Reply
	if scan.MainPages == 0 {
		replies = append(replies, data.TopReplies...)
	}
	replies = append(replies, data.Replies...)
	ids := make([]int64, 0, len(replies))
	for _, r := range replies {
		ids = append(ids, int64(r.Rpid))
	}
	previous, err := c.St.RootReplyCounts(scan.Bvid, ids)
	if err != nil {
		return err
	}
	var comments []*store.Comment
	var roots []*store.CommentScanRoot
	for _, reply := range replies {
		comments = append(comments, originComment(reply, int64(reply.Root), scan))
		next.HighWater = max(next.HighWater, reply.Ctime)
		preview := map[int64]bool{}
		for _, sr := range reply.Replies {
			comments = append(comments, originComment(sr, int64(reply.Rpid), scan))
			preview[int64(sr.Rpid)] = true
		}
		if reply.Rcount <= 0 {
			continue
		}
		root := &store.CommentScanRoot{Root: int64(reply.Rpid), NextPN: 1, Expected: reply.Rcount, Done: len(preview) >= reply.Rcount}
		if !scan.Full {
			if old, ok := previous[root.Root]; ok {
				if old == reply.Rcount {
					continue
				}
				root.Tail = true
				root.NextPN = max(1, (old-1)/commentPageSize)
			}
		}
		if maxSub := c.Cfg.View().Collector.SubRepliesMax; maxSub > 0 && len(preview) >= maxSub && !root.Done {
			root.Done = true
			next.Capped = true
		}
		roots = append(roots, root)
	}
	oldHead := !scan.Full && scan.Floor > 0 && len(data.Replies) > 0
	for _, reply := range data.Replies {
		if reply.Ctime <= 0 || reply.Ctime > scan.Floor {
			oldHead = false
		}
	}
	nextOffset := data.Cursor.PaginationReply.NextOffset
	next.MainDone = data.Cursor.IsEnd || oldHead
	stalled := false
	if !next.MainDone && nextOffset == scan.Offset {
		progress, err := c.St.HasUnseenScanComments(scan.Bvid, ids)
		if err != nil {
			return err
		}
		stalled = !progress
	}
	if !next.MainDone && (nextOffset == "" || stalled || next.MainPages >= 1000) {
		next.MainDone = true
		next.Capped = true
		c.Log.Warn("评论主楼分页未能继续，跳过完整核对", "bvid", scan.Bvid, "页数", next.MainPages, "重复且无新评论", stalled)
	}
	next.Offset = nextOffset
	if err := c.St.CommitCommentPage(&next, comments, roots, nil, commentPageSize, c.Cfg.View().Collector.CommentsMax, c.Cfg.View().Collector.SubRepliesMax); err != nil {
		return err
	}
	*scan = next
	return nil
}
func (c *Collector) commitSubComments(scan *store.CommentScan, root *store.CommentScanRoot, data *biliapi.ReplySubData) error {
	next := *scan
	next.Requests++
	next.SubPages++
	r := *root
	r.NextPN++
	r.Expected = max(r.Expected, data.Page.Count)
	comments := make([]*store.Comment, 0, len(data.Replies))
	for _, reply := range data.Replies {
		comments = append(comments, originComment(reply, r.Root, scan))
	}
	if err := c.St.CommitCommentPage(&next, comments, nil, &r, commentPageSize, c.Cfg.View().Collector.CommentsMax, c.Cfg.View().Collector.SubRepliesMax); err != nil {
		return err
	}
	*scan = next
	return nil
}
func (c *Collector) reportCommentScan(ctx context.Context, scan *store.CommentScan) {
	f := 0.15
	if scan.Total > 0 {
		f += 0.19 * min(1, float64(scan.Seen)/float64(scan.Total))
	}
	mode := "日常增量"
	if scan.Full {
		mode = "全量核对"
	}
	taskflow.Report(ctx, f, fmt.Sprintf("%s：评论 %d 条，主楼 %d 页，楼中楼 %d 页", mode, scan.Seen, scan.MainPages, scan.SubPages))
}
func originComment(reply biliapi.Reply, parent int64, scan *store.CommentScan) *store.Comment {
	location := ""
	if reply.ReplyControl != nil {
		location = stringsTrimPrefix(reply.ReplyControl.Location, "IP属地：")
	}
	raw, _ := json.Marshal(reply)
	return &store.Comment{Rpid: int64(reply.Rpid), Bvid: scan.Bvid, ParentRpid: parent, RootRpid: orDefault(int64(reply.Root), parent), MemberMid: int64(reply.Member.Mid), MemberName: reply.Member.Uname, MemberFace: reply.Member.Avatar, MemberLevel: reply.Member.LevelInfo.CurrentLevel, Content: reply.Content.Message, LikeCount: reply.Like, ReplyCount: reply.Rcount, Ctime: reply.Ctime, Location: location, FirstSeenAt: scan.Epoch, LastSeenAt: scan.Epoch, RawJSON: string(raw)}
}
func orDefault(v, def int64) int64 {
	if v != 0 {
		return v
	}
	return def
}
func stringsTrimPrefix(s, prefix string) string {
	if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):]
	}
	return s
}
