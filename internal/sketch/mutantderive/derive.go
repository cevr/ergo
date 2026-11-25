package mutantderive

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"ergo.local/prototype/internal/gobind"
	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/stdlib"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/sketchfront"
)

// Operator names a typed derived mutation operator.
type Operator string

const (
	OpCmpSwap          Operator = "cmp-swap"
	OpBoundOffByOne    Operator = "bound-off-by-one"
	OpDropStmt         Operator = "drop-stmt"
	OpMatchArmDefault  Operator = "match-arm-default"
	OpReturnZero       Operator = "return-zero"
	OpDropFails        Operator = "drop-fails"
	OpWeakenLawPremise Operator = "weaken-law-premise"
	OpFlipSomeNone     Operator = "flip-some-none"
)

// Mutant is one viable derived mutation on an Ergo source file.
type Mutant struct {
	ID       string
	File     string
	AbsFile  string
	Decl     string
	Operator Operator
	Line     int
	From     string
	To       string
	Source   string
}

// Options configures package-level derived mutant generation.
type Options struct {
	// BaseRoot makes Mutant.File relative to BaseRoot when non-empty.
	BaseRoot string
	// FilterFile restricts generation to a single absolute file path when non-empty.
	FilterFile string
	// LookupViable optionally returns a cached viability verdict (viable, found)
	// keyed by (pkgDigest, relFile, formattedSourceDigest).
	LookupViable func(key string) (viable bool, ok bool)
	// StoreViable optionally stores a viability verdict for key.
	StoreViable func(key string, viable bool)
}

type rawCandidate struct {
	decl     string
	op       Operator
	start    int
	end      int
	replace  string
	line     int
	fromText string
	toText   string
}

// GenerateSource derives all viable typed mutants for a single Ergo source file.
func GenerateSource(filename, source string) []Mutant {
	a := sketchfront.Check(source)
	root := a.Root()
	if root.Package == nil || len(root.Files) == 0 || root.Files[0].Syntax == nil {
		return nil
	}
	baseCodes := diagCounts(a)
	baseFmt := isFormatted(source)
	file := root.Files[0].Syntax
	cands := collectFileCandidates(root.Package, file)
	return finalizeCandidates(filename, "", source, cands, baseFmt, func(formatted string) bool {
		after := sketchfront.Check(formatted)
		return !introducesDiagnostics(baseCodes, after)
	})
}

// GeneratePackage derives all viable typed mutants for the root package of a
// checked folder Analysis. Every returned mutant is formatted and verified not
// to introduce any checker diagnostics.
func GeneratePackage(pkgDir string, platform gobind.Platform, a sketchfront.Analysis, opts Options) []Mutant {
	dir, pkg, files, ok := a.RootPackage()
	if !ok || pkg == nil || len(files) == 0 {
		return nil
	}
	if pkgDir == "" {
		pkgDir = dir
	}
	baseCodes := diagCounts(a)
	pkgDigest := packageFilesDigest(a, platform)
	filterClean := ""
	if opts.FilterFile != "" {
		filterClean = filepath.Clean(opts.FilterFile)
	}

	// Sort files deterministically by name.
	ordered := append([]*syntax.File(nil), files...)
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].Name < ordered[j].Name
	})

	var out []Mutant
	for _, f := range ordered {
		if f == nil || f.Src == "" {
			continue
		}
		absFile := filepath.Clean(filepath.Join(pkgDir, filepath.Base(f.Name)))
		if filterClean != "" && absFile != filterClean {
			continue
		}
		relFile := filepath.Base(f.Name)
		if opts.BaseRoot != "" {
			if r, err := filepath.Rel(opts.BaseRoot, absFile); err == nil && !strings.HasPrefix(r, "..") {
				relFile = filepath.ToSlash(r)
			}
		} else if f.Name != "" {
			relFile = filepath.ToSlash(f.Name)
		}

		baseFmt := isFormatted(f.Src)
		cands := collectFileCandidates(pkg, f)
		muts := finalizeCandidates(relFile, absFile, f.Src, cands, baseFmt, func(formatted string) bool {
			var cacheKey string
			if opts.LookupViable != nil || opts.StoreViable != nil {
				cacheKey = viabilityCacheKey(pkgDigest, relFile, formatted)
			}
			if opts.LookupViable != nil {
				if viable, hit := opts.LookupViable(cacheKey); hit {
					return viable
				}
			}
			var viable bool
			if diags, fastOK := a.CheckRootOverlay(absFile, formatted); fastOK {
				viable = !introducesDiagnosticList(baseCodes, diags)
			} else {
				overlay := sketchfront.OverlayFS(map[string]string{absFile: formatted})
				after, err := sketchfront.CheckDirFS(overlay, pkgDir, platform)
				viable = err == nil && !introducesDiagnostics(baseCodes, after)
			}
			if opts.StoreViable != nil {
				opts.StoreViable(cacheKey, viable)
			}
			return viable
		})
		out = append(out, muts...)
	}
	return out
}

