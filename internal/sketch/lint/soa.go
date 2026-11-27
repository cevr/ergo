package lint

import (
	"slices"
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

// The flat file index (LINT-SOA-1). Each file is walked once per lint run,
// on the first rule that asks, into one column per node kind the rules
// read, in syntax.Inspect's pre-order, with a flag byte beside the columns
// whose rules ask where a node sits. Rules scan the columns of the kinds
// they inspect instead of walking the whole file again.
//
// The columns hold the checker's nodes because check.Info is keyed by
// them. Each node column is one row kind of compiler/syntax (kIdent,
// kCallExpr, kAssignStmt, kCompositeLit, kBinaryExpr, kSelectorExpr,
// kField, kDeclStmt, kCatchExpr, kRangeStmt, kBlockStmt, kIndexExpr,
// kFuncLit, kContextEntry) in the order a pre-order visit of the rows
// gives, so a producer that reads the rows fills them with row ids once
// the checker's facts are keyed by row: the flags, the per-declaration
// spans, the body spans and the line table are computed from positions and
// nesting only. Three things do not map one to one: the switches column
// joins kSwitchStmt, kTypeSwitchStmt and kCatchExpr; the doc-comment set
// is keyed by comment group, which the rows hold as comment-column ranges;
// and the nodes the decoder derives (an or-pattern's expansion, a
// refinement's parse function, a codec pair's law) have no row.

const (
	nodeInTestLaw  uint8 = 1 << iota // inside a TestDecl or LawDecl
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
		if _, ok := d.(*syntax.TypeDecl); ok {
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
		testFlags := flags | nodeInTestLaw
		if x.Params != nil {
			fi.visitFieldList(x.Params, testFlags, nil, false, bodies)
		}
		if x.Body != nil {
			fi.visit(x.Body, testFlags, nil, true, bodies)
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
		for _, fc := range x.Facts {
			if fc != nil {
				fi.visit(fc.Cond, flags, loop, inBody, bodies)
				if fc.When != nil {
					fi.visit(fc.When, flags, loop, inBody, bodies)
				}
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
	case *syntax.FactClause:
		if x == nil {
			return
		}
		fi.visit(x.Cond, flags, loop, inBody, bodies)
		if x.When != nil {
			fi.visit(x.When, flags, loop, inBody, bodies)
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
