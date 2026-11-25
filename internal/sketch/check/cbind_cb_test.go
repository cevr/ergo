package check

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

func testCBindCallbackFile(path string) string {
	return testCBindingHeader(path) + `
extern go ` + path + `.Load func Load(path string) fails go.Error needs go.Foreign
extern go ` + path + `.LiveCallbacks func LiveCallbacks() int needs go.Foreign

extern go ` + path + `.Context type Context {} implements Closer
extern go ` + path + `.Context.Close func (h own *Context) Close() fails go.Error needs go.Foreign

// ergo:cdata
extern go ` + path + `.Node type Node {
	ContextId  uint64,
	Slot       uint32,
	Generation uint32,
}

// ergo:cdata
extern go ` + path + `.Point type Point {
	X float32,
	Y float32,
}

// ergo:cdata
extern go ` + path + `.Visit type Visit {
	Node   Node,
	Ticket uint64,
}

extern go ` + path + `.ContextCreate func ContextCreate() go.Ref[Context] fails go.Error needs go.Foreign
extern go ` + path + `.NodeCreate func NodeCreate(ctx *Context, kind uint32) Node fails go.Error needs go.Foreign
// ergo:reads name
extern go ` + path + `.NodeSetName func NodeSetName(ctx *Context, node Node, name []byte) fails go.Error needs go.Foreign
// ergo:scoped writer.data
// ergo:reads text
extern go ` + path + `.ContextWrite func ContextWrite[CE, CR](ctx *Context, text []byte, maxBytes uint32, writer func(data []byte) int64 fails CE needs CR) uint32 fails CE | go.Error needs CR | go.Foreign
extern go ` + path + `.NodeSetMeasure func NodeSetMeasure(ctx *Context, node Node, measure own func(width float32, widthMode uint32, height float32, heightMode uint32) [2]float32) fails go.Error needs go.Foreign
`
}