func finalizeCandidates(relFile, absFile, origSrc string, cands []rawCandidate, baseFmt bool, isViable func(formatted string) bool) []Mutant {
	if len(cands) == 0 {
		return nil
	}
	type preparedCand struct {
		c         rawCandidate
		formatted string
	}
	origNorm := syntax.CheckText(origSrc)
	seenNorm := map[string]bool{origNorm: true}
	var prepared []preparedCand
	for _, c := range cands {
		if c.start < 0 || c.end < c.start || c.end > len(origSrc) {
			continue
		}
		raw := origSrc[:c.start] + c.replace + origSrc[c.end:]
		res, err := sketchfront.Format(raw, 0)
		if err != nil {
			continue
		}
		formatted := res.Text
		if baseFmt {
			// Verify canonical format fixpoint (FK7a).
			if checkRes, err := sketchfront.Format(formatted, 0); err != nil || checkRes.Changed {
				continue
			}
		}
		norm := syntax.CheckText(formatted)
		if seenNorm[norm] {
			continue
		}
		seenNorm[norm] = true
		prepared = append(prepared, preparedCand{c: c, formatted: formatted})
	}
	if len(prepared) == 0 {
		return nil
	}
	okFlags := make([]bool, len(prepared))
	workers := runtime.GOMAXPROCS(0)
	if workers < 1 {
		workers = 1
	}
	if workers > len(prepared) {
		workers = len(prepared)
	}
	if workers == 1 {
		for i, p := range prepared {
			okFlags[i] = isViable(p.formatted)
		}
	} else {
		var next atomic.Int64
		var wg sync.WaitGroup
		wg.Add(workers)
		for w := 0; w < workers; w++ {
			go func() {
				defer wg.Done()
				for {
					i := int(next.Add(1) - 1)
					if i >= len(prepared) {
						return
					}
					okFlags[i] = isViable(prepared[i].formatted)
				}
			}()
		}
		wg.Wait()
	}

	seqByKey := map[string]int{}
	var out []Mutant
	for i, p := range prepared {
		if !okFlags[i] {
			continue
		}
		c := p.c
		key := fmt.Sprintf("%s:%s:%s:L%d", relFile, c.decl, c.op, c.line)
		seqByKey[key]++
		id := fmt.Sprintf("derived:%s:%d", key, seqByKey[key])
		out = append(out, Mutant{
			ID:       id,
			File:     relFile,
			AbsFile:  absFile,
			Decl:     c.decl,
			Operator: c.op,
			Line:     c.line,
			From:     c.fromText,
			To:       c.toText,
			Source:   p.formatted,
		})
	}
	return out
}

