// Package levels 回填与拉取 B 站用户等级：
//   - 评论者等级：原始 reply 快照（data/raw/x_v2_reply_reply|x_v2_reply_wbi_main）里
//     有完整 member.level_info，可离线回填存量，无需网络；
//   - UP主等级：view 接口的 owner 是裁剪对象不含等级，需 space wbi acc/info 拉取
//     （风控敏感、需登录态，未登录自动跳过）。
//
// teriteri 前端契约：VLevel(handleLevel(user.exp))，故展示层用 LevelExp 把等级
// 映射为经验下限驱动徽章渲染。
package levels

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"bili-saver/internal/biliapi"
	"bili-saver/internal/store"
)

// snapshotDirs 含 member.level_info 的评论快照端点目录（raw/ 下按端点组织）。
var snapshotDirs = []string{"x_v2_reply_reply", "x_v2_reply_wbi_main"}

// ownerFetchPause space 接口风控敏感，逐 UP主 拉取间隔。
const ownerFetchPause = 800 * time.Millisecond

type Backfiller struct {
	St      *store.Store
	DataDir string
	API     *biliapi.Client // 可为 nil：仅支持离线快照回填
	Log     *slog.Logger
}

func New(st *store.Store, dataDir string, api *biliapi.Client, log *slog.Logger) *Backfiller {
	if log == nil {
		log = slog.Default()
	}
	return &Backfiller{St: st, DataDir: dataDir, API: api, Log: log}
}

// snapReply 快照里用到的最小字段（嵌套楼中楼同样带 member.level_info）。
type snapReply struct {
	Rpid   int64 `json:"rpid"`
	Member struct {
		LevelInfo struct {
			CurrentLevel int `json:"current_level"`
		} `json:"level_info"`
	} `json:"member"`
	Replies []snapReply `json:"replies"`
}

type snapBody struct {
	Data struct {
		Replies    []snapReply `json:"replies"`
		TopReplies []snapReply `json:"top_replies"`
	} `json:"data"`
}

func collectLevels(rs []snapReply, into map[int64]int) {
	for _, r := range rs {
		if r.Rpid != 0 {
			into[r.Rpid] = r.Member.LevelInfo.CurrentLevel
		}
		collectLevels(r.Replies, into)
	}
}

// CommentLevelsFromSnapshots 扫全部评论快照回填 member_level（只补 0，幂等）。
// 返回回填行数；单文件损坏跳过不中断。
func (b *Backfiller) CommentLevelsFromSnapshots(ctx context.Context, progress func(done, total int)) (int, error) {
	var files []string
	for _, dir := range snapshotDirs {
		root := filepath.Join(b.DataDir, "raw", dir)
		if _, err := os.Stat(root); err != nil {
			continue
		}
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if !d.IsDir() && strings.HasSuffix(path, ".json.gz") {
				files = append(files, path)
			}
			return nil
		})
	}
	total := len(files)
	if total == 0 {
		b.Log.Warn("未找到评论快照，跳过等级回填", "dir", filepath.Join(b.DataDir, "raw"))
		return 0, nil
	}

	levels := make(map[int64]int, 4096)
	for i, f := range files {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if err := func() error {
			gz, err := os.Open(f)
			if err != nil {
				return err
			}
			defer gz.Close()
			zr, err := gzip.NewReader(gz)
			if err != nil {
				return err
			}
			defer zr.Close()
			var body snapBody
			if err := json.NewDecoder(zr).Decode(&body); err != nil {
				return fmt.Errorf("解析失败: %w", err)
			}
			collectLevels(body.Data.Replies, levels)
			collectLevels(body.Data.TopReplies, levels)
			return nil
		}(); err != nil {
			b.Log.Debug("快照跳过", "file", filepath.Base(f), "err", err)
		}
		if progress != nil {
			progress(i+1, total)
		}
	}
	if len(levels) == 0 {
		return 0, nil
	}

	tx, err := b.St.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck
	stmt, err := tx.Prepare(`UPDATE comments SET member_level=? WHERE rpid=? AND member_level=0`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	n := 0
	for rpid, lv := range levels {
		if lv <= 0 {
			continue
		}
		res, err := stmt.Exec(lv, rpid)
		if err != nil {
			return 0, err
		}
		if affected, _ := res.RowsAffected(); affected > 0 {
			n++
		}
	}
	return n, tx.Commit()
}

