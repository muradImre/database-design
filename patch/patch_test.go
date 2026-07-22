package patch_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/muradImre/database-design/patch"
)

func apply(t *testing.T, doc, ops string) ([]byte, error) {
	t.Helper()
	return patch.New().Apply([]byte(doc), []byte(ops))
}

func mustEqualJSON(t *testing.T, got []byte, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("unmarshal got: %v", err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("unmarshal want: %v", err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestAddReplaceRemove(t *testing.T) {
	out, err := apply(t, `{"a":1}`, `[{"op":"add","path":"/b","value":2}]`)
	if err != nil {
		t.Fatal(err)
	}
	mustEqualJSON(t, out, `{"a":1,"b":2}`)

	out, err = apply(t, `{"a":1}`, `[{"op":"replace","path":"/a","value":9}]`)
	if err != nil {
		t.Fatal(err)
	}
	mustEqualJSON(t, out, `{"a":9}`)

	out, err = apply(t, `{"a":1,"b":2}`, `[{"op":"remove","path":"/b"}]`)
	if err != nil {
		t.Fatal(err)
	}
	mustEqualJSON(t, out, `{"a":1}`)
}

func TestNestedAndArrays(t *testing.T) {
	doc := `{"user":{"tags":["x","z"]}}`
	out, err := apply(t, doc, `[{"op":"add","path":"/user/tags/1","value":"y"}]`)
	if err != nil {
		t.Fatal(err)
	}
	mustEqualJSON(t, out, `{"user":{"tags":["x","y","z"]}}`)

	out, err = apply(t, doc, `[{"op":"add","path":"/user/tags/-","value":"end"}]`)
	if err != nil {
		t.Fatal(err)
	}
	mustEqualJSON(t, out, `{"user":{"tags":["x","z","end"]}}`)

	out, err = apply(t, doc, `[{"op":"remove","path":"/user/tags/0"}]`)
	if err != nil {
		t.Fatal(err)
	}
	mustEqualJSON(t, out, `{"user":{"tags":["z"]}}`)
}

func TestMoveCopyTest(t *testing.T) {
	out, err := apply(t, `{"a":{"x":1},"b":{}}`, `[{"op":"move","from":"/a/x","path":"/b/y"}]`)
	if err != nil {
		t.Fatal(err)
	}
	mustEqualJSON(t, out, `{"a":{},"b":{"y":1}}`)

	out, err = apply(t, `{"a":1}`, `[{"op":"copy","from":"/a","path":"/b"}]`)
	if err != nil {
		t.Fatal(err)
	}
	mustEqualJSON(t, out, `{"a":1,"b":1}`)

	if _, err := apply(t, `{"a":1}`, `[{"op":"test","path":"/a","value":1}]`); err != nil {
		t.Fatalf("passing test should succeed: %v", err)
	}
	if _, err := apply(t, `{"a":1}`, `[{"op":"test","path":"/a","value":2}]`); err == nil {
		t.Fatal("failing test should error")
	}
}

func TestErrors(t *testing.T) {
	cases := []struct{ name, doc, ops string }{
		{"replace missing", `{"a":1}`, `[{"op":"replace","path":"/missing","value":1}]`},
		{"remove missing", `{"a":1}`, `[{"op":"remove","path":"/missing"}]`},
		{"bad op", `{"a":1}`, `[{"op":"frobnicate","path":"/a","value":1}]`},
		{"index out of range", `{"a":[1]}`, `[{"op":"replace","path":"/a/5","value":1}]`},
		{"remove root", `{"a":1}`, `[{"op":"remove","path":""}]`},
	}
	for _, c := range cases {
		if _, err := apply(t, c.doc, c.ops); err == nil {
			t.Errorf("%s: expected error", c.name)
		}
	}
}

// TestFailedOpDoesNotPersistPartial documents that a failing op aborts the whole
// patch; callers receive an error and should keep the original document.
func TestSequentialOps(t *testing.T) {
	out, err := apply(t, `{"n":0}`, `[
		{"op":"replace","path":"/n","value":1},
		{"op":"add","path":"/m","value":2},
		{"op":"remove","path":"/n"}
	]`)
	if err != nil {
		t.Fatal(err)
	}
	mustEqualJSON(t, out, `{"m":2}`)
}