// TestCCallbackScopedBorrow verifies the CFFI-3 scoped-borrow non-escape
// property over seeded callback bodies attempting every escape vector
// (direct return, tail return, tuple/record/slice return, outer let mut store,
// Append into outer [][]byte, escaping closure capture, own parameter, index
// write, mutable alias, alias/sub-slice escape, opaque callback value) while
// accepting read-only indexing, len/.Len(), iteration, local sub-slice reads,
// .Clone(), slices.Clone, and // ergo:reads calls.
// Kills cffi3-scoped-slice-returns and cffi3-scoped-slice-captured.
func TestCCallbackScopedBorrow(t *testing.T) {
	const pkgPath = "example.test/cffi/cb"
	binding := testCBindCallbackFile(pkgPath)

	type vector struct {
		name      string
		wantError bool
		wantCode  string
		preamble  string
		body      string
	}

	escapeVectors := []vector{
		{
			name:      "direct-return-via-inner-frame",
			wantError: true,
			wantCode:  CodeType,
			body: `
	let mut saved = []byte{}
	let _ = try cb.ContextWrite(ctx, src, 4, func(data []byte) int64 {
		let leaked = (func() []byte { return data })()
		saved = leaked
		int64(len(data))
	})`,
		},
		{
			name:      "tail-return-via-inner-frame",
			wantError: true,
			wantCode:  CodeType,
			body: `
	let mut saved = []byte{}
	let _ = try cb.ContextWrite(ctx, src, 4, func(data []byte) int64 {
		let leaked = (func() []byte { data })()
		saved = leaked
		int64(len(data))
	})`,
		},
		{
			name:      "tuple-return",
			wantError: true,
			wantCode:  CodeType,
			body: `
	let _ = try cb.ContextWrite(ctx, src, 4, func(data []byte) int64 {
		let _ = (data, 1)
		int64(len(data))
	})`,
		},
		{
			name:      "record-store",
			wantError: true,
			wantCode:  CodeType,
			preamble:  "type Holder { Buf []byte }\n",
			body: `
	let _ = try cb.ContextWrite(ctx, src, 4, func(data []byte) int64 {
		let _ = Holder{Buf: data}
		int64(len(data))
	})`,
		},
		{
			name:      "slice-literal",
			wantError: true,
			wantCode:  CodeType,
			body: `
	let _ = try cb.ContextWrite(ctx, src, 4, func(data []byte) int64 {
		let _ = [][]byte{data}
		int64(len(data))
	})`,
		},
		{
			name:      "outer-let-mut-store",
			wantError: true,
			wantCode:  CodeType,
			body: `
	let mut outer = []byte{}
	let _ = try cb.ContextWrite(ctx, src, 4, func(data []byte) int64 {
		outer = data
		int64(len(data))
	})
	let _ = len(outer)`,
		},
		{
			name:      "append-into-outer-slices",
			wantError: true,
			wantCode:  CodeType,
			body: `
	let mut chunks = [][]byte{}
	let _ = try cb.ContextWrite(ctx, src, 4, func(data []byte) int64 {
		chunks = chunks.Append(data)
		int64(len(data))
	})
	let _ = len(chunks)`,
		},
		{
			name:      "escaping-closure-capture",
			wantError: true,
			wantCode:  CodeType,
			body: `
	let mut keeper = func() byte { byte(0) }
	let _ = try cb.ContextWrite(ctx, src, 4, func(data []byte) int64 {
		let f func() byte = func() byte { data[0] }
		keeper = f
		int64(len(data))
	})
	let _ = keeper()`,
		},
		{
			name:      "pass-to-own-param",
			wantError: true,
			wantCode:  CodeType,
			preamble:  "func sink(b own []byte) int { len(b) }\n",
			body: `
	let _ = try cb.ContextWrite(ctx, src, 4, func(data []byte) int64 {
		let _ = sink(data)
		int64(len(data))
	})`,
		},
		{
			name:      "index-write-mutation",
			wantError: true,
			wantCode:  CodeType,
			body: `
	let _ = try cb.ContextWrite(ctx, src, 4, func(data []byte) int64 {
		data[0] = byte(1)
		int64(len(data))
	})`,
		},
		{
			name:      "let-mut-alias",
			wantError: true,
			wantCode:  CodeType,
			body: `
	let _ = try cb.ContextWrite(ctx, src, 4, func(data []byte) int64 {
		let mut alias = data
		let _ = len(alias)
		int64(len(data))
	})`,
		},
		{
			name:      "immutable-alias-escape",
			wantError: true,
			wantCode:  CodeType,
			body: `
	let mut outer = []byte{}
	let _ = try cb.ContextWrite(ctx, src, 4, func(data []byte) int64 {
		let alias = data
		outer = alias
		int64(len(data))
	})
	let _ = len(outer)`,
		},
		{
			name:      "subslice-escape",
			wantError: true,
			wantCode:  CodeType,
			body: `
	let mut outer = []byte{}
	let _ = try cb.ContextWrite(ctx, src, 4, func(data []byte) int64 {
		let sub = data[0:1]
		outer = sub
		int64(len(data))
	})
	let _ = len(outer)`,
		},
		{
			name:      "subslice-direct-outer-store",
			wantError: true,
			wantCode:  CodeType,
			body: `
	let mut outer = []byte{}
	let _ = try cb.ContextWrite(ctx, src, 4, func(data []byte) int64 {
		outer = data[0:1]
		int64(len(data))
	})
	let _ = len(outer)`,
		},
		{
			name:      "same-pkg-func-escaping",
			wantError: true,
			wantCode:  CodeType,
			preamble:  "type Box { B []byte }\nfunc badWriter(data []byte) int64 {\n\tlet _ = Box{B: data}\n\tint64(len(data))\n}\n",
			body: `
	let _ = try cb.ContextWrite(ctx, src, 4, badWriter)`,
		},
		{
			name:      "opaque-callback-refused",
			wantError: true,
			wantCode:  CodeType,
			preamble:  "func callWith(ctx *cb.Context, src []byte, f func(data []byte) int64) uint32 fails go.Error needs go.Foreign {\n\ttry cb.ContextWrite(ctx, src, 4, f)\n}\n",
			body: `
	let _ = try callWith(ctx, src, func(data []byte) int64 { int64(len(data)) })`,
		},
	}

	safeVectors := []vector{
		{
			name: "read-index-len-loop-subslice",
			body: `
	let mut sum int64 = 0
	let _ = try cb.ContextWrite(ctx, src, 4, func(data []byte) int64 {
		let n = len(data)
		let m = data.Len()
		if n > 0 && m > 0 {
			let first = data[0]
			sum = sum + int64(first)
			let sub = data[0:1]
			sum = sum + int64(sub[0])
		}
		for _, b in data {
			sum = sum + int64(b)
		}
		int64(n)
	})
	let _ = sum`,
		},
		{
			name: "clone-method-escapes-safely",
			body: `
	let mut chunks = [][]byte{}
	let _ = try cb.ContextWrite(ctx, src, 4, func(data []byte) int64 {
		let owned = data.Clone()
		chunks = chunks.Append(owned)
		int64(len(data))
	})
	let _ = len(chunks)`,
		},
		{
			name: "go-reads-call-accepted",
			body: `
	let node = try cb.NodeCreate(ctx, 1)
	let _ = try cb.ContextWrite(ctx, src, 4, func(data []byte) int64 {
		try cb.NodeSetName(ctx, node, data)
		int64(len(data))
	})`,
		},
		{
			name:     "same-pkg-func-safe",
			preamble: "func okWriter(data []byte) int64 {\n\tlet mut total int64 = 0\n\tfor _, b in data {\n\t\ttotal = total + int64(b)\n\t}\n\tlet _ = total\n\tint64(len(data))\n}\n",
			body: `
	let _ = try cb.ContextWrite(ctx, src, 4, okWriter)`,
		},
	}

	// Run deterministic matrix plus seeded shuffled trials over escape and safe vectors.
	all := append(append([]vector(nil), escapeVectors...), safeVectors...)
	r := rand.New(rand.NewPCG(0xcff13, 0x5c09ed))
	perm := r.Perm(len(all))
	for _, idx := range perm {
		tc := all[idx]
		t.Run(tc.name, func(t *testing.T) {
			mainSrc := fmt.Sprintf(`import go %s

%s
func run() fails go.Error needs go.Foreign {
	let ctx = try cb.ContextCreate()
	let src = []byte{1, 2, 3, 4}
%s
}
`, pkgPath, tc.preamble, tc.body)
			_, ds := checkCBindProgram(t, pkgPath, binding, mainSrc, nil)
			if tc.wantError {
				if !hasDiagCode(ds, tc.wantCode) {
					t.Fatalf("expected %s diagnostic for %s, got: %v\n%s", tc.wantCode, tc.name, ds, mainSrc)
				}
			} else if len(ds) != 0 {
				t.Fatalf("expected clean check for %s, got: %v\n%s", tc.name, ds, mainSrc)
			}
		})
	}
}

