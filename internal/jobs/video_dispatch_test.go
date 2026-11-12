package jobs

import (
	"fmt"
	"math/rand"
	"sort"
	"testing"
)

func TestVideoDispatchOrderNoDuplicateTokens(t *testing.T) {
	m := NewManager(nil, nil, 1)
	m.videoTypes = map[string]bool{"archive": true}
	rng := rand.New(rand.NewSource(7))
	want := make([]pendingJob, 20000)
	for i := range want {
		j := pendingJob{id: int64(i + 1), prio: rng.Intn(8)}
		want[i] = j
		m.setVideoKeyLocked(j.id, "archive", fmt.Sprintf("BV%d", rng.Intn(64)))
		m.pushPendingLocked(j.id, j.prio)
	}
	sort.Slice(want, func(i, j int) bool { return jobBefore(want[i], want[j]) })
	i := 0
	for m.pending.size > 0 || len(m.resumed) > 0 {
		j := m.popReadyLocked()
		if !m.reserveVideoLocked(j) {
			continue
		}
		if i >= len(want) || j != want[i] {
			t.Fatalf("at %d got %+v", i, j)
		}
		i++
		m.releaseVideoLocked(j.id)
	}
	if i != len(want) {
		t.Fatalf("lost jobs: %d", i)
	}
	if len(m.videoKeys)+len(m.videoTokens)+len(m.blockedVideos)+len(m.activeVideos) != 0 {
		t.Fatal("dispatch metadata leaked")
	}
}