func collectFileCandidates(pkg *check.Package, file *syntax.File) []rawCandidate {
	isTestFile := strings.HasSuffix(filepath.Base(file.Name), "_test.ergo")
	var out []rawCandidate
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *syntax.FuncDecl:
			if isTestFile || d.Body == nil || d.Extern != nil || d.Derived != nil || d.Name == nil {
				continue
			}
			declName := d.Name.Name
			out = append(out, collectFuncCandidates(pkg, file, d, declName)...)
		case *syntax.LawDecl:
			if d.Derived != nil || d.Pair != nil || d.Name == nil {
				continue
			}
			out = append(out, collectLawCandidates(file, d, d.Name.Name)...)
		case *syntax.TypeDecl:
			if d.Name == nil || d.Origin != nil {
				continue
			}
			switch spec := d.Spec.(type) {
			case *syntax.InterfaceType:
				for _, law := range spec.Laws {
					if law.Derived != nil || law.Pair != nil || law.Name == nil {
						continue
					}
					out = append(out, collectLawCandidates(file, law, d.Name.Name+"."+law.Name.Name)...)
				}
			case *syntax.MachineType:
				for _, law := range spec.Laws {
					if law.Derived != nil || law.Pair != nil || law.Name == nil {
						continue
					}
					out = append(out, collectLawCandidates(file, law, d.Name.Name+"."+law.Name.Name)...)
				}
			}
		}
	}
	return out
}

func collectFuncCandidates(pkg *check.Package, file *syntax.File, fn *syntax.FuncDecl, declName string) []rawCandidate {
	var out []rawCandidate
	src := file.Src

	// 1. return-zero on the function body.
	out = append(out, collectReturnZero(pkg, file, fn, declName)...)

	// 2. drop-fails on the function signature's error row.
	out = append(out, collectDropFails(pkg, file, fn, declName)...)

	// 3. Walk statements and expressions inside fn.Body.
	syntax.Inspect(fn.Body, func(n syntax.Node) bool {
		if n == nil {
			return false
		}
		switch node := n.(type) {
		case *syntax.BinaryExpr:
			if swapped, ok := swapCmpOp(node.Op); ok && node.OpPos.IsValid() {
				if isBoolType(pkg.Info.Types[node]) {
					opStr := node.Op.String()
					start := int(node.OpPos)
					end := start + len(opStr)
					if start >= 0 && end <= len(src) && src[start:end] == opStr {
						out = append(out, rawCandidate{
							decl:     declName,
							op:       OpCmpSwap,
							start:    start,
							end:      end,
							replace:  swapped,
							line:     file.Line(node.OpPos),
							fromText: opStr,
							toText:   swapped,
						})
					}
				}
			} else if node.Op == syntax.DOTDOT && node.Y != nil {
				if t := pkg.Info.Types[node.Y]; isIntType(t) {
					if c, ok := offByOneExpr(file, declName, node.Y, t, -1); ok {
						out = append(out, c)
					}
				}
			}

		case *syntax.SliceExpr:
			if node.High != nil {
				if t := pkg.Info.Types[node.High]; isIntType(t) {
					if c, ok := offByOneExpr(file, declName, node.High, t, -1); ok {
						out = append(out, c)
					}
				}
			}
			if node.Low != nil {
				if t := pkg.Info.Types[node.Low]; isIntType(t) {
					if c, ok := offByOneExpr(file, declName, node.Low, t, +1); ok {
						out = append(out, c)
					}
				}
			}

		case *syntax.BlockStmt:
			out = append(out, collectDropStmts(pkg, file, declName, node.List)...)

		case *syntax.CaseClause:
			out = append(out, collectDropStmts(pkg, file, declName, node.Body)...)

		case *syntax.CatchArm:
			out = append(out, collectDropStmts(pkg, file, declName, node.Body)...)

		case *syntax.SwitchStmt:
			out = append(out, collectMatchArmDefaults(pkg, file, declName, node, node.Clauses)...)

		case *syntax.TypeSwitchStmt:
			out = append(out, collectMatchArmDefaults(pkg, file, declName, node, node.Clauses)...)

		case *syntax.CompositeLit:
			if c, ok := collectFlipSomeNone(pkg, file, declName, node); ok {
				out = append(out, c)
			}
		}
		return true
	})

	return out
}

func swapCmpOp(op syntax.Token) (string, bool) {
	switch op {
	case syntax.LSS:
		return "<=", true
	case syntax.LEQ:
		return "<", true
	case syntax.GTR:
		return ">=", true
	case syntax.GEQ:
		return ">", true
	case syntax.EQL:
		return "!=", true
	case syntax.NEQ:
		return "==", true
	}
	return "", false
}

