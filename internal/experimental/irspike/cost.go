package irspike

import (
	"bufio"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"

	"ergo.local/prototype/internal/sketch/lower"
)

// The IR format as the derivative's decoder reads it (a mirror of lower's
// unexported types: the receipt decodes what the compiler decodes).
type costFile struct {
	V     int        `json:"v"`
	Files []string   `json:"files"`
	Funcs []costFunc `json:"funcs"`
}

type costFunc struct {
	Name   string `json:"name"`
	Pos    [3]int `json:"pos"`
	End    [3]int `json:"end"`
	Locals []struct {
		Name string `json:"name"`
		Type string `json:"type"`
		Pos  [3]int `json:"pos"`
	} `json:"locals,omitempty"`
	Body []*costStmt `json:"body"`
}

type costStmt struct {
	K       string      `json:"k"`
	Pos     [3]int      `json:"pos"`
	Local   int         `json:"local,omitempty"`
	Key     *int        `json:"key,omitempty"`
	Val     *int        `json:"val,omitempty"`
	X       *costExpr   `json:"x,omitempty"`
	Then    []*costStmt `json:"then,omitempty"`
	Else    []*costStmt `json:"else,omitempty"`
	Body    []*costStmt `json:"body,omitempty"`
	End     [3]int      `json:"end"`
	ElsePos [3]int      `json:"epos"`
	ElseEnd [3]int      `json:"eend"`
}

type costExpr struct {
	K     string      `json:"k"`
	Pos   [3]int      `json:"pos"`
	I     int         `json:"i,omitempty"`
	T     string      `json:"t,omitempty"`
	V     string      `json:"v,omitempty"`
	Op    string      `json:"op,omitempty"`
	X     *costExpr   `json:"x,omitempty"`
	Y     *costExpr   `json:"y,omitempty"`
	Name  string      `json:"name,omitempty"`
	Args  []*costExpr `json:"args,omitempty"`
	Elems []*costExpr `json:"elems,omitempty"`
	Keys  []string    `json:"keys,omitempty"`
}

func (f *costFile) nodes() int {
	n := 0
	var stmts func([]*costStmt)
	var expr func(*costExpr)
	expr = func(x *costExpr) {
		if x == nil {
			return
		}
		n++
		expr(x.X)
		expr(x.Y)
		for _, a := range x.Args {
			expr(a)
		}
		for _, e := range x.Elems {
			expr(e)
		}
	}
	stmts = func(list []*costStmt) {
		for _, s := range list {
			n++
			expr(s.X)
			stmts(s.Then)
			stmts(s.Else)
			stmts(s.Body)
		}
	}
	for _, fn := range f.Funcs {
		n++
		stmts(fn.Body)
	}
	return n
}

// CostRow is the decode-cost receipt of one program: instructions per
// node of the IR path's JSON (encode, decode) and of the source path's Go
// text for the same bodies (parse), and the whole compile of the program
// package by each path (median of reps; the machine code is the same, so
// the difference is the front end's).
type CostRow struct {
	Program   string  `json:"program"`
	IRFuncs   int     `json:"ir_funcs"`
	IRNodes   int     `json:"ir_nodes"`
	GoNodes   int     `json:"go_nodes"`
	JSONBytes int     `json:"json_bytes"`
	Decode    float64 `json:"decode_instr_per_ir_node"`
	Encode    float64 `json:"encode_instr_per_ir_node"`
	Parse     float64 `json:"go_parse_instr_per_go_node"`
	// CompileSource and CompileIR are the program package's compile, in
	// instructions (median); Saved is their difference per Go node of the
	// IR bodies (positive: the IR path is cheaper).
	CompileSource int64   `json:"compile_source"`
	CompileIR     int64   `json:"compile_ir"`
	Saved         float64 `json:"saved_instr_per_go_node"`
}

// goBodyNodes counts the AST nodes of the bodies of the named functions
// (lowered names; a method is Recv.Name) in a Go file's text.
func goBodyNodes(text []byte, names []string) (int, error) {
	f, err := parser.ParseFile(token.NewFileSet(), "main.go", text, parser.SkipObjectResolution)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		name := fd.Name.Name
		if fd.Recv != nil && len(fd.Recv.List) == 1 {
			t := fd.Recv.List[0].Type
			if s, ok := t.(*ast.StarExpr); ok {
				t = s.X
			}
			if id, ok := t.(*ast.Ident); ok {
				name = id.Name + "." + name
			}
		}
		if !slices.Contains(names, name) {
			continue
		}
		ast.Inspect(fd.Body, func(x ast.Node) bool {
			if x != nil {
				n++
			}
			return true
		})
	}
	return n, nil
}

