package jobs

import "bili-saver/internal/store"

// PumpUserDownloads reserves quota before enqueueing. Waiting requests consume no worker.
func (m *Manager) PumpUserDownloads(dataDir string) error {
	if err := m.St.ReconcileDownloadRequests(); err != nil {
		return err
	}
	requests, err := m.St.PendingUserDownloads(store.Now())
	if err != nil {
		return err
	}
	for _, r := range requests {
		complete := m.St.VideoFilesPresent(dataDir, r.Bvid)
		m.mu.Lock()
		id, bvid, priority, err := m.St.QueueUserDownload(r.ID, store.Now(), complete)
		if err == nil && id != 0 {
			m.setVideoKeyLocked(id, "user_download", bvid)
			m.pushPendingLocked(id, priority)
			m.cond.Signal()
		}
		m.mu.Unlock()
		if err != nil {
			return err
		}
	}
	return nil
}
