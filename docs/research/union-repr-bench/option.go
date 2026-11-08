package unionreprbench

import (
	"strconv"
	"strings"
)

// Question 10 (X1, std-align §4.5): the prelude's Option[T] in form A (what
// the lowering emits today: lower/decls.go, intrinsic Get) and in form C
// (struct{ v T; ok bool }), against Go's comma-ok.
//
// None of these functions is marked noinline: the lowered Get and Cut are
// small Go functions that Go may inline, and inlining lets escape analysis
// keep a box on the stack when the Option does not escape. The benchmark
// measures what Go does with them.

type Some[T any] struct{ Value T }
type None struct{}

type OptionA[T any] interface{ isOptionA() }

func (Some[_]) isOptionA() {}
func (None) isOptionA()    {}

// getA is the body the lowering writes for the prelude's m.Get(k).
func getA[K comparable, V any](m map[K]V, k K) OptionA[V] {
	if v, ok := m[k]; ok {
		return Some[V]{Value: v}
	}
	return None{}
}

// OptionC is form C. The zero value is None, which is the one empty
// variant; v is zero when ok is false (the zero-slot invariant).
type OptionC[T any] struct {
	v  T
	ok bool
}

func getC[K comparable, V any](m map[K]V, k K) OptionC[V] {
	v, ok := m[k]
	return OptionC[V]{v: v, ok: ok}
}

// Halves is std strings.Halves.
type Halves struct{ Before, After string }

// cutA is std strings.Cut (internal/sketch/std/strings/strings.ergo) as the
// lowering emits it: Ergo over Index and Slice, returning Option[Halves].
func cutA(s, sep string) OptionA[Halves] {
	i := strings.Index(s, sep)
	if i < 0 {
		return None{}
	}
	return Some[Halves]{Value: Halves{Before: s[0:i], After: s[i+len(sep):]}}
}

func cutC(s, sep string) OptionC[Halves] {
	i := strings.Index(s, sep)
	if i < 0 {
		return OptionC[Halves]{}
	}
	return OptionC[Halves]{v: Halves{Before: s[0:i], After: s[i+len(sep):]}, ok: true}
}

// Inputs: 1024 keys, half of them in the map.

func optionMapInt(small bool) (map[string]int, []string) {
	m := map[string]int{}
	keys := make([]string, 1024)
	r := newRand(10)
	for i := range keys {
		keys[i] = "key-" + strconv.Itoa(i)
		if r.next()%2 == 0 {
			v := 256 + i
			if small {
				v = i % 256
			}
			m[keys[i]] = v
		}
	}
	return m, keys
}

func optionMapString() (map[string]string, []string) {
	m := map[string]string{}
	keys := make([]string, 1024)
	r := newRand(11)
	for i := range keys {
		keys[i] = "key-" + strconv.Itoa(i)
		if r.next()%2 == 0 {
			m[keys[i]] = "value-" + keys[i]
		}
	}
	return m, keys
}

func cutInputs() []string {
	out := make([]string, 1024)
	r := newRand(12)
	for i := range out {
		if r.next()%2 == 0 {
			out[i] = "key" + strconv.Itoa(i) + "=value" + strconv.Itoa(i)
		} else {
			out[i] = "key" + strconv.Itoa(i)
		}
	}
	return out
}

// The consumers match the Option and fold it into an int, as a lowered
// `match m.Get(k) { case Some as s: … case None: … }` does.

func sumGetIntA(m map[string]int, keys []string, n int) int {
	t := 0
	for i := 0; i < n; i++ {
		switch o := getA(m, keys[i&1023]).(type) {
		case Some[int]:
			t += o.Value
		case None:
			t--
		}
	}
	return t
}

func sumGetIntC(m map[string]int, keys []string, n int) int {
	t := 0
	for i := 0; i < n; i++ {
		if o := getC(m, keys[i&1023]); o.ok {
			t += o.v
		} else {
			t--
		}
	}
	return t
}

func sumGetIntGo(m map[string]int, keys []string, n int) int {
	t := 0
	for i := 0; i < n; i++ {
		if v, ok := m[keys[i&1023]]; ok {
			t += v
		} else {
			t--
		}
	}
	return t
}

