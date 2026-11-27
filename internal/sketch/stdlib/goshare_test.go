package stdlib_test

import (
	"bufio"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/stdlib"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestStdGoShareOnlyFalls is NP4f's ledger of the Go that std's checked
// packages bind in the Ergo runtime (owner q202: std's computation is Ergo
// alone, and std binds Go's library through import go as a user does). It
// reads every extern of every std package whose Go target is a package of
// ergo.local/prototype/runtime and compares them with
// testdata/std-go-share.txt (stdGoShare):
//
//   - an extern with no row is refused: a new Go body in std is classed
//     before it lands, and a class may not grow (below);
//   - a row with no extern is stale: the body moved, so the row goes;
//   - a library, computation or language row names the unit that moves it;
//   - each class holds at most its ceiling (stdGoShareCeiling), and a
//     class below its ceiling fails until the ceiling is lowered to it:
//     the counts only fall.
//
// It is a one-way ledger, not a pinned count of a
// generated corpus: each row is a declaration a person reads.
func TestStdGoShareOnlyFalls(t *testing.T) {
	rows := readStdGoShare(t, "testdata/std-go-share.txt")
	for _, p := range stdGoShareProblems(stdRuntimeExterns(t), rows, stdGoShareCeiling) {
		t.Error(p)
	}
}

// stdGoShareCeiling is the most rows each class of testdata/std-go-share.txt
// may hold. It only falls: a move lowers its class's ceiling to the count
// it leaves.
var stdGoShareCeiling = map[string]int{
	"provider":    103, // NOU-PROVIDER-ROWS: +1 queue.NewOpen; PROC-RUN-FLOOR: +1 task.StopAfter; PROCESS-RUN: +3; CAP stack (917): +2; I-FLIP: +1/-1; I22: +25; NET1: +1; PTY-REATTACH: +4; baseline sync (actor 3, log 1, prelude 1, task 4, tasks +2, testing 1, trace 2): 89 + 14 = 103
	"host":        67,  // CAP stack (917): +5; I22: +14; NET1: +2; UC1: +1; Unix sockets: +4; PTY-REATTACH: +10; FS-ENTRY: +1; PROCESS-CHILD: +1; FS-SCAN: +1; FS-TEST-ROOT: +1 (fs.OpenTestRoot); SQL: +8 (sql.host*)
	"library":     93,  // CAP stack (917): +12; I22: +14; hmac/totp digest keyState/number: +4
	"language":    12,  // I22: +2; NO-PRELUDE core map/slice ops: +9
	"computation": 47,  // I22: +5; NP4f: -11; HTTP-RAWPATH: +1; json check helpers +3; net.loopbackAddress +1
	"type":        119, // NOU/PROC-RUN/CAP/I22/NET1/STD-COPY/UC1/Unix/PTY/FS-ENTRY: 107; PROCESS-CHILD: +4; FS-SCAN: +1 (fs.rawDirEntry); http.Denied/prelude.TestDirectory/task.Defect/sql.*: +7
}

// stdGoShareClasses are the classes a row may name; the true ones need a
// unit.
var stdGoShareClasses = map[string]bool{
	"provider": false, "host": false, "type": false,
	"library": true, "computation": true, "language": true,
}

// stdGoShareRow is one row of the ledger.
type stdGoShareRow struct {
	class, unit string
	line        int
}

// readStdGoShare reads the ledger at file: `package<TAB>name<TAB>class`,
// or with a fourth field, the unit, keyed by package.name.
func readStdGoShare(t *testing.T, file string) map[string]stdGoShareRow {
	t.Helper()
	f, err := os.Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out := map[string]stdGoShareRow{}
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		p := strings.Split(line, "\t")
		if len(p) < 3 || len(p) > 4 {
			t.Errorf("%s:%d: want `package<TAB>name<TAB>class[<TAB>unit]`, got %q", file, n, line)
			continue
		}
		key := p[0] + "." + p[1]
		if _, dup := out[key]; dup {
			t.Errorf("%s:%d: a second row for %s", file, n, key)
		}
		row := stdGoShareRow{class: p[2], line: n}
		if len(p) == 4 {
			row.unit = p[3]
		}
		out[key] = row
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// stdRuntimeExterns are the functions and types of the checked std
// packages whose extern binds a package of the Ergo runtime, as
// package.name.
func stdRuntimeExterns(t *testing.T) []string {
	t.Helper()
	var out []string
	// The prelude is no importable package: its declarations are read from
	// its parsed source. Its builtin.ergo is the language's own operations
	// on slices, Maps, strings and runes (builtin.ergo, PRIM-2b): their
	// bodies are the language's, as + is int's, not Go that std binds.
	for name, src := range stdlib.Prelude() {
		if name == "builtin.ergo" {
			continue
		}
		f, err := syntax.Parse(src)
		if err != nil {
			t.Fatalf("prelude %s: %v", name, err)
		}
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *syntax.FuncDecl:
				if d.Extern != nil {
					if target, _ := d.Extern.Split(); runtimeTarget(target) {
						out = append(out, "prelude."+d.Name.Name)
					}
				}
			case *syntax.TypeDecl:
				if d.Extern != nil {
					if target, _ := d.Extern.Split(); runtimeTarget(target) {
						out = append(out, "prelude."+d.Name.Name)
					}
				}
			}
		}
	}
	for _, path := range stdlib.Packages() {
		p, err := check.Std.Import(path)
		if err != nil {
			t.Fatalf("check.Std.Import(%q): %v", path, err)
		}
		for _, f := range p.Funcs {
			if f.Decl != nil && f.Decl.Extern != nil {
				if target, _ := f.Decl.Extern.Split(); runtimeTarget(target) {
					out = append(out, p.Name+"."+f.Name)
				}
			}
		}
		for _, tn := range p.Types {
			if tn.Decl != nil && tn.Decl.Extern != nil {
				if target, _ := tn.Decl.Extern.Split(); runtimeTarget(target) {
					out = append(out, p.Name+"."+tn.Name)
				}
			}
		}
	}
	slices.Sort(out)
	return out
}

