package selfhostcheck

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/experimental/irspike"
	linked "ergo.local/prototype/internal/linked/compiler"
	"ergo.local/prototype/internal/opcount"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestCompilerDoDFloor enforces the compiler's static Data-Oriented Design
// invariants across compiler/** and verifies deterministic counted
// instruction, allocation, and memory floors on compiler/, plus exact IR
// agreement on generated and example IR files.
func TestCompilerDoDFloor(t *testing.T) {
	root := repoRoot()
	checkCompilerStaticDoD(t, root)

	if err := opcount.Available(); err != nil {
		t.Skipf("opcount unavailable: %v", err)
	}

	srcs, totalBytes := loadCompilerFloorSources(t, root)
	if len(srcs) == 0 || totalBytes == 0 {
		t.Fatal("no compiler sources loaded")
	}

	ctx := context.Background()

	measureLoop := func(fn func() error) (instrB, allocsB, bytesB float64) {
		const rounds = 3
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		prevGC := debug.SetGCPercent(-1)
		defer debug.SetGCPercent(prevGC)

		var instrs, allocs, allocBytes []float64
		for range rounds {
			var m0, m1 runtime.MemStats
			runtime.ReadMemStats(&m0)
			var innerErr error
			n, cerr := opcount.CountThread(func() {
				innerErr = fn()
			})
			runtime.ReadMemStats(&m1)
			if cerr != nil {
				t.Fatalf("opcount: %v", cerr)
			}
			if innerErr != nil {
				t.Fatal(innerErr)
			}
			b := float64(totalBytes)
			instrs = append(instrs, float64(n)/b)
			allocs = append(allocs, float64(m1.Mallocs-m0.Mallocs)/b)
			allocBytes = append(allocBytes, float64(m1.TotalAlloc-m0.TotalAlloc)/b)
		}
		return opcount.Median(instrs), opcount.Median(allocs), opcount.Median(allocBytes)
	}

	// Format floor on compiler/** (baseline 05d04ac3d: 259.62 instr/B, 0.0685 allocs/B, 38.13 bytes/B).
	fmtInstr, fmtAllocs, fmtBytes := measureLoop(func() error {
		for _, src := range srcs {
			out, err := linked.FormatSource(ctx, src, 0)
			if err != nil {
				return err
			}
			if !strings.HasPrefix(out, "ok\n") {
				return fmt.Errorf("FormatSource failed: %.80q", out)
			}
		}
		return nil
	})
	t.Logf("compiler format floor (GOGC=off median): %.2f instr/B, %.4f allocs/B, %.2f bytes/B", fmtInstr, fmtAllocs, fmtBytes)
	if fmtInstr > 250.0 {
		t.Errorf("compiler format instr/B = %.2f exceeds DoD ceiling 250.0 (baseline 259.62)", fmtInstr)
	}
	if fmtAllocs > 0.020 {
		t.Errorf("compiler format allocs/B = %.4f exceeds DoD ceiling 0.020 (baseline 0.0685)", fmtAllocs)
	}
	if fmtBytes > 27.5 {
		t.Errorf("compiler format bytes/B = %.2f exceeds DoD ceiling 27.5 (baseline 38.13)", fmtBytes)
	}

	// Syntax floor on compiler/** (baseline 05d04ac3d: 0.0030 allocs/B, 33.28 bytes/B).
	synInstr, synAllocs, synBytes := measureLoop(func() error {
		for _, src := range srcs {
			cols, err := linked.Columns(ctx, src)
			if err != nil {
				return err
			}
			if len(cols) == 0 {
				return fmt.Errorf("empty columns")
			}
		}
		return nil
	})
	t.Logf("compiler syntax floor (GOGC=off median): %.2f instr/B, %.4f allocs/B, %.2f bytes/B", synInstr, synAllocs, synBytes)
	if synAllocs > 0.0028 {
		t.Errorf("compiler syntax allocs/B = %.4f exceeds DoD ceiling 0.0028 (baseline 0.0030)", synAllocs)
	}
	if synBytes > 30.5 {
		t.Errorf("compiler syntax bytes/B = %.2f exceeds DoD ceiling 30.5 (baseline 33.28)", synBytes)
	}

	// Representative IR write agreement + allocation/instruction floor (generated + board).
	checkIRFloor(t, root)
}

