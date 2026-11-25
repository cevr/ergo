package apigen_test

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/apigen"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketchfront"
)

// seedVariable replays one seed of TestApiHubAgrees.
const seedVariable = "ERGO_API_SEED"

// TestApiHubAgrees is AF0's acceptance property: for generated contracts
// (records, a union, Option, slices, Maps, unit values and failure rows of
// zero to three records, the shapes of TestRpcLoopbackAgrees), the hub
// ergo gen api writes checks, and its laws CallIsTheOperation and
// ClientIsCall run at check time, in the reference interpreter, with more
// than zero cases each, and hold. Every contract has the operation
// Pair(key string, label string), two parameters of one type, so a Call
// that swaps them is refuted on every seed. A failing seed prints the
// Ergo source, the law's report and the replay command.
//
// It runs in process: the hub's laws need no Go build, since they run
// over a Client whose send answers a given Outcome.
func TestApiHubAgrees(t *testing.T) {
	seeds := []uint64{1, 2, 3}
	if s := os.Getenv(seedVariable); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("%s=%q: %v", seedVariable, s, err)
		}
		seeds = []uint64{n}
	}
	for _, seed := range seeds {
		t.Run(fmt.Sprint(seed), func(t *testing.T) { hubAgrees(t, seed) })
	}
}

func hubAgrees(t *testing.T, seed uint64) {
	r := rand.New(rand.NewPCG(seed, 0xa91))
	src := contractSource(r, seed)
	files := map[string]string{"ergo.mod": "module loop\n", "c0/contract.ergo": src}
	dir := t.TempDir()
	for name, text := range files {
		writeFile(t, filepath.Join(dir, name), text)
	}
	replay := fmt.Sprintf("replay: %s=%d go test -count=1 -run '^TestApiHubAgrees$' ./internal/apigen", seedVariable, seed)
	a, err := sketchfront.CheckDir(filepath.Join(dir, "c0"))
	if err != nil || a.Root().Package == nil || len(a.Diagnostics) > 0 {
		t.Fatalf("seed %d: the contract does not check: %v %v\n%s", seed, err, a.Diagnostics, sources(files))
	}
	out, err := apigen.Generate(a.Root().Package, "loop/c0", "Svc")
	if err != nil {
		t.Fatalf("seed %d: ergo gen api: %v\n%s\n%s", seed, err, sources(files), replay)
	}
	for name, text := range out.Files {
		writeFile(t, filepath.Join(dir, "c0", out.Dir, name), text)
		files["c0/"+out.Dir+"/"+name] = text
	}
	hub, err := sketchfront.CheckDir(filepath.Join(dir, "c0", out.Dir))
	if err != nil {
		t.Fatal(err)
	}
	if len(hub.Diagnostics) > 0 {
		var ds []string
		for _, d := range hub.Diagnostics {
			ds = append(ds, fmt.Sprintf("%s:%d:%d: %s %s", d.File, d.Span.Line, d.Span.Column, d.Code, d.Message))
		}
		t.Fatalf("seed %d: the hub does not check:\n%s\n%s\n%s", seed, strings.Join(ds, "\n"), sources(files), replay)
	}
	b := eval.DefaultBudget()
	b.Seed = seed
	rep, err := hub.Evaluate(b)
	if err != nil {
		t.Fatalf("seed %d: evaluate: %v\n%s", seed, err, replay)
	}
	for _, law := range []string{"law CallIsTheOperation", "law ClientIsCall"} {
		found := false
		for _, res := range rep.Results {
			if res.Name != law {
				continue
			}
			found = true
			if res.Status != eval.Passed || res.Cases == 0 {
				var args []string
				for _, a := range res.Arguments {
					args = append(args, a.Name+" = "+a.Value)
				}
				t.Errorf("seed %d: %s: %s after %d cases: %s\n%s\n%s\n%s", seed, law, res.Status, res.Cases, res.Reason, strings.Join(args, "\n"), sources(files), replay)
			}
		}
		if !found {
			t.Errorf("seed %d: the hub has no %s\n%s", seed, law, replay)
		}
	}
}

