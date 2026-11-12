package regexpplan

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"ergo.local/prototype/internal/proclimit"
	"ergo.local/prototype/internal/testinputs"
	"ergo.local/prototype/internal/testwait"
)

func TestMain(m *testing.M) { os.Exit(testinputs.Main(m)) }

// TestTaggedConstructionAgrees kills priority inversion and history truncation.
// Seam: immutable AST -> Plan -> match, checked serialization. The independent
// AST enumerator knows no NFA instructions or deterministic correspondence.
func TestTaggedConstructionAgrees(t *testing.T) {
	seed := int64(73491)
	if s := os.Getenv("ERGO_REGEX_SEED"); s != "" {
		var err error
		seed, err = strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
	}
	r := rand.New(rand.NewSource(seed))
	for trial := 0; trial < 80; trial++ {
		id := 0
		n := generated(r, 3, &id)
		for _, mode := range []Mode{Parse, Find} {
			p, err := Compile(n, mode, DefaultLimits())
			if err != nil {
				t.Fatalf("seed=%d trial=%d AST=%s: %v", seed, trial, pattern(n), err)
			}
			texts := []string{"", witness(n, r), witness(n, r) + "x", "x" + witness(n, r)}
			for i := 0; i < 8; i++ {
				var text []byte
				for size := r.Intn(7); size > 0; size-- {
					text = append(text, byte(r.Intn(256)))
				}
				texts = append(texts, string(text))
			}
			for _, text := range texts {
				want := astMatch(n, p, text, mode)
				ref, got := p.Reference(text), p.Match(text)
				if !sameMatch(want, ref) || !sameMatch(want, got) {
					t.Fatalf("seed=%d trial=%d pattern=%s text=%q mode=%d AST=%+v NFA=%+v selected=%+v\nreplay: ERGO_REGEX_SEED=%d go test -run TestTaggedConstructionAgrees ./internal/sketch/regexpplan", seed, trial, pattern(n), text, mode, want, ref, got, seed)
				}
				goProjection(t, n, p, text, want)
			}
			for _, engine := range []Engine{NFA, Direct, Table} {
				if engine != NFA && p.ir == nil {
					continue
				}
				src, err := p.emit(engine)
				if err != nil {
					t.Fatal(err)
				}
				if err := p.ValidateGo(src); err != nil {
					t.Fatalf("seed=%d %s: %v", seed, engine, err)
				}
				typecheck(t, src)
				if engine != NFA {
					// Execute actual emitted transitions, not the candidate IR.
					for _, text := range texts {
						got := emittedMatch(t, p, src, text)
						want := astMatch(n, p, text, mode)
						if !sameMatch(got, want) {
							t.Fatalf("seed=%d pattern=%s text=%q engine=%s emitted=%+v AST=%+v", seed, pattern(n), text, engine, got, want)
						}
					}
				}
			}
			// Reuse after failure and after success has no retained capture state.
			for _, text := range []string{witness(n, r), "\xff", "", witness(n, r)} {
				if !sameMatch(p.Match(text), astMatch(n, p, text, mode)) {
					t.Fatalf("seed=%d reuse pattern=%s text=%q", seed, pattern(n), text)
				}
			}
		}
	}
}

func sameMatch(a, b Match) bool {
	return a.Start == b.Start && a.End == b.End && a.OK == b.OK && slices.Equal(a.Events, b.Events)
}

