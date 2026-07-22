// Package pair defines a generic ordered key/value tuple used by the index
// implementations to return query results.
package pair

import "cmp"

// Pair is a key/value tuple returned by ordered range queries.
type Pair[K cmp.Ordered, V any] struct {
	Key   K
	Value V
}
