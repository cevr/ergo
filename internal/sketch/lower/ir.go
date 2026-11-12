package lower

// The typed-IR build path (wf10 IR1 spike, then plan-tier: IR is the build
// path, owner 2026-11-12). LowerIR lowers a unit as Lower does, except
// that each function whose body the IR builder can express gets a typed
// IR body instead of a Go body: its Go declaration has no body, and the IR
// file carries the body for the patched compiler's -ergoir entry
// (toolchain/ir). Every other function keeps its source body, and the
// reason is recorded. Source emission is the test-time reference: Lower
// never reads any of this.
//
// The builder is one file per construct family, so two lanes can grow it
// at once: ir.go (format, entry, report), ir_types.go (types),
// ir_stmt.go (statements), ir_expr.go (expressions), ir_call.go (calls).

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// IRFileName is the IR file LowerIR adds to the module.
const IRFileName = "ergoir.json"

// IRReport is what LowerIR did: the functions given IR bodies, and why the
// others kept their source bodies (reason -> count).
type IRReport struct {
	IR       []string       `json:"ir"`
	Fallback map[string]int `json:"fallback"`
	Hash     string         `json:"hash"`
}

// LowerIR is Lower with the experimental IR bodies. main.go ends with the
// IR file's hash, so Go's build cache keys the main package's compile on
// the IR as well as on the Go text.
func LowerIR(u Unit) ([]File, IRReport, error) {
	if err := u.Validate(); err != nil {
		return nil, IRReport{}, err
	}
	l := newLowerer(u)
	l.ir = &irState{files: map[string]int{}, report: IRReport{Fallback: map[string]int{}}}
	code, err := l.program()
	if err != nil {
		return nil, IRReport{}, err
	}
	sort.Slice(l.ir.out.Funcs, func(i, j int) bool { return l.ir.out.Funcs[i].Name < l.ir.out.Funcs[j].Name })
	l.ir.out.V = 1
	data, err := json.Marshal(&l.ir.out)
	if err != nil {
		return nil, IRReport{}, err
	}
	sum := sha256.Sum256(data)
	l.ir.report.Hash = hex.EncodeToString(sum[:])
	code = append(code, []byte("\n// ergoir sha256:"+l.ir.report.Hash+"\n")...)
	files := append(moduleFiles(u.Go, code), l.assetFiles()...)
	files = append(files, File{Path: IRFileName, Data: data})
	sort.Strings(l.ir.report.IR)
	return files, l.ir.report, nil
}

// irState is the IR a LowerIR run collects.
type irState struct {
	out    irFile
	files  map[string]int
	report IRReport
}

// The IR format; the derivative's noder/ergoir.go reads the same shape.
type irFile struct {
	V     int      `json:"v"`
	Files []string `json:"files"`
	Funcs []irFunc `json:"funcs"`
}

type irFunc struct {
	Name   string    `json:"name"`
	Pos    irPos     `json:"pos"`
	End    irPos     `json:"end"`
	Locals []irLocal `json:"locals,omitempty"`
	Body   []*irStmt `json:"body"`
}

type irLocal struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Pos  irPos  `json:"pos"`
}

type irPos [3]int

type irStmt struct {
	K     string    `json:"k"`
	Pos   irPos     `json:"pos"`
	Local int       `json:"local,omitempty"`
	Key   *int      `json:"key,omitempty"`
	Val   *int      `json:"val,omitempty"`
	X     *irExpr   `json:"x,omitempty"`
	Then  []*irStmt `json:"then,omitempty"`
	Else  []*irStmt `json:"else,omitempty"`
	Body  []*irStmt `json:"body,omitempty"`
	End   irPos     `json:"end"`
	// ElsePos and ElseEnd bound an if's else block (DWARF scopes must
	// increase).
	ElsePos irPos `json:"epos"`
	ElseEnd irPos `json:"eend"`
}

