package lower

import "testing"

const arenaStorageSource = `import arena
type payload { Items []int, Words Map[string, int] }
func retained() arena.Of[payload] {
	let mut a = arena.New[payload]()
	let r = a.Put(payload{Items: []int{1, 2, 3}, Words: Map[string, int]{"kept": 1}})
	let _ = a.Free(r) catch { case arena.Stale: Unreachable("fresh ref") }
	a
}
func freeSlotIsStale() bool {
	let value = payload{Items: []int{1}, Words: Map[string, int]{"kept": 1}}
	let mut a = arena.New[payload]()
	let mut b = arena.New[payload]()
	let ra = a.Put(value)
	let _ = a.Free(ra) catch { case arena.Stale: Unreachable("fresh ref") }
	let rb = b.Put(value)
	let _ = b.Free(rb) catch { case arena.Stale: Unreachable("fresh ref") }
	// This Ref has a's free slot's generation, but must not read its
	// cleared host storage. Wrong-store handles may alias a live value,
	// never an uninitialized one.
	let other = b.Put(value)
	let _ = a.Get(other) catch { case arena.Stale: return true }
	false
}
func main() {}
`

const arenaStorageDriver = `package main
import (
	"reflect"
	"testing"
)
func TestFreedStorage(t *testing.T) {
	a := retained(nil)
	// Inspect storage, not reachability through Get: its Stale check can
	// hide a retained value. No finalizer or scheduling-dependent GC probe.
	var retains func(reflect.Value) bool
	retains = func(v reflect.Value) bool {
		switch v.Kind() {
		case reflect.Slice, reflect.Map, reflect.Pointer, reflect.Func, reflect.Chan, reflect.Interface:
			return !v.IsNil()
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ { if retains(v.Field(i)) { return true } }
		case reflect.Array:
			for i := 0; i < v.Len(); i++ { if retains(v.Index(i)) { return true } }
		case reflect.String:
			return v.Len() != 0
		}
		return false
	}
	store := reflect.ValueOf(a)
	found := false
	for i := 0; i < store.NumField(); i++ {
		cells := store.Field(i)
		if cells.Kind() != reflect.Slice { continue }
		found = true
		cells = cells.Slice(0, cells.Cap())
		for j := 0; j < cells.Len(); j++ {
			if retains(cells.Index(j)) { t.Fatal("Free retains the removed pointerful value") }
		}
	}
	if !found { t.Fatal("the arena has no physical storage to inspect") }
	if !freeSlotIsStale(nil) { t.Fatal("a foreign-store Ref exposes cleared host storage") }
}
`

// TestStdArenaReleasesStorage catches the F4 mutant that removes the
// clearing write. Existing stale/ref laws see only a typed failure and
// cannot see whether a freed slice or Map stays reachable in storage.
// It builds Go because this observes the actual host storage after Free,
// not just source typing or the lowered program's result.
func TestStdArenaReleasesStorage(t *testing.T) {
	needsBuild(t)
	goTestEmitted(t, mustLower(t, "arenastorage", arenaStorageSource), arenaStorageDriver)
}
