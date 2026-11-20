package irspike

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// ownedCallSrc hands its Map over on each rebinding m = put(m, k), a call
// the value analysis marks owned (values.go V5).
const ownedCallSrc = `func put(m Map[int, int], k int) Map[int, int] {
	let mut c = m
	c = c.Put(k, k)
	c
}

func grow(n int) int {
	let mut m = Map[int, int]{}
	for i in 0..n {
		m = put(m, i)
	}
	len(m)
}

func main() needs Console {
	Console.Println("\(grow(4))")
}
`

// TestIROwnedCallsCallOwnedBody checks the fact an owned call promises
// (V5, FORK unit 3's lowering, IR-OWN): grow's rebinding m = put(m, k)
// hands m over, so its IR calls put's owned body, which takes m as it is,
// never the wrapper that copies it on every call. Both print the same, so
// TestIROwnedParamsMatchSource cannot tell them apart; only the IR does.
// It reads LowerIR's IR in process. Mutant ir-own-call-wrapper: an owned
// call calls the wrapper.
func TestIROwnedCallsCallOwnedBody(t *testing.T) {
	got := irCalls(t, ownedCallSrc, "grow", "call")
	if !got[lower.HelperSpelling("", "putOwned")] {
		t.Fatalf("grow's IR does not call put's owned body (its calls: %v)\n%s", got, ownedCallSrc)
	}
}

// irCalls is the set of names the IR body of fn calls by a node of one of
// kinds ("call", "pcall", "mcall"), from LowerIR's IR of text, in process.
func irCalls(t *testing.T, text, fn string, kinds ...string) map[string]bool {
	t.Helper()
	files, _, err := lower.LowerIR(loadText(t, t.TempDir(), text))
	if err != nil {
		t.Fatal(err)
	}
	var ir struct {
		Funcs []struct {
			Name string `json:"name"`
			Body []any  `json:"body"`
		} `json:"funcs"`
	}
	for _, f := range files {
		if f.Path == lower.IRFileName {
			if err := json.Unmarshal(f.Data, &ir); err != nil {
				t.Fatal(err)
			}
		}
	}
	want := map[string]bool{}
	for _, k := range kinds {
		want[k] = true
	}
	got := map[string]bool{}
	var walk func(v any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			if k, ok := v["k"].(string); ok && want[k] {
				got[fmt.Sprint(v["name"])] = true
			}
			for _, w := range v {
				walk(w)
			}
		case []any:
			for _, w := range v {
				walk(w)
			}
		}
	}
	for _, f := range ir.Funcs {
		if f.Name == fn {
			walk(f.Body)
		}
	}
	return got
}

// loadText checks the Ergo program text, written to dir.
func loadText(t *testing.T, dir, text string) lower.Unit {
	t.Helper()
	file := filepath.Join(dir, "p.ergo")
	if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	u, err := Load(file)
	if err != nil {
		t.Fatalf("the program does not check: %v\n%s", err, text)
	}
	return u
}
