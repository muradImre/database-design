// Package shardedidx implements a concurrent ordered key/value index that
// supports concurrent writers by striping keys across several independent
// copy-on-write B-trees (btreeidx.Tree).
//
// Concurrency model:
//
//   - Each key is hashed to one of N shards. A shard is a full COW B-tree, so
//     reads within a shard are lock-free and writers within a shard are
//     serialized by that shard's own mutex.
//   - Because different keys usually land on different shards, writers to
//     distinct keys proceed in parallel — the single-writer bottleneck of a lone
//     B-tree is reduced to one-writer-per-shard. This is the classic striping /
//     partitioning approach to concurrent writers.
//
// Tradeoff versus latch-coupling: a range/full query fans out to every shard and
// merges the results. Each shard's slice is a consistent snapshot, but the merged
// view across shards is not a single atomic snapshot of the whole index. For an
// unordered document store that listing semantics are acceptable, and in return
// the design stays provably correct by reusing the tested COW tree rather than
// hand-written node latches.
package shardedidx

import (
	"cmp"
	"context"
	"fmt"
	"hash/fnv"
	"sort"

	"github.com/muradImre/database-design/btreeidx"
	"github.com/muradImre/database-design/pair"
)

// defaultShards is the number of independent trees. More shards allow more
// concurrent writers at the cost of wider query fan-out.
const defaultShards = 16

// Tree is a sharded, concurrent ordered index.
type Tree[K cmp.Ordered, V any] struct {
	shards []*btreeidx.Tree[K, V]
}

// New creates a sharded index with the default shard count.
func New[K cmp.Ordered, V any]() *Tree[K, V] {
	return NewWithShards[K, V](defaultShards)
}

// NewWithShards creates a sharded index with n shards (n is clamped to >= 1).
func NewWithShards[K cmp.Ordered, V any](n int) *Tree[K, V] {
	if n < 1 {
		n = 1
	}
	shards := make([]*btreeidx.Tree[K, V], n)
	for i := range shards {
		shards[i] = btreeidx.New[K, V]()
	}
	return &Tree[K, V]{shards: shards}
}

// shardFor selects the shard that owns key.
func (t *Tree[K, V]) shardFor(key K) *btreeidx.Tree[K, V] {
	h := fnv.New32a()
	fmt.Fprintf(h, "%v", key)
	return t.shards[h.Sum32()%uint32(len(t.shards))]
}

// Upsert inserts or updates key in its shard. Upserts to keys on different
// shards run concurrently.
func (t *Tree[K, V]) Upsert(key K, check func(key K, currValue V, exists bool) (newValue V, err error)) (bool, error) {
	return t.shardFor(key).Upsert(key, check)
}

// Remove deletes key from its shard, returning the previous value if present.
func (t *Tree[K, V]) Remove(key K) (V, bool) {
	return t.shardFor(key).Remove(key)
}

// Find returns the value for key, if present. It is lock-free.
func (t *Tree[K, V]) Find(key K) (V, bool) {
	return t.shardFor(key).Find(key)
}

// Query returns every pair with start <= key <= end (or the whole index when
// start == end), in ascending key order, by querying every shard and merging.
func (t *Tree[K, V]) Query(ctx context.Context, start K, end K) ([]pair.Pair[K, V], error) {
	var all []pair.Pair[K, V]
	for _, sh := range t.shards {
		res, err := sh.Query(ctx, start, end)
		if err != nil {
			return nil, err
		}
		all = append(all, res...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Key < all[j].Key })
	return all, nil
}
