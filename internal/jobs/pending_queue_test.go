package jobs

import (
	"math/rand"
	"sort"
	"testing"
)

// 随机交错入队/消费与稳定排序参考实现比较，覆盖任意优先级、桶删除/重建、FIFO。
func TestPendingQueueMatchesStablePriorityOrder(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	var q pendingQueue
	var expected []pendingJob
	for id := int64(1); id <= 2500; id++ {
		p := r.Intn(31) - 15
		q.push(id, p)
		expected = append(expected, pendingJob{id: id, prio: p})
		if id%11 == 0 {
			sort.SliceStable(expected, func(i, j int) bool { return expected[i].prio > expected[j].prio })
			for i := 0; i < 7; i++ {
				if got := q.pop(); got != expected[0] {
					t.Fatalf("got %+v want %+v", got, expected[0])
				}
				expected = expected[1:]
			}
		}
	}
	sort.SliceStable(expected, func(i, j int) bool { return expected[i].prio > expected[j].prio })
	for _, want := range expected {
		if got := q.pop(); got != want {
			t.Fatalf("got %+v want %+v", got, want)
		}
	}
	if q.size != 0 || len(q.buckets) != 0 || len(q.priorities) != 0 {
		t.Fatal("queue not drained")
	}
	// 大桶跨过已用前缀回收门槛，队尾顺序不变。
	for id := int64(1); id <= 20000; id++ {
		q.push(id, 1)
	}
	for id := int64(1); id <= 20000; id++ {
		if got := q.pop(); got.id != id {
			t.Fatalf("compaction lost FIFO: %+v expected %d", got, id)
		}
	}
}

// 保留前一版二分插入+数组搬移作为基线，比较 2 万普通任务中插入 2 千优先任务。
func BenchmarkPendingPriorityBurst(b *testing.B) {
	for _, mode := range []string{"sorted-slice", "priority-buckets"} {
		b.Run(mode, func(b *testing.B) {
			b.ReportAllocs()
			for round := 0; round < b.N; round++ {
				if mode == "sorted-slice" {
					var q []pendingJob
					for id := int64(1); id <= 22000; id++ {
						p := 0
						if id > 20000 {
							p = 1
						}
						pos := sort.Search(len(q), func(i int) bool { return q[i].prio < p })
						q = append(q, pendingJob{})
						copy(q[pos+1:], q[pos:])
						q[pos] = pendingJob{id: id, prio: p}
					}
					var sum int64
					for len(q) > 0 {
						sum += q[0].id
						q = q[1:]
					}
					if sum == 0 {
						b.Fatal("empty")
					}
				} else {
					var q pendingQueue
					for id := int64(1); id <= 22000; id++ {
						p := 0
						if id > 20000 {
							p = 1
						}
						q.push(id, p)
					}
					var sum int64
					for q.size > 0 {
						sum += q.pop().id
					}
					if sum == 0 {
						b.Fatal("empty")
					}
				}
			}
		})
	}
}
