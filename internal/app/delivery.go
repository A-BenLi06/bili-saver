package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"bili-saver/internal/store"
)

func (a *App) startDeliveries(ctx context.Context) {
	a.deliveryOnce.Do(func() {
		ctx, cancel := context.WithCancel(ctx)
		a.deliveryCancel = cancel
		a.deliveryWG.Add(1)
		go func() {
			defer a.deliveryWG.Done()
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			for {
				if err := a.DrainDeliveries(ctx); err != nil && ctx.Err() == nil {
					a.Log.Warn("archive delivery deferred", "err", err)
				}
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
	})
}
func (a *App) DrainDeliveries(ctx context.Context) error {
	a.deliveryMu.Lock()
	defer a.deliveryMu.Unlock()
	rows, err := a.St.PendingDeliveries(store.Now(), 32)
	if err != nil {
		return err
	}
	for _, d := range rows {
		if err = ctx.Err(); err != nil {
			return err
		}
		var runErr error
		switch {
		case d.Target == "media":
			if a.Cfg.View().Media.Enabled {
				var resultErr error
				result, resultErr := a.Med.RebuildAll()
				runErr = resultErr
				if runErr == nil && len(result.Errors) > 0 {
					runErr = fmt.Errorf("library projection incomplete: %v", result.Errors)
				}
			}
		case strings.HasPrefix(d.Target, "plugin:"):
			if !a.Cfg.View().Plugins.Enabled {
				if err = a.St.FinishDelivery(d.ID, fmt.Errorf("plugin engine disabled")); err != nil {
					return err
				}
				continue
			}
			var payload map[string]any
			if runErr = json.Unmarshal([]byte(d.Payload), &payload); runErr == nil {
				runErr = a.Bus.Deliver(strings.TrimPrefix(d.Target, "plugin:"), d.ID, d.EventID, d.Event, payload)
			}
		default:
			runErr = fmt.Errorf("unknown delivery target %s", d.Target)
		}
		if err = a.St.FinishDelivery(d.ID, runErr); err != nil {
			return err
		}
	}
	return nil
}
