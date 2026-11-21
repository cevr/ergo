package stdlib_test

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"regexp"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestStdOpCallCopyAgreesOnIR is the copy property (TestStdCopyAgrees) for
// the calls of an operation type's operations on the IR path, which the
// copy property's callers (functions taken as values) never make: sync and
// queue have no such function. A seeded
// caller program calls the operations of a std operation type that binds a
// runtime/v1 Go type (sync.Ref, queue.Queue), in a drawn order and subset;
// lowered with LowerIR against the std package and against its copy in a
// module that trusts its Go, the caller's IR bodies are the same, after
// folding positions and the copy's import path, and both are IR bodies.
// So an operation call reaches the runtime method by the type's
// declaration, as the lowering's ir_pkg.go boundOpCall reads it, never by
// std membership or a std name (STD HAS NO PRIVILEGES).
//
// ERGO_STD_COPY_SEED=<n> runs only seed n. A failing seed prints the
// program, both first differing lines and the replay command.
func TestStdOpCallCopyAgreesOnIR(t *testing.T) {
	seeds := []uint64{0, 1, 2, 3, 4, 5}
	if s := envSeed(); s != nil {
		seeds = []uint64{*s}
	}
	for _, c := range opCallCases {
		pair := checkCopy(t, c.path)
		if len(pair.diags) > 0 {
			t.Fatalf("the copy of %s does not check: %v", c.path, pair.diags)
		}
		for _, seed := range seeds {
			src := opCallProgram(c, seed)
			a, aIR := opCallIR(t, pair, src, false)
			b, bIR := opCallIR(t, pair, src, true)
			replay := fmt.Sprintf("replay: ERGO_STD_COPY_SEED=%d go test -run '^TestStdOpCallCopyAgreesOnIR$' ./internal/sketch/stdlib", seed)
			if !aIR || !bIR {
				t.Errorf("%s seed %d: the caller's main is no IR body (std %v, copy %v): %s\nprogram:\n%s\n%s", c.path, seed, aIR, bIR, a, src, replay)
				continue
			}
			if a != b {
				x, y := firstDiff(a, b)
				t.Errorf("%s seed %d: the caller's IR differs.\nprogram:\n%s\nstd:  %s\ncopy: %s\n%s", c.path, seed, src, x, y, replay)
			}
		}
	}
}

// envSeed is ERGO_STD_COPY_SEED, when set.
func envSeed() *uint64 {
	var s uint64
	if v := strings.TrimSpace(os.Getenv("ERGO_STD_COPY_SEED")); v != "" {
		if _, err := fmt.Sscan(v, &s); err == nil {
			return &s
		}
	}
	return nil
}

// opCallCase is a std package with an operation type, the program's
// statements that make a value of it (v), and its operation calls.
type opCallCase struct {
	path  string
	setup string
	calls []string
}

var opCallCases = []opCallCase{
	{
		path:  "sync",
		setup: "\tlet v = sync.NewRef(1)\n\tlet mut t = 0\n",
		calls: []string{
			"\tv.Set(t + 2)\n",
			"\tt = t + v.Get()\n",
			"\tv.Update(func(x int) int { x + 3 })\n",
			"\tlet g%d = v.Get()\n\tt = t + g%[1]d\n",
		},
	},
	{
		path:  "queue",
		setup: "\tlet v = queue.New[int](4)\n\tlet mut t = 0\n",
		calls: []string{
			"\tt = t + v.Len()\n",
			"\tv.End()\n",
		},
	},
}

// opCallProgram is a main of c's package that makes v and calls its
// operations: all in order for seed 0, else a drawn order and subset,
// each call at most twice.
func opCallProgram(c opCallCase, seed uint64) string {
	calls := append(append([]string(nil), c.calls...), c.calls...)
	if seed != 0 {
		r := rand.New(rand.NewPCG(seed, 0x0bca11))
		r.Shuffle(len(calls), func(i, j int) { calls[i], calls[j] = calls[j], calls[i] })
		calls = calls[:1+r.IntN(len(calls))]
	}
	var b strings.Builder
	b.WriteString("import " + c.path + "\n\nfunc main() {\n" + c.setup)
	for i, call := range calls {
		if strings.Contains(call, "%") {
			call = fmt.Sprintf(call, i)
		}
		b.WriteString(call)
	}
	b.WriteString("\tt = t + 1\n\t_ = t\n}\n")
	return b.String()
}

// irPositions are the position fields of an IR node.
var irPositions = regexp.MustCompile(`"(pos|end|epos|eend|elsepos|elseend)":\[[0-9,]*\],?`)

// opCallIR lowers src against the std package or its copy with LowerIR,
// and returns the IR of the caller's main (its locals and body, positions
// and the copy's import path folded) and whether main is an IR body.
func opCallIR(t *testing.T, pair copyPair, src string, copied bool) (string, bool) {
	t.Helper()
	u, ds := checkMain(src, pair, copied)
	if len(ds) > 0 {
		t.Fatalf("std package %s (copy %v): the caller program does not check: %v\n%s", pair.path, copied, ds, src)
	}
	if copied {
		u.Imports = []lower.Unit{pair.unit}
	}
	files, rep, err := lower.LowerIR(u)
	if err != nil {
		return "lowering error: " + err.Error(), false
	}
	var data []byte
	for _, f := range files {
		if strings.HasSuffix(f.Path, lower.IRFileName) {
			data = f.Data
		}
	}
	var ir struct {
		Funcs []json.RawMessage `json:"funcs"`
	}
	if err := json.Unmarshal(data, &ir); err != nil {
		return "no IR file: " + fmt.Sprint(err), false
	}
	var out strings.Builder
	for _, raw := range ir.Funcs {
		var fn struct {
			Name   string          `json:"name"`
			Locals json.RawMessage `json:"locals"`
			Body   json.RawMessage `json:"body"`
		}
		if json.Unmarshal(raw, &fn) != nil || !strings.HasPrefix(fn.Name, "main") {
			continue
		}
		text := string(fn.Locals) + "\n" + string(fn.Body)
		text = irPositions.ReplaceAllString(text, "")
		if copied {
			text = strings.ReplaceAll(text, "copy/"+pair.path, pair.path)
		}
		out.WriteString("== " + fn.Name + "\n" + strings.ReplaceAll(text, "},{", "},\n{") + "\n")
	}
	isIR := false
	for _, name := range rep.IR {
		isIR = isIR || strings.HasPrefix(name, "main")
	}
	if !isIR {
		out.WriteString(fmt.Sprintf("fallback %v\n", rep.Fallback))
	}
	return out.String(), isIR
}