func sumGetStringA(m map[string]string, keys []string, n int) int {
	t := 0
	for i := 0; i < n; i++ {
		switch o := getA(m, keys[i&1023]).(type) {
		case Some[string]:
			t += len(o.Value)
		case None:
			t--
		}
	}
	return t
}

func sumGetStringC(m map[string]string, keys []string, n int) int {
	t := 0
	for i := 0; i < n; i++ {
		if o := getC(m, keys[i&1023]); o.ok {
			t += len(o.v)
		} else {
			t--
		}
	}
	return t
}

func sumGetStringGo(m map[string]string, keys []string, n int) int {
	t := 0
	for i := 0; i < n; i++ {
		if v, ok := m[keys[i&1023]]; ok {
			t += len(v)
		} else {
			t--
		}
	}
	return t
}

func sumCutA(in []string, n int) int {
	t := 0
	for i := 0; i < n; i++ {
		switch o := cutA(in[i&1023], "=").(type) {
		case Some[Halves]:
			t += len(o.Value.Before) - len(o.Value.After)
		case None:
			t--
		}
	}
	return t
}

func sumCutC(in []string, n int) int {
	t := 0
	for i := 0; i < n; i++ {
		if o := cutC(in[i&1023], "="); o.ok {
			t += len(o.v.Before) - len(o.v.After)
		} else {
			t--
		}
	}
	return t
}

func sumCutGo(in []string, n int) int {
	t := 0
	for i := 0; i < n; i++ {
		if before, after, ok := strings.Cut(in[i&1023], "="); ok {
			t += len(before) - len(after)
		} else {
			t--
		}
	}
	return t
}

// The Option escapes: it is returned through a function that is not
// inlined, as an Option is when a std body returns it to user code in
// another function. This is the case the box cannot stay on the stack.

//go:noinline
func lookupA(m map[string]int, k string) OptionA[int] { return getA(m, k) }

//go:noinline
func lookupC(m map[string]int, k string) OptionC[int] { return getC(m, k) }

//go:noinline
func lookupGo(m map[string]int, k string) (int, bool) {
	v, ok := m[k]
	return v, ok
}

//go:noinline
func cutOutA(s, sep string) OptionA[Halves] { return cutA(s, sep) }

//go:noinline
func cutOutC(s, sep string) OptionC[Halves] { return cutC(s, sep) }

//go:noinline
func cutOutGo(s, sep string) (string, string, bool) { return strings.Cut(s, sep) }

func sumCutOutA(in []string, n int) int {
	t := 0
	for i := 0; i < n; i++ {
		switch o := cutOutA(in[i&1023], "=").(type) {
		case Some[Halves]:
			t += len(o.Value.Before) - len(o.Value.After)
		case None:
			t--
		}
	}
	return t
}

func sumCutOutC(in []string, n int) int {
	t := 0
	for i := 0; i < n; i++ {
		if o := cutOutC(in[i&1023], "="); o.ok {
			t += len(o.v.Before) - len(o.v.After)
		} else {
			t--
		}
	}
	return t
}

func sumCutOutGo(in []string, n int) int {
	t := 0
	for i := 0; i < n; i++ {
		if before, after, ok := cutOutGo(in[i&1023], "="); ok {
			t += len(before) - len(after)
		} else {
			t--
		}
	}
	return t
}

func sumLookupA(m map[string]int, keys []string, n int) int {
	t := 0
	for i := 0; i < n; i++ {
		switch o := lookupA(m, keys[i&1023]).(type) {
		case Some[int]:
			t += o.Value
		case None:
			t--
		}
	}
	return t
}

func sumLookupC(m map[string]int, keys []string, n int) int {
	t := 0
	for i := 0; i < n; i++ {
		if o := lookupC(m, keys[i&1023]); o.ok {
			t += o.v
		} else {
			t--
		}
	}
	return t
}

func sumLookupGo(m map[string]int, keys []string, n int) int {
	t := 0
	for i := 0; i < n; i++ {
		if v, ok := lookupGo(m, keys[i&1023]); ok {
			t += v
		} else {
			t--
		}
	}
	return t
}