func checkIRFloor(t *testing.T, root string) {
	t.Helper()
	kinds := irKindTable(t)
	type irCase struct {
		name   string
		packed irPacked
		want   string
	}
	var cases []irCase
	var totalIRBytes int64
	r := rand.New(rand.NewPCG(1, 0x5a4a))
	reached := map[string]bool{}
	for i := range 50 {
		f := irGen(r, kinds, reached)
		want, err := json.Marshal(f)
		if err != nil {
			t.Fatal(err)
		}
		p, err := irPack(kinds, f)
		if err != nil {
			t.Fatal(err)
		}
		cases = append(cases, irCase{name: fmt.Sprintf("gen-%d", i), packed: p, want: string(want)})
		totalIRBytes += int64(len(want))
	}
	boardDir := filepath.Join(root, "examples", "board", "cmd", "board")
	if u, err := irspike.Load(boardDir); err == nil {
		out, _, _, err := lower.LowerIRSplit(u, false)
		if err != nil {
			t.Fatalf("board LowerIRSplit: %v", err)
		}
		for _, file := range out {
			if filepath.Base(file.Path) != lower.IRFileName {
				continue
			}
			var decoded irFile
			dec := json.NewDecoder(bytes.NewReader(file.Data))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&decoded); err != nil {
				t.Fatalf("board decode %s: %v", file.Path, err)
			}
			p, err := irPack(kinds, &decoded)
			if err != nil {
				t.Fatalf("board pack %s: %v", file.Path, err)
			}
			cases = append(cases, irCase{name: "board:" + file.Path, packed: p, want: string(file.Data)})
			totalIRBytes += int64(len(file.Data))
		}
	}

	ctx := context.Background()
	for _, c := range cases {
		got, err := linked.IRWrite(ctx, c.packed.words, c.packed.strs)
		if err != nil {
			t.Fatalf("%s: IRWrite: %v", c.name, err)
		}
		text, ok := strings.CutPrefix(got, "ok\n")
		if !ok || text != c.want {
			t.Fatalf("%s: IRWrite mismatch: got=%.120q want=%.120q", c.name, got, c.want)
		}
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	prevGC := debug.SetGCPercent(-1)
	defer debug.SetGCPercent(prevGC)

	var instrs, allocs, allocBytes []float64
	for range 3 {
		var m0, m1 runtime.MemStats
		runtime.ReadMemStats(&m0)
		var innerErr error
		n, cerr := opcount.CountThread(func() {
			for _, c := range cases {
				got, err := linked.IRWrite(ctx, c.packed.words, c.packed.strs)
				if err != nil {
					innerErr = err
					return
				}
				if !strings.HasPrefix(got, "ok\n") {
					innerErr = fmt.Errorf("%s failed: %.80q", c.name, got)
					return
				}
			}
		})
		runtime.ReadMemStats(&m1)
		if cerr != nil {
			t.Fatalf("opcount ir: %v", cerr)
		}
		if innerErr != nil {
			t.Fatal(innerErr)
		}
		b := float64(totalIRBytes)
		instrs = append(instrs, float64(n)/b)
		allocs = append(allocs, float64(m1.Mallocs-m0.Mallocs)/b)
		allocBytes = append(allocBytes, float64(m1.TotalAlloc-m0.TotalAlloc)/b)
	}
	irInstr := opcount.Median(instrs)
	irAllocs := opcount.Median(allocs)
	irBytes := opcount.Median(allocBytes)
	t.Logf("ir-write floor (GOGC=off median, %d B): %.2f instr/B, %.4f allocs/B, %.2f bytes/B", totalIRBytes, irInstr, irAllocs, irBytes)
	if irInstr > 52.0 {
		t.Errorf("ir-write instr/B = %.2f exceeds DoD ceiling 52.0 (baseline 54.08)", irInstr)
	}
	if irAllocs > 0.045 {
		t.Errorf("ir-write allocs/B = %.4f exceeds DoD ceiling 0.045 (baseline 0.0612)", irAllocs)
	}
	if irBytes > 10.5 {
		t.Errorf("ir-write bytes/B = %.2f exceeds DoD ceiling 10.5", irBytes)
	}
}