type irExpr struct {
	K     string    `json:"k"`
	Pos   irPos     `json:"pos"`
	I     int       `json:"i,omitempty"`
	T     string    `json:"t,omitempty"`
	V     string    `json:"v,omitempty"`
	Op    string    `json:"op,omitempty"`
	X     *irExpr   `json:"x,omitempty"`
	Y     *irExpr   `json:"y,omitempty"`
	Name  string    `json:"name,omitempty"`
	Args  []*irExpr `json:"args,omitempty"`
	Elems []*irExpr `json:"elems,omitempty"`
	Keys  []string  `json:"keys,omitempty"`
}

// irUnsupported stops an IR body: the function keeps its source body.
type irUnsupported struct{ reason string }

func irNo(format string, args ...any) { panic(irUnsupported{fmt.Sprintf(format, args...)}) }

// irBuilder builds one function's IR body.
type irBuilder struct {
	fc     *funcCtx
	l      *lowerer
	st     *irState
	fn     *irFunc
	locals map[*check.Var]int
	next   int
	env    int // the environment's local, or -1 in a pure function
	result check.Type
}

// body tries an IR body for fd. params are the Ergo parameters in order; it
// reports whether fd got one. The caller has written fd's Go head.
func (st *irState) body(fc *funcCtx, fd *funcDecl, params []*check.Var) (ok bool) {
	f := fd.fn
	b := &irBuilder{fc: fc, l: fc.l, st: st, locals: map[*check.Var]int{}, env: -1, result: fc.frame.result}
	defer func() {
		if r := recover(); r != nil {
			u, isNo := r.(irUnsupported)
			if !isNo {
				panic(r)
			}
			st.report.Fallback[u.reason]++
			ok = false
		}
	}()
	switch {
	case !fc.directives:
		irNo("library body")
	case fd.fallible:
		irNo("fallible function")
	case fd.ptr:
		irNo("pointer receiver")
	case len(fc.witness) > 0:
		irNo("witness parameter")
	case f.Decl.Body == nil:
		irNo("no body")
	}
	// Go parameter order: receiver, environment, parameters (emitBody).
	for i, v := range params {
		if v == nil {
			irNo("unnamed parameter")
		}
		if i == 0 && fd.method != nil {
			b.locals[v] = b.next
			b.next++
			if !fd.pure {
				b.env = b.next
				b.next++
			}
			continue
		}
		if i == 0 && !fd.pure {
			b.env = b.next
			b.next++
		}
		b.locals[v] = b.next
		b.next++
		b.goType(v.Type, f.Decl)
	}
	if len(params) == 0 && !fd.pure {
		b.env = b.next
		b.next++
	}
	if b.result != nil {
		b.goType(b.result, f.Decl)
	}
	name := fd.name
	if fd.method != nil {
		name = fd.method.name + "." + fd.name
	}
	b.fn = &irFunc{Name: name, Pos: b.pos(f.Decl, f.Decl.Pos()), End: b.pos(f.Decl, f.Decl.Body.Rbrace)}
	if b.env >= 0 && frameFootprint(f.Pkg.Info, f.Decl).EntryPoll && !b.l.knownClosed(f) && fc.frame.polls {
		b.fn.Body = append(b.fn.Body, b.poll(f.Decl))
	}
	b.fn.Body = append(b.fn.Body, b.tail(f.Decl.Body.List, b.result != nil)...)
	st.out.Funcs = append(st.out.Funcs, *b.fn)
	st.report.IR = append(st.report.IR, name)
	return true
}

// pos is n's position as the IR spells it.
func (b *irBuilder) pos(n syntax.Node, p syntax.Pos) irPos {
	if _, ok := b.l.sourceOf(n); !ok {
		irNo("node outside the program's sources")
	}
	_, path, line, col := b.l.at(n, p)
	i, ok := b.st.files[path]
	if !ok {
		i = len(b.st.out.Files)
		b.st.files[path] = i
		b.st.out.Files = append(b.st.out.Files, path)
	}
	return irPos{i, line, col}
}
