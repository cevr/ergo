package eval

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestEveryNativeReadsItsDeclaration holds each native of the evaluator,
// pure or effectful, to the std declaration it runs for: nativeTables.reads
// names what the native reads at each argument position, and the test
// compares it with the parameters of every std function that reaches it
// (nativeOf), and of every import go function a std body calls that has a
// goLib native: the same count, and at each position the kind the
// evaluator gives a value of the declared type (zeroOf), a function's
// arity, a host type's name, or T where the native reads no kind. Every
// native has a reads entry, and every entry a native. A declaration that
// adds, drops or moves a parameter under a native that was not changed
// with it fails here, at the fast tier, where it was a panic of a run
// (EVAL-RAWPATH: http's serve passed 8 arguments to a 9-parameter handler,
// and send read a list at a position its declaration had come to give a
// string). Mutants only it kills (testdata/mutants.json):
// arity-serve-handler-short, arity-send-moved.
func TestEveryNativeReadsItsDeclaration(t *testing.T) {
	tb := loadedNatives()
	var bad []string
	for key := range tb.std {
		if _, ok := tb.reads[key]; !ok {
			bad = append(bad, key+": a native with no reads entry")
		}
	}
	for key := range tb.goLib {
		if _, ok := tb.reads[key]; !ok {
			bad = append(bad, key+" (goLib): a native with no reads entry")
		}
	}
	for key := range tb.reads {
		if tb.std[key] == nil && tb.goLib[key] == nil {
			bad = append(bad, key+": a reads entry with no native")
		}
	}
	compared := 0
	compare := func(key, at string, f *check.Func) {
		reads, ok := tb.reads[key]
		if !ok || f.Sig == nil {
			return
		}
		compared++
		got := strings.Fields(reads)
		want := make([]string, len(f.Sig.Params))
		for i, p := range f.Sig.Params {
			want[i] = paramReads(p.Type)
		}
		if len(got) != len(want) {
			bad = append(bad, fmt.Sprintf("%s (%s): the native reads %d arguments %q, the declaration has %d %q", key, at, len(got), reads, len(want), strings.Join(want, " ")))
			return
		}
		for i := range got {
			if got[i] != "T" && got[i] != want[i] {
				bad = append(bad, fmt.Sprintf("%s (%s): argument %d (%s): the native reads %s, the declaration gives %s", key, at, i, f.Sig.Params[i].Name, got[i], want[i]))
			}
		}
	}
	for _, path := range stdPaths(t) {
		pkg, err := check.Std.Import(path)
		if err != nil {
			t.Fatalf("check.Std.Import(%q): %v", path, err)
		}
		for _, f := range pkg.Funcs {
			if key, ok := stdNativeKey(f); ok {
				compare(key, path, f)
			}
		}
		for _, c := range pkg.Info.Calls {
			f := c.Func
			if f == nil {
				continue
			}
			// A call reaches the prelude's natives and the built-in types'
			// operations (builtin.ergo), which no std package declares.
			if key, ok := stdNativeKey(f); ok {
				compare(key, "called from "+path, f)
			}
			if f.Decl == nil || f.Decl.Extern == nil || f.Decl.Extern.Target == nil || f.Pkg == nil || f.Pkg.Std() {
				continue
			}
			goPath, name := f.Decl.Extern.Split()
			if tb.goLib[goPath+"."+name] != nil {
				compare(goPath+"."+name, "import go, from "+path, f)
			}
		}
	}
	if compared < len(tb.std)/2 {
		t.Errorf("compared %d natives with a declaration, of %d: the walk lost the std functions", compared, len(tb.std))
	}
	sort.Strings(bad)
	bad = slices.Compact(bad)
	if len(bad) > 0 {
		t.Errorf("%d natives disagree with their declarations (natives.go, nativeTables.reads):\n%s", len(bad), strings.Join(bad, "\n"))
	}
}

// stdNativeKey is the key of f's std native in nativeTables, as nativeOf
// finds it, when f has one.
func stdNativeKey(f *check.Func) (string, bool) {
	if nativeOf(f) == nil {
		return "", false
	}
	if k, ok := collNatives()[check.CollOpOf(f)]; ok {
		return k, true
	}
	return f.Pkg.Path + "." + f.Name, true
}

// paramReads is the reads word of a parameter of type ty: what a native
// that reads it takes there.
func paramReads(ty check.Type) string {
	r := rtype{t: ty}
	if _, ok := r.t.(*check.TypeParam); ok {
		return "T"
	}
	if p, ok := r.t.(*check.Pointer); ok {
		return "mut:" + paramReads(p.Elem)
	}
	if s, ok := r.under().t.(*check.Signature); ok {
		return fmt.Sprintf("func/%d", len(s.Params))
	}
	if z, ok := zeroOf(r); ok {
		return strings.Replace(fmt.Sprintf("%T", z), "eval.", "", 1)
	}
	if n, ok := r.t.(*check.Named); ok && n.Obj.Pkg != nil {
		return "host:" + n.Obj.Pkg.Path + "." + n.Obj.Name
	}
	return "?" + ty.String()
}
