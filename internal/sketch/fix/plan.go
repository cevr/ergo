package fix

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"

	format "ergo.local/prototype/internal/hostsyntax"
	"ergo.local/prototype/internal/linked/code"
	"ergo.local/prototype/internal/sketch/check"
)

// The clause loop is std code's Plan(Fixes) (SEMTOOLS SEM4b): the linked
// tool plans the change `fixes []` over the program's texts, checking
// through Checker on each round, and the plan's files are the fixed
// texts. Nothing here loops.

// Edit replaces the bytes Start..End of the file File with Text.
type Edit struct {
	File       string
	Start, End int
	Text       string
}

// FixEdits is ds in the order their fixes apply, with the edits of
// each one's fix (std code's Diagnostic.Fix): a clause fix (q137) inserts
// its clause at InsertAt and at each of Owns, "(" at Open and ")" at
// InsertAt before its clause, and the imports that the clause fixes of its
// file need, as one insertion that every such fix of the file lists (std
// code makes equal edits once). Std code inserts at one place in the
// order the fixes list them, so the clause fixes are ordered fails,
// needs, takes, as ergo fix writes them; the other diagnostics keep
// their order. A fix with no clause (a lint fix) replaces its Replace
// bytes at InsertAt.
func FixEdits(texts map[string]string, ds []Diagnostic) ([]Diagnostic, [][]Edit, error) {
	ordered := slices.Clone(ds)
	slices.SortStableFunc(ordered, func(a, b Diagnostic) int { return clauseRank(a) - clauseRank(b) })
	paths := map[string][]string{}
	for _, d := range ordered {
		if d.Fix == nil || d.Fix.Text == "" || d.Fix.Clause == "" {
			continue
		}
		for _, p := range d.Fix.Imports {
			if !slices.Contains(paths[d.File], p) {
				paths[d.File] = append(paths[d.File], p)
			}
		}
	}
	imports := map[string][]Edit{}
	for file, ps := range paths {
		ins, err := importEdits(texts[file], ps)
		if err != nil {
			return nil, nil, fmt.Errorf("fix: %s: %w", file, err)
		}
		for _, x := range ins {
			imports[file] = append(imports[file], Edit{File: file, Start: x.at, End: x.at, Text: x.text})
		}
	}
	edits := make([][]Edit, len(ordered))
	for i, d := range ordered {
		f := d.Fix
		if f == nil || f.Text == "" {
			continue
		}
		if f.Clause == "" {
			edits[i] = []Edit{{File: d.File, Start: f.InsertAt, End: f.InsertAt + f.Replace, Text: f.Text}}
			continue
		}
		var es []Edit
		if f.Open > 0 {
			es = append(es, Edit{File: d.File, Start: f.Open, End: f.Open, Text: "("}, Edit{File: d.File, Start: f.InsertAt, End: f.InsertAt, Text: ")"})
		}
		es = append(es, Edit{File: d.File, Start: f.InsertAt, End: f.InsertAt, Text: f.Text})
		for _, at := range f.Owns {
			es = append(es, Edit{File: d.File, Start: at, End: at, Text: f.Text})
		}
		edits[i] = append(es, imports[d.File]...)
	}
	return ordered, edits, nil
}

// clauseRank orders the clause fixes as ergo fix writes them at one
// offset: fails, needs, then takes and own; a diagnostic without a
// clause fix last.
func clauseRank(d Diagnostic) int {
	if d.Fix == nil || d.Fix.Text == "" {
		return 3
	}
	switch d.Fix.Clause {
	case "fails":
		return 0
	case "needs":
		return 1
	case "takes", "own":
		return 2
	}
	return 3
}