// MeasureCost takes the decode-cost receipt of the program text: self is
// this binary (its tool subcommand meters the compile), reps the builds
// per path.
func MeasureCost(name, text, self string, src, ir Toolchain, work string, reps int) (CostRow, error) {
	row := CostRow{Program: name}
	dir := filepath.Join(work, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return row, err
	}
	file := filepath.Join(dir, "p.ergo")
	if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
		return row, err
	}
	u, err := Load(file)
	if err != nil {
		return row, err
	}
	srcMod, irMod := filepath.Join(dir, "source"), filepath.Join(dir, "ir")
	if _, err := Emit(u, Source, srcMod); err != nil {
		return row, err
	}
	rep, err := Emit(u, IR, irMod)
	if err != nil {
		return row, err
	}
	row.IRFuncs = len(rep.IR)
	data, err := os.ReadFile(filepath.Join(irMod, lower.IRFileName))
	if err != nil {
		return row, err
	}
	row.JSONBytes = len(data)
	var f costFile
	if err := json.Unmarshal(data, &f); err != nil {
		return row, err
	}
	row.IRNodes = f.nodes()
	srcText, err := os.ReadFile(filepath.Join(srcMod, "main.go"))
	if err != nil {
		return row, err
	}
	if row.GoNodes, err = goBodyNodes(srcText, rep.IR); err != nil {
		return row, err
	}
	irText, err := os.ReadFile(filepath.Join(irMod, "main.go"))
	if err != nil {
		return row, err
	}
	// In process: the collector off, so only the work itself counts.
	defer debug.SetGCPercent(debug.SetGCPercent(-1))
	const k = 200
	dec := CountSelf(func() {
		for i := 0; i < k; i++ {
			var g costFile
			_ = json.Unmarshal(data, &g)
		}
	})
	enc := CountSelf(func() {
		for i := 0; i < k; i++ {
			_, _ = json.Marshal(&f)
		}
	})
	parseOf := func(t []byte) int64 {
		return CountSelf(func() {
			for i := 0; i < k; i++ {
				_, _ = parser.ParseFile(token.NewFileSet(), "main.go", t, parser.SkipObjectResolution)
			}
		})
	}
	ps, pi := parseOf(srcText), parseOf(irText)
	if row.IRNodes > 0 && row.GoNodes > 0 {
		row.Decode = float64(dec) / k / float64(row.IRNodes)
		row.Encode = float64(enc) / k / float64(row.IRNodes)
		row.Parse = float64(ps-pi) / k / float64(row.GoNodes)
	}
	compile := func(tc Toolchain, p Path, mod string) (int64, error) {
		var got []int64
		for r := 0; r < reps; r++ {
			// A fresh comment: the program package compiles again, std
			// stays cached.
			mf, err := os.OpenFile(filepath.Join(mod, "main.go"), os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				return 0, err
			}
			fmt.Fprintf(mf, "\n// rep %d\n", r)
			mf.Close()
			log := filepath.Join(dir, fmt.Sprintf("%s-%d.log", p, r))
			if _, err := Build(tc, p, mod, filepath.Join(dir, p.String()+".bin"), BuildOptions{Toolexec: self + " tool " + log}); err != nil {
				return 0, err
			}
			n, err := programCompile(log)
			if err != nil {
				return 0, err
			}
			got = append(got, n)
		}
		slices.Sort(got)
		return got[len(got)/2], nil
	}
	if row.CompileSource, err = compile(src, Source, srcMod); err != nil {
		return row, err
	}
	if row.CompileIR, err = compile(ir, IR, irMod); err != nil {
		return row, err
	}
	if row.GoNodes > 0 {
		row.Saved = float64(row.CompileSource-row.CompileIR) / float64(row.GoNodes)
	}
	return row, nil
}

// programCompile is the program package's compile in a meter log.
func programCompile(log string) (int64, error) {
	f, err := os.Open(log)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		var st ToolStep
		if json.Unmarshal(s.Bytes(), &st) == nil && st.Tool == "compile" && (st.Pkg == "ergo.local/program" || st.Pkg == "main") {
			return st.Cost.Instructions, nil
		}
	}
	return 0, fmt.Errorf("%s: no compile of the program package", strings.TrimSpace(log))
}
