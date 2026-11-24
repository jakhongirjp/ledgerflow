package outbox

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"
)

type fakeSource struct {
	mu        sync.Mutex
	pending   []Event
	published []int64
	attempts  map[int64]int
}

func (f *fakeSource) Claim(_ context.Context, limit int) ([]Event, func(context.Context, []int64, []int64) error, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	batch := slices.Clone(f.pending[:min(limit, len(f.pending))])
	return batch, func(_ context.Context, ok, failed []int64) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.published = append(f.published, ok...)
		f.pending = slices.DeleteFunc(f.pending, func(e Event) bool { return slices.Contains(ok, e.ID) })
		for _, id := range append(ok, failed...) {
			f.attempts[id]++
		}
		return nil
	}, nil
}

type fakePublisher struct {
	failIDs map[int64]bool
	got     []int64
}

func (p *fakePublisher) Publish(_ context.Context, e Event) error {
	if p.failIDs[e.ID] {
		return errors.New("broker down")
	}
	p.got = append(p.got, e.ID)
	return nil
}

func events(ids ...int64) []Event {
	out := make([]Event, len(ids))
	for i, id := range ids {
		out[i] = Event{ID: id, Type: "t", Payload: []byte(`{}`)}
	}
	return out
}

func TestRunOncePublishesInOrderAndKeepsFailures(t *testing.T) {
	src := &fakeSource{pending: events(1, 2, 3), attempts: map[int64]int{}}
	pub := &fakePublisher{failIDs: map[int64]bool{2: true}}
	r := &Relay{Source: src, Publisher: pub, BatchSize: 10}

	n, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 || !slices.Equal(pub.got, []int64{1, 3}) {
		t.Fatalf("n=%d got=%v", n, pub.got)
	}
	if len(src.pending) != 1 || src.pending[0].ID != 2 || src.attempts[2] != 1 {
		t.Fatalf("failed event must stay pending with one attempt: %+v %v", src.pending, src.attempts)
	}

	delete(pub.failIDs, 2)
	if n, _ := r.RunOnce(context.Background()); n != 1 || len(src.pending) != 0 {
		t.Fatalf("retry did not publish: n=%d pending=%v", n, src.pending)
	}
}

func TestRunDrainsBacklogAndStops(t *testing.T) {
	ids := make([]int64, 25)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	src := &fakeSource{pending: events(ids...), attempts: map[int64]int{}}
	pub := &fakePublisher{}
	r := &Relay{Source: src, Publisher: pub, BatchSize: 10, Interval: time.Hour}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	deadline := time.After(2 * time.Second)
	for {
		src.mu.Lock()
		n := len(src.published)
		src.mu.Unlock()
		if n == 25 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("backlog not drained, published %d", n)
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
