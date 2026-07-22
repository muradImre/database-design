// Package btreeidx implements a concurrent ordered key/value index as a
// copy-on-write (COW) B-tree. It satisfies the project's DBIndex interface.
//
// Concurrency model:
//
//   - Nodes are immutable once published. A mutation clones every node on the
//     path from the root to the change and swaps in a new root with a single
//     atomic store. Published nodes are therefore never modified in place.
//   - Readers (Find, Query) load the root pointer atomically and traverse an
//     immutable snapshot. They take no locks, never block, never block writers,
//     and always observe a consistent tree — so a range scan is a stable
//     snapshot without any retry logic.
//   - Writers (Upsert, Remove) are serialized by a single mutex. This is a
//     single-writer / multi-reader (MVCC-style) model: writes never block reads.
//
// This is a genuine concurrent B-tree we own, not a global lock around a
// non-concurrent map. Getting concurrent *writers* would require fine-grained
// latch coupling; the COW design is chosen here for correctness and lock-free
// reads.
package btreeidx

import (
	"cmp"
	"context"
	"sync"
	"sync/atomic"

	"github.com/muradImre/database-design/pair"
)

// defaultDegree is the minimum degree t: each node holds between t-1 and 2t-1
// keys (the root may hold fewer), and up to 2t children.
const defaultDegree = 32

// node is an immutable B-tree node. Leaves have no children.
type node[K cmp.Ordered, V any] struct {
	keys     []K
	vals     []V
	children []*node[K, V]
}

func (n *node[K, V]) leaf() bool { return len(n.children) == 0 }

// clone returns a shallow copy whose slices are independently owned, so the
// copy can be mutated without touching the (published, immutable) original.
func (n *node[K, V]) clone() *node[K, V] {
	m := &node[K, V]{
		keys: append([]K(nil), n.keys...),
		vals: append([]V(nil), n.vals...),
	}
	if len(n.children) > 0 {
		m.children = append([]*node[K, V](nil), n.children...)
	}
	return m
}

