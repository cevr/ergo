package bindc

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/format"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestErgoTextParses: both golden Ergo texts parse with
// internal/sketch/syntax and are in canonical internal/sketch/format form;
// each holds `import go <path>`, one extern per typed function (target
// <path>.<GoName>, no needs row) and one `// not bound` line per raw-only
// function; and a control with a broken extern does not parse.
func TestErgoTextParses(t *testing.T) {
	for _, h := range headers {
		t.Run(h.header, func(t *testing.T) {
			m := parseHeader(t, h.dir, h.header)
			b := bindHeader(t, h.dir, h.header, m)
			path := filepath.Join(h.dir, strings.TrimSuffix(h.header, ".h")+".ergo")
			src := string(readInput(t, path))
			f, err := syntax.Parse(src)
			if err != nil {
				t.Fatalf("%s does not parse:\n%v", path, err)
			}
			if got, err := format.Source(src); err != nil || got != src {
				t.Errorf("%s is not in canonical format (%v); first difference: %s", path, err, firstDiff([]byte(src), []byte(got)))
			}
			var imports, externs []string
			for _, d := range f.Decls {
				switch d := d.(type) {
				case *syntax.ImportDecl:
					for _, s := range d.Specs {
						imports = append(imports, s.Path.Value)
					}
				case *syntax.FuncDecl:
					if d.Extern == nil || d.Body != nil {
						t.Errorf("%s: func %s is not an extern", path, d.Name.Name)
						continue
					}
					if d.Type.Needs != nil {
						t.Errorf("%s: extern %s has a needs row", path, d.Name.Name)
					}
					externs = append(externs, d.Extern.Target.Value+" "+d.Name.Name)
				}
			}
			if !slices.Equal(imports, []string{b.Contract.Go.ImportPath}) {
				t.Errorf("%s imports %v, want [%s]", path, imports, b.Contract.Go.ImportPath)
			}
			var want []string
			notBound := 0
			for _, bf := range b.Funcs {
				if bf.Sig != nil {
					want = append(want, b.Contract.Go.ImportPath+"."+bf.GoName+" "+bf.GoName)
				} else {
					notBound++
					if !strings.Contains(src, "// not bound: "+bf.C.Name+" (") {
						t.Errorf("%s has no not-bound line for %s", path, bf.C.Name)
					}
				}
			}
			if !slices.Equal(externs, want) {
				t.Errorf("%s: %d externs, want %d (one per typed function, in order)", path, len(externs), len(want))
			}
			if n := strings.Count(src, "// not bound: "); n != notBound {
				t.Errorf("%s: %d not-bound lines, want %d", path, n, notBound)
			}
			// The Status union: a variant per code, no success arm.
			for _, x := range b.Codes {
				if v := strings.TrimPrefix(b.CodeGoName(x), "Status") + "{}"; !strings.Contains(src, v) {
					t.Errorf("%s: the Status union has no variant %s", path, v)
				}
			}
			if v := goCamel(strings.TrimPrefix(b.Contract.Status.Success, b.Contract.Status.MacroPrefix)) + "{}"; strings.Contains(src, v) {
				t.Errorf("%s: the Status union has the success arm %s", path, v)
			}
			// Records hold no autofill field.
			for _, a := range b.Contract.Autofill {
				if f := "\t" + goCamel(a.Field) + " "; strings.Contains(src, f) {
					t.Errorf("%s: a record has the autofill field %s", path, a.Field)
				}
			}
			// The control: an extern without its target does not parse.
			bad := strings.Replace(src, "extern go "+b.Contract.Go.ImportPath+".", "extern go ", 1)
			if _, err := syntax.Parse(bad); err == nil {
				t.Errorf("%s: an extern without its import path parses", path)
			}
		})
	}
}