func offByOneExpr(file *syntax.File, declName string, expr syntax.Expr, typ check.Type, delta int) (rawCandidate, bool) {
	start, end := int(expr.Pos()), int(expr.End())
	if start < 0 || end <= start || end > len(file.Src) {
		return rawCandidate{}, false
	}
	orig := file.Src[start:end]
	var repl string
	if lit, ok := expr.(*syntax.BasicLit); ok && lit.Kind == syntax.INT {
		if v, err := strconv.ParseInt(lit.Value, 0, 64); err == nil {
			nv := v + int64(delta)
			if nv < 0 && isUnsignedIntType(typ) {
				nv = v + 1
			}
			repl = strconv.FormatInt(nv, 10)
		}
	}
	if repl == "" {
		if delta < 0 {
			repl = "(" + orig + ") - 1"
		} else {
			repl = "(" + orig + ") + 1"
		}
	}
	return rawCandidate{
		decl:     declName,
		op:       OpBoundOffByOne,
		start:    start,
		end:      end,
		replace:  repl,
		line:     file.Line(expr.Pos()),
		fromText: orig,
		toText:   repl,
	}, true
}

func collectDropStmts(pkg *check.Package, file *syntax.File, declName string, stmts []syntax.Stmt) []rawCandidate {
	var out []rawCandidate
	for _, st := range stmts {
		if st == nil {
			continue
		}
		// Never drop the block's value-producing tail expression.
		if pkg.Info.Tails[st] != nil {
			continue
		}
		switch st.(type) {
		case *syntax.AssignStmt, *syntax.ExprStmt:
			start, end := int(st.Pos()), int(st.End())
			if start < 0 || end <= start || end > len(file.Src) {
				continue
			}
			orig := file.Src[start:end]
			out = append(out, rawCandidate{
				decl:     declName,
				op:       OpDropStmt,
				start:    start,
				end:      end,
				replace:  "",
				line:     file.Line(st.Pos()),
				fromText: orig,
				toText:   "",
			})
		}
	}
	return out
}

func collectMatchArmDefaults(pkg *check.Package, file *syntax.File, declName string, matchExpr syntax.Expr, clauses []*syntax.CaseClause) []rawCandidate {
	if len(clauses) < 2 {
		return nil
	}
	var t check.Type
	if m := pkg.Info.Matches[matchExpr]; m != nil && m.Result != nil {
		t = m.Result
	} else {
		t = pkg.Info.Types[matchExpr]
	}
	isValue := t != nil && t != check.NoValue
	var zeroExpr string
	if isValue {
		var ok bool
		zeroExpr, ok = zeroExprForType(t, pkg)
		if !ok {
			return nil
		}
	}

	var out []rawCandidate
	for _, cl := range clauses {
		if len(cl.Body) == 0 {
			continue
		}
		if endsWithReturnOrUnreachable(cl.Body) {
			continue
		}
		start := int(cl.Body[0].Pos())
		end := int(cl.Body[len(cl.Body)-1].End())
		if start < 0 || end <= start || end > len(file.Src) {
			continue
		}
		orig := file.Src[start:end]
		if !isValue {
			// Statement match arm: replace non-returning body with discarded pattern bindings (if any) or no-op.
			bindings := clausePatternBindings(pkg, cl)
			if len(bindings) == 0 {
				continue
			}
			var lines []string
			for _, name := range bindings {
				lines = append(lines, "let _ = "+name)
			}
			repl := strings.Join(lines, "\n")
			if syntax.CheckText(orig) == syntax.CheckText(repl) {
				continue
			}
			out = append(out, rawCandidate{
				decl:     declName,
				op:       OpMatchArmDefault,
				start:    start,
				end:      end,
				replace:  repl,
				line:     file.Line(cl.Case),
				fromText: orig,
				toText:   repl,
			})
			continue
		}

		// Expression match arm: replace arm body with zero/default value of match type,
		// discarding any pattern-bound names so unused-binding S110 doesn't reject it.
		bindings := clausePatternBindings(pkg, cl)
		var lines []string
		for _, name := range bindings {
			lines = append(lines, "let _ = "+name)
		}
		lines = append(lines, zeroExpr)
		repl := strings.Join(lines, "\n")
		if syntax.CheckText(orig) == syntax.CheckText(repl) {
			continue
		}
		out = append(out, rawCandidate{
			decl:     declName,
			op:       OpMatchArmDefault,
			start:    start,
			end:      end,
			replace:  repl,
			line:     file.Line(cl.Case),
			fromText: orig,
			toText:   repl,
		})
	}
	return out
}

