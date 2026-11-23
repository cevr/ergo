package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/linked/code"
	"ergo.local/prototype/internal/sketch/api"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/facts"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/sketchfront"
)

const codeHelp = `usage: ergo code [--in DIR] [--json] COMMAND [ARGS...]

ergo code answers questions about the code of the program in DIR (the
current folder by default) from its fact tables: std code's Reader, as
` + "`ergo gen cli`" + ` makes its command line (docs/spec/code.md). The commands:

  describe                 the revision, the packages, and whether they check
  symbols [--name N] [--match exact|prefix|part] [--kind K] [--package P]
                           declarations by name, kind and package
  symbol ADDRESS           the declaration at a handle (pkg.Name,
                           pkg.Type.Member), a position (file:line:column)
                           or a short name (Name, Type.Member)
  references --of ADDRESS [--kinds K]...
                           the sites that name the declaration

  plan CHANGE              plan a change and keep the plan in .ergo/plans;
                           it writes no source file. CHANGE is text:
                           replace ADDRESS "TEXT", insert after ADDRESS
                           "TEXT", delete ADDRESS, fixes [RULE...],
                           rename ADDRESS to "NAME", script (CHANGE; ...);
                           or a code.Change as JSON
  apply ID                 write a kept, ready plan, all or nothing
  recover                  undo or finish an apply that stopped

Lists are paged with --offset and --limit (100 by default, at most 1000).
With --json the answer is its JSON; a failure (NoSymbol, Ambiguous) is
its tagged JSON. A package that does not check gives the rows the checker
resolved, and the answer says complete: false.

Exit status: 0 answered; 1 a typed failure, or the program cannot be read;
2 invalid invocation.
`

// codeCommand is `ergo code`: it checks the program, writes its
// fact tables as code.Tables, and runs std code's command line over them
// (internal/linked/code, the Ergo of ergo code, lowered to Go).
func codeCommand(args []string) error {
	dir := "."
	if len(args) > 0 && (args[0] == "--in" || strings.HasPrefix(args[0], "--in=")) {
		if v, ok := strings.CutPrefix(args[0], "--in="); ok {
			dir, args = v, args[1:]
		} else if len(args) < 2 {
			return invalidInvocation(fmt.Errorf("ergo code: --in takes a folder"))
		} else {
			dir, args = args[1], args[2:]
		}
	}
	// Before COMMAND the usage takes only --json (and --in, read above): an
	// unknown option is an invalid invocation, refused before the check.
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			break
		}
		if arg != "--json" {
			return invalidInvocation(fmt.Errorf("ergo code: unknown option %s", arg))
		}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return exitStatusError{code: 1, message: "ergo code: " + err.Error()}
	}
	asJSON := false
	for i, arg := range args {
		if arg == "--json" {
			asJSON = true
			continue
		}
		switch arg {
		case "plan", "apply", "recover":
			return codePlanCommand(abs, arg, args[i+1:], asJSON)
		}
		break
	}
	a, err := sketchfront.CheckDir(abs)
	if err != nil {
		return exitStatusError{code: 1, message: "ergo code: " + err.Error()}
	}
	tables, err := json.Marshal(codeTablesOf(a))
	if err != nil {
		return exitStatusError{code: 1, message: "ergo code: " + err.Error()}
	}
	out, err := code.Run(context.Background(), args, string(tables))
	if err != nil {
		return exitStatusError{code: 1, message: "ergo code: " + err.Error()}
	}
	if len(out) != 3 {
		return exitStatusError{code: 1, message: fmt.Sprintf("ergo code: the command line gave %d parts", len(out))}
	}
	os.Stdout.WriteString(out[1])
	os.Stderr.WriteString(out[2])
	status, err := strconv.Atoi(out[0])
	if err != nil {
		return exitStatusError{code: 1, message: "ergo code: status " + out[0]}
	}
	if status != 0 {
		return exitStatusError{code: status, handled: true}
	}
	return nil
}

