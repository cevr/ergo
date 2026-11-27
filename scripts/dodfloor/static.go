package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

type StaticFinding struct {
	Rule    string
	File    string
	Line    int
	Col     int
	Message string
}

type StaticReport struct {
	FilesScanned    int
	StructsScanned  int
	FieldsScanned   int
	MapTypesScanned int
	FuncsScanned    int
	Findings        []StaticFinding
}

type parsedErgoFile struct {
	rel    string
	folder string
	ast    *syntax.File
}

var scalarInt64Payloads = map[string]bool{
	"compiler/ir/ir.ergo:File.V":       true,
	"compiler/lower/lower.ergo:CInt.I": true,
}

func isFloorOwnedFile(rel string) bool {
	return strings.HasPrefix(rel, "compiler/syntax/") ||
		strings.HasPrefix(rel, "compiler/format/") ||
		strings.HasPrefix(rel, "compiler/ir/")
}

func isFixtureOrLawFile(rel string) bool {
	base := filepath.Base(rel)
	return base == "laws.ergo" || base == "fixture.ergo" || base == "lexerror.ergo" || base == "typeset.ergo"
}

func CheckStatic(root string) (StaticReport, error) {
	compilerDir := filepath.Join(root, "compiler")
	var rep StaticReport
	module := ""
	if data, err := os.ReadFile(filepath.Join(compilerDir, "ergo.mod")); err == nil {
		for line := range strings.SplitSeq(string(data), "\n") {
			if name, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
				module = strings.TrimSpace(name)
			}
		}
	}

	var files []parsedErgoFile
	kinds := map[[2]string]string{}
	err := filepath.WalkDir(compilerDir, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			if e.Name() == "testdata" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".ergo") {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		f, perr := syntax.Parse(string(src))
		if perr != nil {
			return fmt.Errorf("%s: %v", rel, perr)
		}
		folder := filepath.Dir(path)
		files = append(files, parsedErgoFile{rel: rel, folder: folder, ast: f})
		rep.FilesScanned++
		for _, d := range f.Decls {
			if td, ok := d.(*syntax.TypeDecl); ok && td.Name != nil {
				kinds[[2]string{folder, td.Name.Name}] = declaredMapKeyKind(td.Spec)
			}
		}
		return nil
	})
	if err != nil {
		return rep, err
	}

	for _, f := range files {
		folders, stdDense := collectImports(f.ast, compilerDir, module)
		checkMapRules(f, folders, stdDense, kinds, &rep)
		checkStructWidthRules(f, &rep)
		checkScratchAllocRules(f, &rep)
	}

	slices.SortFunc(rep.Findings, func(a, b StaticFinding) int {
		if a.File != b.File {
			return strings.Compare(a.File, b.File)
		}
		if a.Line != b.Line {
			return a.Line - b.Line
		}
		if a.Col != b.Col {
			return a.Col - b.Col
		}
		return strings.Compare(a.Rule, b.Rule)
	})
	return rep, nil
}

func collectImports(f *syntax.File, compilerDir, module string) (map[string]string, string) {
	folders := map[string]string{}
	stdDense := ""
	for _, d := range f.Decls {
		id, ok := d.(*syntax.ImportDecl)
		if !ok {
			continue
		}
		for _, s := range id.Specs {
			path := s.Path.Value
			if p, err := strconv.Unquote(path); err == nil {
				path = p
			}
			if s.Kind == syntax.ImportGo {
				continue
			}
			if path == "dense" {
				stdDense = "dense"
				if s.Kind == syntax.ImportNamed && s.Name != nil {
					stdDense = s.Name.Name
				}
				continue
			}
			if module == "" {
				continue
			}
			rest, ok := strings.CutPrefix(path, module+"/")
			if !ok {
				continue
			}
			name := rest[strings.LastIndex(rest, "/")+1:]
			if s.Kind == syntax.ImportNamed && s.Name != nil {
				name = s.Name.Name
			}
			folders[name] = filepath.Join(compilerDir, filepath.FromSlash(rest))
		}
	}
	return folders, stdDense
}

func declaredMapKeyKind(spec syntax.Expr) string {
	switch s := spec.(type) {
	case *syntax.Ident:
		switch s.Name {
		case "string":
			return ""
		case "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64", "uintptr", "byte", "rune":
			return "a dense id"
		}
		return "a defined type"
	case *syntax.StructType:
		return "a record"
	case *syntax.InterfaceType:
		if len(s.Union) > 0 {
			return "a union"
		}
		return "an operation type"
	}
	return "a defined type"
}