func generated(r *rand.Rand, depth int, id *int) *Node {
	if depth == 0 {
		return Byte(byte('a' + r.Intn(3)))
	}
	switch r.Intn(7) {
	case 0:
		return &Node{Kind: Empty}
	case 1:
		n := Byte('a')
		n.Set[1] |= 1 << ('b' % 64)
		return n
	case 2, 3:
		kind := Concat
		if r.Intn(2) == 0 {
			kind = Alternative
		}
		return &Node{Kind: kind, Children: []*Node{generated(r, depth-1, id), generated(r, depth-1, id)}}
	case 4:
		*id++
		name := fmt.Sprintf("G%d", *id)
		return &Node{Kind: Capture, Name: name, Children: []*Node{generated(r, depth-1, id)}}
	default:
		body := Byte(byte('a' + r.Intn(3)))
		if depth > 1 && r.Intn(2) == 0 {
			// A mandatory byte makes the body productive, while the tail
			// reaches nested repeats, absent captures and repeated records.
			body = &Node{Kind: Concat, Children: []*Node{body, generated(r, depth-2, id)}}
		}
		if r.Intn(2) == 0 {
			*id++
			body = &Node{Kind: Capture, Name: fmt.Sprintf("G%d", *id), Children: []*Node{body}}
		}
		min := r.Intn(2)
		max := min + r.Intn(3)
		if r.Intn(3) == 0 {
			max = -1
		}
		return &Node{Kind: Repeat, Min: min, Max: max, Children: []*Node{body}}
	}
}

func witness(n *Node, r *rand.Rand) string {
	switch n.Kind {
	case Empty:
		return ""
	case Bytes:
		var values []byte
		for b := 0; b < 256; b++ {
			if n.Set[b/64]&(1<<uint(b%64)) != 0 {
				values = append(values, byte(b))
			}
		}
		return string(values[r.Intn(len(values))])
	case Capture:
		return witness(n.Children[0], r)
	case Concat:
		var s string
		for _, child := range n.Children {
			s += witness(child, r)
		}
		return s
	case Alternative:
		return witness(n.Children[r.Intn(len(n.Children))], r)
	case Repeat:
		max := n.Max
		if max < 0 {
			max = n.Min + 3
		}
		count := n.Min + r.Intn(max-n.Min+1)
		var s string
		for i := 0; i < count; i++ {
			s += witness(n.Children[0], r)
		}
		return s
	}
	panic("bad generator")
}

type outcome struct {
	pos    int
	events []Event
}

func astMatch(n *Node, p *Plan, text string, mode Mode) Match {
	ids := map[string]int{}
	for i, name := range p.names {
		ids[name] = i + 1
	}
	var eval func(*Node, outcome) []outcome
	eval = func(n *Node, in outcome) []outcome {
		switch n.Kind {
		case Empty:
			return []outcome{in}
		case Bytes:
			if in.pos < len(text) && n.Set[text[in.pos]/64]&(1<<(text[in.pos]%64)) != 0 {
				return []outcome{{in.pos + 1, in.events}}
			}
		case Capture:
			open := Event{ids[n.Name], in.pos}
			in.events = append(slices.Clone(in.events), open)
			out := eval(n.Children[0], in)
			for i := range out {
				out[i].events = append(slices.Clone(out[i].events), Event{-ids[n.Name], out[i].pos})
			}
			return out
		case Concat:
			out := []outcome{in}
			for _, child := range n.Children {
				var next []outcome
				for _, v := range out {
					next = append(next, eval(child, v)...)
				}
				out = next
			}
			return out
		case Alternative:
			var out []outcome
			for _, child := range n.Children {
				out = append(out, eval(child, in)...)
			}
			return out
		case Repeat:
			var repeat func(outcome, int) []outcome
			repeat = func(in outcome, count int) []outcome {
				var out []outcome
				if n.Max < 0 || count < n.Max {
					for _, v := range eval(n.Children[0], in) {
						out = append(out, repeat(v, count+1)...)
					}
				}
				if count >= n.Min {
					out = append(out, in)
				}
				return out
			}
			return repeat(in, 0)
		}
		return nil
	}
	last := 0
	if mode == Find {
		last = len(text)
	}
	for start := 0; start <= last; start++ {
		for _, v := range eval(n, outcome{pos: start}) {
			if mode == Find || v.pos == len(text) {
				return Match{start, v.pos, v.events, true}
			}
		}
	}
	return Match{}
}