func clausePatternBindings(pkg *check.Package, cl *syntax.CaseClause) []string {
	var names []string
	seen := map[string]bool{}
	for _, pat := range cl.List {
		syntax.Inspect(pat, func(n syntax.Node) bool {
			id, ok := n.(*syntax.Ident)
			if !ok || id.Name == "_" || seen[id.Name] {
				return true
			}
			if obj := pkg.Info.Defs[id]; obj != nil {
				if _, isVar := obj.(*check.Var); isVar {
					seen[id.Name] = true
					names = append(names, id.Name)
				}
			}
			return true
		})
	}
	return names
}

func endsWithReturnOrUnreachable(stmts []syntax.Stmt) bool {
	if len(stmts) == 0 {
		return false
	}
	last := stmts[len(stmts)-1]
	if _, ok := last.(*syntax.ReturnStmt); ok {
		return true
	}
	if es, ok := last.(*syntax.ExprStmt); ok {
		if call, ok := es.X.(*syntax.CallExpr); ok {
			if id, ok := call.Fun.(*syntax.Ident); ok && id.Name == "Unreachable" {
				return true
			}
		}
	}
	return false
}

func collectReturnZero(pkg *check.Package, file *syntax.File, fn *syntax.FuncDecl, declName string) []rawCandidate {
	if fn.Body == nil || len(fn.Body.List) == 0 {
		return nil
	}
	obj, ok := pkg.Info.Defs[fn.Name].(*check.Func)
	if !ok || obj == nil || obj.Sig == nil {
		return nil
	}
	sig := obj.Sig
	start, end := int(fn.Body.Lbrace), int(fn.Body.Rbrace)+1
	if start < 0 || end <= start || end > len(file.Src) {
		return nil
	}
	orig := file.Src[start:end]

	var repl string
	switch len(sig.Results) {
	case 0:
		repl = "{}"
	case 1:
		z, ok := zeroExprForType(sig.Results[0], pkg)
		if !ok {
			return nil
		}
		repl = "{\n\t" + z + "\n}"
	default:
		return nil
	}
	if syntax.CheckText(orig) == syntax.CheckText(repl) {
		return nil
	}
	// Also skip if body is just `return <zero>`.
	if len(sig.Results) == 1 {
		z, _ := zeroExprForType(sig.Results[0], pkg)
		if syntax.CheckText(orig) == syntax.CheckText("{\n\treturn "+z+"\n}") {
			return nil
		}
	}
	return []rawCandidate{{
		decl:     declName,
		op:       OpReturnZero,
		start:    start,
		end:      end,
		replace:  repl,
		line:     file.Line(fn.Body.Lbrace),
		fromText: orig,
		toText:   repl,
	}}
}

func collectDropFails(pkg *check.Package, file *syntax.File, fn *syntax.FuncDecl, declName string) []rawCandidate {
	if fn.Type == nil || fn.Type.Results == nil {
		return nil
	}
	var errRow *syntax.ErrorRow
	for _, f := range fn.Type.Results.List {
		if er, ok := f.Type.(*syntax.ErrorRow); ok {
			errRow = er
			break
		}
	}
	if errRow == nil || len(errRow.Types) == 0 {
		return nil
	}
	// Fast pre-check: collect failure types directly returned as error values in fn.Body.
	returnedErrTypes := map[check.Type]bool{}
	syntax.Inspect(fn.Body, func(n syntax.Node) bool {
		if expr, ok := n.(syntax.Expr); ok {
			if isSuccess, ok := pkg.Info.ReturnedValues[expr]; ok && !isSuccess {
				if t := pkg.Info.Types[expr]; t != nil {
					returnedErrTypes[t] = true
				}
			}
		}
		return true
	})

	var out []rawCandidate
	for i, te := range errRow.Types {
		if t := pkg.Info.Types[te]; t != nil && returnedErrTypes[t] {
			// Body definitely returns this error; dropping it cannot type-check.
			continue
		}
		var start, end int
		if len(errRow.Types) == 1 {
			start = int(errRow.Pos())
			end = int(errRow.End())
		} else if i < len(errRow.Types)-1 {
			start = int(errRow.Types[i].Pos())
			end = int(errRow.Types[i+1].Pos())
		} else {
			start = int(errRow.Types[i-1].End())
			end = int(errRow.Types[i].End())
		}
		if start < 0 || end <= start || end > len(file.Src) {
			continue
		}
		out = append(out, rawCandidate{
			decl:     declName,
			op:       OpDropFails,
			start:    start,
			end:      end,
			replace:  "",
			line:     file.Line(te.Pos()),
			fromText: file.Src[start:end],
			toText:   "",
		})
	}
	return out
}

