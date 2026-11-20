package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	format "ergo.local/prototype/internal/hostsyntax"
	"ergo.local/prototype/internal/linked/code"
	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/fix"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/sketchfront"
)

// `ergo code plan`, `apply` and `recover` (SEMTOOLS SEM4b; docs/spec/
// code.md, Plans and Apply): std code's Planner and Writer, run by the
// linked tool (internal/linked/code, plan.ergo). The planner checks
// through the product Checker, which is this file's Go checker and
// formatter, answering the tool's requests through an io.ReadWriter
// (codeBridge). A plan is kept as JSON in the workspace's plan folder
// (PlansDir); apply reads it there and writes through the host's file
// system (code.HostFiles), by the apply protocol.

// plansDir is where `ergo code plan` keeps its plans, from the workspace
// root. Plan writes no other file.
const plansDir = ".ergo/plans"

// codeWorkspace is the program `ergo code` was asked about: the folder
// checked, the module root (the folder its files' paths are relative
// to), and the sources of its own module's packages.
type codeWorkspace struct {
	dir, root string
	sources   []codeSource
}

type codeSource struct {
	File string `json:"file"`
	Text string `json:"text"`
}

// loadCodeWorkspace reads the program in dir (absolute).
func loadCodeWorkspace(dir string) (codeWorkspace, error) {
	a, err := sketchfront.CheckDir(dir)
	if err != nil {
		return codeWorkspace{}, err
	}
	w := codeWorkspace{dir: dir, root: dir}
	paths := a.SourcePaths()
	for _, p := range a.Packages() {
		if p.Module != "" {
			continue
		}
		for _, f := range p.Files {
			abs, ok := paths[f.Rel]
			if !ok {
				continue
			}
			data, err := os.ReadFile(abs)
			if err != nil {
				return codeWorkspace{}, err
			}
			w.sources = append(w.sources, codeSource{File: f.Rel, Text: string(data)})
			if r, ok := strings.CutSuffix(filepath.ToSlash(abs), "/"+f.Rel); ok {
				w.root = filepath.FromSlash(r)
			}
		}
	}
	slices.SortFunc(w.sources, func(a, b codeSource) int { return cmp.Compare(a.File, b.File) })
	return w, nil
}