// runtimeTarget reports a Go import path of the Ergo runtime.
func runtimeTarget(path string) bool {
	return path == "ergo.local/prototype/runtime" || strings.HasPrefix(path, "ergo.local/prototype/runtime/")
}

// stdGoShareProblems compares the externs found with the ledger's rows and
// the ceilings, and returns each disagreement.
func stdGoShareProblems(externs []string, rows map[string]stdGoShareRow, ceiling map[string]int) []string {
	var out []string
	count := map[string]int{}
	seen := map[string]bool{}
	for _, key := range externs {
		seen[key] = true
		row, ok := rows[key]
		if !ok {
			out = append(out, fmt.Sprintf("std extern %s binds the Ergo runtime and has no row in testdata/std-go-share.txt: class it (a library call binds Go through import go, a computation is Ergo source)", key))
			continue
		}
		count[row.class]++
	}
	for _, key := range slices.Sorted(mapsKeys(rows)) {
		row := rows[key]
		needsUnit, known := stdGoShareClasses[row.class]
		switch {
		case !known:
			out = append(out, fmt.Sprintf("testdata/std-go-share.txt:%d: %s has the class %q; want provider, host, library, language, computation or type", row.line, key, row.class))
		case needsUnit && row.unit == "":
			out = append(out, fmt.Sprintf("testdata/std-go-share.txt:%d: %s is %s and names no unit that moves it", row.line, key, row.class))
		}
		if !seen[key] {
			out = append(out, fmt.Sprintf("testdata/std-go-share.txt:%d: %s is no std extern of the Ergo runtime now; delete its row", row.line, key))
		}
	}
	for _, class := range slices.Sorted(mapsKeys(ceiling)) {
		n, most := count[class], ceiling[class]
		switch {
		case n > most:
			out = append(out, fmt.Sprintf("std binds %d %s externs in the Ergo runtime, over the ceiling %d: the Go share of std only falls", n, class, most))
		case n < most:
			out = append(out, fmt.Sprintf("std binds %d %s externs in the Ergo runtime now; lower stdGoShareCeiling[%q] from %d to %d", n, class, class, most, n))
		}
	}
	for class := range count {
		if _, ok := ceiling[class]; !ok {
			out = append(out, fmt.Sprintf("the class %s has no ceiling in stdGoShareCeiling", class))
		}
	}
	return out
}

// TestStdGoShareRule checks the ledger's rule on planted inputs, so that a
// rule that admits too much fails even while std agrees with the ledger: a
// new extern with no row, a class over its ceiling, a stale row, a row with
// no unit, and a class under its ceiling are each refused, and the agreeing
// input is not.
func TestStdGoShareRule(t *testing.T) {
	rows := map[string]stdGoShareRow{
		"bits.Len":   {class: "library", unit: "NP4f-1", line: 1},
		"actor.Self": {class: "provider", line: 2},
	}
	ceiling := map[string]int{"library": 1, "provider": 1}
	agree := []string{"actor.Self", "bits.Len"}
	if p := stdGoShareProblems(agree, rows, ceiling); len(p) != 0 {
		t.Fatalf("the agreeing ledger is refused: %v", p)
	}
	withRow := func(key string, row stdGoShareRow) map[string]stdGoShareRow {
		m := map[string]stdGoShareRow{key: row}
		for k, v := range rows {
			m[k] = v
		}
		return m
	}
	for _, c := range []struct {
		name    string
		externs []string
		rows    map[string]stdGoShareRow
		ceiling map[string]int
		want    string
	}{
		{"unclassed", append(slices.Clone(agree), "bits.Len64"), rows, ceiling, "has no row"},
		{"rises", append(slices.Clone(agree), "bits.Len64"), withRow("bits.Len64", stdGoShareRow{class: "library", unit: "NP4f-1", line: 3}), ceiling, "over the ceiling"},
		{"stale", []string{"actor.Self"}, rows, map[string]int{"library": 0, "provider": 1}, "delete its row"},
		{"no unit", append(slices.Clone(agree), "cmp.Less"), withRow("cmp.Less", stdGoShareRow{class: "computation", line: 3}), map[string]int{"library": 1, "provider": 1, "computation": 1}, "names no unit"},
		{"falls", []string{"actor.Self"}, map[string]stdGoShareRow{"actor.Self": rows["actor.Self"]}, ceiling, "lower stdGoShareCeiling"},
		{"unknown class", agree, withRow("x.Y", stdGoShareRow{class: "misc", line: 3}), ceiling, "has the class"},
	} {
		p := stdGoShareProblems(c.externs, c.rows, c.ceiling)
		if !slices.ContainsFunc(p, func(s string) bool { return strings.Contains(s, c.want) }) {
			t.Errorf("%s: want a problem with %q, got %v", c.name, c.want, p)
		}
	}
}