func collectFlipSomeNone(pkg *check.Package, file *syntax.File, declName string, lit *syntax.CompositeLit) (rawCandidate, bool) {
	t := pkg.Info.Types[lit]
	if !isSomeType(t) {
		return rawCandidate{}, false
	}
	// Only flip when the enclosing context expects Option[T] (proven by the
	// checker's implicit widening conversion), so `None` never leaves an
	// unconstrained type parameter or changes a `Some[T]`-typed variable.
	conv, ok := pkg.Info.Implicit[lit]
	if !ok || !isOptionType(conv.To) {
		return rawCandidate{}, false
	}
	start, end := int(lit.Pos()), int(lit.End())
	if start < 0 || end <= start || end > len(file.Src) {
		return rawCandidate{}, false
	}
	return rawCandidate{
		decl:     declName,
		op:       OpFlipSomeNone,
		start:    start,
		end:      end,
		replace:  "None",
		line:     file.Line(lit.Pos()),
		fromText: file.Src[start:end],
		toText:   "None",
	}, true
}

func collectLawCandidates(file *syntax.File, law *syntax.LawDecl, declName string) []rawCandidate {
	var out []rawCandidate
	// 1. Parameter `where` premises: drop one conjunct of `a && b`, or drop the whole `where P`.
	if law.Params != nil {
		for _, field := range law.Params.List {
			if field.Where == nil {
				continue
			}
			out = append(out, weakenConjuncts(file, declName, field.Where, syntax.LAND)...)
			if field.WherePos.IsValid() {
				start, end := int(field.WherePos), int(field.Where.End())
				if start >= 0 && end > start && end <= len(file.Src) {
					out = append(out, rawCandidate{
						decl:     declName,
						op:       OpWeakenLawPremise,
						start:    start,
						end:      end,
						replace:  "",
						line:     file.Line(field.WherePos),
						fromText: file.Src[start:end],
						toText:   "",
					})
				}
			}
		}
	}
	// 2. Rewrite law `when` guard.
	if guard := law.RewriteGuard(); guard != nil {
		out = append(out, weakenConjuncts(file, declName, guard, syntax.LAND)...)
		if law.Rewrite != nil && law.Rewrite.When.IsValid() {
			start, end := int(law.Rewrite.When), int(guard.End())
			if start >= 0 && end > start && end <= len(file.Src) {
				out = append(out, rawCandidate{
					decl:     declName,
					op:       OpWeakenLawPremise,
					start:    start,
					end:      end,
					replace:  "",
					line:     file.Line(law.Rewrite.When),
					fromText: file.Src[start:end],
					toText:   "",
				})
			}
		}
	}
	// 3. Top-level premise guards in a law body:
	//    `if a && b { ... }` (premise guarding assertions -> drop conjunct)
	//    `if a || b { return ... }` / `if a || b { skip ... }` (bailout guard -> drop disjunct)
	if law.Body != nil {
		for _, st := range law.Body.List {
			ifs, ok := st.(*syntax.IfStmt)
			if !ok || ifs.Cond == nil {
				continue
			}
			if isEarlyExitBlock(ifs.Body) && ifs.Else == nil {
				out = append(out, weakenConjuncts(file, declName, ifs.Cond, syntax.LOR)...)
			} else {
				out = append(out, weakenConjuncts(file, declName, ifs.Cond, syntax.LAND)...)
			}
		}
	}
	return out
}

