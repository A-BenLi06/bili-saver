package jobs

// 唤醒任务保留原加入顺序；只有资源释放后的队首进入小堆，普通积压仍按桶 O(1)。
type resumedQueue []pendingJob

func (q resumedQueue) Len() int           { return len(q) }
func jobBefore(a, b pendingJob) bool      { return a.prio > b.prio || (a.prio == b.prio && a.id < b.id) }
func (q resumedQueue) Less(i, j int) bool { return jobBefore(q[i], q[j]) }
func (q resumedQueue) Swap(i, j int)      { q[i], q[j] = q[j], q[i] }
func (q *resumedQueue) Push(v any)        { *q = append(*q, v.(pendingJob)) }
func (q *resumedQueue) Pop() any          { n := len(*q) - 1; v := (*q)[n]; *q = (*q)[:n]; return v }

func (m *Manager) popReadyLocked() pendingJob {
	if m.control.size+len(m.controlResumed) > 0 &&
		(m.pending.size+len(m.resumed) == 0 || jobBefore(readyPeek(&m.control, m.controlResumed), readyPeek(&m.pending, m.resumed))) {
		return readyPop(&m.control, &m.controlResumed)
	}
	return readyPop(&m.pending, &m.resumed)
}
