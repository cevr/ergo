package v1

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// TestCompactCallsAgree holds the indexed table to the wide reference
// over seeded call chains with two files and ambiguous same-line calls.
// It also guards the promised per-site byte bound (bs6-site-wide), which
// report tests cannot observe. Replay one subtest with -run
// 'TestCompactCallsAgree/seed_N$'. No binary is needed: the report reads
// the same table and Go-stack bytes through ergoStack.
func TestCompactCallsAgree(t *testing.T) {
	if n := reflect.TypeFor[SourceCall]().Size(); n > 24 {
		t.Fatalf("call site uses %d bytes, budget 24", n)
	}
	t.Cleanup(func() { RegisterCalls(nil, nil); registered = false })
	for seed := uint64(1); seed <= 32; seed++ {
		t.Run(fmt.Sprintf("seed_%d", seed), func(t *testing.T) {
			r := rand.New(rand.NewPCG(seed, seed+1))
			var ds []struct {
				Name      string
				File      string
				Line, End int
			}
			var wide []sourceCall
			var stack strings.Builder
			var want []string
			stack.WriteString("goroutine 1 [running]:\npanic(value)\n\t/go/runtime/panic.go:1\n")
			for i, n := 0, 2+r.IntN(6); i < n; i++ {
				name, file, line := fmt.Sprintf("f%d", i), fmt.Sprintf("part%d.ergo", i%2), 10*i+2
				ds = append(ds, struct {
					Name      string
					File      string
					Line, End int
				}{name, file, line - 1, line + 1})
				fmt.Fprintf(&stack, "main.%s(...)\n\t%s:%d +0x18\n", name, file, line)
				if i == 0 {
					want = append(want, fmt.Sprintf("%s at %s:%d", name, file, line))
					continue
				}
				column := 1 + r.IntN(200)
				callee := fmt.Sprintf("f%d", i-1)
				wide = append(wide, sourceCall{name, callee, file, line, column})
				if r.IntN(2) == 0 {
					wide = append(wide, sourceCall{name, "notTheCallee", file, line, column + 1})
				}
				want = append(want, fmt.Sprintf("%s calls %s at %s:%d:%d", name, callee, file, line, column))
			}
			r.Shuffle(len(wide), func(i, j int) { wide[i], wide[j] = wide[j], wide[i] })
			var names []string
			indices := map[string]uint32{}
			intern := func(s string) uint32 {
				if i, ok := indices[s]; ok {
					return i
				}
				i := uint32(len(names))
				names = append(names, s)
				indices[s] = i
				return i
			}
			var compact []SourceCall
			for _, c := range wide {
				compact = append(compact, SourceCall{Func: intern(c.Func), Callee: intern(c.Callee), File: intern(c.File), Line: uint32(c.Line), Column: uint32(c.Column)})
			}
			RegisterCalls(ds, wide)
			reference := ergoStack([]byte(stack.String()))
			RegisterCompactCalls(ds, names, compact)
			got := ergoStack([]byte(stack.String()))
			if !slices.Equal(got, reference) || !slices.Equal(got, want) {
				t.Fatalf("seed %d; replay -run 'TestCompactCallsAgree/seed_%d$'\nstack:\n%s\nnames %q\ncompact %+v\nwide %+v\ncompact report %q\nreference report %q\nwant %q", seed, seed, stack.String(), names, compact, wide, got, reference, want)
			}
		})
	}
}
