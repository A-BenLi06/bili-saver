package jobs

import (
	"container/heap"
	"context"
)

// PreferAtYield marks bounded control work. Long handlers lend their worker at
// safe request boundaries; no additional goroutine/worker or API quota is added.
func (m *Manager) PreferAtYield(types ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.controlTypes == nil {
		m.controlTypes = make(map[string]bool)
	}
	for _, typ := range types {
		m.controlTypes[typ] = true
	}
}

func (m *Manager) setControlLocked(id int64, typ string) {
	if m.controlTypes[typ] {
		if m.controlJobs == nil {
			m.controlJobs = make(map[int64]bool)
		}
		m.controlJobs[id] = true
	}
}

func (m *Manager) enqueueReadyLocked(id int64, priority int) {
	if m.controlJobs[id] {
		m.control.push(id, priority)
	} else {
		m.pending.push(id, priority)
	}
}

func (m *Manager) resumeReadyLocked(p pendingJob) {
	if m.controlJobs[p.id] {
		heap.Push(&m.controlResumed, p)
	} else {
		heap.Push(&m.resumed, p)
	}
}

func readyPeek(q *pendingQueue, resumed resumedQueue) pendingJob {
	if len(resumed) > 0 && (q.size == 0 || jobBefore(resumed[0], q.peek())) {
		return resumed[0]
	}
	return q.peek()
}

func readyPop(q *pendingQueue, resumed *resumedQueue) pendingJob {
	if len(*resumed) > 0 && (q.size == 0 || jobBefore((*resumed)[0], q.peek())) {
		return heap.Pop(resumed).(pendingJob)
	}
	return q.pop()
}

func (m *Manager) readySizeLocked() int {
	return m.pending.size + len(m.resumed) + m.control.size + len(m.controlResumed)
}

func (m *Manager) finishReservation(id int64) {
	m.mu.Lock()
	m.releaseVideoLocked(id)
	delete(m.controlJobs, id)
	m.mu.Unlock()
}

func (m *Manager) runOneControl(ctx context.Context) error {
	for {
		m.mu.Lock()
		if ctx.Err() != nil || m.stopped || m.control.size+len(m.controlResumed) == 0 {
			m.mu.Unlock()
			return ctx.Err()
		}
		p := readyPop(&m.control, &m.controlResumed)
		if !m.reserveVideoLocked(p) {
			m.mu.Unlock()
			continue
		}
		m.mu.Unlock()
		// Control handlers have no yield hook, so nesting is bounded at one.
		m.process(ctx, p.id)
		m.finishReservation(p.id)
		return ctx.Err()
	}
}