func pattern(n *Node) string {
	switch n.Kind {
	case Empty:
		return "(?:)"
	case Bytes:
		s := "["
		for b := 0; b < 256; b++ {
			if n.Set[b/64]&(1<<uint(b%64)) != 0 {
				s += fmt.Sprintf("\\x%02x", b)
			}
		}
		return s + "]"
	case Capture:
		return "(?P<" + n.Name + ">" + pattern(n.Children[0]) + ")"
	case Concat, Alternative:
		var parts []string
		for _, c := range n.Children {
			parts = append(parts, pattern(c))
		}
		sep := ""
		if n.Kind == Alternative {
			sep = "|"
		}
		return "(?:" + strings.Join(parts, sep) + ")"
	case Repeat:
		max := strconv.Itoa(n.Max)
		if n.Max < 0 {
			max = ""
		}
		return "(?:" + pattern(n.Children[0]) + "){" + strconv.Itoa(n.Min) + "," + max + "}"
	}
	panic("bad AST")
}

func goProjection(t *testing.T, n *Node, p *Plan, text string, want Match) {
	t.Helper()
	// Only ASCII is the common byte/rune domain; arbitrary bytes are still
	// compared against the independent AST reference above.
	for i := range text {
		if text[i] >= 128 {
			return
		}
	}
	s := pattern(n)
	if p.mode == Parse {
		s = "\\A(?:" + s + ")\\z"
	}
	re := regexp.MustCompile(s)
	idx := re.FindStringSubmatchIndex(text)
	if (idx != nil) != want.OK {
		t.Fatalf("Go projection pattern=%s text=%q Go=%v AST=%+v", s, text, idx, want)
	}
	if idx == nil {
		return
	}
	if idx[0] != want.Start || idx[1] != want.End {
		t.Fatalf("Go span pattern=%s text=%q Go=%v AST=%+v", s, text, idx, want)
	}
	open, last := map[int]int{}, map[int][2]int{}
	for _, e := range want.Events {
		if e.Tag > 0 {
			open[e.Tag] = e.Pos
		} else {
			last[-e.Tag] = [2]int{open[-e.Tag], e.Pos}
		}
	}
	for i, name := range p.names {
		g := re.SubexpIndex(name)
		span, present := last[i+1]
		if !present {
			span = [2]int{-1, -1}
		}
		if span != [2]int{idx[2*g], idx[2*g+1]} {
			t.Fatalf("Go last capture pattern=%s text=%q name=%s Go=%v AST=%+v", s, text, name, idx, want)
		}
	}
}

func typecheck(t *testing.T, src string) {
	t.Helper()
	fs := token.NewFileSet()
	f, err := parser.ParseFile(fs, "pattern.go", src, 0)
	if err == nil {
		_, err = (&types.Config{}).Check("pattern", fs, []*ast.File{f}, nil)
	}
	if err != nil {
		t.Fatalf("emitted Go: %v\n%s", err, src)
	}
}

func emittedMatch(t *testing.T, p *Plan, src, text string) Match {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "pattern.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	fn := f.Decls[0].(*ast.FuncDecl)
	offsets, tableErr := stringConstant(fn, "offsets")
	data, _ := stringConstant(fn, "data")
	var direct map[int][257]edge
	if tableErr != nil {
		direct, err = readDirect(fn)
		if err != nil {
			t.Fatal(err)
		}
	}
	last := 0
	if p.mode == Find {
		last = len(text)
	}
	for start := 0; start <= last; start++ {
		q := 0
		var events []Event
		for pos := start; ; pos++ {
			b := 256
			if pos < len(text) {
				b = int(text[pos])
			}
			var e edge
			if tableErr == nil {
				e, err = readTable(offsets, data, q, b)
			} else {
				e = direct[q][b]
			}
			if err != nil {
				t.Fatal(err)
			}
			if e.accept && (p.mode == Find || pos == len(text)) {
				for _, tag := range e.tags {
					events = append(events, Event{tag, pos})
				}
				return Match{start, pos, events, true}
			}
			if e.to < 0 || pos == len(text) {
				break
			}
			for _, tag := range e.tags {
				events = append(events, Event{tag, pos})
			}
			q = e.to
		}
	}
	return Match{}
}

