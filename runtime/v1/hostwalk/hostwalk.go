// Package hostwalk gives the runtime its walk of a value as data (binsize
// BS8): a durable actor's recovered state may hold hosts the recover made
// (runtime/v1 actor_recover.go), and only reflect can find them in a
// state of any type. The core does not link reflect, so a program that
// recovers hosts imports this package, whose init sets the walk
// (rt.UseHeldWalk).
package hostwalk

import (
	"path"
	"reflect"

	"ergo.local/prototype/runtime/rt"
)

func init() { rt.UseHeldWalk(held) }

// rtPath is the import path of runtime/rt.
var rtPath = reflect.TypeFor[rt.Task]().PkgPath()

// runtimePkgs are the packages whose values held does not enter: runtime/rt
// and runtime/v1, its sibling, which this package does not import (a test
// of runtime/v1 imports it).
var runtimePkgs = map[string]bool{
	rtPath:                   true,
	path.Dir(rtPath) + "/v1": true,
}

// held reports, for each pointer of targets, whether the value root points
// to holds it. It walks the value as data: structs, arrays, slices, maps,
// interfaces and pointers, each pointer once. It does not enter a function
// (a host a closure captures is not held as data) or a pointer to a value
// of the runtime's own types (an actor, a queue, a Ref, or a host that is
// not in targets), which is a handle, not data.
func held(root any, targets []any) []bool {
	want := make(map[uintptr]bool, len(targets))
	for _, h := range targets {
		want[reflect.ValueOf(h).Pointer()] = false
	}
	found := 0
	seen := map[uintptr]bool{}
	stack := []reflect.Value{reflect.ValueOf(root).Elem()}
	for len(stack) > 0 && found < len(want) {
		v := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		switch v.Kind() {
		case reflect.Pointer:
			if v.IsNil() {
				continue
			}
			p := v.Pointer()
			if was, ok := want[p]; ok {
				if !was {
					want[p] = true
					found++
				}
				continue
			}
			if seen[p] || runtimePkgs[v.Type().Elem().PkgPath()] {
				continue
			}
			seen[p] = true
			stack = append(stack, v.Elem())
		case reflect.Interface:
			if !v.IsNil() {
				stack = append(stack, v.Elem())
			}
		case reflect.Struct:
			for i := v.NumField() - 1; i >= 0; i-- {
				stack = append(stack, v.Field(i))
			}
		case reflect.Slice, reflect.Array:
			if !mayHold(v.Type().Elem()) {
				continue
			}
			for i := v.Len() - 1; i >= 0; i-- {
				stack = append(stack, v.Index(i))
			}
		case reflect.Map:
			if !mayHold(v.Type().Key()) && !mayHold(v.Type().Elem()) {
				continue
			}
			it := v.MapRange()
			for it.Next() {
				stack = append(stack, it.Key(), it.Value())
			}
		}
	}
	out := make([]bool, len(targets))
	for i, h := range targets {
		out[i] = want[reflect.ValueOf(h).Pointer()]
	}
	return out
}

// mayHold reports whether a value of type t may hold a pointer: an
// element of a basic kind holds no host.
func mayHold(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Struct, reflect.Slice, reflect.Array, reflect.Map:
		return true
	}
	return false
}
