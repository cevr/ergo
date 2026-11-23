package check_test

import (
	"fmt"
	"go/importer"
	"go/types"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/std/gostd"
)

// TestGoInterfaceMethodSets compares the checker at a Go call boundary
// against go/types. Each seed permutes the mixed method sets and varies
// the source names. In particular, a native ReaderAt's ReadAt has more
// than one operand, so undoing write adaptation only on single-parameter
// methods is not enough.
func TestGoInterfaceMethodSets(t *testing.T) {
	seed := uint64(1)
	if s := os.Getenv("ERGO_GO_INTERFACE_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seed = n
	}
	rng := rand.New(rand.NewPCG(seed, 278))
	type member struct{ path, name string }
	values := []member{
		{"os", "File"}, {"bytes", "Buffer"}, {"strings", "Reader"},
		{"strings", "Builder"}, {"strings", "Replacer"}, {"io", "ReadWriter"},
	}
	interfaces := []member{
		{"io", "Reader"}, {"io", "ReaderAt"}, {"io", "ReadWriter"},
		{"io", "Writer"}, {"io", "StringWriter"}, {"io", "Seeker"}, {"fmt", "Stringer"},
	}
	model := importer.Default()
	pkgs := map[string]*types.Package{}
	goType := func(m member) types.Type {
		if pkgs[m.path] == nil {
			p, err := model.Import(m.path)
			if err != nil {
				t.Fatal(err)
			}
			pkgs[m.path] = p
		}
		return pkgs[m.path].Scope().Lookup(m.name).Type()
	}
	for _, i := range rng.Perm(len(values)) {
		v := values[i]
		native := goType(v)
		_, isInterface := native.Underlying().(*types.Interface)
		for _, j := range rng.Perm(len(interfaces)) {
			it := interfaces[j]
			if v == it {
				continue // identity does not inspect the method set
			}
			iface := goType(it).Underlying().(*types.Interface)
			for _, ptr := range []bool{false, true} {
				if ptr && isInterface {
					continue // pointers to Go interfaces have no Ergo binding
				}
				t.Run(fmt.Sprintf("%s.%s/%s.%s/ptr=%t", v.path, v.name, it.path, it.name, ptr), func(t *testing.T) {
					from, want := v.path+"."+v.name, types.Implements(native, iface)
					if ptr {
						from, want = "go.Ref["+from+"]", types.Implements(types.NewPointer(native), iface)
					}
					mode := ""
					if v == (member{"os", "File"}) {
						mode = "own " // the binding declares File a resource
					}
					const shim = "example.com/take"
					binding := fmt.Sprintf("import go %s\nextern go %s.Take func Take(x own %s.%s) needs go.Foreign\n", it.path, shim, it.path, it.name)
					// Only the argument conversion is under test: Take is a
					// binding stub, and no Go body is built or invoked.
					imp := drvImporter{check.NewGoBindings(func(path string) (string, bool) {
						if path == shim {
							return binding, true
						}
						return gostd.Binding(path)
					})}
					x := fmt.Sprintf("x%d", rng.Uint32())
					src := fmt.Sprintf("import go %s\nimport go %s\nfunc Probe(%s %s%s) needs Ambient | go.Foreign { take.Take(%s) }\n", v.path, shim, x, mode, from, x)
					ds := dsCheck(t, src, imp)
					got := len(ds) == 0
					if got != want || len(ds) > 0 && (len(ds) != 1 || ds[0].Code != "S108" || !strings.Contains(ds[0].Message, "in argument")) {
						t.Errorf("seed %d: go/types=%t, Ergo=%t (%v)\n%s\nbinding:\n%s\nreplay: ERGO_GO_INTERFACE_SEED=%d go test -run '^TestGoInterfaceMethodSets$' ./internal/sketch/check", seed, want, got, ds, src, binding, seed)
					}
				})
			}
		}
	}
}
