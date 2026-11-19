package lower

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

// providerChurnSource churns a Map whose provider has no normal form (the
// prelude's slot table, types.md Maps 5) through Put, Get and Delete.
const providerChurnSource = `type Spread {} implements Hashing[int]

func Hash(_ Spread, k int) uint64 { uint64(k) * 2654435761 }

func Equal(_ Spread, a int, b int) bool { a == b }

func churn(n int) int {
	let mut m = Map[int, int, Spread]{}
	let mut hits = 0
	for i in 0..n {
		m[i % 64] = i
		match m.Get(i % 32) {
			case Some: hits = hits + 1
			case None:
		}
		if i % 3 == 0 {
			m = m.Delete(i % 64)
		}
	}
	hits + len(m)
}

func main() needs Console {
	Console.Println("\(churn(100))")
}
`

// TestProviderTableCopiesAreDirect checks the promised form of a provider
// Map's lowering in a copy for one provider (MONO1; MAP-PROVIDER-PROBE,
// CONSTRAINTS C5d), where every operation of the provider is a known pure
// function (rows.md, Footprints 3: such a call is pure):
//
//   - the copy takes no witness parameter (its operations are direct
//     calls, so a witness would be dead);
//   - its frame does not poll: the probe loop, which the table's size
//     bounds, has no cancellation check, nor has the copy's entry;
//   - every function of the program has an IR body (no text fallback).
//
// Each is a count, logged; the expected count is 0.
func TestProviderTableCopiesAreDirect(t *testing.T) {
	files, rep, err := LowerIR(factsUnit(t, providerChurnSource))
	if err != nil {
		t.Fatal(err)
	}
	var main string
	var ir irFile
	for _, f := range files {
		switch f.Path {
		case "main.go":
			main = string(f.Data)
		case IRFileName:
			if err := json.Unmarshal(f.Data, &ir); err != nil {
				t.Fatal(err)
			}
		}
	}
	witnesses := len(regexp.MustCompile(`(?m)^func \w+Mono_i\d+\([^)]*\bw\w* struct \{`).FindAllString(main, -1))
	polls := 0
	copies := 0
	for _, f := range ir.Funcs {
		if !strings.Contains(f.Name, "Mono_i") {
			continue
		}
		copies++
		data, err := json.Marshal(f.Body)
		if err != nil {
			t.Fatal(err)
		}
		polls += strings.Count(string(data), `"name":"Check"`)
	}
	fallbacks := 0
	for _, n := range rep.Fallback {
		fallbacks += n
	}
	t.Logf("copies %d: witness parameters %d, polls %d; fallbacks %v", copies, witnesses, polls, rep.Fallback)
	if copies == 0 {
		t.Fatalf("no copy for the provider:\n%s", main)
	}
	if witnesses != 0 || polls != 0 || fallbacks != 0 {
		t.Errorf("a copy for the provider Spread takes %d witness parameters and polls %d times; fallbacks %v", witnesses, polls, rep.Fallback)
	}
	// The emitter's text of the same copies (the IR's fallback) still
	// compiles: callers pass no witness to a copy that takes none.
	typecheckGo(t, []*lowered{mustLower(t, "providerchurn", providerChurnSource)})
}
