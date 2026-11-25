package lint

import (
	"slices"
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Flat columnar SoA file index and zero-closure AST walker (LINT-SOA-1).
//
// Each file is walked once per lint run (lazily on first rule demand) to
// populate dense typed pointer columns in exact pre-order DFS order. Rules
// iterate only the flat column(s) of the node kinds they inspect instead of
// running repeated full-file syntax.Inspect traversals.

const (
	nodeInTestLaw  uint8 = 1 << iota // inside a top-level TestDecl or LawDecl
	nodeInTypeDecl                   // inside a top-level TypeDecl
	nodeInFuncBody                   // inside a top-level FuncDecl body
)

type declSpan struct {
	identStart, identEnd       int32
	callStart, callEnd         int32
	declStmtStart, declStmtEnd int32
	indexStart, indexEnd       int32
}

// fileIndex holds flat columnar node arrays and precomputed line/comment
// indexes for one parsed file, built once per lint run.
type fileIndex struct {
	file *syntax.File

	// Per-top-level-declaration slice bounds into the flat columns below.
	decls []declSpan

	// Flat typed columns in exact syntax.Inspect pre-order DFS order.
	idents         []*syntax.Ident
	calls          []*syntax.CallExpr
	callFlags      []uint8
	assigns        []*syntax.AssignStmt
	assignLoops    []syntax.Stmt // innermost enclosing loop inside a FuncDecl/TestDecl/LawDecl body (nil outside)
	assignInBody   []bool        // true when inside a FuncDecl/TestDecl/LawDecl body
	lits           []*syntax.CompositeLit
	binaries       []*syntax.BinaryExpr
	binaryFlags    []uint8
	selectors      []*syntax.SelectorExpr
	fields         []*syntax.Field
	declStmts      []*syntax.DeclStmt
	switches       []syntax.Node // *syntax.SwitchStmt, *syntax.TypeSwitchStmt, *syntax.CatchExpr
	catches        []*syntax.CatchExpr
	catchFlags     []uint8
	ranges         []*syntax.RangeStmt
	blocks         []*syntax.BlockStmt
	blockFlags     []uint8
	indices        []*syntax.IndexExpr
	funcLits       []*syntax.FuncLit
	contextEntries []*syntax.ContextEntry

	// Comment & body metadata built in the same single pass.
	docComments map[*syntax.CommentGroup]bool
	bodySpans   [][2]syntax.Pos // sorted, merged open-interval body spans

	// Flat line-start offset table, built lazily on first line query.
	lineStarts []int32
}

// fileIndex returns the cached columnar index for (unit, index, file),
// building it on first use.
func (s *runShared) fileIndex(unit, index int, f *syntax.File) *fileIndex {
	if f == nil {
		return nil
	}
	if s.unitFiles == nil {
		s.unitFiles = make([][]*fileIndex, len(s.in.Units))
	}
	if unit >= 0 && unit < len(s.unitFiles) {
		row := s.unitFiles[unit]
		if row == nil {
			row = make([]*fileIndex, len(s.in.Units[unit].Files))
			s.unitFiles[unit] = row
		}
		if index >= 0 && index < len(row) {
			if fi := row[index]; fi != nil && fi.file == f {
				return fi
			}
			fi := buildFileIndex(f)
			row[index] = fi
			return fi
		}
	}
	if s.byFile == nil {
		s.byFile = map[*syntax.File]*fileIndex{}
	}
	if fi := s.byFile[f]; fi != nil {
		return fi
	}
	fi := buildFileIndex(f)
	s.byFile[f] = fi
	return fi
}

// fileIndexOf returns the cached columnar index for an arbitrary syntax.File
// belonging to the run.
func (s *runShared) fileIndexOf(f *syntax.File) *fileIndex {
	if f == nil {
		return nil
	}
	if s.byFile != nil {
		if fi := s.byFile[f]; fi != nil {
			return fi
		}
	}
	for u := range s.in.Units {
		for i := range s.in.Units[u].Files {
			if s.in.Units[u].Files[i].Syntax == f {
				return s.fileIndex(u, i, f)
			}
		}
	}
	if s.byFile == nil {
		s.byFile = map[*syntax.File]*fileIndex{}
	}
	fi := buildFileIndex(f)
	s.byFile[f] = fi
	return fi
}

// buildFileIndex walks f once with zero closures and populates all flat
// columnar slices in exact syntax.Inspect pre-order DFS order.
func buildFileIndex(f *syntax.File) *fileIndex {
	est := len(f.Src) / 40
	fi := &fileIndex{
		file:         f,
		idents:       make([]*syntax.Ident, 0, est),
		calls:        make([]*syntax.CallExpr, 0, est/4),
		callFlags:    make([]uint8, 0, est/4),
		assigns:      make([]*syntax.AssignStmt, 0, est/16),
		assignLoops:  make([]syntax.Stmt, 0, est/16),
		assignInBody: make([]bool, 0, est/16),
		binaries:     make([]*syntax.BinaryExpr, 0, est/8),
		binaryFlags:  make([]uint8, 0, est/8),
		selectors:    make([]*syntax.SelectorExpr, 0, est/4),
		fields:       make([]*syntax.Field, 0, est/8),
		blocks:       make([]*syntax.BlockStmt, 0, est/16),
		blockFlags:   make([]uint8, 0, est/16),
		lits:         make([]*syntax.CompositeLit, 0, est/16),
	}
	if len(f.Comments) > 0 {
		fi.docComments = make(map[*syntax.CommentGroup]bool, len(f.Decls)+4)
	}
	var rawBodies [][2]syntax.Pos
	fi.indexFile(f, &rawBodies)
	fi.bodySpans = mergeBodySpans(rawBodies)
	return fi
}

func (fi *fileIndex) recordDoc(g *syntax.CommentGroup) {
	if g != nil && fi.docComments != nil {
		fi.docComments[g] = true
	}
}

func (fi *fileIndex) indexFile(f *syntax.File, bodies *[][2]syntax.Pos) {
	if len(f.Decls) > 0 {
		fi.decls = make([]declSpan, len(f.Decls))
	}
	for i, d := range f.Decls {
		flags := uint8(0)
		switch d.(type) {
		case *syntax.TestDecl, *syntax.LawDecl:
			flags = nodeInTestLaw
		case *syntax.TypeDecl:
			flags = nodeInTypeDecl
		}
		iStart := int32(len(fi.idents))
		cStart := int32(len(fi.calls))
		dStart := int32(len(fi.declStmts))
		xStart := int32(len(fi.indices))
		fi.visit(d, flags, nil, false, bodies)
		fi.decls[i] = declSpan{
			identStart:    iStart,
			identEnd:      int32(len(fi.idents)),
			callStart:     cStart,
			callEnd:       int32(len(fi.calls)),
			declStmtStart: dStart,
			declStmtEnd:   int32(len(fi.declStmts)),
			indexStart:    xStart,
			indexEnd:      int32(len(fi.indices)),
		}
	}
}

func (fi *fileIndex) visit(n syntax.Node, flags uint8, loop syntax.Stmt, inBody bool, bodies *[][2]syntax.Pos) {
	if n == nil {
		return
	}
	switch x := n.(type) {
	case *syntax.Ident:
		if x == nil {
			return
		}
		fi.idents = append(fi.idents, x)
	case *syntax.BasicLit:
		return
	case *syntax.CallExpr:
		if x == nil {
			return
		}
		fi.calls = append(fi.calls, x)
		fi.callFlags = append(fi.callFlags, flags)
		fi.visit(x.Fun, flags, loop, inBody, bodies)
		for _, a := range x.Args {
			fi.visit(a, flags, loop, inBody, bodies)
		}
		if x.Trailing != nil {
			fi.visit(x.Trailing, flags, loop, inBody, bodies)
		}
	case *syntax.SelectorExpr:
		if x == nil {
			return
		}
		fi.selectors = append(fi.selectors, x)
		fi.visit(x.X, flags, loop, inBody, bodies)
		if x.Sel != nil {
			fi.idents = append(fi.idents, x.Sel)
		}
	case *syntax.BinaryExpr:
		if x == nil {
			return
		}
		fi.binaries = append(fi.binaries, x)
		fi.binaryFlags = append(fi.binaryFlags, flags)
		fi.visit(x.X, flags, loop, inBody, bodies)
		fi.visit(x.Y, flags, loop, inBody, bodies)
	case *syntax.UnaryExpr:
		if x == nil {
			return
		}
		fi.visit(x.X, flags, loop, inBody, bodies)
	case *syntax.ParenExpr:
		if x == nil {
			return
		}
		fi.visit(x.X, flags, loop, inBody, bodies)
	case *syntax.StarExpr:
		if x == nil {
			return
		}
		fi.visit(x.X, flags, loop, inBody, bodies)
	case *syntax.TryExpr:
		if x == nil {
			return
		}
		fi.visit(x.X, flags, loop, inBody, bodies)
	case *syntax.IndexExpr:
		if x == nil {
			return
		}
		fi.indices = append(fi.indices, x)
		fi.visit(x.X, flags, loop, inBody, bodies)
		for _, idx := range x.Indices {
			fi.visit(idx, flags, loop, inBody, bodies)
		}
	case *syntax.SliceExpr:
		if x == nil {
			return
		}
		fi.visit(x.X, flags, loop, inBody, bodies)
		fi.visit(x.Low, flags, loop, inBody, bodies)
		fi.visit(x.High, flags, loop, inBody, bodies)
		fi.visit(x.Max, flags, loop, inBody, bodies)
	case *syntax.CompositeLit:
		if x == nil {
			return
		}
		fi.lits = append(fi.lits, x)
		fi.visit(x.Type, flags, loop, inBody, bodies)
		fi.visit(x.Base, flags, loop, inBody, bodies)
		for _, e := range x.Elts {
			fi.visit(e, flags, loop, inBody, bodies)
		}
	case *syntax.KeyValueExpr:
		if x == nil {
			return
		}
		fi.visit(x.Key, flags, loop, inBody, bodies)
		fi.visit(x.Value, flags, loop, inBody, bodies)
	case *syntax.FuncLit:
		if x == nil {
			return
		}
		fi.funcLits = append(fi.funcLits, x)
		if x.Body != nil {
			*bodies = append(*bodies, [2]syntax.Pos{x.Body.Pos(), x.Body.End()})
		}
		if x.Type != nil {
			fi.visit(x.Type, flags, nil, inBody, bodies)
		}
		if x.Body != nil {
			fi.visit(x.Body, flags, nil, inBody, bodies)
		}
	case *syntax.CatchExpr:
		if x == nil {
			return
		}
		fi.switches = append(fi.switches, x)
		fi.catches = append(fi.catches, x)
		fi.catchFlags = append(fi.catchFlags, flags)
		fi.visit(x.X, flags, loop, inBody, bodies)
		for _, a := range x.Arms {
			fi.visit(a, flags, loop, inBody, bodies)
		}
	case *syntax.CatchArm:
		if x == nil {
			return
		}
		for _, t := range x.Types {
			fi.visit(t, flags, loop, inBody, bodies)
		}
		for _, s := range x.Body {
			fi.visit(s, flags, loop, inBody, bodies)
		}
	case *syntax.BlockStmt:
		if x == nil {
			return
		}
		fi.blocks = append(fi.blocks, x)
		fi.blockFlags = append(fi.blockFlags, flags)
		for _, s := range x.List {
			fi.visit(s, flags, loop, inBody, bodies)
		}
	case *syntax.ExprStmt:
		if x == nil {
			return
		}
		fi.visit(x.X, flags, loop, inBody, bodies)
	case *syntax.AssignStmt:
		if x == nil {
			return
		}
		fi.assigns = append(fi.assigns, x)
		fi.assignLoops = append(fi.assignLoops, loop)
		fi.assignInBody = append(fi.assignInBody, inBody)
		for _, l := range x.Lhs {
			fi.visit(l, flags, loop, inBody, bodies)
		}
		for _, r := range x.Rhs {
			fi.visit(r, flags, loop, inBody, bodies)
		}
	case *syntax.DeclStmt:
		if x == nil {
			return
		}
		fi.declStmts = append(fi.declStmts, x)
		if x.Decl != nil {
			fi.visit(x.Decl, flags, loop, inBody, bodies)
		}
	case *syntax.ReturnStmt:
		if x == nil {
			return
		}
		for _, r := range x.Results {
			fi.visit(r, flags, loop, inBody, bodies)
		}
	case *syntax.IfStmt:
		if x == nil {
			return
		}
		fi.visit(x.Cond, flags, loop, inBody, bodies)
		if x.Body != nil {
			fi.visit(x.Body, flags, loop, inBody, bodies)
		}
		fi.visit(x.Else, flags, loop, inBody, bodies)
	case *syntax.SwitchStmt:
		if x == nil {
			return
		}
		fi.switches = append(fi.switches, x)
		fi.visit(x.Tag, flags, loop, inBody, bodies)
		for _, c := range x.Clauses {
			fi.visit(c, flags, loop, inBody, bodies)
		}
	case *syntax.TypeSwitchStmt:
		if x == nil {
			return
		}
		fi.switches = append(fi.switches, x)
		for _, g := range x.Subjects {
			if g != nil {
				fi.visit(g.X, flags, loop, inBody, bodies)
			}
		}
		for _, c := range x.Clauses {
			fi.visit(c, flags, loop, inBody, bodies)
		}
	case *syntax.CaseClause:
		if x == nil {
			return
		}
		for _, e := range x.List {
			fi.visit(e, flags, loop, inBody, bodies)
		}
		for _, s := range x.Body {
			fi.visit(s, flags, loop, inBody, bodies)
		}
	case *syntax.ForStmt:
		if x == nil {
			return
		}
		if x.Cond != nil {
			fi.visit(x.Cond, flags, x, inBody, bodies)
		}
		if x.Body != nil {
			fi.visit(x.Body, flags, x, inBody, bodies)
		}
	case *syntax.RangeStmt:
		if x == nil {
			return
		}
		fi.ranges = append(fi.ranges, x)
		fi.visit(x.Key, flags, loop, inBody, bodies)
		fi.visit(x.Value, flags, loop, inBody, bodies)
		fi.visit(x.X, flags, loop, inBody, bodies)
		if x.Body != nil {
			fi.visit(x.Body, flags, x, inBody, bodies)
		}
	case *syntax.FuncDecl:
		if x == nil {
			return
		}
		fi.recordDoc(x.Doc)
		if x.Body != nil {
			*bodies = append(*bodies, [2]syntax.Pos{x.Body.Pos(), x.Body.End()})
		}
		if x.Extern != nil {
			fi.visit(x.Extern, flags, nil, false, bodies)
		}
		if x.Recv != nil {
			fi.visitFieldList(x.Recv, flags, nil, false, bodies)
		}
		if x.Name != nil {
			fi.idents = append(fi.idents, x.Name)
		}
		if x.TypeParams != nil {
			fi.visitFieldList(x.TypeParams, flags, nil, false, bodies)
		}
		if x.Type != nil {
			fi.visit(x.Type, flags, nil, false, bodies)
		}
		fi.visit(x.Implements, flags, nil, false, bodies)
		if x.Body != nil {
			fi.visit(x.Body, flags|nodeInFuncBody, nil, true, bodies)
		}
	case *syntax.VarDecl:
		if x == nil {
			return
		}
		fi.recordDoc(x.Doc)
		if x.Extern != nil {
			fi.visit(x.Extern, flags, loop, inBody, bodies)
		}
		for _, s := range x.Specs {
			fi.visit(s, flags, loop, inBody, bodies)
		}
	case *syntax.VarSpec:
		if x == nil {
			return
		}
		for _, id := range x.Names {
			if id != nil {
				fi.idents = append(fi.idents, id)
			}
		}
		fi.visit(x.Pattern, flags, loop, inBody, bodies)
		fi.visit(x.Type, flags, loop, inBody, bodies)
		for _, v := range x.Values {
			fi.visit(v, flags, loop, inBody, bodies)
		}
	case *syntax.TypeDecl:
		if x == nil {
			return
		}
		fi.recordDoc(x.Doc)
		if x.Extern != nil {
			fi.visit(x.Extern, flags, nil, false, bodies)
		}
		if x.Name != nil {
			fi.idents = append(fi.idents, x.Name)
		}
		if x.TypeParams != nil {
			fi.visitFieldList(x.TypeParams, flags, nil, false, bodies)
		}
		fi.visit(x.Spec, flags, nil, false, bodies)
		fi.visit(x.Where, flags, nil, false, bodies)
		fi.visit(x.Derive, flags, nil, false, bodies)
		for _, im := range x.Implements {
			fi.visit(im, flags, nil, false, bodies)
		}
	case *syntax.ImportDecl:
		if x == nil {
			return
		}
		fi.recordDoc(x.Doc)
		for _, s := range x.Specs {
			if s != nil && s.Name != nil {
				fi.idents = append(fi.idents, s.Name)
			}
		}
	case *syntax.TestDecl:
		if x == nil {
			return
		}
		fi.recordDoc(x.Doc)
		if x.Body != nil {
			*bodies = append(*bodies, [2]syntax.Pos{x.Body.Pos(), x.Body.End()})
		}
		if x.Name != nil {
			fi.idents = append(fi.idents, x.Name)
		}
		if x.Params != nil {
			fi.visitFieldList(x.Params, flags, nil, false, bodies)
		}
		if x.Body != nil {
			fi.visit(x.Body, flags, nil, true, bodies)
		}
	case *syntax.LawDecl:
		if x == nil {
			return
		}
		fi.recordDoc(x.Doc)
		if x.Body != nil {
			*bodies = append(*bodies, [2]syntax.Pos{x.Body.Pos(), x.Body.End()})
		}
		lawFlags := flags | nodeInTestLaw
		if x.Name != nil {
			fi.idents = append(fi.idents, x.Name)
		}
		if x.TypeParams != nil {
			fi.visitFieldList(x.TypeParams, lawFlags, nil, false, bodies)
		}
		if x.Params != nil {
			fi.visitFieldList(x.Params, lawFlags, nil, false, bodies)
		}
		if x.Body != nil {
			fi.visit(x.Body, lawFlags, nil, flags&nodeInTypeDecl == 0, bodies)
		}
		if x.Reach != nil {
			for _, id := range x.Reach.Targets {
				if id != nil {
					fi.idents = append(fi.idents, id)
				}
			}
			if x.Reach.Pred != nil {
				fi.idents = append(fi.idents, x.Reach.Pred)
			}
		}
	case *syntax.ContextDecl:
		if x == nil {
			return
		}
		fi.recordDoc(x.Doc)
		if x.Name != nil {
			fi.idents = append(fi.idents, x.Name)
		}
		if x.Provides != nil {
			for _, e := range x.Provides.Names {
				fi.visit(e, flags, nil, false, bodies)
			}
		}
		if x.Fails != nil {
			for _, e := range x.Fails.Types {
				fi.visit(e, flags, nil, false, bodies)
			}
		}
		if x.Needs != nil {
			for _, e := range x.Needs.Names {
				fi.visit(e, flags, nil, false, bodies)
			}
		}
		if x.Body != nil {
			for _, e := range x.Body.Entries {
				fi.visit(e, flags, nil, false, bodies)
			}
		}
	case *syntax.ContextLit:
		if x == nil {
			return
		}
		if x.Provides != nil {
			for _, e := range x.Provides.Names {
				fi.visit(e, flags, loop, inBody, bodies)
			}
		}
		if x.Fails != nil {
			for _, e := range x.Fails.Types {
				fi.visit(e, flags, loop, inBody, bodies)
			}
		}
		if x.Needs != nil {
			for _, e := range x.Needs.Names {
				fi.visit(e, flags, loop, inBody, bodies)
			}
		}
		if x.Body != nil {
			for _, e := range x.Body.Entries {
				fi.visit(e, flags, loop, inBody, bodies)
			}
		}
	case *syntax.ContextEntry:
		if x == nil {
			return
		}
		fi.recordDoc(x.Doc)
		fi.contextEntries = append(fi.contextEntries, x)
		fi.visit(x.Key, flags, loop, inBody, bodies)
		for _, k := range x.Keys {
			fi.visit(k, flags, loop, inBody, bodies)
		}
		fi.visit(x.Value, flags, loop, inBody, bodies)
	case *syntax.Field:
		if x == nil {
			return
		}
		fi.recordDoc(x.Doc)
		fi.fields = append(fi.fields, x)
		for _, id := range x.Names {
			if id != nil {
				fi.idents = append(fi.idents, id)
			}
		}
		fi.visit(x.Type, flags, loop, inBody, bodies)
		fi.visit(x.Where, flags, loop, inBody, bodies)
	case *syntax.FieldList:
		if x != nil {
			fi.visitFieldList(x, flags, loop, inBody, bodies)
		}
	case *syntax.FuncType:
		if x == nil {
			return
		}
		if x.TypeParams != nil {
			fi.visitFieldList(x.TypeParams, flags, loop, inBody, bodies)
		}
		if x.Params != nil {
			fi.visitFieldList(x.Params, flags, loop, inBody, bodies)
		}
		if x.Results != nil {
			fi.visitFieldList(x.Results, flags, loop, inBody, bodies)
		}
		if x.Needs != nil {
			for _, e := range x.Needs.Names {
				fi.visit(e, flags, loop, inBody, bodies)
			}
		}
	case *syntax.StructType:
		if x != nil && x.Fields != nil {
			fi.visitFieldList(x.Fields, flags, loop, inBody, bodies)
		}
	case *syntax.InterfaceType:
		if x == nil {
			return
		}
		for _, m := range x.Includes {
			fi.visit(m, flags, loop, inBody, bodies)
		}
		for _, m := range x.Methods {
			fi.visit(m, flags, loop, inBody, bodies)
		}
		for _, l := range x.Laws {
			fi.visit(l, flags, loop, inBody, bodies)
		}
		for _, h := range x.Harnesses {
			if h != nil && h.Name != nil {
				fi.idents = append(fi.idents, h.Name)
			}
		}
		for _, t := range x.Terms {
			if t != nil {
				fi.visit(t.Type, flags, loop, inBody, bodies)
			}
		}
		for _, u := range x.Union {
			fi.visit(u, flags, loop, inBody, bodies)
		}
	case *syntax.MachineType:
		if x == nil {
			return
		}
		fi.visit(x.State, flags, loop, inBody, bodies)
		fi.visit(x.Signal, flags, loop, inBody, bodies)
		for _, e := range x.Entries {
			if e != nil {
				fi.recordDoc(e.Doc)
				fi.visit(e.Value, flags, loop, inBody, bodies)
			}
		}
		for _, l := range x.Laws {
			fi.visit(l, flags, loop, inBody, bodies)
		}
	case *syntax.MachineEntry:
		if x == nil {
			return
		}
		fi.recordDoc(x.Doc)
		fi.visit(x.Value, flags, loop, inBody, bodies)
	case *syntax.ArrayType:
		if x != nil {
			fi.visit(x.Elt, flags, loop, inBody, bodies)
		}
	case *syntax.MapType:
		if x == nil {
			return
		}
		fi.visit(x.Key, flags, loop, inBody, bodies)
		fi.visit(x.Value, flags, loop, inBody, bodies)
		fi.visit(x.Hashing, flags, loop, inBody, bodies)
	case *syntax.ErrorRow:
		if x == nil {
			return
		}
		for _, e := range x.Types {
			fi.visit(e, flags, loop, inBody, bodies)
		}
	case *syntax.NeedsClause:
		if x == nil {
			return
		}
		for _, e := range x.Names {
			fi.visit(e, flags, loop, inBody, bodies)
		}
	case *syntax.ProvidesClause:
		if x == nil {
			return
		}
		for _, e := range x.Names {
			fi.visit(e, flags, loop, inBody, bodies)
		}
	case *syntax.TupleExpr:
		if x == nil {
			return
		}
		for _, e := range x.Elts {
			fi.visit(e, flags, loop, inBody, bodies)
		}
	case *syntax.VariantSpec:
		if x == nil {
			return
		}
		if x.Name != nil {
			fi.idents = append(fi.idents, x.Name)
		}
		if x.TypeArgs != nil {
			fi.visitFieldList(x.TypeArgs, flags, loop, inBody, bodies)
		}
		if x.Fields != nil {
			fi.visitFieldList(x.Fields, flags, loop, inBody, bodies)
		}
	case *syntax.AsPattern:
		if x == nil {
			return
		}
		fi.visit(x.X, flags, loop, inBody, bodies)
		if x.Name != nil {
			fi.idents = append(fi.idents, x.Name)
		}
	case *syntax.OrPattern:
		if x == nil {
			return
		}
		for _, a := range x.Alts {
			fi.visit(a, flags, loop, inBody, bodies)
		}
	case *syntax.InterpolatedString:
		if x == nil {
			return
		}
		for _, e := range x.Exprs {
			fi.visit(e, flags, loop, inBody, bodies)
		}
	case *syntax.AssertStmt:
		if x != nil {
			fi.visit(x.X, flags, loop, inBody, bodies)
		}
	case *syntax.DieStmt:
		if x != nil {
			fi.visit(x.X, flags, loop, inBody, bodies)
		}
	case *syntax.AlwaysStmt:
		if x != nil {
			fi.visit(x.X, flags, loop, inBody, bodies)
		}
	case *syntax.SometimesStmt:
		if x != nil {
			fi.visit(x.X, flags, loop, inBody, bodies)
		}
	case *syntax.TypeGuard:
		if x != nil {
			fi.visit(x.X, flags, loop, inBody, bodies)
		}
	case *syntax.Harness:
		if x != nil && x.Name != nil {
			fi.idents = append(fi.idents, x.Name)
		}
	}
}

func (fi *fileIndex) visitFieldList(l *syntax.FieldList, flags uint8, loop syntax.Stmt, inBody bool, bodies *[][2]syntax.Pos) {
	for _, f := range l.List {
		fi.visit(f, flags, loop, inBody, bodies)
	}
}

// mergeBodySpans sorts raw body spans and merges strictly overlapping open
// intervals (a, b) ∪ (c, d) = (a, max(b, d)) when c < b, preserving exact
// inBody open-interval membership while enabling O(log N) binary search.
func mergeBodySpans(raw [][2]syntax.Pos) [][2]syntax.Pos {
	if len(raw) <= 1 {
		return raw
	}
	slices.SortFunc(raw, func(a, b [2]syntax.Pos) int {
		if a[0] != b[0] {
			return int(a[0] - b[0])
		}
		return int(a[1] - b[1])
	})
	out := raw[:1]
	for _, s := range raw[1:] {
		last := &out[len(out)-1]
		if s[0] < last[1] {
			if s[1] > last[1] {
				last[1] = s[1]
			}
			continue
		}
		out = append(out, s)
	}
	return out
}

// inBody reports whether pos lies strictly inside any body span of fi.
func (fi *fileIndex) inBody(pos syntax.Pos) bool {
	spans := fi.bodySpans
	lo, hi := 0, len(spans)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if spans[mid][0] < pos {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo > 0 && pos < spans[lo-1][1]
}

// ensureLines builds the flat []int32 line-start table once per file.
func (fi *fileIndex) ensureLines() {
	if fi.lineStarts != nil {
		return
	}
	src := fi.file.Src
	starts := make([]int32, 1, 1+len(src)/40)
	starts[0] = 0
	for off := 0; off < len(src); {
		nl := strings.IndexByte(src[off:], '\n')
		if nl < 0 {
			break
		}
		off += nl + 1
		starts = append(starts, int32(off))
	}
	fi.lineStarts = starts
}

// lineBounds returns [start, end) of the line containing pos (without '\n')
// using O(log N) binary search on the flat line-start table.
func (fi *fileIndex) lineBounds(pos int) (start, end int) {
	fi.ensureLines()
	starts := fi.lineStarts
	srcLen := len(fi.file.Src)
	if pos < 0 {
		pos = 0
	}
	if pos > srcLen {
		pos = srcLen
	}
	// Find largest index idx such that int(starts[idx]) <= pos.
	lo, hi := 0, len(starts)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if int(starts[mid]) <= pos {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	idx := lo - 1
	if idx < 0 {
		idx = 0
	}
	start = int(starts[idx])
	if idx+1 < len(starts) {
		end = int(starts[idx+1]) - 1
	} else {
		end = srcLen
	}
	return start, end
}

// walkNode traverses the syntax tree rooted at n in exact syntax.Inspect
// pre-order DFS order, without allocating per-node closures or variadic
// argument slices.
func walkNode(n syntax.Node, fn func(syntax.Node) bool) {
	if n == nil || isTypedNil(n) || !fn(n) {
		return
	}
	switch x := n.(type) {
	case *syntax.File:
		for _, d := range x.Decls {
			walkNode(d, fn)
		}
	case *syntax.ImportDecl:
		for _, s := range x.Specs {
			if s != nil {
				walkNode(s, fn)
			}
		}
	case *syntax.ImportSpec:
		if x.Name != nil {
			walkNode(x.Name, fn)
		}
		if x.Path != nil {
			walkNode(x.Path, fn)
		}
	case *syntax.VarDecl:
		if x.Extern != nil {
			walkNode(x.Extern, fn)
		}
		for _, s := range x.Specs {
			if s != nil {
				walkNode(s, fn)
			}
		}
	case *syntax.VarSpec:
		for _, id := range x.Names {
			if id != nil {
				walkNode(id, fn)
			}
		}
		if x.Pattern != nil {
			walkNode(x.Pattern, fn)
		}
		if x.Type != nil {
			walkNode(x.Type, fn)
		}
		for _, v := range x.Values {
			walkNode(v, fn)
		}
	case *syntax.TypeDecl:
		if x.Extern != nil {
			walkNode(x.Extern, fn)
		}
		if x.Name != nil {
			walkNode(x.Name, fn)
		}
		if x.TypeParams != nil {
			walkNode(x.TypeParams, fn)
		}
		if x.Spec != nil {
			walkNode(x.Spec, fn)
		}
		if x.Where != nil {
			walkNode(x.Where, fn)
		}
		if x.Derive != nil {
			walkNode(x.Derive, fn)
		}
		for _, im := range x.Implements {
			walkNode(im, fn)
		}
	case *syntax.FuncDecl:
		if x.Extern != nil {
			walkNode(x.Extern, fn)
		}
		if x.Recv != nil {
			walkNode(x.Recv, fn)
		}
		if x.Name != nil {
			walkNode(x.Name, fn)
		}
		if x.TypeParams != nil {
			walkNode(x.TypeParams, fn)
		}
		if x.Type != nil {
			walkNode(x.Type, fn)
		}
		if x.Implements != nil {
			walkNode(x.Implements, fn)
		}
		if x.Body != nil {
			walkNode(x.Body, fn)
		}
	case *syntax.ExternSpec:
		if x.Target != nil {
			walkNode(x.Target, fn)
		}
	case *syntax.TestDecl:
		if x.Name != nil {
			walkNode(x.Name, fn)
		}
		if x.Params != nil {
			walkNode(x.Params, fn)
		}
		if x.Body != nil {
			walkNode(x.Body, fn)
		}
	case *syntax.Harness:
		if x.Name != nil {
			walkNode(x.Name, fn)
		}
	case *syntax.LawDecl:
		if x.Name != nil {
			walkNode(x.Name, fn)
		}
		if x.TypeParams != nil {
			walkNode(x.TypeParams, fn)
		}
		if x.Params != nil {
			walkNode(x.Params, fn)
		}
		if x.Body != nil {
			walkNode(x.Body, fn)
		}
		if x.Reach != nil {
			for _, id := range x.Reach.Targets {
				if id != nil {
					walkNode(id, fn)
				}
			}
			if x.Reach.Pred != nil {
				walkNode(x.Reach.Pred, fn)
			}
		}
	case *syntax.AssertStmt:
		walkNode(x.X, fn)
	case *syntax.DieStmt:
		walkNode(x.X, fn)
	case *syntax.AlwaysStmt:
		walkNode(x.X, fn)
	case *syntax.SometimesStmt:
		walkNode(x.X, fn)
	case *syntax.VariantSpec:
		if x.Name != nil {
			walkNode(x.Name, fn)
		}
		if x.TypeArgs != nil {
			walkNode(x.TypeArgs, fn)
		}
		if x.Fields != nil {
			walkNode(x.Fields, fn)
		}
	case *syntax.AsPattern:
		walkNode(x.X, fn)
		if x.Name != nil {
			walkNode(x.Name, fn)
		}
	case *syntax.OrPattern:
		for _, a := range x.Alts {
			walkNode(a, fn)
		}
	case *syntax.InterpolatedString:
		for i, t := range x.Texts {
			if t != nil {
				walkNode(t, fn)
			}
			if i < len(x.Exprs) {
				walkNode(x.Exprs[i], fn)
			}
		}
	case *syntax.ContextDecl:
		if x.Name != nil {
			walkNode(x.Name, fn)
		}
		if x.Provides != nil {
			walkNode(x.Provides, fn)
		}
		if x.Fails != nil {
			walkNode(x.Fails, fn)
		}
		if x.Needs != nil {
			walkNode(x.Needs, fn)
		}
		if x.Body != nil {
			walkNode(x.Body, fn)
		}
	case *syntax.ContextBody:
		for _, e := range x.Entries {
			if e != nil {
				walkNode(e, fn)
			}
		}
	case *syntax.ContextEntry:
		walkNode(x.Key, fn)
		for _, k := range x.Keys {
			walkNode(k, fn)
		}
		walkNode(x.Value, fn)
	case *syntax.Field:
		for _, id := range x.Names {
			if id != nil {
				walkNode(id, fn)
			}
		}
		walkNode(x.Type, fn)
		if x.Key != nil {
			walkNode(x.Key, fn)
		}
		walkNode(x.Where, fn)
	case *syntax.FieldList:
		for _, fl := range x.List {
			if fl != nil {
				walkNode(fl, fn)
			}
		}
	case *syntax.NeedsClause:
		for _, e := range x.Names {
			walkNode(e, fn)
		}
	case *syntax.ProvidesClause:
		for _, e := range x.Names {
			walkNode(e, fn)
		}
	case *syntax.CompositeLit:
		walkNode(x.Type, fn)
		walkNode(x.Base, fn)
		for _, e := range x.Elts {
			walkNode(e, fn)
		}
	case *syntax.KeyValueExpr:
		walkNode(x.Key, fn)
		walkNode(x.Value, fn)
	case *syntax.FuncLit:
		if x.Type != nil {
			walkNode(x.Type, fn)
		}
		if x.Body != nil {
			walkNode(x.Body, fn)
		}
	case *syntax.ParenExpr:
		walkNode(x.X, fn)
	case *syntax.SelectorExpr:
		walkNode(x.X, fn)
		if x.Sel != nil {
			walkNode(x.Sel, fn)
		}
	case *syntax.IndexExpr:
		walkNode(x.X, fn)
		for _, idx := range x.Indices {
			walkNode(idx, fn)
		}
	case *syntax.SliceExpr:
		walkNode(x.X, fn)
		walkNode(x.Low, fn)
		walkNode(x.High, fn)
		walkNode(x.Max, fn)
	case *syntax.TypeGuard:
		walkNode(x.X, fn)
	case *syntax.CallExpr:
		walkNode(x.Fun, fn)
		for _, a := range x.Args {
			walkNode(a, fn)
		}
		if x.Trailing != nil {
			walkNode(x.Trailing, fn)
		}
	case *syntax.StarExpr:
		walkNode(x.X, fn)
	case *syntax.UnaryExpr:
		walkNode(x.X, fn)
	case *syntax.BinaryExpr:
		walkNode(x.X, fn)
		walkNode(x.Y, fn)
	case *syntax.TryExpr:
		walkNode(x.X, fn)
	case *syntax.CatchExpr:
		walkNode(x.X, fn)
		for _, a := range x.Arms {
			if a != nil {
				walkNode(a, fn)
			}
		}
	case *syntax.CatchArm:
		for _, t := range x.Types {
			walkNode(t, fn)
		}
		for _, s := range x.Body {
			walkNode(s, fn)
		}
	case *syntax.ContextLit:
		if x.Provides != nil {
			walkNode(x.Provides, fn)
		}
		if x.Fails != nil {
			walkNode(x.Fails, fn)
		}
		if x.Needs != nil {
			walkNode(x.Needs, fn)
		}
		if x.Body != nil {
			walkNode(x.Body, fn)
		}
	case *syntax.ArrayType:
		walkNode(x.Elt, fn)
	case *syntax.MapType:
		walkNode(x.Key, fn)
		walkNode(x.Value, fn)
		if x.Hashing != nil {
			walkNode(x.Hashing, fn)
		}
	case *syntax.FuncType:
		if x.TypeParams != nil {
			walkNode(x.TypeParams, fn)
		}
		if x.Params != nil {
			walkNode(x.Params, fn)
		}
		if x.Results != nil {
			walkNode(x.Results, fn)
		}
		if x.Needs != nil {
			walkNode(x.Needs, fn)
		}
		if x.Takes != nil {
			walkNode(x.Takes, fn)
		}
	case *syntax.StructType:
		if x.Fields != nil {
			walkNode(x.Fields, fn)
		}
	case *syntax.InterfaceType:
		for _, m := range x.Includes {
			if m != nil {
				walkNode(m, fn)
			}
		}
		for _, m := range x.Methods {
			if m != nil {
				walkNode(m, fn)
			}
		}
		for _, l := range x.Laws {
			if l != nil {
				walkNode(l, fn)
			}
		}
		for _, h := range x.Harnesses {
			if h != nil {
				walkNode(h, fn)
			}
		}
		for _, t := range x.Terms {
			if t != nil {
				walkNode(t.Type, fn)
			}
		}
		for _, u := range x.Union {
			walkNode(u, fn)
		}
	case *syntax.ErrorRow:
		for _, t := range x.Types {
			walkNode(t, fn)
		}
	case *syntax.MachineType:
		walkNode(x.State, fn)
		walkNode(x.Signal, fn)
		for _, e := range x.Entries {
			if e != nil {
				walkNode(e, fn)
			}
		}
		for _, l := range x.Laws {
			if l != nil {
				walkNode(l, fn)
			}
		}
	case *syntax.MachineEntry:
		walkNode(x.Value, fn)
	case *syntax.TupleExpr:
		for _, e := range x.Elts {
			walkNode(e, fn)
		}
	case *syntax.DeclStmt:
		if x.Decl != nil {
			walkNode(x.Decl, fn)
		}
	case *syntax.ExprStmt:
		walkNode(x.X, fn)
	case *syntax.AssignStmt:
		for _, l := range x.Lhs {
			walkNode(l, fn)
		}
		for _, r := range x.Rhs {
			walkNode(r, fn)
		}
	case *syntax.ReturnStmt:
		for _, r := range x.Results {
			walkNode(r, fn)
		}
	case *syntax.BlockStmt:
		for _, s := range x.List {
			walkNode(s, fn)
		}
	case *syntax.IfStmt:
		walkNode(x.Cond, fn)
		if x.Body != nil {
			walkNode(x.Body, fn)
		}
		walkNode(x.Else, fn)
	case *syntax.CaseClause:
		for _, e := range x.List {
			walkNode(e, fn)
		}
		for _, s := range x.Body {
			walkNode(s, fn)
		}
	case *syntax.SwitchStmt:
		walkNode(x.Tag, fn)
		for _, c := range x.Clauses {
			if c != nil {
				walkNode(c, fn)
			}
		}
	case *syntax.TypeSwitchStmt:
		for _, g := range x.Subjects {
			if g != nil {
				walkNode(g, fn)
			}
		}
		for _, c := range x.Clauses {
			if c != nil {
				walkNode(c, fn)
			}
		}
	case *syntax.ForStmt:
		walkNode(x.Cond, fn)
		if x.Body != nil {
			walkNode(x.Body, fn)
		}
	case *syntax.RangeStmt:
		walkNode(x.Key, fn)
		walkNode(x.Value, fn)
		walkNode(x.X, fn)
		if x.Body != nil {
			walkNode(x.Body, fn)
		}
	}
}

// isTypedNil reports whether n holds a typed nil pointer for any syntax.Node
// concrete pointer type that can appear typed-nil at a call site.
func isTypedNil(n syntax.Node) bool {
	switch x := n.(type) {
	case *syntax.Ident:
		return x == nil
	case *syntax.BasicLit:
		return x == nil
	case *syntax.FieldList:
		return x == nil
	case *syntax.TakesClause:
		return x == nil
	case *syntax.NeedsClause:
		return x == nil
	case *syntax.ProvidesClause:
		return x == nil
	case *syntax.ErrorRow:
		return x == nil
	case *syntax.BlockStmt:
		return x == nil
	case *syntax.FuncType:
		return x == nil
	case *syntax.CallExpr:
		return x == nil
	case *syntax.ContextBody:
		return x == nil
	case *syntax.VarDecl:
		return x == nil
	case *syntax.ExternSpec:
		return x == nil
	case *syntax.LawDecl:
		return x == nil
	case *syntax.Harness:
		return x == nil
	case *syntax.StructType:
		return x == nil
	case *syntax.File:
		return x == nil
	}
	return false
}