func loadCompilerFloorSources(t *testing.T, root string) ([]string, int64) {
	t.Helper()
	compilerDir := filepath.Join(root, "compiler")
	var paths []string
	var totalBytes int64
	err := filepath.WalkDir(compilerDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".ergo") {
			info, ierr := d.Info()
			if ierr != nil {
				return ierr
			}
			paths = append(paths, path)
			totalBytes += info.Size()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(paths)
	srcs := make([]string, len(paths))
	for i, p := range paths {
		raw, rerr := os.ReadFile(p)
		if rerr != nil {
			t.Fatal(rerr)
		}
		srcs[i] = string(raw)
	}
	return srcs, totalBytes
}

func checkCompilerStaticDoD(t *testing.T, root string) {
	t.Helper()
	compilerDir := filepath.Join(root, "compiler")
	module := ""
	if data, err := os.ReadFile(filepath.Join(compilerDir, "ergo.mod")); err == nil {
		for line := range strings.SplitSeq(string(data), "\n") {
			if name, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
				module = strings.TrimSpace(name)
			}
		}
	}

	type parsedFile struct {
		rel    string
		folder string
		ast    *syntax.File
	}
	var files []parsedFile
	kinds := map[[2]string]string{}

	declKind := func(spec syntax.Expr) string {
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
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		f, perr := syntax.Parse(string(raw))
		if perr != nil {
			return fmt.Errorf("%s: %v", rel, perr)
		}
		folder := filepath.Dir(path)
		files = append(files, parsedFile{rel: rel, folder: folder, ast: f})
		for _, d := range f.Decls {
			if td, ok := d.(*syntax.TypeDecl); ok && td.Name != nil {
				kinds[[2]string{folder, td.Name.Name}] = declKind(td.Spec)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	isFloorOwned := func(rel string) bool {
		return strings.HasPrefix(rel, "compiler/syntax/") ||
			strings.HasPrefix(rel, "compiler/format/") ||
			strings.HasPrefix(rel, "compiler/ir/")
	}
	isSkipFile := func(rel string) bool {
		base := filepath.Base(rel)
		return base == "laws.ergo" || base == "fixture.ergo" || base == "lexerror.ergo" || base == "typeset.ergo"
	}
	scalarPayloads := map[string]bool{
		"compiler/ir/ir.ergo:File.V":       true,
		"compiler/lower/lower.ergo:CInt.I": true,
	}

	var violations []string
	for _, f := range files {
		floorOwned := isFloorOwned(f.rel) && !isSkipFile(f.rel)
		folders := map[string]string{}
		stdDense := ""
		for _, d := range f.ast.Decls {
			id, ok := d.(*syntax.ImportDecl)
			if !ok {
				continue
			}
			for _, s := range id.Specs {
				path := s.Path.Value
				if p, uerr := strconv.Unquote(path); uerr == nil {
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
				what := ""
				switch k := m.Key.(type) {
				case *syntax.StarExpr:
					what = "a borrow"
				case *syntax.Ident:
					if k.Name != "string" {
						if v, exists := kinds[[2]string{f.folder, k.Name}]; exists {
							what = v
						} else if floorOwned {
							what = "non-string key " + k.Name
						}
					}
				case *syntax.SelectorExpr:
					if q, ok := k.X.(*syntax.Ident); ok && folders[q.Name] != "" {
						what = kinds[[2]string{folders[q.Name], k.Sel.Name}]
					} else if floorOwned {
						what = "qualified key"
					}
				case *syntax.IndexExpr:
					if s, ok := k.X.(*syntax.SelectorExpr); ok && (s.Sel.Name == "ID" || s.Sel.Name == "Opt") {
						if q, ok := s.X.(*syntax.Ident); ok && stdDense != "" && q.Name == stdDense {
							what = "a dense id"
						}
					}
					if what == "" && floorOwned {
						what = "indexed key"
					}
				default:
					if floorOwned {
						what = "composite key"
					}
				}
				if what != "" {
					line, _ := f.ast.Position(m.Key.Pos())
					violations = append(violations, fmt.Sprintf("%s:%d [no-pointer-keyed-maps] %s", f.rel, line, what))
				}
				return true
			})
		}

		checkFields := func(typeName string, fl *syntax.FieldList) {
			if fl == nil {
				return
			}
			for _, field := range fl.List {
				if field.Type == nil {
					continue
				}
				for _, nm := range field.Names {
					key := f.rel + ":" + typeName + "." + nm.Name
					if scalarPayloads[key] {
						continue
					}
					switch ty := field.Type.(type) {
					case *syntax.Ident:
						if ty.Name == "int32" || ty.Name == "int64" {
							line, _ := f.ast.Position(ty.Pos())
							violations = append(violations, fmt.Sprintf("%s:%d [dense-uint32-ids] %s.%s uses %s", f.rel, line, typeName, nm.Name, ty.Name))
						}
					case *syntax.StarExpr:
						line, _ := f.ast.Position(ty.Pos())
						violations = append(violations, fmt.Sprintf("%s:%d [dense-uint32-ids] %s.%s uses pointer field", f.rel, line, typeName, nm.Name))
					case *syntax.ArrayType:
						if id, ok := ty.Elt.(*syntax.Ident); ok {
							if id.Name == "int32" || id.Name == "int64" || (floorOwned && id.Name == "int" && typeName != "census") {
								line, _ := f.ast.Position(ty.Pos())
								violations = append(violations, fmt.Sprintf("%s:%d [dense-uint32-ids] %s.%s uses []%s", f.rel, line, typeName, nm.Name, id.Name))
							}
						} else if _, isStar := ty.Elt.(*syntax.StarExpr); isStar {
							line, _ := f.ast.Position(ty.Pos())
							violations = append(violations, fmt.Sprintf("%s:%d [dense-uint32-ids] %s.%s uses pointer slice", f.rel, line, typeName, nm.Name))
						}
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

		if floorOwned {
			for _, d := range f.ast.Decls {
				fd, ok := d.(*syntax.FuncDecl)
				if !ok || fd.Body == nil || fd.Name == nil {
					continue
				}
				fnName := fd.Name.Name
				if strings.HasPrefix(fnName, "test") || strings.HasSuffix(fnName, "Test") || fnName == "sourcesOf" || fnName == "censusParents" || fnName == "FixLines" {
					continue
				}
				callsSelfOrChildren := false
				syntax.Inspect(fd.Body, func(n syntax.Node) bool {
					if c, ok := n.(*syntax.CallExpr); ok {
						if id, ok := c.Fun.(*syntax.Ident); ok && (id.Name == fnName || id.Name == "children") {
							callsSelfOrChildren = true
						}
						if sel, ok := c.Fun.(*syntax.SelectorExpr); ok && sel.Sel.Name == "Children" {
							callsSelfOrChildren = true
						}
					}
					return true
				})
				var checkNode func(node syntax.Node, inLoop bool)
				checkNode = func(node syntax.Node, inLoop bool) {
					if node == nil {
						return
					}
					syntax.Inspect(node, func(n syntax.Node) bool {
						switch x := n.(type) {
						case *syntax.ForStmt:
							checkNode(x.Cond, true)
							checkNode(x.Body, true)
							return false
						case *syntax.RangeStmt:
							checkNode(x.X, inLoop)
							checkNode(x.Body, true)
							return false
						case *syntax.CallExpr:
							if sel, ok := x.Fun.(*syntax.SelectorExpr); ok {
								if q, ok := sel.X.(*syntax.Ident); ok && q.Name == "slices" && sel.Sel.Name == "Repeat" {
									line, _ := f.ast.Position(x.Pos())
									violations = append(violations, fmt.Sprintf("%s:%d [no-per-node-scratch-allocs] slices.Repeat in %s", f.rel, line, fnName))
								}
							}
							if inLoop || callsSelfOrChildren {
								for _, arg := range x.Args {
									if lit, ok := arg.(*syntax.CompositeLit); ok && len(lit.Elts) == 0 {
										if _, isArr := lit.Type.(*syntax.ArrayType); isArr {
											line, _ := f.ast.Position(arg.Pos())
											violations = append(violations, fmt.Sprintf("%s:%d [no-per-node-scratch-allocs] empty slice literal in %s", f.rel, line, fnName))
										}
									}
								}
							}
						}
						return true
					})
				}
				checkNode(fd.Body, false)
			}
		}
	}

	if len(violations) > 0 {
		t.Fatalf("compiler static DoD violations (%d):\n  %s", len(violations), strings.Join(violations, "\n  "))
	}
}