// TestConstructionRejectsCorruption kills byte/EOF loss, tag reorder and
// serializer drift even when an imagined sampled oracle always says "passed".
func TestConstructionRejectsCorruption(t *testing.T) {
	for b := 0; b < 256; b++ {
		n := &Node{Kind: Capture, Name: "Outer", Children: []*Node{{Kind: Capture, Name: "Inner", Children: []*Node{Byte(byte(b))}}}}
		p, err := Compile(n, Parse, DefaultLimits())
		if err != nil || p.ir == nil {
			t.Fatalf("byte %d: %v", b, err)
		}
		for q, s := range p.ir.states {
			for v, e := range s.edges {
				if e.to < 0 && !e.accept {
					continue
				}
				bad := cloneCandidate(p.ir)
				bad.states[q].edges[v] = edge{to: -1}
				if validate(p, bad) == nil {
					t.Fatalf("lost byte/EOF edge admitted %d/%d", q, v)
				}
				if len(e.tags) > 1 {
					bad = cloneCandidate(p.ir)
					slices.Reverse(bad.states[q].edges[v].tags)
					if validate(p, bad) == nil {
						t.Fatalf("reordered tags admitted %d/%d", q, v)
					}
				}
				// Check EOF separately from the ordinary byte class.
				if v != 256 && s.edges[256].accept {
					bad = cloneCandidate(p.ir)
					bad.states[q].edges[256] = edge{to: -1}
					if validate(p, bad) == nil {
						t.Fatal("lost EOF edge admitted")
					}
				}
				break
			}
		}
		if b == 0 {
			for _, engine := range []Engine{NFA, Direct, Table} {
				src, err := p.emit(engine)
				if err != nil {
					t.Fatal(err)
				}
				bad := strings.Replace(src, "pos < len(s)", "pos <= len(s)", 1)
				if bad == src || p.ValidateGo(bad) == nil {
					t.Fatalf("%s unchecked bounds drift", engine)
				}
			}
			src, _ := p.emit(Direct)
			bad := strings.Replace(src, actionLiteral([]int{2, 1}), actionLiteral([]int{1, 2}), 1)
			if bad == src || p.ValidateGo(bad) == nil {
				t.Fatal("actual emitted action reorder admitted")
			}
		}
	}
}