// codePlanCommand runs `ergo code plan|apply|recover` with args after the
// command word, over the program in dir.
func codePlanCommand(dir, command string, args []string, asJSON bool) error {
	w, err := loadCodeWorkspace(dir)
	if err != nil {
		return exitStatusError{code: 1, message: "ergo code: " + err.Error()}
	}
	var out []string
	switch command {
	case "plan":
		if len(args) != 1 {
			return invalidInvocation(fmt.Errorf("ergo code plan takes one change, as text or JSON"))
		}
		sources, err := json.Marshal(w.sources)
		if err != nil {
			return exitStatusError{code: 1, message: "ergo code: " + err.Error()}
		}
		out, err = code.Plan(context.Background(), &codeBridge{w: w}, string(sources), args[0], asJSON)
		if err != nil {
			return exitStatusError{code: 1, message: "ergo code plan: " + err.Error()}
		}
		if len(out) == 4 && out[3] != "" {
			if err := keepPlan(w.root, out[3]); err != nil {
				return exitStatusError{code: 1, message: "ergo code plan: " + err.Error()}
			}
		}
	case "apply":
		if len(args) != 1 {
			return invalidInvocation(fmt.Errorf("ergo code apply takes one plan id"))
		}
		data, err := os.ReadFile(planPath(w.root, args[0]))
		if errors.Is(err, os.ErrNotExist) {
			return exitStatusError{code: 1, message: "ergo code apply: no plan " + args[0]}
		} else if err != nil {
			return exitStatusError{code: 1, message: "ergo code apply: " + err.Error()}
		}
		out, err = code.Apply(context.Background(), w.root, string(data))
		if err != nil {
			return exitStatusError{code: 1, message: "ergo code apply: " + err.Error()}
		}
	case "recover":
		if len(args) != 0 {
			return invalidInvocation(fmt.Errorf("ergo code recover takes no argument"))
		}
		out, err = code.Recover(context.Background(), w.root)
		if err != nil {
			return exitStatusError{code: 1, message: "ergo code recover: " + err.Error()}
		}
	}
	if len(out) < 3 {
		return exitStatusError{code: 1, message: fmt.Sprintf("ergo code %s: the tool gave %d parts", command, len(out))}
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

// planPath is where the plan id is kept. An id is hex; any other text
// names no plan.
func planPath(root, id string) string {
	for _, r := range id {
		if !strings.ContainsRune("0123456789abcdef", r) {
			id = "-"
			break
		}
	}
	return filepath.Join(root, filepath.FromSlash(plansDir), id+".json")
}

// keepPlan writes the plan whose JSON is text into the plan folder.
func keepPlan(root, text string) error {
	var p struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(text), &p); err != nil || p.ID == "" {
		return fmt.Errorf("the plan has no id")
	}
	path := planPath(root, p.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(text), 0o644)
}

// codeBridge is the product Checker's Go side: the linked tool writes an
// ask (JSON) and reads the reply to its end; each read after a write
// answers the request written.
type codeBridge struct {
	w     codeWorkspace
	req   bytes.Buffer
	reply *bytes.Reader
}

func (b *codeBridge) Write(p []byte) (int, error) {
	b.reply = nil
	return b.req.Write(p)
}

func (b *codeBridge) Read(p []byte) (int, error) {
	if b.reply == nil {
		b.reply = bytes.NewReader(b.answer(b.req.Bytes()))
		b.req.Reset()
	}
	n, err := b.reply.Read(p)
	if err == io.EOF {
		b.reply = nil
	}
	return n, err
}

// answer is the JSON reply to the ask whose JSON is req.
func (b *codeBridge) answer(req []byte) []byte {
	var ask struct {
		Tag     string       `json:"_tag"`
		Sources []codeSource `json:"sources"`
		Text    string       `json:"text"`
	}
	failed := func(why string) []byte {
		out, _ := json.Marshal(map[string]string{"_tag": "replyFailed", "reason": why})
		return out
	}
	if err := json.Unmarshal(req, &ask); err != nil {
		return failed("the request is not JSON: " + err.Error())
	}
	switch ask.Tag {
	case "askFormat":
		text, err := format.Source(ask.Text)
		if err != nil {
			return failed(err.Error())
		}
		out, _ := json.Marshal(map[string]string{"_tag": "replyText", "text": text})
		return out
	case "askCheck":
		r, err := codeReportOf(b.w, ask.Sources)
		if err != nil {
			return failed(err.Error())
		}
		out, err := json.Marshal(struct {
			Tag    string     `json:"_tag"`
			Report codeReport `json:"report"`
		}{"replyReport", r})
		if err != nil {
			return failed(err.Error())
		}
		return out
	}
	return failed("an unknown request " + ask.Tag)
}

// codeReport is code.Report's JSON: what the product Checker gives for a
// check of some sources.
type (
	codeReport struct {
		Tables      codeTables       `json:"tables"`
		Extents     []codeExtent     `json:"extents"`
		Diagnostics []codeDiagnostic `json:"diagnostics"`
		Code        string           `json:"code"`
		Binds       []codeBind       `json:"binds"`
		Workspace   []string         `json:"workspace"`
	}
	codeExtent struct {
		Handle string `json:"handle"`
		File   string `json:"file"`
		Start  int    `json:"start"`
		End    int    `json:"end"`
	}
	codeDiagnostic struct {
		Code    string     `json:"code"`
		Message string     `json:"message"`
		File    string     `json:"file"`
		Line    int        `json:"line"`
		Column  int        `json:"column"`
		Fix     []codeEdit `json:"fix"`
	}
	codeEdit struct {
		File  string `json:"file"`
		Start int    `json:"start"`
		End   int    `json:"end"`
		Text  string `json:"text"`
	}
	codeBind struct {
		Op string `json:"op"`
		By string `json:"by"`
	}
)

// codeReportOf checks sources (module-relative paths and texts) over the
// workspace w: its fact tables, the extent of each top-level declaration,
// the diagnostics with their fixes' edits, Code (the root's implementation fingerprint: the
// token text of the program, positions and comments left out), the
// Provides edges, and the workspace's packages.
func codeReportOf(w codeWorkspace, sources []codeSource) (codeReport, error) {
	overlay := map[string]string{}
	texts := map[string]string{}
	for _, s := range sources {
		overlay[filepath.Join(w.root, filepath.FromSlash(s.File))] = s.Text
		texts[s.File] = s.Text
	}
	a, err := sketchfront.CheckDirOptions(w.dir, overlay, check.Options{})
	if err != nil {
		return codeReport{}, err
	}
	r := codeReport{Tables: codeTablesOf(a), Extents: []codeExtent{}, Diagnostics: []codeDiagnostic{}, Binds: []codeBind{}, Workspace: []string{}}
	if fps := a.Fingerprints(); len(fps) > 0 {
		r.Code = fps[len(fps)-1].Implementation
	}
	handles := map[string]string{} // file:line:column of a declared name → handle
	for _, p := range r.Tables.Packages {
		for _, d := range p.Decls {
			handles[fmt.Sprintf("%s:%d:%d", d.File, d.Line, d.Column)] = d.Handle
		}
	}
	for _, p := range a.Packages() {
		if p.Module == "" {
			r.Workspace = append(r.Workspace, p.Path)
		}
		for _, f := range p.Files {
			if f.Syntax == nil {
				continue
			}
			r.Extents = append(r.Extents, extentsOf(f.Rel, f.Syntax, handles)...)
			r.Binds = append(r.Binds, bindsOf(p, f.Syntax)...)
		}
	}
	r.Diagnostics = codeDiagnosticsOf(a.Diagnostics, texts)
	return r, nil
}

// extentsOf is the extent of each top-level declaration of f that has a
// handle: from its first byte to just past the newline of its last line.
func extentsOf(file string, f *syntax.File, handles map[string]string) []codeExtent {
	var out []codeExtent
	for _, d := range f.Decls {
		var name *syntax.Ident
		switch d := d.(type) {
		case *syntax.TypeDecl:
			name = d.Name
		case *syntax.FuncDecl:
			name = d.Name
		case *syntax.ContextDecl:
			name = d.Name
		case *syntax.TestDecl:
			name = d.Name
		case *syntax.LawDecl:
			name = d.Name
		case *syntax.VarDecl:
			if len(d.Specs) == 1 && len(d.Specs[0].Names) == 1 {
				name = d.Specs[0].Names[0]
			}
		}
		if name == nil {
			continue
		}
		line, col := f.Position(name.Pos())
		h, ok := handles[fmt.Sprintf("%s:%d:%d", file, line, col)]
		if !ok {
			continue
		}
		start, end := int(d.Pos()), int(d.End())
		if nl := strings.IndexByte(f.Src[end:], '\n'); nl >= 0 {
			end += nl + 1
		} else {
			end = len(f.Src)
		}
		out = append(out, codeExtent{Handle: h, File: file, Start: start, End: end})
	}
	return out
}

// bindsOf is the Provides edges f declares: each implements clause's
// operation type, provided by the type that writes it.
func bindsOf(p sketchfront.CheckedPackage, f *syntax.File) []codeBind {
	pkg := p.Partial
	if pkg == nil || pkg.Info == nil {
		return nil
	}
	var out []codeBind
	for _, d := range f.Decls {
		td, ok := d.(*syntax.TypeDecl)
		if !ok {
			continue
		}
		for _, x := range td.Implements {
			var id *syntax.Ident
			switch x := x.(type) {
			case *syntax.Ident:
				id = x
			case *syntax.SelectorExpr:
				id = x.Sel
			}
			if id == nil {
				continue
			}
			tn, ok := pkg.Info.Uses[id].(*check.TypeName)
			if !ok || tn.Pkg == nil {
				continue
			}
			op := tn.Pkg.Path
			if op == "main" {
				op = p.Path
			}
			out = append(out, codeBind{Op: op + "." + tn.Name, By: p.Path + "." + td.Name.Name})
		}
	}
	return out
}

// codeDiagnosticsOf is ds as code.Diagnostics, each with the edits of
// its fix, in the order the fixes apply (fix.FixEdits).
func codeDiagnosticsOf(ds []report.Diagnostic, texts map[string]string) []codeDiagnostic {
	var keyed []fix.Diagnostic
	for _, d := range ds {
		keyed = append(keyed, fix.Diagnostic{File: d.File, Diagnostic: d})
	}
	ordered, edits, err := fix.FixEdits(texts, keyed)
	if err != nil {
		ordered, edits = keyed, make([][]fix.Edit, len(keyed))
	}
	out := []codeDiagnostic{}
	for i, d := range ordered {
		cd := codeDiagnostic{Code: d.Code, Message: d.Message, File: d.File, Line: d.Span.Line, Column: d.Span.Column, Fix: []codeEdit{}}
		for _, e := range edits[i] {
			cd.Fix = append(cd.Fix, codeEdit(e))
		}
		out = append(out, cd)
	}
	return out
}
