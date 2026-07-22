// Package patch implements RFC 6902 JSON Patch. An Applier takes a target JSON
// document and a JSON Patch document (both as raw bytes) and returns the patched
// document, supporting the add, remove, replace, move, copy, and test
// operations with RFC 6901 JSON Pointer paths.
package patch

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// Operation is a single RFC 6902 operation.
type Operation struct {
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	From  string          `json:"from,omitempty"`
	Value json.RawMessage `json:"value,omitempty"`
}

// Applier applies JSON Patch documents. It holds no state and is safe for
// concurrent use.
type Applier struct{}

// New returns a new Applier.
func New() *Applier { return &Applier{} }

// Apply parses patchJSON as an RFC 6902 patch, applies it to document, and
// returns the resulting JSON. The original inputs are never mutated.
func (a *Applier) Apply(document, patchJSON []byte) ([]byte, error) {
	var ops []Operation
	if err := json.Unmarshal(patchJSON, &ops); err != nil {
		return nil, fmt.Errorf("invalid patch document: %w", err)
	}

	var doc any
	if err := json.Unmarshal(document, &doc); err != nil {
		return nil, fmt.Errorf("invalid target document: %w", err)
	}

	for i, op := range ops {
		next, err := apply(doc, op)
		if err != nil {
			return nil, fmt.Errorf("operation %d (%s %s): %w", i, op.Op, op.Path, err)
		}
		doc = next
	}
	return json.Marshal(doc)
}

func apply(doc any, op Operation) (any, error) {
	switch op.Op {
	case "add":
		v, err := decodeValue(op)
		if err != nil {
			return nil, err
		}
		return addAt(doc, mustTokens(op.Path), v)
	case "replace":
		v, err := decodeValue(op)
		if err != nil {
			return nil, err
		}
		return replaceAt(doc, mustTokens(op.Path), v)
	case "remove":
		toks, err := tokens(op.Path)
		if err != nil {
			return nil, err
		}
		next, _, err := removeAt(doc, toks)
		return next, err
	case "move":
		return move(doc, op)
	case "copy":
		return copyOp(doc, op)
	case "test":
		return test(doc, op)
	default:
		return nil, fmt.Errorf("unsupported op %q", op.Op)
	}
}

func move(doc any, op Operation) (any, error) {
	fromToks, err := tokens(op.From)
	if err != nil {
		return nil, fmt.Errorf("invalid from: %w", err)
	}
	next, moved, err := removeAt(doc, fromToks)
	if err != nil {
		return nil, err
	}
	return addAt(next, mustTokens(op.Path), moved)
}

func copyOp(doc any, op Operation) (any, error) {
	fromToks, err := tokens(op.From)
	if err != nil {
		return nil, fmt.Errorf("invalid from: %w", err)
	}
	v, err := getAt(doc, fromToks)
	if err != nil {
		return nil, err
	}
	return addAt(doc, mustTokens(op.Path), deepCopy(v))
}

func test(doc any, op Operation) (any, error) {
	toks, err := tokens(op.Path)
	if err != nil {
		return nil, err
	}
	got, err := getAt(doc, toks)
	if err != nil {
		return nil, err
	}
	want, err := decodeValue(op)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(got, want) {
		return nil, fmt.Errorf("test failed: value mismatch")
	}
	return doc, nil
}

// addAt inserts value at the location described by toks (RFC 6902 "add").
func addAt(node any, toks []string, value any) (any, error) {
	if len(toks) == 0 {
		return value, nil
	}
	tok := toks[0]
	if len(toks) == 1 {
		switch n := node.(type) {
		case map[string]any:
			n[tok] = value
			return n, nil
		case []any:
			if tok == "-" {
				return append(n, value), nil
			}
			idx, err := index(tok, len(n), true)
			if err != nil {
				return nil, err
			}
			n = append(n, nil)
			copy(n[idx+1:], n[idx:])
			n[idx] = value
			return n, nil
		default:
			return nil, fmt.Errorf("cannot add to non-container")
		}
	}
	return recurse(node, toks, func(child any) (any, error) {
		return addAt(child, toks[1:], value)
	})
}

// replaceAt overwrites an existing location (RFC 6902 "replace").
func replaceAt(node any, toks []string, value any) (any, error) {
	if len(toks) == 0 {
		return value, nil
	}
	tok := toks[0]
	if len(toks) == 1 {
		switch n := node.(type) {
		case map[string]any:
			if _, ok := n[tok]; !ok {
				return nil, fmt.Errorf("path not found: %q", tok)
			}
			n[tok] = value
			return n, nil
		case []any:
			idx, err := index(tok, len(n), false)
			if err != nil {
				return nil, err
			}
			n[idx] = value
			return n, nil
		default:
			return nil, fmt.Errorf("cannot replace in non-container")
		}
	}
	return recurse(node, toks, func(child any) (any, error) {
		return replaceAt(child, toks[1:], value)
	})
}