// TestEmittedDriversAgree kills emitted-driver history truncation, which a
// typecheck and transition-only evaluator cannot observe. It builds Go because
// it executes the actual Go drivers, including their local closure/history
// storage, against independent AST results. All cases share one binary.
func TestEmittedDriversAgree(t *testing.T) {
	needsBuild(t)
	r := rand.New(rand.NewSource(73491))
	var source strings.Builder
	source.WriteString("package main\nimport \"fmt\"\n")
	var checks strings.Builder
	add := func(n *Node, trial int) {
		for _, mode := range []Mode{Parse, Find} {
			p, err := Compile(n, mode, DefaultLimits())
			if err != nil {
				t.Fatal(err)
			}
			texts := []string{"", witness(n, r), witness(n, r) + "x", "x" + witness(n, r), "abacaba"}
			for _, engine := range []Engine{NFA, Direct, Table} {
				if engine != NFA && p.ir == nil {
					continue
				}
				src, err := p.emit(engine)
				if err != nil {
					t.Fatal(err)
				}
				suffix := fmt.Sprintf("_%d_%d_%s", trial, mode, engine)
				src = strings.TrimPrefix(src, "package pattern\n")
				src = strings.NewReplacer("func Match(", "func Match"+suffix+"(", "func step(", "func step"+suffix+"(", ":= step(", ":= step"+suffix+"(").Replace(src)
				source.WriteString(src)
				for _, text := range texts {
					want := astMatch(n, p, text, mode)
					events := make([][2]int, len(want.Events))
					for j, e := range want.Events {
						events[j] = [2]int{e.Tag, e.Pos}
					}
					expected := fmt.Sprint(want.Start, want.End, events, want.OK)
					fmt.Fprintf(&checks, "{ a,b,e,ok:=Match%s(%q);if got:=fmt.Sprint(a,b,e,ok);got!=%q {panic(fmt.Sprintf(\"trial %d pattern %%s text %%q: %%s\",%q,%q,got))} }\n", suffix, text, expected, trial, pattern(n), text)
				}
			}
		}
	}
	// A productive repeated nested capture necessarily exercises NFA history
	// storage, independent of whether a random pattern happens to need it.
	add(&Node{Kind: Repeat, Min: 2, Max: 4, Children: []*Node{{Kind: Capture, Name: "Outer", Children: []*Node{{Kind: Capture, Name: "Inner", Children: []*Node{Byte('a')}}}}}}, 0)
	for trial := 1; trial <= 12; trial++ {
		id := 0
		add(generated(r, 3, &id), trial)
	}
	source.WriteString("func main(){\n")
	source.WriteString(checks.String())
	source.WriteString("}\n")
	oneBinary(t, source.String())
}

func needsBuild(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("full tier: builds Go")
	}
}

// oneBinary builds Go because TestEmittedDriversAgree must execute actual
// serialized drivers. One dispatcher contains all generated matcher cases.
func oneBinary(t *testing.T, source string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "matchers")
	build := exec.Command("go", "build", "-buildvcs=false", "-p", "2", "-o", bin, path)
	build.Dir = dir
	bounded(t, build)
	bounded(t, exec.Command(bin))
}

func bounded(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	ctx, cancel := testwait.Context(t, t.Context())
	defer cancel()
	// Keep the group we start, and only that group, until all descendants
	// are stopped. The context is a hang guard, not a performance assertion.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := proclimit.Start(cmd, proclimit.DefaultLimit); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var err error
	select {
	case err = <-done:
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	case <-ctx.Done():
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		<-done
		t.Fatalf("hung generated matcher check %s: %s", cmd.Path, output.String())
	}
	if err != nil {
		t.Fatalf("%s: %v\n%s", cmd.Path, err, output.String())
	}
}

func cloneCandidate(c *candidate) *candidate {
	out := &candidate{states: slices.Clone(c.states)}
	for q := range out.states {
		for b := range out.states[q].edges {
			out.states[q].edges[b].tags = slices.Clone(out.states[q].edges[b].tags)
		}
	}
	return out
}

