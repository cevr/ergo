package v1

import (
	"fmt"
	"strings"
	"testing"
)

// TestWithCapacity: an empty slice with room for n, never n fabricated
// elements; a negative n is a defect (stdlib.md, Slices).
func TestWithCapacity(t *testing.T) {
	xs := WithCapacity[string](3)
	if len(xs) != 0 || cap(xs) != 3 {
		t.Fatalf("len %d cap %d; want 0, 3", len(xs), cap(xs))
	}
	xs = Append(xs, "a")
	xs = Append(xs, "b")
	if fmt.Sprint(xs) != "[a b]" {
		t.Fatalf("%v; want [a b]", xs)
	}
	n := -1
	p := recovered(func() { WithCapacity[int](n) })
	if e, ok := p.(error); !ok || !strings.Contains(e.Error(), "makeslice") {
		t.Fatalf("WithCapacity(-1) raised %#v; want Go's makeslice defect", p)
	}
}

// TestGet: Get is Go's comma-ok read, the zero value and false for a
// missing key.
func TestGet(t *testing.T) {
	m := map[string]int{"a": 1}
	if v, ok := Get(m, "a"); v != 1 || !ok {
		t.Fatalf("Get(a) = %v, %v; want 1, true", v, ok)
	}
	if v, ok := Get(m, "b"); v != 0 || ok {
		t.Fatalf("Get(b) = %v, %v; want 0, false", v, ok)
	}
}

// TestDelete: Delete removes the key from the Map itself and returns it.
func TestDelete(t *testing.T) {
	m := map[string]int{"a": 1, "b": 2}
	got := Delete(m, "a")
	if len(got) != 1 || got["b"] != 2 || len(m) != 1 {
		t.Fatalf("Delete gave %v, left %v; want map[b:2] for both", got, m)
	}
}