// TestCCallbackRegisteredLifetime verifies that ScopeRegistered callbacks
// (`own func`) refuse capturing a `with`-bound capability (S102), refuse
// capturing a frame-local borrow (S108), accept pure value captures, and that
// tampered bindings putting `needs`/`fails` on an `own func` C callback are
// rejected as S108.
func TestCCallbackRegisteredLifetime(t *testing.T) {
	const pkgPath = "example.test/cffi/reg"
	binding := testCBindCallbackFile(pkgPath)

	t.Run("captures-with-capability-refused", func(t *testing.T) {
		mainSrc := fmt.Sprintf(`import go %s

type Meter {
	Tick() float32,
}

type StaticMeter {} implements Meter
func Tick(m StaticMeter) float32 { float32(1.0) }

func run() fails go.Error needs go.Foreign {
	let ctx = try reg.ContextCreate()
	let node = try reg.NodeCreate(ctx, 1)
	with context { Meter: StaticMeter } {
		try reg.NodeSetMeasure(ctx, node, func(w float32, wm uint32, h float32, hm uint32) [2]float32 {
			let t = Meter.Tick()
			[2]float32{w + t, h}
		})
	}
}
`, pkgPath)
		_, ds := checkCBindProgram(t, pkgPath, binding, mainSrc, nil)
		if !hasDiagCode(ds, "S102") {
			t.Fatalf("expected S102 when ScopeRegistered callback captures with-bound capability, got: %v", ds)
		}
	})

	t.Run("captures-local-borrow-refused", func(t *testing.T) {
		mainSrc := fmt.Sprintf(`import go %s

func run() fails go.Error needs go.Foreign {
	let ctx = try reg.ContextCreate()
	let node = try reg.NodeCreate(ctx, 1)
	let mut scale float32 = 2.0
	let p = &scale
	try reg.NodeSetMeasure(ctx, node, func(w float32, wm uint32, h float32, hm uint32) [2]float32 {
		[2]float32{w * *p, h}
	})
}
`, pkgPath)
		_, ds := checkCBindProgram(t, pkgPath, binding, mainSrc, nil)
		if !hasDiagCode(ds, CodeType) {
			t.Fatalf("expected S108 when ScopeRegistered callback captures local borrow, got: %v", ds)
		}
	})

	t.Run("pure-value-capture-accepted", func(t *testing.T) {
		mainSrc := fmt.Sprintf(`import go %s

func run() fails go.Error needs go.Foreign {
	let ctx = try reg.ContextCreate()
	let node = try reg.NodeCreate(ctx, 1)
	let scale float32 = 2.0
	try reg.NodeSetMeasure(ctx, node, func(w float32, wm uint32, h float32, hm uint32) [2]float32 {
		[2]float32{w * scale, h + float32(1.0)}
	})
}
`, pkgPath)
		_, ds := checkCBindProgram(t, pkgPath, binding, mainSrc, nil)
		if len(ds) != 0 {
			t.Fatalf("pure ScopeRegistered callback should check clean, got: %v", ds)
		}
	})

	t.Run("tampered-registered-cb-with-needs-refused", func(t *testing.T) {
		tampered := strings.Replace(
			binding,
			"measure own func(width float32, widthMode uint32, height float32, heightMode uint32) [2]float32",
			"measure own func(width float32, widthMode uint32, height float32, heightMode uint32) [2]float32 needs go.Foreign",
			1,
		)
		gb := NewGoBindings(func(p string) (string, bool) {
			if p == pkgPath {
				return tampered, true
			}
			return "", false
		})
		gb.ResolveAll(pkgPath)
		if ds := gb.Diagnostics(); len(ds) == 0 {
			t.Fatalf("expected tampered ScopeRegistered binding with needs row to fail ResolveAll")
		}
	})

	t.Run("tampered-unannotated-cb-slice-refused", func(t *testing.T) {
		tampered := strings.Replace(binding, "// ergo:scoped writer.data\n", "", 1)
		gb := NewGoBindings(func(p string) (string, bool) {
			if p == pkgPath {
				return tampered, true
			}
			return "", false
		})
		gb.ResolveAll(pkgPath)
		if ds := gb.Diagnostics(); len(ds) == 0 {
			t.Fatalf("expected C callback slice without // ergo:scoped to fail ResolveAll")
		}
	})
}

func hasDiagCode(ds []Diagnostic, code string) bool {
	for _, d := range ds {
		if d.Code == code {
			return true
		}
	}
	return false
}