// TestBudgetsAndIdentity kills budget-as-proof, nullable-loop admission,
// ambiguous-history admission, source-cap bypass and capture-policy cache drift.
func TestBudgetsAndIdentity(t *testing.T) {
	n := &Node{Kind: Capture, Name: "X", Children: []*Node{Byte('a')}}
	for _, mode := range []Mode{Parse, Find} {
		for _, choose := range []string{"direct", "table", "size", "states", "work"} {
			limits := DefaultLimits()
			want := Direct
			switch choose {
			case "table":
				limits.DirectBytes, want = 0, Table
			case "size":
				limits.DirectBytes, limits.TableBytes, want = 0, 0, NFA
			case "states":
				limits.States, want = 0, NFA
			case "work":
				limits.Work, want = 0, NFA
			}
			p, err := Compile(n, mode, limits)
			if err != nil || p.info.Engine != want {
				t.Fatalf("%s: plan=%v err=%v", choose, p, err)
			}
			if !sameMatch(p.Match("a"), astMatch(n, p, "a", mode)) {
				t.Fatal("budget fallback changed captures")
			}
			var result Match
			allocs, controlAllocs := 0.0, 0.0
			measurePlan := func() { allocs += testing.AllocsPerRun(10, func() { result = p.Match("a") }) }
			measureControl := func() {
				controlAllocs += testing.AllocsPerRun(10, func() { result = fixedControl("a", mode) })
			}
			for round := 0; round < 5; round++ {
				if round%2 == 0 {
					measurePlan()
					measureControl()
				} else {
					measureControl()
					measurePlan()
				}
			}
			allocs, controlAllocs = allocs/5, controlAllocs/5
			if !sameMatch(result, p.Match("a")) {
				t.Fatal("receipt control does not perform equivalent work")
			}
			t.Logf("counted mode=%d selection=%s nodes=%d instructions=%d states=%d work=%d Go-bytes=%d table-bytes=%d in-process-allocs/parse=%.0f hand-control-allocs/parse=%.0f", mode, p.info.Engine, p.info.Nodes, p.info.Instructions, p.info.States, p.info.Work, p.info.GoBytes, p.info.TableBytes, allocs, controlAllocs)
		}
	}
	for size := 1; size <= 10; size++ {
		limits := DefaultLimits()
		limits.Nodes = size
		large := &Node{Kind: Repeat, Min: 0, Max: size, Offset: 17, Children: []*Node{Byte('a')}}
		if _, err := Compile(large, Parse, limits); err == nil {
			t.Fatal("expansion materialized past budget")
		}
		limits = DefaultLimits()
		limits.Instructions = size
		fixed := &Node{Kind: Repeat, Min: size, Max: size, Children: []*Node{Byte('a')}}
		if _, err := Compile(fixed, Parse, limits); err == nil {
			t.Fatal("instruction budget ignored")
		}
	}
	for _, root := range []*Node{
		{Kind: Repeat, Max: -1, Children: []*Node{{Kind: Empty}}},
		{Kind: Bytes},
		{Kind: Capture, Name: "X"},
	} {
		if _, err := Compile(root, Parse, DefaultLimits()); err == nil {
			t.Fatal("invalid AST admitted")
		}
	}
	cycle := &Node{Kind: Concat}
	cycle.Children = []*Node{cycle}
	if _, err := Compile(cycle, Parse, DefaultLimits()); err == nil {
		t.Fatal("cycle admitted")
	}
	ambiguous := &Node{Kind: Alternative, Children: []*Node{n, {Kind: Capture, Name: "Y", Children: []*Node{Byte('a')}}}}
	p, err := Compile(ambiguous, Parse, DefaultLimits())
	if err != nil || p.ir != nil || p.info.Engine != NFA {
		t.Fatal("ambiguous histories licensed fast plan")
	}
	a, _ := Compile(n, Parse, DefaultLimits())
	b, _ := Compile(n, Find, DefaultLimits())
	c, _ := Compile(&Node{Kind: Capture, Name: "Y", Children: []*Node{Byte('a')}}, Parse, DefaultLimits())
	if a.Key() == b.Key() || a.Key() == c.Key() {
		t.Fatal("semantic cache identities collided")
	}
	before := a.Match("a")
	n.Children[0].Set = [4]uint64{}
	names := a.Names()
	names[0] = "changed"
	if !sameMatch(a.Match("a"), before) || reflect.DeepEqual(names, a.Names()) {
		t.Fatal("construction input or names alias compiled plan")
	}
}

// fixedControl is the optimized Go scanner for the counted X="a" workload,
// including the same returned capture events (not just a boolean result).
func fixedControl(text string, mode Mode) Match {
	start := 0
	if mode == Parse {
		if len(text) != 1 || text[0] != 'a' {
			return Match{}
		}
	} else {
		start = strings.IndexByte(text, 'a')
		if start < 0 {
			return Match{}
		}
	}
	return Match{start, start + 1, []Event{{1, start}, {-1, start + 1}}, true}
}