// The JSON of std code's Tables and its records (docs/spec/code.md): the
// seam between the Go checker's tables and the Ergo reader, until the
// mixed binary of capstone S1 passes the tables without text.
type (
	codeTables struct {
		Revision string        `json:"revision"`
		Root     string        `json:"root"`
		Packages []codePackage `json:"packages"`
	}
	codePackage struct {
		Path        string         `json:"path"`
		Complete    bool           `json:"complete"`
		Diagnostics int            `json:"diagnostics"`
		Decls       []codeDecl     `json:"decls"`
		Sites       []codeSite     `json:"sites"`
		Contracts   []codeContract `json:"contracts"`
		Calls       []codeCall     `json:"calls"`
		Needs       []codeNeed     `json:"needs"`
		Fails       []codeFailing  `json:"fails"`
		Machines    []codeMachine  `json:"machines"`
	}
	// codeContract is api.Function with code.Contract's keys; an answer's
	// JSON decodes back into api.Function, whose field names match them
	// but for case.
	codeContract struct {
		Name            string   `json:"name"`
		Signature       string   `json:"signature"`
		TypeParameters  []string `json:"typeParameters"`
		Fails           string   `json:"fails"`
		Needs           string   `json:"needs"`
		Takes           []string `json:"takes"`
		TakesParameters []int    `json:"takesParameters"`
		TakesSelf       bool     `json:"takesSelf"`
		TakesInferred   bool     `json:"takesInferred"`
		Own             []string `json:"own"`
	}
	codeDecl struct {
		Handle   string `json:"handle"`
		Kind     string `json:"kind"`
		Name     string `json:"name"`
		Package  string `json:"package"`
		Exported bool   `json:"exported"`
		File     string `json:"file"`
		Line     int    `json:"line"`
		Column   int    `json:"column"`
	}
	codeSite struct {
		Symbol  string `json:"symbol"`
		Kind    string `json:"kind"`
		Package string `json:"package"`
		File    string `json:"file"`
		Line    int    `json:"line"`
		Column  int    `json:"column"`
		In      string `json:"in"`
	}
	// codeCall is a call edge of the tables (facts.Table.Calls) with its
	// site's position: code.Call.
	codeCall struct {
		From       string        `json:"from"`
		To         string        `json:"to"`
		File       string        `json:"file"`
		Line       int           `json:"line"`
		Column     int           `json:"column"`
		Brings     []string      `json:"brings"`
		Discharged []string      `json:"discharged"`
		Bound      []codeBinding `json:"bound"`
		Raises     []string      `json:"raises"`
		Caught     []string      `json:"caught"`
	}
	// codeBinding is a provided edge of the tables (facts.Table.Provided)
	// at a call: code.Binding.
	codeBinding struct {
		Capability string `json:"capability"`
		Provider   string `json:"provider"`
	}
	// codeFailing is a fails edge of the tables (facts.Table.Fails):
	// code.Failing.
	codeFailing struct {
		Of      string `json:"of"`
		Failure string `json:"failure"`
	}
	// codeMachine is a machine graph of the tables (facts.MachineGraph):
	// code.MachineGraph.
	codeMachine struct {
		Handle  string      `json:"handle"`
		Name    string      `json:"name"`
		States  []string    `json:"states"`
		Signals []string    `json:"signals"`
		Initial string      `json:"initial"`
		Arms    []codeArm   `json:"arms"`
		Timers  []codeTimer `json:"timers"`
		Work    []codePlace `json:"work"`
		Exit    []codePlace `json:"exit"`
		Unsure  []string    `json:"unsure"`
		// Computed are the computed entries (MACHINE-EXPR), which have no
		// graph.
		Computed []string `json:"computed"`
	}
	codeArm struct {
		At        codePlace        `json:"at"`
		States    []string         `json:"states"`
		AnyState  bool             `json:"anyState"`
		Signals   []string         `json:"signals"`
		AnySignal bool             `json:"anySignal"`
		Partial   bool             `json:"partial"`
		Edges     []codeTransition `json:"edges"`
		Alts      []codeAlt        `json:"alts"`
	}
	codeAlt struct {
		States    []string `json:"states"`
		AnyState  bool     `json:"anyState"`
		Signals   []string `json:"signals"`
		AnySignal bool     `json:"anySignal"`
		Partial   bool     `json:"partial"`
	}
	codeTransition struct {
		Decision string    `json:"decision"`
		To       string    `json:"to"`
		At       codePlace `json:"at"`
	}
	codeTimer struct {
		State  string    `json:"state"`
		Signal string    `json:"signal"`
		At     codePlace `json:"at"`
	}
	codePlace struct {
		What string `json:"what"`
		File string `json:"file"`
		Line int    `json:"line"`
	}
	// codeNeed is a needs edge of the tables (facts.Table.Needs): code.Need.
	codeNeed struct {
		Of         string `json:"of"`
		Capability string `json:"capability"`
	}
)