// planRows runs the clause loop as Plan(Fixes) over texts, checked by
// chk in the default mode. It records in changes each widened signature
// in its last form, and returns the fixed texts, the diagnostics of the
// last check, and the rounds (the checks of the loop, the last one that
// found no fix included).
func planRows(texts map[string]string, chk Checker, changes map[[3]string]Change) (map[string]string, []Diagnostic, int, error) {
	b := &rowsBridge{chk: chk}
	var sources []codeSource
	for file, text := range texts {
		sources = append(sources, codeSource{File: file, Text: text})
	}
	slices.SortFunc(sources, func(a, b codeSource) int {
		if a.File < b.File {
			return -1
		}
		if a.File > b.File {
			return 1
		}
		return 0
	})
	src, err := json.Marshal(sources)
	if err != nil {
		return nil, nil, 0, err
	}
	out, err := code.Plan(context.Background(), b, string(src), "fixes []", false)
	if err != nil {
		return nil, nil, 0, err
	}
	if b.err != nil {
		return nil, nil, 0, b.err
	}
	if len(out) != 4 || out[0] != "0" {
		return nil, nil, 0, fmt.Errorf("fix: %s", out[2])
	}
	var plan struct {
		Files []struct {
			File string `json:"file"`
			Text string `json:"text"`
		} `json:"files"`
	}
	if err := json.Unmarshal([]byte(out[3]), &plan); err != nil {
		return nil, nil, 0, err
	}
	fixed := map[string]string{}
	for k, v := range texts {
		fixed[k] = v
	}
	for _, f := range plan.Files {
		fixed[f.File] = f.Text
	}
	for k, c := range b.changes {
		changes[k] = c
	}
	// The plan checks its result once more after the loop's last check:
	// that check is the loop's, counted once.
	return fixed, b.last, b.checks - 1, nil
}

type codeSource struct {
	File string `json:"file"`
	Text string `json:"text"`
}

// rowsBridge is the Checker of planRows' plan: each check asks chk, and
// answers its diagnostics with the edits of each fix
// (FixEdits). It keeps the widened signatures, the last check's
// diagnostics, and the number of checks.
type rowsBridge struct {
	chk     Checker
	req     bytes.Buffer
	reply   *bytes.Reader
	changes map[[3]string]Change
	last    []Diagnostic
	checks  int
	err     error
}

func (b *rowsBridge) Write(p []byte) (int, error) {
	b.reply = nil
	return b.req.Write(p)
}

func (b *rowsBridge) Read(p []byte) (int, error) {
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

func (b *rowsBridge) answer(req []byte) []byte {
	var ask struct {
		Tag     string       `json:"_tag"`
		Sources []codeSource `json:"sources"`
		Text    string       `json:"text"`
	}
	failed := func(err error) []byte {
		if b.err == nil {
			b.err = err
		}
		out, _ := json.Marshal(map[string]string{"_tag": "replyFailed", "reason": err.Error()})
		return out
	}
	if err := json.Unmarshal(req, &ask); err != nil {
		return failed(err)
	}
	if ask.Tag == "askFormat" {
		text, err := format.Source(ask.Text)
		if err != nil {
			return failed(err)
		}
		out, _ := json.Marshal(map[string]string{"_tag": "replyText", "text": text})
		return out
	}
	texts := map[string]string{}
	for _, s := range ask.Sources {
		texts[s.File] = s.Text
	}
	b.checks++
	o, err := b.chk(texts, check.Options{})
	if err != nil {
		return failed(err)
	}
	b.last = o.Diagnostics
	ordered, edits, err := FixEdits(texts, o.Diagnostics)
	if err != nil {
		return failed(err)
	}
	if b.changes == nil {
		b.changes = map[[3]string]Change{}
	}
	var fixes []Diagnostic
	for _, d := range o.Diagnostics {
		if d.Fix != nil && d.Fix.Text != "" {
			fixes = append(fixes, d)
		}
	}
	if _, err := apply(texts, fixes, b.changes); err != nil {
		return failed(err)
	}
	type wireEdit struct {
		File  string `json:"file"`
		Start int    `json:"start"`
		End   int    `json:"end"`
		Text  string `json:"text"`
	}
	type diag struct {
		Code    string     `json:"code"`
		Message string     `json:"message"`
		File    string     `json:"file"`
		Line    int        `json:"line"`
		Column  int        `json:"column"`
		Fix     []wireEdit `json:"fix"`
	}
	ds := []diag{}
	for i, d := range ordered {
		x := diag{Code: d.Code, Message: d.Message, File: d.File, Line: d.Span.Line, Column: d.Span.Column, Fix: []wireEdit{}}
		for _, e := range edits[i] {
			x.Fix = append(x.Fix, wireEdit(e))
		}
		ds = append(ds, x)
	}
	type tables struct {
		Revision string `json:"revision"`
		Root     string `json:"root"`
		Packages []any  `json:"packages"`
	}
	out, err := json.Marshal(map[string]any{"_tag": "replyReport", "report": map[string]any{
		"tables": tables{Packages: []any{}}, "extents": []any{}, "diagnostics": ds,
		"code": "", "binds": []any{}, "workspace": []string{},
	}})
	if err != nil {
		return failed(err)
	}
	return out
}