// removeAt deletes a location, returning the new node and the removed value.
func removeAt(node any, toks []string) (any, any, error) {
	if len(toks) == 0 {
		return nil, nil, fmt.Errorf("cannot remove document root")
	}
	tok := toks[0]
	if len(toks) == 1 {
		switch n := node.(type) {
		case map[string]any:
			v, ok := n[tok]
			if !ok {
				return nil, nil, fmt.Errorf("path not found: %q", tok)
			}
			delete(n, tok)
			return n, v, nil
		case []any:
			idx, err := index(tok, len(n), false)
			if err != nil {
				return nil, nil, err
			}
			v := n[idx]
			return append(n[:idx], n[idx+1:]...), v, nil
		default:
			return nil, nil, fmt.Errorf("cannot remove from non-container")
		}
	}
	var removed any
	next, err := recurse(node, toks, func(child any) (any, error) {
		nc, r, err := removeAt(child, toks[1:])
		removed = r
		return nc, err
	})
	return next, removed, err
}

// recurse descends one level into node at toks[0], applies fn to the child, and
// writes the result back, returning the (possibly reallocated) node.
func recurse(node any, toks []string, fn func(child any) (any, error)) (any, error) {
	tok := toks[0]
	switch n := node.(type) {
	case map[string]any:
		child, ok := n[tok]
		if !ok {
			return nil, fmt.Errorf("path not found: %q", tok)
		}
		nc, err := fn(child)
		if err != nil {
			return nil, err
		}
		n[tok] = nc
		return n, nil
	case []any:
		idx, err := index(tok, len(n), false)
		if err != nil {
			return nil, err
		}
		nc, err := fn(n[idx])
		if err != nil {
			return nil, err
		}
		n[idx] = nc
		return n, nil
	default:
		return nil, fmt.Errorf("path traverses non-container at %q", tok)
	}
}

func getAt(node any, toks []string) (any, error) {
	cur := node
	for _, tok := range toks {
		switch n := cur.(type) {
		case map[string]any:
			v, ok := n[tok]
			if !ok {
				return nil, fmt.Errorf("path not found: %q", tok)
			}
			cur = v
		case []any:
			idx, err := index(tok, len(n), false)
			if err != nil {
				return nil, err
			}
			cur = n[idx]
		default:
			return nil, fmt.Errorf("path traverses non-container at %q", tok)
		}
	}
	return cur, nil
}

// tokens parses an RFC 6901 JSON Pointer into its reference tokens.
func tokens(path string) ([]string, error) {
	if path == "" {
		return nil, nil
	}
	if !strings.HasPrefix(path, "/") {
		return nil, fmt.Errorf("invalid JSON pointer %q", path)
	}
	parts := strings.Split(path[1:], "/")
	for i, p := range parts {
		p = strings.ReplaceAll(p, "~1", "/")
		p = strings.ReplaceAll(p, "~0", "~")
		parts[i] = p
	}
	return parts, nil
}

// mustTokens parses a pointer, returning an empty slice on error so the caller
// treats an invalid pointer as the document root; add/replace validate targets.
func mustTokens(path string) []string {
	t, err := tokens(path)
	if err != nil {
		return nil
	}
	return t
}

// index parses an array reference token. When allowEnd is true, an index equal
// to the length (or "-") is permitted, denoting the position after the last
// element (used by "add").
func index(tok string, length int, allowEnd bool) (int, error) {
	if tok == "-" {
		if allowEnd {
			return length, nil
		}
		return 0, fmt.Errorf("index %q not valid here", tok)
	}
	if len(tok) > 1 && tok[0] == '0' {
		return 0, fmt.Errorf("invalid array index %q", tok)
	}
	idx, err := strconv.Atoi(tok)
	if err != nil || idx < 0 {
		return 0, fmt.Errorf("invalid array index %q", tok)
	}
	limit := length - 1
	if allowEnd {
		limit = length
	}
	if idx > limit {
		return 0, fmt.Errorf("array index %d out of range", idx)
	}
	return idx, nil
}

func decodeValue(op Operation) (any, error) {
	if len(op.Value) == 0 {
		return nil, fmt.Errorf("%q requires a value", op.Op)
	}
	var v any
	if err := json.Unmarshal(op.Value, &v); err != nil {
		return nil, fmt.Errorf("invalid value: %w", err)
	}
	return v, nil
}

func deepCopy(v any) any {
	switch n := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(n))
		for k, vv := range n {
			m[k] = deepCopy(vv)
		}
		return m
	case []any:
		s := make([]any, len(n))
		for i, vv := range n {
			s[i] = deepCopy(vv)
		}
		return s
	default:
		return v
	}
}
