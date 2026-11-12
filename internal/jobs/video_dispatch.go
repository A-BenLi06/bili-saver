package jobs

import (
	"encoding/json"
)

// RegisterVideo 注册按 payload.bvid 串行的执行体；在排发前互斥，等待者不占 worker。
// 普通任务与无 bvid 的异常载荷仍按原队列处理。Start 前注册。
func (m *Manager) RegisterVideo(jobType string, h Handler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handlers[jobType] = h
	if m.videoTypes == nil {
		m.videoTypes = make(map[string]bool)
	}
	m.videoTypes[jobType] = true
}

func (m *Manager) setVideoKeyLocked(id int64, jobType, bvid string) {
	if !m.videoTypes[jobType] || bvid == "" {
		return
	}
	if m.videoKeys == nil {
		m.videoKeys = make(map[int64]string)
		m.videoTokens = make(map[int64]int)
		m.activeVideos = make(map[string]int64)
		m.blockedVideos = make(map[string]*pendingQueue)
	}
	m.videoKeys[id] = bvid
}

func (m *Manager) submittedVideoKeyLocked(id int64, jobType string, payload []byte) {
	if !m.videoTypes[jobType] {
		return
	}
	var key struct {
		Bvid string `json:"bvid"`
	}
	if json.Unmarshal(payload, &key) == nil {
		m.setVideoKeyLocked(id, jobType, key.Bvid)
	}
}

// 每个视频保存自己的优先级 FIFO；全局就绪队列只放尚未运行视频的队首。
// 高优先级到来改变队首时旧令牌惰性作废，避免扫描/删除全局积压队列。
func (m *Manager) enqueueVideoLocked(pj pendingJob) {
	key := m.videoKeys[pj.id]
	q := m.blockedVideos[key]
	var old pendingJob
	if q == nil {
		q = &pendingQueue{}
		m.blockedVideos[key] = q
	} else if q.size > 0 {
		old = q.peek()
	}
	q.push(pj.id, pj.prio)
	if m.activeVideos[key] == 0 {
		head := q.peek()
		if head.id != old.id {
			m.videoTokens[head.id]++
			m.enqueueReadyLocked(head.id, head.prio)
		}
	}
}

// reserveVideoLocked 只接受当前队首令牌，同视频等待者从未占用 worker。
func (m *Manager) reserveVideoLocked(pj pendingJob) bool {
	tokens, keyed := m.videoTokens[pj.id]
	if keyed {
		if tokens == 1 {
			delete(m.videoTokens, pj.id)
		} else {
			m.videoTokens[pj.id] = tokens - 1
		}
	}
	key := m.videoKeys[pj.id]
	if key == "" {
		return !keyed // 旧令牌可能在对应任务结束之后才出队。
	}
	q := m.blockedVideos[key]
	if m.activeVideos[key] != 0 || q == nil || q.peek().id != pj.id {
		return false
	}
	q.pop()
	if q.size == 0 {
		delete(m.blockedVideos, key)
	}
	m.activeVideos[key] = pj.id
	return true
}

func (m *Manager) releaseVideoLocked(id int64) {
	key := m.videoKeys[id]
	if key == "" {
		return
	}
	delete(m.videoKeys, id)
	delete(m.activeVideos, key)
	if q := m.blockedVideos[key]; q != nil {
		next := q.peek()
		m.videoTokens[next.id]++
		m.resumeReadyLocked(next)
	}
	m.cond.Broadcast()
}
