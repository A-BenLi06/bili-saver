package jobs

// pendingQueue 每个优先级一个 FIFO，只对非空优先级维护最大堆。
// 同级 push/pop 摊销 O(1)，新优先级/桶清空 O(log P)，与积压任务数 N 无关。
// 所有操作由 Manager.mu 保护。
type pendingQueue struct {
	buckets    map[int]*pendingBucket
	priorities []int
	size       int
}

type pendingBucket struct {
	ids  []int64
	head int
}

func (q *pendingQueue) peek() pendingJob {
	prio := q.priorities[0]
	b := q.buckets[prio]
	return pendingJob{id: b.ids[b.head], prio: prio}
}

func (q *pendingQueue) push(id int64, prio int) {
	if q.buckets == nil {
		q.buckets = make(map[int]*pendingBucket)
	}
	b := q.buckets[prio]
	if b == nil {
		b = &pendingBucket{}
		q.buckets[prio] = b
		q.priorities = append(q.priorities, prio)
		for i := len(q.priorities) - 1; i > 0; {
			parent := (i - 1) / 2
			if q.priorities[parent] >= q.priorities[i] {
				break
			}
			q.priorities[parent], q.priorities[i] = q.priorities[i], q.priorities[parent]
			i = parent
		}
	}
	b.ids = append(b.ids, id)
	q.size++
}

func (q *pendingQueue) pop() pendingJob {
	prio := q.priorities[0]
	b := q.buckets[prio]
	id := b.ids[b.head]
	b.head++
	q.size--
	if b.head == len(b.ids) {
		delete(q.buckets, prio)
		last := len(q.priorities) - 1
		q.priorities[0] = q.priorities[last]
		q.priorities = q.priorities[:last]
		for i := 0; 2*i+1 < len(q.priorities); {
			child := 2*i + 1
			if child+1 < len(q.priorities) && q.priorities[child+1] > q.priorities[child] {
				child++
			}
			if q.priorities[i] >= q.priorities[child] {
				break
			}
			q.priorities[i], q.priorities[child] = q.priorities[child], q.priorities[i]
			i = child
		}
	} else if b.head >= 4096 && b.head >= len(b.ids)/2 {
		// 长队列消费后回收已用前缀，避免等待少量尾部任务时保留巨大数组。
		b.ids = append([]int64(nil), b.ids[b.head:]...)
		b.head = 0
	}
	return pendingJob{id: id, prio: prio}
}
