package levels

import (
	"context"
	"time"
)

type ownerFlight struct {
	done chan struct{}
	err  error
}
type ownerResult struct {
	level int
	valid bool
	until time.Time
}

// Incremental archive hooks only query this video's owner, never the full
// library. Failed/soft-blocked owners cool down five minutes; level 0 is cached
// as a valid response too. Manual OwnerLevels remains an explicit full backfill.
func (b *Backfiller) ensureMid(ctx context.Context, mid int64) error {
	if b.API == nil || !b.API.HasCookie("SESSDATA") {
		return nil
	}
	b.ownerMu.Lock()
	if r, ok := b.ownerCache[mid]; ok && time.Now().Before(r.until) {
		b.ownerMu.Unlock()
		if r.valid {
			return b.saveOwnerLevel(mid, r.level)
		}
		return nil
	}
	if f := b.ownerFlights[mid]; f != nil {
		b.ownerMu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-f.done:
			return f.err
		}
	}
	if b.ownerFlights == nil {
		b.ownerFlights = map[int64]*ownerFlight{}
		b.ownerCache = map[int64]ownerResult{}
	}
	f := &ownerFlight{done: make(chan struct{})}
	b.ownerFlights[mid] = f
	b.ownerMu.Unlock()
	info, err := b.API.SpaceAccInfo(ctx, mid)
	r := ownerResult{until: time.Now().Add(5 * time.Minute)}
	if err == nil && info.LevelInfo != nil {
		r.level, r.valid, r.until = info.LevelInfo.CurrentLevel, true, time.Now().Add(12*time.Hour)
		err = b.saveOwnerLevel(mid, r.level)
	} else if err == nil {
		b.Log.Warn("UP主等级响应缺少等级（登录态无效或软风控），仅该UP暂停5分钟后重试", "mid", mid)
	}
	b.ownerMu.Lock()
	if ctx.Err() == nil && (err == nil || !r.valid) {
		if len(b.ownerCache) >= 1024 {
			for key := range b.ownerCache {
				delete(b.ownerCache, key)
				break
			}
		}
		b.ownerCache[mid] = r
	}
	f.err = err
	delete(b.ownerFlights, mid)
	close(f.done)
	b.ownerMu.Unlock()
	return err
}

func (b *Backfiller) saveOwnerLevel(mid int64, level int) error {
	if level == 0 {
		return nil // Valid level zero has no database work to apply.
	}
	_, err := b.St.DB.Exec(`UPDATE videos SET owner_level=? WHERE owner_mid=? AND owner_level=0 AND owner_level<>?`, level, mid, level)
	return err
}