// TestApiRefusesTwoFailuresOfOneName is AF0's diagnostic row: two failure
// types of one name (run.Closed and board.Closed) would be one Failure
// variant and one JSON tag, so ergo gen api refuses them and names
// both.
func TestApiRefusesTwoFailuresOfOneName(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "ergo.mod"), "module loop\n")
	writeFile(t, filepath.Join(dir, "c0", "run", "run.ergo"), "type Closed { Run string }\n")
	writeFile(t, filepath.Join(dir, "c0", "board", "board.ergo"), "type Closed { Board string }\n")
	writeFile(t, filepath.Join(dir, "c0", "contract.ergo"), "import loop/c0/board\nimport loop/c0/run\n\ntype Svc {\n\tStart(id string) fails run.Closed,\n\tPin(id string) fails board.Closed,\n}\n")
	a, err := sketchfront.CheckDir(filepath.Join(dir, "c0"))
	if err != nil || a.Root().Package == nil || len(a.Diagnostics) > 0 {
		t.Fatalf("the contract does not check: %v %v", err, a.Diagnostics)
	}
	_, err = apigen.Generate(a.Root().Package, "loop/c0", "Svc")
	if err == nil || !strings.Contains(err.Error(), "run.Closed and board.Closed have one name, Closed") {
		t.Errorf("ergo gen api of two failures named Closed: %v", err)
	}
}

// TestApiHubCheckedClientRefusesMismatch verifies that a generated hub's
// CheckedClient fails with rpc.Transport when a peer returns a sibling
// operation's Reply or a Failure outside the operation's declared row, and
// preserves the operation's own value and declared failures.
func TestApiHubCheckedClientRefusesMismatch(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "ergo.mod"), "module mismatch\n")
	writeFile(t, filepath.Join(dir, "svc", "svc.ergo"), strings.Join([]string{
		"type Neg { N int }",
		"type Missing { Key string }",
		"type Svc {",
		"\tAdd(n int) int fails Neg,",
		"\tGet(key string) int fails Missing,",
		"\tPing() string,",
		"}",
		"",
	}, "\n"))
	a, err := sketchfront.CheckDir(filepath.Join(dir, "svc"))
	if err != nil || a.Root().Package == nil || len(a.Diagnostics) > 0 {
		t.Fatalf("contract does not check: %v %v", err, a.Diagnostics)
	}
	out, err := apigen.Generate(a.Root().Package, "mismatch/svc", "Svc")
	if err != nil {
		t.Fatalf("ergo gen api: %v", err)
	}
	for name, text := range out.Files {
		writeFile(t, filepath.Join(dir, "svc", out.Dir, name), text)
	}
	writeFile(t, filepath.Join(dir, "svc", out.Dir, "mismatch_test.ergo"), strings.Join([]string{
		"import rpc",
		"import mismatch/svc",
		"",
		"law SiblingReplyIsTransport(n int) {",
		"\tlet p = NewCheckedClient(func(_ Request) Outcome fails rpc.Transport { Done{Reply: GetReply{Value: n}} })",
		"\tlet _ = AddChecked(p, n) catch {",
		"\tcase svc.Neg:",
		"\t\treturn false",
		"\tcase rpc.Transport:",
		"\t\treturn true",
		"\t}",
		"\tfalse",
		"}",
		"",
		"law OutsideRowFailureIsTransport(key string, n int) {",
		"\tlet p = NewCheckedClient(func(_ Request) Outcome fails rpc.Transport { Rejected{Failure: Missing{E: svc.Missing{Key: key}}} })",
		"\tlet _ = AddChecked(p, n) catch {",
		"\tcase svc.Neg:",
		"\t\treturn false",
		"\tcase rpc.Transport:",
		"\t\treturn true",
		"\t}",
		"\tfalse",
		"}",
		"",
		"law InfallibleRejectedIsTransport(key string) {",
		"\tlet p = NewCheckedClient(func(_ Request) Outcome fails rpc.Transport { Rejected{Failure: Missing{E: svc.Missing{Key: key}}} })",
		"\tlet _ = PingChecked(p) catch {",
		"\tcase rpc.Transport:",
		"\t\treturn true",
		"\t}",
		"\tfalse",
		"}",
		"",
	}, "\n"))
	hub, err := sketchfront.CheckDir(filepath.Join(dir, "svc", out.Dir))
	if err != nil {
		t.Fatal(err)
	}
	if len(hub.Diagnostics) > 0 {
		t.Fatalf("hub with mismatch tests does not check: %v", hub.Diagnostics)
	}
	b := eval.DefaultBudget()
	b.Seed = 1
	rep, err := hub.Evaluate(b)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	for _, law := range []string{"law SiblingReplyIsTransport", "law OutsideRowFailureIsTransport", "law InfallibleRejectedIsTransport"} {
		found := false
		for _, res := range rep.Results {
			if res.Name == law {
				found = true
				if res.Status != eval.Passed || res.Cases == 0 {
					t.Errorf("%s: %s after %d cases: %s", law, res.Status, res.Cases, res.Reason)
				}
			}
		}
		if !found {
			t.Errorf("missing %s", law)
		}
	}
}

