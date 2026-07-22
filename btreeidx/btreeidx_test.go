package btreeidx

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"sync"
	"testing"
)

func TestBasicOps(t *testing.T) {
	tr := New[string, int]()

	ok, err := tr.Upsert("b", func(k string, curr int, exists bool) (int, error) {
		if exists {
			t.Fatal("should not exist")
		}
		return 2, nil
	})
	if !ok || err != nil {
		t.Fatalf("upsert: ok=%v err=%v", ok, err)
	}

	for k, v := range map[string]int{"a": 1, "c": 3} {
		if ok, err := tr.Upsert(k, func(string, int, bool) (int, error) { return v, nil }); !ok || err != nil {
			t.Fatalf("upsert %s: %v %v", k, ok, err)
		}
	}

	if v, found := tr.Find("b"); !found || v != 2 {
		t.Fatalf("find b: %v %v", v, found)
	}
	if _, found := tr.Find("zzz"); found {
		t.Fatal("zzz should not exist")
	}

	results, err := tr.Query(context.Background(), "a", "b")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Key != "a" || results[1].Key != "b" {
		t.Fatalf("range: %+v", results)
	}

	if v, removed := tr.Remove("a"); !removed || v != 1 {
		t.Fatalf("remove: %v %v", v, removed)
	}
	if _, found := tr.Find("a"); found {
		t.Fatal("a should be gone")
	}
	if _, removed := tr.Remove("a"); removed {
		t.Fatal("double remove should report false")
	}
}

// TestSplitAndMergeSorted inserts and deletes enough keys to force many node
// splits and merges, verifying ordered iteration stays correct throughout.
func TestSplitAndMergeSorted(t *testing.T) {
	tr := New[int, int]()
	const n = 5000

	perm := rand.Perm(n)
	for _, k := range perm {
		if ok, err := tr.Upsert(k, func(int, int, bool) (int, error) { return k * 10, nil }); !ok || err != nil {
			t.Fatalf("upsert %d: %v %v", k, ok, err)
		}
	}

	full, _ := tr.Query(context.Background(), 0, n)
	if len(full) != n {
		t.Fatalf("full scan: got %d want %d", len(full), n)
	}
	for i := 0; i < n; i++ {
		if full[i].Key != i || full[i].Value != i*10 {
			t.Fatalf("scan order wrong at %d: %+v", i, full[i])
		}
	}

	// Delete a random half.
	del := rand.Perm(n)[:n/2]
	deleted := make(map[int]bool, len(del))
	for _, k := range del {
		if _, removed := tr.Remove(k); !removed {
			t.Fatalf("remove %d reported not found", k)
		}
		deleted[k] = true
	}

	var want []int
	for k := 0; k < n; k++ {
		if !deleted[k] {
			want = append(want, k)
		}
	}
	res, _ := tr.Query(context.Background(), 0, n)
	if len(res) != len(want) {
		t.Fatalf("after delete: got %d want %d", len(res), len(want))
	}
	for i, w := range want {
		if res[i].Key != w || res[i].Value != w*10 {
			t.Fatalf("mismatch at %d: got %+v want key %d", i, res[i], w)
		}
	}
	// Remaining keys still findable; deleted ones gone.
	for k := 0; k < n; k++ {
		v, found := tr.Find(k)
		if deleted[k] {
			if found {
				t.Fatalf("deleted key %d still present", k)
			}
		} else if !found || v != k*10 {
			t.Fatalf("find %d: %v %v", k, v, found)
		}
	}
}

func TestUpsertReplaceAndCheckError(t *testing.T) {
	tr := New[int, string]()
	tr.Upsert(1, func(int, string, bool) (string, error) { return "a", nil })
	tr.Upsert(1, func(k int, curr string, exists bool) (string, error) {
		if !exists || curr != "a" {
			t.Fatalf("expected existing 'a', got exists=%v curr=%q", exists, curr)
		}
		return "b", nil
	})
	if v, _ := tr.Find(1); v != "b" {
		t.Fatalf("expected replaced value b, got %q", v)
	}

	// check error on existing key -> (false, nil), value unchanged
	ok, err := tr.Upsert(1, func(int, string, bool) (string, error) {
		return "", fmt.Errorf("nope")
	})
	if ok || err != nil {
		t.Fatalf("existing+error should be (false,nil), got (%v,%v)", ok, err)
	}
	if v, _ := tr.Find(1); v != "b" {
		t.Fatalf("value should be unchanged, got %q", v)
	}

	// check error on new key -> (false, err)
	ok, err = tr.Upsert(2, func(int, string, bool) (string, error) {
		return "", fmt.Errorf("boom")
	})
	if ok || err == nil {
		t.Fatalf("new+error should be (false,err), got (%v,%v)", ok, err)
	}
}

func TestRangeBounds(t *testing.T) {
	tr := New[int, int]()
	for i := 0; i < 100; i++ {
		tr.Upsert(i, func(int, int, bool) (int, error) { return i, nil })
	}
	res, _ := tr.Query(context.Background(), 10, 20)
	if len(res) != 11 || res[0].Key != 10 || res[len(res)-1].Key != 20 {
		t.Fatalf("inclusive range [10,20] wrong: %d entries", len(res))
	}
	res, _ = tr.Query(context.Background(), -5, 5)
	if len(res) != 6 {
		t.Fatalf("range [-5,5] should clamp to [0,5]=6, got %d", len(res))
	}
}

func TestQueryCancel(t *testing.T) {
	tr := New[int, int]()
	for i := 0; i < 1000; i++ {
		tr.Upsert(i, func(int, int, bool) (int, error) { return i, nil })
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := tr.Query(ctx, 0, 1000)
	if err != nil {
		t.Fatalf("cancel should not error, got %v", err)
	}
	if len(res) > 1 {
		t.Fatalf("cancelled query returned too much: %d", len(res))
	}
}

// TestConcurrentReadWrite runs many writers and readers together. Run with
// -race to check the copy-on-write discipline.
func TestConcurrentReadWrite(t *testing.T) {
	tr := New[int, int]()
	const keys = 500

	var wg sync.WaitGroup
	// Writers: upsert and delete.
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(seed)))
			for i := 0; i < 5000; i++ {
				k := rng.Intn(keys)
				if rng.Intn(3) == 0 {
					tr.Remove(k)
				} else {
					tr.Upsert(k, func(int, int, bool) (int, error) { return k, nil })
				}
			}
		}(w)
	}
	// Readers: point lookups and range scans on live snapshots.
	for r := 0; r < 8; r++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(seed + 1000)))
			for i := 0; i < 5000; i++ {
				tr.Find(rng.Intn(keys))
				res, _ := tr.Query(context.Background(), 0, keys)
				// A snapshot must always be sorted and unique.
				if !sort.SliceIsSorted(res, func(a, b int) bool { return res[a].Key < res[b].Key }) {
					t.Error("snapshot not sorted")
					return
				}
			}
		}(r)
	}
	wg.Wait()
}

// BenchmarkParallelReads measures point-lookup throughput under many concurrent
// readers. Because reads are lock-free, throughput should scale with -cpu.
func BenchmarkParallelReads(b *testing.B) {
	tr := New[int, int]()
	const keys = 100_000
	for i := 0; i < keys; i++ {
		tr.Upsert(i, func(int, int, bool) (int, error) { return i, nil })
	}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		rng := rand.New(rand.NewSource(1))
		for pb.Next() {
			tr.Find(rng.Intn(keys))
		}
	})
}
