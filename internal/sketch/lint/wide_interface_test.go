package lint_test

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lint"
	"ergo.local/prototype/internal/sketchfront"
)

// wideConf is every rule off but wide-observed-interface, with max as
// its maxSurfaceList (0: the option absent).
func wideConf(t *testing.T, max int) *lint.Configuration {
	t.Helper()
	settings := map[string]lint.Setting{}
	for _, r := range lint.Builtin().Rules() {
		settings[r.Name] = lint.Setting{Severity: lint.Off, HasSeverity: true}
	}
	s := lint.Setting{}
	if max > 0 {
		s.Options, _ = json.Marshal(map[string]int{"maxSurfaceList": max})
	}
	settings["wide-observed-interface"] = s
	conf, err := lint.Builtin().Configure(lint.Config{Version: lint.ConfigVersion, Rules: settings}, []string{lint.Layout})
	if err != nil {
		t.Fatal(err)
	}
	return conf
}

// wideModel is the drawn module as the reference reads it: lib's funcs,
// the types each names, the private helpers each calls, the funcs the
// client calls outside tests (with how many sites), those only its tests
// call, and the re-exporting lets the client calls.
type wideModel struct {
	funcs   int
	types   [][]int
	calls   [][]int
	helpers [][]int
	nhelp   int
	used    map[int]int
	tested  []int
	lets    []int
}

// draw is a client-and-library module drawn from r.
func drawWide(r *rand.Rand) wideModel {
	m := wideModel{funcs: 1 + r.IntN(12), nhelp: r.IntN(24), used: map[int]int{}}
	ntypes := r.IntN(6)
	for j := range m.funcs {
		var ts []int
		for k := range ntypes {
			if r.IntN(4) == 0 {
				ts = append(ts, k)
			}
		}
		if ntypes > 0 && len(ts) == 0 && r.IntN(2) == 0 {
			ts = append(ts, r.IntN(ntypes))
		}
		m.types = append(m.types, ts)
		var hs []int
		if m.nhelp > 0 {
			for range r.IntN(5) {
				hs = append(hs, r.IntN(m.nhelp))
			}
		}
		m.calls = append(m.calls, hs)
		_ = j
	}
	for h := range m.nhelp {
		var hs []int
		if r.IntN(2) == 0 {
			hs = append(hs, r.IntN(m.nhelp))
		}
		_ = h
		m.helpers = append(m.helpers, hs)
	}
	for j := range m.funcs {
		switch r.IntN(4) {
		case 0, 1:
			m.used[j] = 1 + r.IntN(3)
		case 2:
			m.tested = append(m.tested, j)
		}
	}
	if len(m.used) == 0 {
		m.used[0] = 1
	}
	for j := range m.funcs {
		if r.IntN(5) == 0 {
			m.lets = append(m.lets, j)
		}
	}
	return m
}

// files is the module's source.
func (m wideModel) files() map[string]string {
	var types, lib, client, re strings.Builder
	ntypes := 0
	for _, ts := range m.types {
		for _, k := range ts {
			ntypes = max(ntypes, k+1)
		}
	}
	for k := range ntypes {
		fmt.Fprintf(&types, "type T%d { V int }\n\n", k)
	}
	for j := range m.funcs {
		var params, body []string
		for _, k := range m.types[j] {
			params = append(params, fmt.Sprintf("t%d T%d", k, k))
			body = append(body, fmt.Sprintf("t%d.V", k))
		}
		for _, h := range m.calls[j] {
			body = append(body, fmt.Sprintf("h%d()", h))
		}
		fmt.Fprintf(&lib, "func F%d(%s) int { %s }\n\n", j, strings.Join(params, ", "), strings.Join(append(body, fmt.Sprint(j)), " + "))
	}
	for h := range m.nhelp {
		body := []string{"1"}
		for _, g := range m.helpers[h] {
			if g > h {
				body = append(body, fmt.Sprintf("h%d()", g))
			}
		}
		fmt.Fprintf(&lib, "func h%d() int { %s }\n\n", h, strings.Join(body, " + "))
	}
	call := func(j int) string {
		var args []string
		for _, k := range m.types[j] {
			args = append(args, fmt.Sprintf("lib.T%d{V: %d}", k, k))
		}
		return fmt.Sprintf("lib.F%d(%s)", j, strings.Join(args, ", "))
	}
	client.WriteString("import app/lib\n")
	if len(m.lets) > 0 {
		client.WriteString("import app/re\n")
		re.WriteString("import app/lib\n\n")
		for _, j := range m.lets {
			fmt.Fprintf(&re, "let G%d = lib.F%d\n\n", j, j)
		}
	}
	client.WriteString("\n")
	var sum []string
	for _, j := range slices.Sorted(mapKeys(m.used)) {
		for s := range m.used[j] {
			fmt.Fprintf(&client, "func C%d_%d() int { %s }\n\n", j, s, call(j))
			sum = append(sum, fmt.Sprintf("C%d_%d()", j, s))
		}
	}
	for _, j := range m.lets {
		var args []string
		for _, k := range m.types[j] {
			args = append(args, fmt.Sprintf("lib.T%d{V: %d}", k, k))
		}
		fmt.Fprintf(&client, "func R%d() int { re.G%d(%s) }\n\n", j, j, strings.Join(args, ", "))
		sum = append(sum, fmt.Sprintf("R%d()", j))
	}
	fmt.Fprintf(&client, "func Sum() int { %s }\n\n", strings.Join(sum, " + "))
	for _, j := range m.tested {
		fmt.Fprintf(&client, "test Uses%d {\n\tassert %s > -1\n}\n\n", j, call(j))
	}
	out := map[string]string{
		"ergo.mod":           "module app\n",
		"lib/lib.ergo":       lib.String(),
		"client/client.ergo": client.String(),
		"cmd/app/main.ergo":  "import app/client\n\nfunc main() needs Console {\n\tConsole.Println(\"\\(client.Sum())\")\n}\n",
	}
	if ntypes > 0 {
		out["lib/types.ergo"] = types.String()
	}
	if len(m.lets) > 0 {
		out["re/re.ergo"] = re.String()
	}
	return out
}