func writeFile(t testing.TB, path, src string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

func sources(files map[string]string) string {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		fmt.Fprintf(&b, "--- %s\n%s", n, files[n])
	}
	return b.String()
}

// The generated contract.

var fieldNames = []string{"Name", "Count", "IsOn", "RetryAfterMs", "Tags", "Weights", "Extra", "Note"}

// paramNames avoid the names the generators use for their locals.
var paramNames = []string{"key", "count", "label", "opts", "items", "flag"}

// contractSource is a contract package: records, a union Shape, three
// failure records and the operation type Svc.
func contractSource(r *rand.Rand, seed uint64) string {
	var records []string
	basic := func() string { return []string{"int", "string", "bool", "float64"}[r.IntN(4)] }
	var anyType func(depth int) string
	anyType = func(depth int) string {
		switch k := r.IntN(8); {
		case k < 2 || depth > 1:
			return basic()
		case k == 2 && len(records) > 0:
			return records[r.IntN(len(records))]
		case k == 3:
			return "Shape"
		case k == 4:
			return "[]" + anyType(depth+1)
		case k == 5:
			return "Map[string, " + anyType(depth+1) + "]"
		case k == 6:
			return "Option[" + anyType(depth+1) + "]"
		}
		return basic()
	}
	fields := func(n int, of func() string) string {
		names := append([]string{}, fieldNames...)
		r.Shuffle(len(names), func(i, j int) { names[i], names[j] = names[j], names[i] })
		var fs []string
		for i := 0; i < n; i++ {
			fs = append(fs, names[i]+" "+of())
		}
		return strings.Join(fs, ", ")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "// Package c0 is a generated contract (seed %d).\n\n", seed)
	var decls []string
	for k := r.IntN(3); k > 0; k-- {
		name := fmt.Sprintf("Rec%d", len(records))
		decls = append(decls, fmt.Sprintf("type %s { %s }", name, fields(r.IntN(3), func() string { return anyType(1) })))
		records = append(records, name)
	}
	var variants []string
	for k, name := range []string{"Dot", "Line", "Box"}[:1+r.IntN(3)] {
		variants = append(variants, name+"{"+fields(k, func() string { return anyType(1) })+"}")
	}
	decls = append(decls, "type Shape = "+strings.Join(variants, " | "))
	for k := 0; k < 3; k++ {
		decls = append(decls, fmt.Sprintf("type Bad%d { %s }", k, fields(r.IntN(3), basic)))
	}
	b.WriteString(strings.Join(decls, "\n") + "\n\ntype Svc {\n")
	b.WriteString("\tPair(key string, label string) string fails Bad0,\n")
	for k := 1 + r.IntN(3); k > 0; k-- {
		names := append([]string{}, paramNames...)
		r.Shuffle(len(names), func(i, j int) { names[i], names[j] = names[j], names[i] })
		var params []string
		for j := r.IntN(3); j > 0; j-- {
			params = append(params, names[j]+" "+anyType(0))
		}
		sig := fmt.Sprintf("Op%d(%s)", k, strings.Join(params, ", "))
		if r.IntN(4) > 0 {
			sig += " " + anyType(0)
		}
		var fails []string
		for _, f := range r.Perm(3)[:r.IntN(4)] {
			fails = append(fails, fmt.Sprintf("Bad%d", f))
		}
		if len(fails) > 0 {
			sig += " fails " + strings.Join(fails, " | ")
		}
		fmt.Fprintf(&b, "\t%s,\n", sig)
	}
	b.WriteString("}\n")
	return b.String()
}