func weakenConjuncts(file *syntax.File, declName string, expr syntax.Expr, op syntax.Token) []rawCandidate {
	parts := splitLogicalChain(expr, op)
	if len(parts) < 2 {
		return nil
	}
	start, end := int(expr.Pos()), int(expr.End())
	if start < 0 || end <= start || end > len(file.Src) {
		return nil
	}
	orig := file.Src[start:end]
	joiner := " " + op.String() + " "
	var out []rawCandidate
	for dropIdx := range parts {
		kept := make([]string, 0, len(parts)-1)
		for i, p := range parts {
			if i == dropIdx {
				continue
			}
			ps, pe := int(p.Pos()), int(p.End())
			if ps < 0 || pe <= ps || pe > len(file.Src) {
				kept = nil
				break
			}
			kept = append(kept, file.Src[ps:pe])
		}
		if len(kept) == 0 {
			continue
		}
		repl := strings.Join(kept, joiner)
		out = append(out, rawCandidate{
			decl:     declName,
			op:       OpWeakenLawPremise,
			start:    start,
			end:      end,
			replace:  repl,
			line:     file.Line(parts[dropIdx].Pos()),
			fromText: orig,
			toText:   repl,
		})
	}
	return out
}

func splitLogicalChain(e syntax.Expr, op syntax.Token) []syntax.Expr {
	if p, ok := e.(*syntax.ParenExpr); ok {
		return splitLogicalChain(p.X, op)
	}
	b, ok := e.(*syntax.BinaryExpr)
	if !ok || b.Op != op {
		return []syntax.Expr{e}
	}
	return append(splitLogicalChain(b.X, op), splitLogicalChain(b.Y, op)...)
}

func isEarlyExitBlock(b *syntax.BlockStmt) bool {
	if b == nil || len(b.List) == 0 {
		return false
	}
	last := b.List[len(b.List)-1]
	switch last.(type) {
	case *syntax.ReturnStmt, *syntax.SkipStmt:
		return true
	}
	return false
}

func zeroExprForType(t check.Type, pkg *check.Package) (string, bool) {
	if t == nil {
		return "", false
	}
	switch typ := t.(type) {
	case *check.Basic:
		return basicZeroExpr(typ.Kind)
	case *check.Named:
		if typ.Obj == nil {
			return "", false
		}
		// Prelude Option[T] -> None.
		if isPreludeObj(typ.Obj, "Option") {
			return "None", true
		}
		// Same-package non-generic named types.
		if typ.Obj.Pkg == pkg && len(typ.TArgs) == 0 {
			u := underlying(typ)
			switch under := u.(type) {
			case *check.Struct:
				if len(under.Fields) == 0 {
					return typ.Obj.Name, true
				}
			case *check.Basic:
				if z, ok := basicZeroExpr(under.Kind); ok {
					return typ.Obj.Name + "(" + z + ")", true
				}
			case *check.Union:
				if z, ok := unionZeroExpr(under, pkg); ok {
					return z, true
				}
			}
		}
		return "", false
	case *check.Slice:
		if elemName, ok := printableTypeInPkg(typ.Elem, pkg); ok {
			return "[]" + elemName + "{}", true
		}
		return "", false
	case *check.Union:
		return unionZeroExpr(typ, pkg)
	}
	return "", false
}

func unionZeroExpr(u *check.Union, pkg *check.Package) (string, bool) {
	for _, v := range u.Variants {
		n, ok := v.(*check.Named)
		if !ok || n.Obj == nil || len(n.TArgs) != 0 {
			continue
		}
		if isPreludeObj(n.Obj, "None") {
			return "None", true
		}
		if n.Obj.Pkg == pkg {
			if st, ok := underlying(n).(*check.Struct); ok && len(st.Fields) == 0 {
				return n.Obj.Name, true
			}
		}
	}
	return "", false
}

func underlying(t check.Type) check.Type {
	if n, ok := t.(*check.Named); ok {
		return n.Underlying()
	}
	return t
}