// search returns the index of key in n, or the child index to descend into.
func (n *node[K, V]) search(key K) (int, bool) {
	lo, hi := 0, len(n.keys)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if n.keys[mid] < key {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo < len(n.keys) && n.keys[lo] == key {
		return lo, true
	}
	return lo, false
}

// Tree is a concurrent copy-on-write B-tree.
type Tree[K cmp.Ordered, V any] struct {
	root atomic.Pointer[node[K, V]]
	t    int
	wmu  sync.Mutex // serializes writers
}

// New creates an empty B-tree index.
func New[K cmp.Ordered, V any]() *Tree[K, V] {
	return &Tree[K, V]{t: defaultDegree}
}

func (tr *Tree[K, V]) maxKeys() int { return 2*tr.t - 1 }

// Find returns the value for key, if present. It is lock-free.
func (tr *Tree[K, V]) Find(key K) (V, bool) {
	n := tr.root.Load()
	for n != nil {
		i, found := n.search(key)
		if found {
			return n.vals[i], true
		}
		if n.leaf() {
			break
		}
		n = n.children[i]
	}
	var zero V
	return zero, false
}

// Upsert inserts or updates key. The check callback observes the current value
// and returns the value to store. It runs under the writer lock so the
// observe-and-set is atomic with respect to other writers.
//
// If the key exists and check returns an error,
// Upsert returns (false, nil); if the key is new and check errors, it returns
// (false, err); on success it returns (true, nil).
func (tr *Tree[K, V]) Upsert(key K, check func(key K, currValue V, exists bool) (newValue V, err error)) (bool, error) {
	tr.wmu.Lock()
	defer tr.wmu.Unlock()

	curr, exists := tr.findLocked(key)
	newValue, err := check(key, curr, exists)
	if err != nil {
		if exists {
			return false, nil
		}
		return false, err
	}
	tr.insertLocked(key, newValue)
	return true, nil
}

// Remove deletes key, returning the previous value when present.
func (tr *Tree[K, V]) Remove(key K) (V, bool) {
	tr.wmu.Lock()
	defer tr.wmu.Unlock()

	root := tr.root.Load()
	if root == nil {
		var zero V
		return zero, false
	}
	newRoot, val, removed := tr.deleteNode(root, key)
	if !removed {
		return val, false
	}
	// Shrink the height if the root became an empty internal node.
	if !newRoot.leaf() && len(newRoot.keys) == 0 {
		newRoot = newRoot.children[0]
	}
	if newRoot.leaf() && len(newRoot.keys) == 0 {
		tr.root.Store(nil)
	} else {
		tr.root.Store(newRoot)
	}
	return val, true
}

// Query returns every pair with start <= key <= end in ascending order.
// When start == end it returns the entire tree (the convention used by the
// storage layer for an unscoped listing). It is lock-free and operates on a
// consistent snapshot. Cancelling ctx stops early and returns the partial
// result with a nil error.
func (tr *Tree[K, V]) Query(ctx context.Context, start K, end K) ([]pair.Pair[K, V], error) {
	root := tr.root.Load()
	var results []pair.Pair[K, V]
	fullScan := start == end

	var walk func(n *node[K, V]) bool
	walk = func(n *node[K, V]) bool {
		if n == nil {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		default:
		}
		i := 0
		if !fullScan {
			i, _ = n.search(start)
		}
		if n.leaf() {
			for ; i < len(n.keys); i++ {
				if !fullScan && n.keys[i] > end {
					return true
				}
				results = append(results, pair.Pair[K, V]{Key: n.keys[i], Value: n.vals[i]})
			}
			return true
		}
		for ; i <= len(n.keys); i++ {
			if !walk(n.children[i]) {
				return false
			}
			if i < len(n.keys) {
				k := n.keys[i]
				if !fullScan && k > end {
					return true
				}
				if fullScan || k >= start {
					results = append(results, pair.Pair[K, V]{Key: k, Value: n.vals[i]})
				}
			}
		}
		return true
	}
	walk(root)
	return results, nil
}

// findLocked reads the current value; callers must hold wmu.
func (tr *Tree[K, V]) findLocked(key K) (V, bool) {
	return tr.Find(key)
}

// insertLocked inserts or replaces key; callers must hold wmu.
func (tr *Tree[K, V]) insertLocked(key K, val V) {
	root := tr.root.Load()
	if root == nil {
		tr.root.Store(&node[K, V]{keys: []K{key}, vals: []V{val}})
		return
	}
	nc, split, mk, mv, right := tr.insertNode(root, key, val)
	if split {
		tr.root.Store(&node[K, V]{
			keys:     []K{mk},
			vals:     []V{mv},
			children: []*node[K, V]{nc, right},
		})
		return
	}
	tr.root.Store(nc)
}

// insertNode returns the rewritten subtree. If the subtree split, it returns
// split=true with the promoted median (mk/mv) and the new right sibling.
func (tr *Tree[K, V]) insertNode(n *node[K, V], key K, val V) (out *node[K, V], split bool, mk K, mv V, right *node[K, V]) {
	m := n.clone()
	i, found := m.search(key)
	if found {
		m.vals[i] = val
		return m, false, mk, mv, nil
	}
	if m.leaf() {
		m.keys = insertAt(m.keys, i, key)
		m.vals = insertAt(m.vals, i, val)
	} else {
		nc, childSplit, ck, cv, cr := tr.insertNode(m.children[i], key, val)
		m.children[i] = nc
		if childSplit {
			m.keys = insertAt(m.keys, i, ck)
			m.vals = insertAt(m.vals, i, cv)
			m.children = insertAt(m.children, i+1, cr)
		}
	}
	if len(m.keys) > tr.maxKeys() {
		return tr.splitNode(m)
	}
	return m, false, mk, mv, nil
}

// splitNode splits an overfull node (2t keys) into two, promoting the median.
func (tr *Tree[K, V]) splitNode(m *node[K, V]) (*node[K, V], bool, K, V, *node[K, V]) {
	mid := tr.t - 1
	left := &node[K, V]{
		keys: append([]K(nil), m.keys[:mid]...),
		vals: append([]V(nil), m.vals[:mid]...),
	}
	right := &node[K, V]{
		keys: append([]K(nil), m.keys[mid+1:]...),
		vals: append([]V(nil), m.vals[mid+1:]...),
	}
	if !m.leaf() {
		left.children = append([]*node[K, V](nil), m.children[:mid+1]...)
		right.children = append([]*node[K, V](nil), m.children[mid+1:]...)
	}
	return left, true, m.keys[mid], m.vals[mid], right
}

// deleteNode removes key from the subtree rooted at n (immutable) and returns
// the rewritten subtree. It maintains the B-tree invariant that any child it
// descends into has at least t keys (borrow or merge as needed).
func (tr *Tree[K, V]) deleteNode(n *node[K, V], key K) (*node[K, V], V, bool) {
	var zero V
	x := n.clone()
	i, found := x.search(key)

	if found {
		if x.leaf() {
			val := x.vals[i]
			x.keys = removeAt(x.keys, i)
			x.vals = removeAt(x.vals, i)
			return x, val, true
		}
		val := x.vals[i]
		left := x.children[i]
		right := x.children[i+1]
		switch {
		case len(left.keys) >= tr.t:
			pk, pv := maxEntry(left)
			nc, _, _ := tr.deleteNode(left, pk)
			x.children[i] = nc
			x.keys[i] = pk
			x.vals[i] = pv
		case len(right.keys) >= tr.t:
			sk, sv := minEntry(right)
			nc, _, _ := tr.deleteNode(right, sk)
			x.children[i+1] = nc
			x.keys[i] = sk
			x.vals[i] = sv
		default:
			merged := tr.merge(x, i)
			nc, _, _ := tr.deleteNode(merged, key)
			x.children[i] = nc
		}
		return x, val, true
	}

	if x.leaf() {
		return x, zero, false
	}
	if len(x.children[i].keys) < tr.t {
		i = tr.fill(x, i)
	}
	nc, val, removed := tr.deleteNode(x.children[i], key)
	x.children[i] = nc
	return x, val, removed
}

// fill ensures x.children[i] has at least t keys by borrowing from a sibling or
// merging, and returns the index of the child to descend into afterwards.
func (tr *Tree[K, V]) fill(x *node[K, V], i int) int {
	if i > 0 && len(x.children[i-1].keys) >= tr.t {
		tr.borrowPrev(x, i)
		return i
	}
	if i < len(x.children)-1 && len(x.children[i+1].keys) >= tr.t {
		tr.borrowNext(x, i)
		return i
	}
	if i < len(x.children)-1 {
		tr.merge(x, i)
		return i
	}
	tr.merge(x, i-1)
	return i - 1
}

// merge combines children[i], separator key[i], and children[i+1] into one
// node placed at children[i]; it mutates the (cloned) node x. Returns the
// merged node.
func (tr *Tree[K, V]) merge(x *node[K, V], i int) *node[K, V] {
	left := x.children[i].clone()
	right := x.children[i+1]

	left.keys = append(left.keys, x.keys[i])
	left.vals = append(left.vals, x.vals[i])
	left.keys = append(left.keys, right.keys...)
	left.vals = append(left.vals, right.vals...)
	if !left.leaf() {
		left.children = append(left.children, right.children...)
	}

	x.keys = removeAt(x.keys, i)
	x.vals = removeAt(x.vals, i)
	x.children[i] = left
	x.children = removeAt(x.children, i+1)
	return left
}

// borrowPrev moves a key from the left sibling into children[i] through x.
func (tr *Tree[K, V]) borrowPrev(x *node[K, V], i int) {
	child := x.children[i].clone()
	sib := x.children[i-1].clone()

	child.keys = append([]K{x.keys[i-1]}, child.keys...)
	child.vals = append([]V{x.vals[i-1]}, child.vals...)

	last := len(sib.keys) - 1
	x.keys[i-1] = sib.keys[last]
	x.vals[i-1] = sib.vals[last]
	sib.keys = sib.keys[:last]
	sib.vals = sib.vals[:last]

	if !child.leaf() {
		lc := sib.children[len(sib.children)-1]
		child.children = append([]*node[K, V]{lc}, child.children...)
		sib.children = sib.children[:len(sib.children)-1]
	}

	x.children[i-1] = sib
	x.children[i] = child
}

// borrowNext moves a key from the right sibling into children[i] through x.
func (tr *Tree[K, V]) borrowNext(x *node[K, V], i int) {
	child := x.children[i].clone()
	sib := x.children[i+1].clone()

	child.keys = append(child.keys, x.keys[i])
	child.vals = append(child.vals, x.vals[i])

	x.keys[i] = sib.keys[0]
	x.vals[i] = sib.vals[0]
	sib.keys = sib.keys[1:]
	sib.vals = sib.vals[1:]

	if !child.leaf() {
		fc := sib.children[0]
		child.children = append(child.children, fc)
		sib.children = sib.children[1:]
	}

	x.children[i] = child
	x.children[i+1] = sib
}

// maxEntry returns the largest key/value in the subtree (reads only).
func maxEntry[K cmp.Ordered, V any](n *node[K, V]) (K, V) {
	for !n.leaf() {
		n = n.children[len(n.children)-1]
	}
	return n.keys[len(n.keys)-1], n.vals[len(n.vals)-1]
}

// minEntry returns the smallest key/value in the subtree (reads only).
func minEntry[K cmp.Ordered, V any](n *node[K, V]) (K, V) {
	for !n.leaf() {
		n = n.children[0]
	}
	return n.keys[0], n.vals[0]
}

// insertAt inserts v at index i, returning the grown slice.
func insertAt[T any](s []T, i int, v T) []T {
	var zero T
	s = append(s, zero)
	copy(s[i+1:], s[i:])
	s[i] = v
	return s
}

// removeAt removes the element at index i, returning the shrunk slice.
func removeAt[T any](s []T, i int) []T {
	copy(s[i:], s[i+1:])
	return s[:len(s)-1]
}
