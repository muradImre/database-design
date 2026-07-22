package shardedidx_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/muradImre/database-design/btreeidx"
	"github.com/muradImre/database-design/shardedidx"
)

func set(idx interface {
	Upsert(key string, check func(key string, curr int, exists bool) (int, error)) (bool, error)
}, key string, val int) {
	_, _ = idx.Upsert(key, func(_ string, _ int, _ bool) (int, error) { return val, nil })
}

func TestBasicOps(t *testing.T) {
	tr := shardedidx.New[string, int]()
	for i := 0; i < 100; i++ {
		set(tr, fmt.Sprintf("k%03d", i), i)
	}
	if v, ok := tr.Find("k050"); !ok || v != 50 {
		t.Fatalf("Find k050 = (%d,%v), want (50,true)", v, ok)
	}
	if _, ok := tr.Find("missing"); ok {
		t.Fatal("Find missing should be false")
	}

	all, err := tr.Query(context.Background(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 100 {
		t.Fatalf("full scan len = %d, want 100", len(all))
	}
	for i := 1; i < len(all); i++ {
		if all[i-1].Key >= all[i].Key {
			t.Fatalf("results not sorted at %d: %q >= %q", i, all[i-1].Key, all[i].Key)
		}
	}

	ranged, err := tr.Query(context.Background(), "k010", "k019")
	if err != nil {
		t.Fatal(err)
	}
	if len(ranged) != 10 || ranged[0].Key != "k010" || ranged[9].Key != "k019" {
		t.Fatalf("range query wrong: got %d entries, first=%q", len(ranged), ranged[0].Key)
	}

	if _, ok := tr.Remove("k050"); !ok {
		t.Fatal("Remove k050 should succeed")
	}
	if _, ok := tr.Find("k050"); ok {
		t.Fatal("k050 should be gone")
	}
}

// TestConcurrentWriters checks that many goroutines writing distinct keys land
// on their shards without data loss. Run under -race to validate the model.
func TestConcurrentWriters(t *testing.T) {
	tr := shardedidx.New[string, int]()
	const writers, perWriter = 16, 500

	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				key := fmt.Sprintf("w%02d-k%04d", w, i)
				set(tr, key, w*perWriter+i)
			}
		}(w)
	}
	wg.Wait()

	all, err := tr.Query(context.Background(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != writers*perWriter {
		t.Fatalf("expected %d entries, got %d", writers*perWriter, len(all))
	}
}

// BenchmarkConcurrentWrites contrasts write throughput of the single-writer COW
// tree with the sharded index under many concurrent writers.
func BenchmarkConcurrentWrites(b *testing.B) {
	b.Run("cow", func(b *testing.B) {
		tr := btreeidx.New[string, int]()
		benchWrites(b, func(k string, v int) { set(tr, k, v) })
	})
	b.Run("sharded", func(b *testing.B) {
		tr := shardedidx.New[string, int]()
		benchWrites(b, func(k string, v int) { set(tr, k, v) })
	})
}

func benchWrites(b *testing.B, write func(string, int)) {
	b.ReportAllocs()
	var goroutine int64
	b.RunParallel(func(pb *testing.PB) {
		id := atomic.AddInt64(&goroutine, 1)
		i := 0
		for pb.Next() {
			write(fmt.Sprintf("key-%d-%d", id, i), i)
			i++
		}
	})
}