func printableTypeInPkg(t check.Type, pkg *check.Package) (string, bool) {
	switch typ := t.(type) {
	case *check.Basic:
		if typ.Kind == check.Invalid || typ.Kind == check.Complex64 || typ.Kind == check.Complex128 {
			return "", false
		}
		return typ.Name, true
	case *check.Named:
		if typ.Obj == nil || len(typ.TArgs) != 0 {
			return "", false
		}
		if typ.Obj.Pkg == nil || typ.Obj.Pkg == pkg {
			return typ.Obj.Name, true
		}
	}
	return "", false
}

func basicZeroExpr(k check.BasicKind) (string, bool) {
	switch k {
	case check.Bool:
		return "false", true
	case check.String:
		return `""`, true
	case check.Int, check.Int8, check.Int16, check.Int32, check.Int64,
		check.Uint, check.Uint8, check.Uint16, check.Uint32, check.Uint64, check.Uintptr:
		return "0", true
	case check.Float32, check.Float64:
		return "0.0", true
	case check.Duration:
		return "0ns", true
	}
	return "", false
}

func isBoolType(t check.Type) bool {
	if t == nil {
		return false
	}
	b, ok := underlying(t).(*check.Basic)
	return ok && b.Kind == check.Bool
}

func isIntType(t check.Type) bool {
	if t == nil {
		return false
	}
	b, ok := underlying(t).(*check.Basic)
	if !ok {
		return false
	}
	switch b.Kind {
	case check.Int, check.Int8, check.Int16, check.Int32, check.Int64,
		check.Uint, check.Uint8, check.Uint16, check.Uint32, check.Uint64, check.Uintptr:
		return true
	}
	return false
}

func isUnsignedIntType(t check.Type) bool {
	if t == nil {
		return false
	}
	b, ok := underlying(t).(*check.Basic)
	if !ok {
		return false
	}
	switch b.Kind {
	case check.Uint, check.Uint8, check.Uint16, check.Uint32, check.Uint64, check.Uintptr:
		return true
	}
	return false
}

func isPreludeObj(obj *check.TypeName, name string) bool {
	if obj == nil || obj.Name != name {
		return false
	}
	return obj.Pkg == nil || obj.Pkg.Path == stdlib.PreludePath
}

func isSomeType(t check.Type) bool {
	n, ok := t.(*check.Named)
	return ok && isPreludeObj(n.Obj, "Some")
}

func isOptionType(t check.Type) bool {
	n, ok := t.(*check.Named)
	return ok && isPreludeObj(n.Obj, "Option")
}

func diagCounts(a sketchfront.Analysis) map[string]int {
	return diagListCounts(a.Diagnostics)
}

func diagListCounts(diags []report.Diagnostic) map[string]int {
	m := map[string]int{}
	for _, d := range diags {
		m[d.Code]++
	}
	return m
}

func introducesDiagnostics(baseCodes map[string]int, after sketchfront.Analysis) bool {
	return introducesDiagnosticList(baseCodes, after.Diagnostics)
}

func introducesDiagnosticList(baseCodes map[string]int, diags []report.Diagnostic) bool {
	afterCodes := diagListCounts(diags)
	for code, count := range afterCodes {
		if count > baseCodes[code] {
			return true
		}
	}
	return false
}

func isFormatted(src string) bool {
	res, err := sketchfront.Format(src, 0)
	return err == nil && !res.Changed
}

func packageFilesDigest(a sketchfront.Analysis, platform gobind.Platform) string {
	h := sha256.New()
	fmt.Fprintf(h, "v1\x00%s\x00%s\x00", platform.GOOS, platform.GOARCH)
	files := a.Files()
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(h, "%s\x00%d\x00%s\x00", name, len(files[name]), files[name])
	}
	return hex.EncodeToString(h.Sum(nil))
}

func viabilityCacheKey(pkgDigest, relFile, formatted string) string {
	h := sha256.New()
	fmt.Fprintf(h, "mutant-viable-v1\x00%s\x00%s\x00%s", pkgDigest, relFile, formatted)
	return "mv:" + hex.EncodeToString(h.Sum(nil))
}