// codeTablesOf is the fact tables of the program a checked, one package
// per checked package in dependency order, the root last (one package for
// a source text). A package with diagnostics gives the rows the checker
// resolved and Complete false, and no contracts; a package the loader did
// not check (it imports a broken one) gives none. The revision is a digest
// of the files' names and texts.
func codeTablesOf(a sketchfront.Analysis) codeTables {
	diags := map[string]int{}
	for _, d := range a.Diagnostics {
		diags[d.File]++
	}
	h := sha256.New()
	out := codeTables{Packages: []codePackage{}}
	pkgs := a.Packages()
	if pkgs == nil {
		pkgs = []sketchfront.CheckedPackage{a.Root()}
	}
	for _, p := range pkgs {
		out.Root = p.Path
		cp := codePackage{Path: p.Path, Complete: p.Package != nil, Decls: []codeDecl{}, Sites: []codeSite{}, Contracts: codeContractsOf(p.Package), Calls: []codeCall{}, Needs: []codeNeed{}, Fails: []codeFailing{}, Machines: []codeMachine{}}
		syn := map[string]*syntax.File{}
		var files []facts.File
		for _, f := range p.Files {
			cp.Diagnostics += diags[f.Rel]
			fmt.Fprintf(h, "%s\x00%d\x00", f.Rel, len(srcOf(f.Syntax)))
			h.Write([]byte(srcOf(f.Syntax)))
			if f.Syntax != nil {
				syn[f.Rel] = f.Syntax
				files = append(files, facts.File{Name: f.Rel, Syntax: f.Syntax})
			}
		}
		// A source text's root has its checked package and no partial one.
		pkg := p.Partial
		if pkg == nil {
			pkg = p.Package
		}
		if pkg != nil && pkg.Info != nil {
			t, _ := facts.Build(pkg, files)
			addCodeRows(&cp, t, p.Path, syn)
		}
		out.Packages = append(out.Packages, cp)
	}
	out.Revision = hex.EncodeToString(h.Sum(nil))[:16]
	return out
}

// codeContractsOf is the contracts of a checked package's exported
// functions; none for nil.
func codeContractsOf(p *check.Package) []codeContract {
	out := []codeContract{}
	if p == nil {
		return out
	}
	for _, f := range api.Functions(p) {
		c := codeContract{Name: f.Name, Signature: f.Signature, TypeParameters: f.TypeParameters, Fails: f.Fails, Needs: f.Needs, Takes: f.Takes, TakesParameters: f.TakesParameters, TakesSelf: f.TakesSelf, TakesInferred: f.TakesInferred, Own: append([]string{}, f.Own...)}
		out = append(out, c)
	}
	return out
}