// OwnerLevels 对缺失等级的 UP主 拉取 space acc/info 并写 videos.owner_level。
// 未登录（无 SESSDATA）时 skipped=true——space 接口未登录基本必被风控，不硬试。
func (b *Backfiller) OwnerLevels(ctx context.Context) (updated int, skipped bool, err error) {
	if b.API == nil {
		return 0, true, nil
	}
	// 真实登录校验：cookie 存在但失效时，space 接口会返回 code=0 却剥离 level_info
	// 的降级数据（实测踩过），必须用 nav 真值而非 cookie 存在性做门槛。
	if !b.API.CachedLogin(ctx) {
		b.Log.Warn("未登录，跳过 UP主等级拉取（space 接口需登录态）；扫码登录后重跑 fetch_levels 即可")
		return 0, true, nil
	}
	type row struct {
		Mid int64
	}
	rows, err := b.St.DB.Query(`SELECT DISTINCT owner_mid FROM videos
		WHERE owner_mid>0 AND owner_level=0`)
	if err != nil {
		return 0, false, err
	}
	var mids []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.Mid); err != nil {
			rows.Close()
			return 0, false, err
		}
		mids = append(mids, r)
	}
	rows.Close()
	for i, r := range mids {
		if err := ctx.Err(); err != nil {
			return updated, false, err
		}
		info, err := b.API.SpaceAccInfo(ctx, r.Mid)
		if err != nil {
			b.Log.Warn("UP主等级拉取失败（可能风控），中断本轮", "mid", r.Mid, "err", err)
			return updated, false, err
		}
		if info.LevelInfo == nil {
			b.Log.Warn("space 返回缺少 level_info（登录态无效或软风控），停止本轮；请重新扫码登录后重试", "mid", r.Mid)
			return updated, true, nil
		}
		if _, err := b.St.DB.Exec(`UPDATE videos SET owner_level=? WHERE owner_mid=? AND owner_level=0`,
			info.LevelInfo.CurrentLevel, r.Mid); err != nil {
			return updated, false, err
		}
		updated++
		if i+1 < len(mids) {
			time.Sleep(ownerFetchPause)
		}
	}
	return updated, false, nil
}

// EnsureOwnerLevel 单视频增量钩子：该 UP主尚无等级记录时拉一次（已有时零网络）。
func (b *Backfiller) EnsureOwnerLevel(ctx context.Context, bvid string) error {
	var mid, level int64
	if err := b.St.DB.QueryRow(`SELECT owner_mid, owner_level FROM videos WHERE bvid=?`, bvid).Scan(&mid, &level); err != nil {
		return err
	}
	if mid <= 0 || level > 0 {
		return nil
	}
	_, skipped, err := b.OwnerLevels(ctx)
	if err != nil {
		return err
	}
	if skipped {
		b.Log.Debug("未登录，UP主等级暂缺", "bvid", bvid)
	}
	return nil
}

// LevelExp B站等级 → teriteri-client handleLevel() 的经验下限：
// 前端按 exp<50→0 / <200→1 / <1500→2 / <4500→3 / <10800→4 / <28800→5 / 其余→6 渲染徽章。
func LevelExp(level int) int {
	var floors = [...]int{0, 50, 200, 1500, 4500, 10800, 28800}
	if level < 0 {
		return floors[0]
	}
	if level >= len(floors) {
		return floors[len(floors)-1]
	}
	return floors[level]
}
