package store

import (
	"sync"
	"testing"
)

func TestAtomicActiveJobCreation(t *testing.T) {
	s := openTest(t)
	const payload = `{"bvid":"BVatomic"}`
	var wg sync.WaitGroup
	ids := make(chan int64, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := s.CreateActiveJobPrio("archive", payload, 5)
			if err != nil {
				t.Error(err)
			}
			ids <- id
		}()
	}
	wg.Wait()
	close(ids)
	var created int
	var first int64
	for id := range ids {
		if id != 0 {
			created++
			first = id
		}
	}
	if created != 1 {
		t.Fatalf("created %d duplicates", created)
	}
	j, err := s.GetJob(first)
	if err != nil || j.Priority != 5 {
		t.Fatalf("priority: %+v %v", j, err)
	}
	if err := s.UpdateJobState(first, "running", "", "", 0); err != nil {
		t.Fatal(err)
	}
	if id, err := s.CreateActiveJobPrio("archive", payload, 8); err != nil || id != 0 {
		t.Fatalf("running dedup: %d %v", id, err)
	}
	if err := s.UpdateJobState(first, "done", "", "", 1); err != nil {
		t.Fatal(err)
	}
	if id, err := s.CreateActiveJobPrio("archive", payload, 8); err != nil || id <= first {
		t.Fatalf("terminal retry: %d %v", id, err)
	}
	if id, err := s.CreateActiveJobPrio("download", payload, 0); err != nil || id == 0 {
		t.Fatalf("different type: %d %v", id, err)
	}
	if id, err := s.CreateActiveJobPrio("archive", `{"bvid":"BVatomic","download":true}`, 0); err != nil || id == 0 {
		t.Fatalf("different scope: %d %v", id, err)
	}
}