// addCodeRows adds the declarations, sites, call edges (with what each
// brings and what a with discharges) and needs rows of t to cp.
// The checker names the root package main; its handles take the
// package's path.
func addCodeRows(cp *codePackage, t *facts.Table, path string, syn map[string]*syntax.File) {
	pos := func(file uint16, off uint32) (string, int, int) {
		name := t.Files[file]
		f := syn[name]
		if f == nil {
			return name, 0, 0
		}
		line, col := f.Position(syntax.Pos(off))
		return name, line, col
	}
	handles := make([]string, len(t.Symbols))
	for id, s := range t.Symbols {
		h := s.Handle
		if t.Path == "main" && strings.HasPrefix(h, "main.") {
			h = path + h[len("main"):]
		}
		if h == "" && s.Declared {
			file, line, col := pos(s.File, s.Off)
			h = fmt.Sprintf("%s:%d:%d", file, line, col)
		}
		handles[id] = h
	}
	for id, s := range t.Symbols {
		if !s.Declared || handles[id] == "" {
			continue
		}
		file, line, col := pos(s.File, s.Off)
		cp.Decls = append(cp.Decls, codeDecl{Handle: handles[id], Kind: s.Kind.String(), Name: s.Name, Package: path, Exported: s.Exported, File: file, Line: line, Column: col})
	}
	for _, s := range t.Sites {
		if handles[s.Sym] == "" {
			continue
		}
		file, line, col := pos(s.File, s.Off)
		in := ""
		if s.Encl != facts.NoSym {
			in = handles[s.Encl]
		}
		cp.Sites = append(cp.Sites, codeSite{Symbol: handles[s.Sym], Kind: s.Kind.String(), Package: path, File: file, Line: line, Column: col, In: in})
	}
	charges := func(edges []facts.Edge) map[uint32][]string {
		out := map[uint32][]string{}
		for _, e := range edges {
			if h := handles[e.To]; h != "" {
				out[e.Site] = append(out[e.Site], h)
			}
		}
		return out
	}
	brings, discharged := charges(t.Brings), charges(t.Discharges)
	raises, caught := charges(t.Raises), charges(t.Caught)
	bound := map[uint32][]codeBinding{}
	for _, e := range t.Provided {
		if handles[e.From] != "" && handles[e.To] != "" {
			bound[e.Site] = append(bound[e.Site], codeBinding{Capability: handles[e.To], Provider: handles[e.From]})
		}
	}
	orEmpty := func(s []string) []string {
		if s == nil {
			return []string{}
		}
		return s
	}
	for _, e := range t.Calls {
		if handles[e.From] == "" || handles[e.To] == "" {
			continue
		}
		s := t.Sites[e.Site]
		file, line, col := pos(s.File, s.Off)
		c := codeCall{From: handles[e.From], To: handles[e.To], File: file, Line: line, Column: col, Brings: orEmpty(brings[e.Site]), Discharged: orEmpty(discharged[e.Site]), Bound: bound[e.Site], Raises: orEmpty(raises[e.Site]), Caught: orEmpty(caught[e.Site])}
		if c.Bound == nil {
			c.Bound = []codeBinding{}
		}
		cp.Calls = append(cp.Calls, c)
	}
	for _, e := range t.Needs {
		if handles[e.From] != "" && handles[e.To] != "" {
			cp.Needs = append(cp.Needs, codeNeed{Of: handles[e.From], Capability: handles[e.To]})
		}
	}
	for _, e := range t.Fails {
		if handles[e.From] != "" && handles[e.To] != "" {
			cp.Fails = append(cp.Fails, codeFailing{Of: handles[e.From], Failure: handles[e.To]})
		}
	}
	for _, m := range t.Machines {
		cp.Machines = append(cp.Machines, codeMachineOf(m, handles[m.Sym], t.Symbols[m.Sym].Name))
	}
}

// codeMachineOf is the machine graph m with its handle and name; every
// list is empty rather than nil, and an arm's nil variants are any.
func codeMachineOf(m facts.MachineGraph, handle, name string) codeMachine {
	list := func(vs []string) []string {
		if vs == nil {
			return []string{}
		}
		return vs
	}
	place := func(p facts.Place) codePlace { return codePlace(p) }
	out := codeMachine{Handle: handle, Name: name, States: list(m.States), Signals: list(m.Signals), Initial: m.Initial, Arms: []codeArm{}, Timers: []codeTimer{}, Work: []codePlace{}, Exit: []codePlace{}, Unsure: list(m.Unsure), Computed: list(m.Computed)}
	for _, a := range m.Arms {
		arm := codeArm{At: place(a.At), States: list(a.States), AnyState: a.States == nil, Signals: list(a.Signals), AnySignal: a.Signals == nil, Partial: a.Partial, Edges: []codeTransition{}, Alts: []codeAlt{}}
		for _, x := range a.Alts {
			arm.Alts = append(arm.Alts, codeAlt{States: list(x.States), AnyState: x.States == nil, Signals: list(x.Signals), AnySignal: x.Signals == nil, Partial: x.Partial})
		}
		for _, e := range a.Edges {
			arm.Edges = append(arm.Edges, codeTransition{Decision: e.Decision, To: e.To, At: place(e.At)})
		}
		out.Arms = append(out.Arms, arm)
	}
	for _, tm := range m.Timers {
		out.Timers = append(out.Timers, codeTimer{State: tm.State, Signal: tm.Signal, At: place(tm.At)})
	}
	for _, w := range m.Work {
		out.Work = append(out.Work, place(w))
	}
	for _, x := range m.Exit {
		out.Exit = append(out.Exit, place(x))
	}
	return out
}

// srcOf is the text of a parsed file, or "" when it did not parse.
func srcOf(f *syntax.File) string {
	if f == nil {
		return ""
	}
	return f.Src
}