func mapKeys(m map[int]int) func(func(int) bool) {
	return func(yield func(int) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// want is the findings the reference gives with max, each message with
// the longest list's name left out.
func (m wideModel) want(limit int) (out []string, wide, narrow, padded bool) {
	all := map[int]int{}
	for j := range m.used {
		all[j]++
	}
	for _, j := range m.lets {
		all[j]++
	}
	entries := slices.Sorted(mapKeys(all))
	groups := map[string]int{}
	for _, j := range entries {
		if len(m.types[j]) == 0 {
			groups[fmt.Sprint("F", j)]++
		}
		for _, k := range m.types[j] {
			groups[fmt.Sprint("T", k)]++
		}
	}
	largest := 0
	for _, n := range groups {
		largest = max(largest, n)
	}
	widest := max(largest, len(groups))
	reached := map[int]bool{}
	var todo []int
	for _, j := range entries {
		todo = append(todo, m.calls[j]...)
	}
	for len(todo) > 0 {
		h := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		if reached[h] {
			continue
		}
		reached[h] = true
		for _, g := range m.helpers[h] {
			if g > h {
				todo = append(todo, g)
			}
		}
	}
	if len(entries) > limit && widest <= limit {
		narrow = true
	}
	if len(entries) > limit && widest > limit {
		wide = true
		msg := fmt.Sprintf("clients in this check use %d callable entries of app/lib; has %d entries (configured list max: %d)", len(entries), largest, limit)
		if len(groups) > largest {
			msg = fmt.Sprintf("clients in this check use %d callable entries of app/lib, in %d lists (configured list max: %d)", len(entries), len(groups), limit)
		}
		out = append(out, msg+fmt.Sprintf("; %d private functions reached, 0 entries with no func behind them", len(reached)))
		padded = len(reached) >= len(entries)
	}
	if n := len(m.lets); n > limit {
		out = append(out, fmt.Sprintf("clients in this check use %d callable entries of app/re, in %d lists (configured list max: %d); 0 private functions reached, %d entries with no func behind them", n, n, limit, n))
	}
	slices.Sort(out)
	return out, wide, narrow, padded
}

var listName = regexp.MustCompile(`; \S+ has (\d+) entries`)

// TestWideObservedInterfaceAgreesWithReference draws a client-and-library
// module: lib's exported funcs, each naming drawn types of lib (a
// parameter and a field read) and calling drawn private helpers, which
// call others; a client package calling drawn funcs from one to three
// declarations each (duplicate sites), others only from its tests, and
// some through lets of a third package that re-export them. A reference
// model that reads only the draw gives the findings at a drawn list max:
// a package is reported when both its distinct production entries and
// its longest list (the number of lists, or the funcs that name one type)
// exceed the max; the private helpers the entries reach are counted and
// never clear a finding; a re-exported entry is its own list with no
// func behind it, and the let's own reading of lib's func is a use of
// lib (as the census reads it). With the option absent nothing is reported. Mutant:
// layout-width-padding-clears-finding (a backing ratio in the gate).
func TestWideObservedInterfaceAgreesWithReference(t *testing.T) {
	wides, narrows, padded := 0, 0, 0
	for _, seed := range propertySeeds(16) {
		r := rand.New(rand.NewPCG(seed, 0x51de))
		m := drawWide(r)
		limit := 1 + r.IntN(5)
		dir := t.TempDir()
		for name, text := range m.files() {
			path := filepath.Join(dir, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		a, err := sketchfront.CheckDir(filepath.Join(dir, "cmd", "app"))
		if err != nil {
			t.Fatal(err)
		}
		in := lint.InputOf(a)
		if !in.Checked {
			t.Fatalf("seed %d: the module does not check: %v\n%v", seed, a.Diagnostics, m.files())
		}
		replay := fmt.Sprintf("replay: go test -run TestWideObservedInterfaceAgreesWithReference ./internal/sketch/lint -lint.seed=%d", seed)
		if res := wideConf(t, 0).Run(in); len(res.Findings) != 0 || !res.PolicyPassed(true) {
			t.Fatalf("seed %d (%s): with no list max: %v", seed, replay, res.Findings)
		}
		res := wideConf(t, limit).Run(in)
		var got []string
		for _, f := range res.Findings {
			got = append(got, listName.ReplaceAllString(f.Message, "; has $1 entries"))
		}
		slices.Sort(got)
		want, wide, narrow, pad := m.want(limit)
		if !slices.Equal(got, want) {
			t.Fatalf("seed %d (%s): max %d:\ngot  %q\nwant %q\n%v", seed, replay, limit, got, want, m.files())
		}
		if !res.PolicyPassed(true) || lint.GateFails(res.Findings, 0) {
			t.Fatalf("seed %d (%s): an advisory finding fails a gate", seed, replay)
		}
		if wide {
			wides++
		}
		if pad {
			padded++
		}
		if narrow {
			narrows++
		}
	}
	t.Logf("%d wide, %d narrow, %d wide with private backing", wides, narrows, padded)
	if len(propertySeeds(16)) > 1 && (wides == 0 || narrows == 0 || padded == 0) {
		t.Fatalf("vacuous: %d wide, %d narrow, %d wide with private backing", wides, narrows, padded)
	}
}