func checkMapRules(f parsedErgoFile, folders map[string]string, stdDense string, kinds map[[2]string]string, rep *StaticReport) {
	floorOwned := isFloorOwnedFile(f.rel) && !isFixtureOrLawFile(f.rel)
	for _, d := range f.ast.Decls {
		if _, isLaw := d.(*syntax.LawDecl); isLaw {
			continue
		}
		if _, isTest := d.(*syntax.TestDecl); isTest {
			continue
		}
		syntax.Inspect(d, func(n syntax.Node) bool {
			m, ok := n.(*syntax.MapType)
			if !ok {
				return true
			}
			rep.MapTypesScanned++
			what := ""
			switch k := m.Key.(type) {
			case *syntax.StarExpr:
				what = "a borrow"
			case *syntax.Ident:
				if k.Name != "string" {
					if v, exists := kinds[[2]string{f.folder, k.Name}]; exists {
						what = v
					} else if floorOwned {
						what = "non-string key " + k.Name + " in hot floor package"
					}
				}
			case *syntax.SelectorExpr:
				if q, ok := k.X.(*syntax.Ident); ok && folders[q.Name] != "" {
					what = kinds[[2]string{folders[q.Name], k.Sel.Name}]
				} else if floorOwned {
					what = "qualified key in hot floor package"
				}
			case *syntax.IndexExpr:
				if s, ok := k.X.(*syntax.SelectorExpr); ok && (s.Sel.Name == "ID" || s.Sel.Name == "Opt") {
					if q, ok := s.X.(*syntax.Ident); ok && stdDense != "" && q.Name == stdDense {
						what = "a dense id"
					}
				}
				if what == "" && floorOwned {
					what = "indexed key in hot floor package"
				}
			default:
				if floorOwned {
					what = "composite key in hot floor package"
				}
			}
			if what != "" {
				line, col := f.ast.Position(m.Key.Pos())
				rep.Findings = append(rep.Findings, StaticFinding{
					Rule:    "no-pointer-keyed-maps",
					File:    f.rel,
					Line:    line,
					Col:     col,
					Message: fmt.Sprintf("Map keyed by %s (%s): use dense uint32 side tables", f.ast.Src[int(m.Key.Pos()):int(m.Key.End())], what),
				})
			}
			return true
		})
	}
}

func checkStructWidthRules(f parsedErgoFile, rep *StaticReport) {
	floorOwned := isFloorOwnedFile(f.rel) && !isFixtureOrLawFile(f.rel)
	checkFields := func(typeName string, fl *syntax.FieldList) {
		if fl == nil {
			return
		}
		rep.StructsScanned++
		for _, field := range fl.List {
			if field.Type == nil {
				continue
			}
			var names []string
			for _, nm := range field.Names {
				names = append(names, nm.Name)
			}
			if len(names) == 0 {
				names = []string{"<embedded>"}
			}
			for _, fieldName := range names {
				rep.FieldsScanned++
				key := f.rel + ":" + typeName + "." + fieldName
				if scalarInt64Payloads[key] {
					continue
				}
				if reason := badFieldType(field.Type, floorOwned, typeName); reason != "" {
					line, col := f.ast.Position(field.Type.Pos())
					rep.Findings = append(rep.Findings, StaticFinding{
						Rule:    "dense-uint32-ids",
						File:    f.rel,
						Line:    line,
						Col:     col,
						Message: fmt.Sprintf("%s.%s uses %s: %s", typeName, fieldName, f.ast.Src[int(field.Type.Pos()):int(field.Type.End())], reason),
					})
				}
			}
		}
	}

	for _, d := range f.ast.Decls {
		td, ok := d.(*syntax.TypeDecl)
		if !ok || td.Name == nil {
			continue
		}
		switch spec := td.Spec.(type) {
		case *syntax.StructType:
			checkFields(td.Name.Name, spec.Fields)
		case *syntax.InterfaceType:
			for _, u := range spec.Union {
				if vs, ok := u.(*syntax.VariantSpec); ok && vs.Name != nil {
					checkFields(vs.Name.Name, vs.Fields)
				}
			}
		}
	}
}

