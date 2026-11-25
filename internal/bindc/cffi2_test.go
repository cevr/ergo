package bindc

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

type bindcGoImporter struct {
	gb *check.GoBindings
}

func (g *bindcGoImporter) Import(path string) (*check.Package, error) { return nil, nil }
func (g *bindcGoImporter) GoBindings() *check.GoBindings              { return g.gb }

func checkWithFixtureBinding(t *testing.T, ergoBinding, src string) []check.Diagnostic {
	t.Helper()
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, src)
	}
	f.Name = "main.ergo"
	gb := check.NewGoBindings(func(path string) (string, bool) {
		if path == "ergo.local/bind/fixture" {
			return ergoBinding, true
		}
		return "", false
	})
	_, ds := check.Check("main", []*syntax.File{f}, &bindcGoImporter{gb: gb})
	return ds
}

func hasDiag(ds []check.Diagnostic, code, substr string) bool {
	for _, d := range ds {
		if d.Code == code && (substr == "" || strings.Contains(d.Message, substr)) {
			return true
		}
	}
	return false
}

// TestCBindingEmittedGoldensCheck verifies that the checked Ergo C bindings
// emitted by ErgoText for both fixture and opentui pass internal/sketch/check's
// GoBindings.ResolveAll with zero diagnostics.
func TestCBindingEmittedGoldensCheck(t *testing.T) {
	for _, h := range headers {
		t.Run(h.header, func(t *testing.T) {
			m := parseHeader(t, h.dir, h.header)
			b := bindHeader(t, h.dir, h.header, m)
			out, err := ErgoText(b)
			if err != nil {
				t.Fatalf("ErgoText: %v", err)
			}
			ip := b.Contract.Go.ImportPath
			gb := check.NewGoBindings(func(path string) (string, bool) {
				if path == ip {
					return string(out), true
				}
				return "", false
			})
			if !gb.ResolveAll(ip) {
				t.Fatalf("ResolveAll(%q) = false", ip)
			}
			if ds := gb.Diagnostics(); len(ds) > 0 {
				t.Fatalf("ResolveAll(%q) diagnostics:\n%s", ip, strings.Join(ds, "\n"))
			}
		})
	}
}

// TestCBindingHandleOwnership checks emitted fixture.ergo through the Ergo
// checker: Context must be a Closer resource, so use-after-destroy and
// discarded Context handles are refused as S108 while scope-exit release
// checks clean. Kills cffi2-handle-not-closer.
func TestCBindingHandleOwnership(t *testing.T) {
	m := parseHeader(t, "testdata/fixture", "fixture.h")
	b := bindHeader(t, "testdata/fixture", "fixture.h", m)
	out, err := ErgoText(b)
	if err != nil {
		t.Fatalf("ErgoText: %v", err)
	}
	binding := string(out)

	// 1. Valid: Context acquired, lent to NodeCreate/NodeCount, auto-released at scope exit.
	validSrc := `import go ergo.local/bind/fixture

func run() uint32 fails go.Error needs go.Foreign {
	let ctx = try fixture.ContextCreate(fixture.ContextOptions{NodeCapacity: 4, Strict: true})
	let _ = try fixture.NodeCreate(ctx, 1)
	try fixture.NodeCount(ctx)
}
`
	if ds := checkWithFixtureBinding(t, binding, validSrc); len(ds) != 0 {
		t.Fatalf("valid C handle program failed check: %v", ds)
	}

	// 2. Use-after-destroy via explicit ContextDestroy must be refused S108.
	uafSrc := `import go ergo.local/bind/fixture

func bad() uint32 fails go.Error needs go.Foreign {
	let ctx = try fixture.ContextCreate(fixture.ContextOptions{NodeCapacity: 4, Strict: true})
	try fixture.ContextDestroy(ctx)
	try fixture.NodeCount(ctx)
}
`
	if ds := checkWithFixtureBinding(t, binding, uafSrc); !hasDiag(ds, "S108", "") {
		t.Fatalf("expected S108 for use-after-destroy of Context, got %v", ds)
	}

	// 3. Discarded Context handle (leak) must be refused S108.
	leakSrc := `import go ergo.local/bind/fixture

func leak() fails go.Error needs go.Foreign {
	let _ = try fixture.ContextCreate(fixture.ContextOptions{NodeCapacity: 4, Strict: true})
}
`
	if ds := checkWithFixtureBinding(t, binding, leakSrc); !hasDiag(ds, "S108", "") {
		t.Fatalf("expected S108 for discarded Context handle, got %v", ds)
	}
}

// TestCBindingRequiresGoForeign checks emitted fixture.ergo through the Ergo
// checker: calling any C-bound function without `needs go.Foreign` (pure func
// or unit test) must be refused. Kills cffi2-drop-foreign-row.
func TestCBindingRequiresGoForeign(t *testing.T) {
	m := parseHeader(t, "testdata/fixture", "fixture.h")
	b := bindHeader(t, "testdata/fixture", "fixture.h", m)
	out, err := ErgoText(b)
	if err != nil {
		t.Fatalf("ErgoText: %v", err)
	}
	binding := string(out)

	pureSrc := `import go ergo.local/bind/fixture

func pure() uint32 fails go.Error {
	try fixture.AbiVersion()
}
`
	ds := checkWithFixtureBinding(t, binding, pureSrc)
	if !hasDiag(ds, "S102", "go.Foreign") {
		t.Fatalf("expected S102 requiring go.Foreign from pure function, got %v", ds)
	}
}
