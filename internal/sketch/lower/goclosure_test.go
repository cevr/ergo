package lower

import (
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestClosureTakesEveryMethodOfACalledName is the law of the closure key's
// by-name rule (LFL; the orchestrator's condition on the RTA departure,
// 2026-11-15): a call the types do not resolve to one method takes every
// method of that name over every package whose declarations the key
// covers, so a method of that name added anywhere in that set, on a new
// type or on a type the walk did not reach, changes the key.
//
// Each seed builds a two-package world, entry package a and its import b:
// F in a makes one dynamic call of M (through an interface declared in a
// or b, or on a value of Go's standard library, which the walk does not
// type) and enters b by a function, a var or a type. The edit adds a
// method M, value or pointer receiver, to a new type or to the unreached
// type Other, in a or in b. Neither world widens, so only the by-name rule
// can change the key.
//
// Mutant: lfl-byname-only-reached (the name's method set is computed only
// over the methods of types already reached).
//
// ERGO_CLOSURE_SEED=N replays one seed.
func TestClosureTakesEveryMethodOfACalledName(t *testing.T) {
	seeds := make([]uint64, 0, 64)
	if s := os.Getenv("ERGO_CLOSURE_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = append(seeds, n)
	} else {
		for i := range uint64(64) {
			seeds = append(seeds, i+1)
		}
	}
	for _, seed := range seeds {
		w := genByNameWorld(seed)
		before := w.closure(false)
		after := w.closure(true)
		fail := func(why string) {
			t.Errorf("seed %d: %s\nreplay: ERGO_CLOSURE_SEED=%d go test -run '^TestClosureTakesEveryMethodOfACalledName$' ./internal/sketch/lower\n--- a\n%s--- b\n%s--- added to %s\n%s", seed, why, seed, w.a, w.b, w.addTo, w.added)
		}
		switch {
		case before.Unloaded || len(before.Widened) > 0 || len(after.Widened) > 0:
			fail(fmt.Sprintf("the world widens or does not load (%v, %v, %v)", before.Unloaded, before.Widened, after.Widened))
		case !covers(before, "ex.test/a/b"):
			fail(fmt.Sprintf("the key does not cover b: %v", before.Decls))
		case before.Hash == after.Hash:
			fail("a method M added in a package the key covers leaves the key unchanged")
		}
	}
}

func covers(c GoClosure, pkg string) bool {
	for _, d := range c.Decls {
		if strings.HasPrefix(d, pkg+" ") {
			return true
		}
	}
	return false
}

type byNameWorld struct {
	a, b, addTo, added string
}

func (w byNameWorld) closure(edited bool) GoClosure {
	files := map[string][]GoSourceFile{
		"ex.test/a":   {{Name: "a.go", Data: []byte(w.a)}},
		"ex.test/a/b": {{Name: "b.go", Data: []byte(w.b)}},
	}
	if edited {
		files[w.addTo] = append(files[w.addTo], GoSourceFile{Name: "added.go", Data: []byte(w.added)})
	}
	world := newGoWorld(func(p string) ([]GoSourceFile, bool) {
		f, ok := files[p]
		return f, ok
	})
	return world.closure("ex.test/a", "F")
}

func genByNameWorld(seed uint64) byNameWorld {
	r := rand.New(rand.NewPCG(seed, 0x62796e))
	ifaceInB := r.IntN(2) == 0
	call := []string{"iface", "std"}[r.IntN(2)]
	enterB := []string{"func", "var", "type"}[r.IntN(3)]

	var a, b strings.Builder
	b.WriteString("package b\n\nvar V = 3\n\nfunc G() int { return V }\n\ntype T struct{ n int }\n\ntype Other struct{}\n\nfunc (Other) N() int { return 1 }\n")
	if ifaceInB {
		b.WriteString("\ntype I interface{ M() int }\n")
	}
	a.WriteString("package a\n\nimport (\n\t\"strings\"\n\n\t\"ex.test/a/b\"\n)\n\nvar _ = strings.ToUpper\n\ntype Other struct{}\n\nfunc (Other) N() int { return 2 }\n")
	iface := "I"
	if ifaceInB {
		iface = "b.I"
	} else {
		a.WriteString("\ntype I interface{ M() int }\n")
	}
	var use string
	switch call {
	case "iface":
		use = "x.M()"
	case "std":
		use = "func() int { var s strings.Builder; return s.M() }()"
	}
	var reachB string
	switch enterB {
	case "func":
		reachB = "b.G()"
	case "var":
		reachB = "b.V"
	case "type":
		reachB = "func() int { var t b.T; _ = t; return 0 }()"
	}
	// The walk meets the dynamic call before b: a method added in a is in
	// a package entered before the name was taken, one added in b in a
	// package entered after it.
	fmt.Fprintf(&a, "\nfunc F(x %s) int { return %s + %s }\n", iface, use, reachB)

	w := byNameWorld{a: a.String(), b: b.String()}
	pkg, name := "a", "a"
	if r.IntN(2) == 0 {
		pkg, name = "b", "b"
	}
	w.addTo = "ex.test/a"
	if pkg == "b" {
		w.addTo = "ex.test/a/b"
	}
	recv := "New"
	if r.IntN(2) == 0 {
		recv = "Other"
	}
	star := ""
	if r.IntN(2) == 0 {
		star = "*"
	}
	var add strings.Builder
	fmt.Fprintf(&add, "package %s\n\n", name)
	if recv == "New" {
		add.WriteString("type New struct{}\n\n")
	}
	fmt.Fprintf(&add, "func (%s%s) M() int { return %d }\n", star, recv, r.IntN(100))
	w.added = add.String()
	return w
}
