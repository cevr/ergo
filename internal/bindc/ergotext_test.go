package bindc

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	format "ergo.local/prototype/internal/hostsyntax"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestErgoTextParses: both golden Ergo texts parse with
// internal/sketch/syntax and are in canonical internal/hostsyntax form;
// each carries `// ergo:bind c <path> <header_sha256> <contract_sha256> <platforms>`,
// `Load`, `Status` + status code constants, `implements Closer` on destroyable
// handles, `// ergo:cdata` on flat records, one `extern go ... needs go.Foreign`
// per typed function (target <path>.<GoName>), and one `// not bound` line per
// raw-only or callback-taking function.
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
			wantHeader := "// ergo:bind c " + b.Contract.Go.ImportPath + " " + b.HeaderSHA256 + " " + b.ContractSHA256 + " " + cBindPlatforms
			if !strings.Contains(src, wantHeader) {
				t.Errorf("%s missing provenance header %q", path, wantHeader)
			}
			var externs []string
			for _, d := range f.Decls {
				switch d := d.(type) {
				case *syntax.ImportDecl:
					t.Errorf("%s: C binding should not self-import Go packages", path)
				case *syntax.FuncDecl:
					if d.Extern == nil || d.Body != nil {
						t.Errorf("%s: func %s is not an extern", path, d.Name.Name)
						continue
					}
					if d.Type.Needs == nil {
						t.Errorf("%s: extern %s is missing needs go.Foreign", path, d.Name.Name)
					}
					if d.Recv == nil {
						externs = append(externs, d.Extern.Target.Value+" "+d.Name.Name)
					}
				}
			}
			want := []string{b.Contract.Go.ImportPath + ".Load Load"}
			for _, cb := range b.Callbacks {
				if cb.Bound() {
					want = append(want, b.Contract.Go.ImportPath+".LiveCallbacks LiveCallbacks")
					break
				}
			}
			notBound := 0
			for _, bf := range b.Funcs {
				why := bf.ErgoUnbound()
				if why == "" {
					want = append(want, b.Contract.Go.ImportPath+"."+bf.GoName+" "+bf.GoName)
					continue
				}
				notBound++
				if !strings.Contains(src, "// not bound: "+bf.C.Name+" (") {
					t.Errorf("%s has no not-bound line for %s", path, bf.C.Name)
				}
				if bf.Sig != nil {
					t.Errorf("%s: typed function %s should be bound, got unbound reason %q", path, bf.C.Name, why)
				}
			}
			if !slices.Equal(externs, want) {
				t.Errorf("%s: %d externs, want %d (Load + one per typed function, in order)", path, len(externs), len(want))
			}
			if n := strings.Count(src, "// not bound: "); n != notBound {
				t.Errorf("%s: %d not-bound lines, want %d", path, n, notBound)
			}
			// Status constants: one per code, no success code.
			for _, x := range b.Codes {
				if v := "let " + b.CodeGoName(x) + " Status"; !strings.Contains(src, v) {
					t.Errorf("%s: missing status constant %s", path, v)
				}
			}
			// Destroyable handles declare implements Closer.
			for _, hd := range b.Contract.Handles {
				if hd.Destroy != "" {
					gn := b.GoTypes[hd.Type]
					if w := "type " + gn + " {} implements Closer"; !strings.Contains(src, w) {
						t.Errorf("%s: handle %s missing %q", path, gn, w)
					}
				}
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