func badFieldType(expr syntax.Expr, floorOwned bool, typeName string) string {
	switch t := expr.(type) {
	case *syntax.Ident:
		if t.Name == "int32" || t.Name == "int64" {
			return "use dense uint32 (or uint8) IDs/offsets instead of int32/int64"
		}
	case *syntax.StarExpr:
		return "pointer field in compiler row/table struct violates flat SoA dense layout"
	case *syntax.ArrayType:
		if id, ok := t.Elt.(*syntax.Ident); ok {
			if id.Name == "int32" || id.Name == "int64" {
				return "column slice uses int32/int64 instead of uint32/uint8"
			}
			if floorOwned && id.Name == "int" && typeName != "census" {
				return "column slice in syntax/format/ir should use uint32/uint8 instead of 8-byte int"
			}
		}
		if star, ok := t.Elt.(*syntax.StarExpr); ok {
			_ = star
			return "slice of pointers in compiler row/table struct"
		}
	}
	return ""
}

func checkScratchAllocRules(f parsedErgoFile, rep *StaticReport) {
	if !isFloorOwnedFile(f.rel) || isFixtureOrLawFile(f.rel) {
		return
	}
	for _, d := range f.ast.Decls {
		fd, ok := d.(*syntax.FuncDecl)
		if !ok || fd.Body == nil || fd.Name == nil {
			continue
		}
		if strings.HasPrefix(fd.Name.Name, "test") || strings.HasSuffix(fd.Name.Name, "Test") || fd.Name.Name == "sourcesOf" || fd.Name.Name == "censusParents" || fd.Name.Name == "FixLines" {
			continue
		}
		rep.FuncsScanned++
		fnName := fd.Name.Name
		callsSelfOrChildren := false
		syntax.Inspect(fd.Body, func(n syntax.Node) bool {
			c, ok := n.(*syntax.CallExpr)
			if !ok {
				return true
			}
			if id, ok := c.Fun.(*syntax.Ident); ok {
				if id.Name == fnName || id.Name == "children" {
					callsSelfOrChildren = true
				}
			}
			if sel, ok := c.Fun.(*syntax.SelectorExpr); ok {
				if sel.Sel.Name == "Children" {
					callsSelfOrChildren = true
				}
			}
			return true
		})

		var checkBlock func(node syntax.Node, inLoop bool)
		checkBlock = func(node syntax.Node, inLoop bool) {
			if node == nil {
				return
			}
			syntax.Inspect(node, func(n syntax.Node) bool {
				switch x := n.(type) {
				case *syntax.ForStmt:
					checkBlock(x.Cond, true)
					checkBlock(x.Body, true)
					return false
				case *syntax.RangeStmt:
					checkBlock(x.X, inLoop)
					checkBlock(x.Body, true)
					return false
				case *syntax.CallExpr:
					if sel, ok := x.Fun.(*syntax.SelectorExpr); ok {
						if q, ok := sel.X.(*syntax.Ident); ok && q.Name == "slices" && sel.Sel.Name == "Repeat" {
							line, col := f.ast.Position(x.Pos())
							rep.Findings = append(rep.Findings, StaticFinding{
								Rule:    "no-per-node-scratch-allocs",
								File:    f.rel,
								Line:    line,
								Col:     col,
								Message: fmt.Sprintf("in %s: slices.Repeat allocates a temp slice literal and full copy; use pre-sized scratch/fast-path", fnName),
							})
						}
					}
					if inLoop || callsSelfOrChildren {
						for _, arg := range x.Args {
							if isEmptySliceLit(arg) {
								line, col := f.ast.Position(arg.Pos())
								rep.Findings = append(rep.Findings, StaticFinding{
									Rule:    "no-per-node-scratch-allocs",
									File:    f.rel,
									Line:    line,
									Col:     col,
									Message: fmt.Sprintf("in %s: empty slice literal %s passed in loop/recursive walk; reuse flat scratch storage", fnName, f.ast.Src[int(arg.Pos()):int(arg.End())]),
								})
							}
						}
					}
				}
				return true
			})
		}
		checkBlock(fd.Body, false)
	}
}

func isEmptySliceLit(e syntax.Expr) bool {
	lit, ok := e.(*syntax.CompositeLit)
	if !ok || len(lit.Elts) != 0 {
		return false
	}
	_, isArr := lit.Type.(*syntax.ArrayType)
	return isArr
}
