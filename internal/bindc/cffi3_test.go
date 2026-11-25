package bindc

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestCCallbackRegisteredLifetime verifies that ErgoText emits `own func`
// (without capability/error rows) for ScopeRegistered callbacks such as
// fixture.NodeSetMeasure, so Ergo refuses capturing a `with`-bound capability
// (S102) or frame-local borrow (S108) while accepting pure closures.
// Kills cffi3-registered-cb-allows-cap.
func TestCCallbackRegisteredLifetime(t *testing.T) {
	m := parseHeader(t, "testdata/fixture", "fixture.h")
	b := bindHeader(t, "testdata/fixture", "fixture.h", m)
	out, err := ErgoText(b)
	if err != nil {
		t.Fatalf("ErgoText: %v", err)
	}
	binding := string(out)

	if !strings.Contains(binding, "// ergo:scoped writer.data") {
		t.Fatalf("emitted fixture.ergo missing // ergo:scoped writer.data")
	}

	// 1. Capturing a with-bound capability in NodeSetMeasure must be refused S102.
	capSrc := `import go ergo.local/bind/fixture

type Meter {
	Tick() float32,
}

type StaticMeter {} implements Meter
func Tick(m StaticMeter) float32 { float32(1.0) }

func run() fails go.Error needs go.Foreign {
	let ctx = try fixture.ContextCreate(fixture.ContextOptions{NodeCapacity: 4, Strict: true})
	let node = try fixture.NodeCreate(ctx, 1)
	with context { Meter: StaticMeter } {
		try fixture.NodeSetMeasure(ctx, node, func(w float32, wm uint32, h float32, hm uint32) [2]float32 {
			let t = Meter.Tick()
			[2]float32{w + t, h}
		})
	}
}
`
	if ds := checkWithFixtureBinding(t, binding, capSrc); !hasDiag(ds, "S102", "") {
		t.Fatalf("expected S102 when NodeSetMeasure captures with-bound capability, got: %v", ds)
	}

	// 2. Capturing a frame-local borrow in NodeSetMeasure must be refused S108.
	borrowSrc := `import go ergo.local/bind/fixture

func run() fails go.Error needs go.Foreign {
	let ctx = try fixture.ContextCreate(fixture.ContextOptions{NodeCapacity: 4, Strict: true})
	let node = try fixture.NodeCreate(ctx, 1)
	let mut factor float32 = 3.0
	let p = &factor
	try fixture.NodeSetMeasure(ctx, node, func(w float32, wm uint32, h float32, hm uint32) [2]float32 {
		[2]float32{w * *p, h}
	})
}
`
	if ds := checkWithFixtureBinding(t, binding, borrowSrc); !hasDiag(ds, check.CodeType, "") {
		t.Fatalf("expected S108 when NodeSetMeasure captures local borrow, got: %v", ds)
	}

	// 3. Pure registered callback + call-scoped ContextWrite with capability capture check clean.
	validSrc := `import go ergo.local/bind/fixture

type Meter {
	Tick() int64,
}

type StaticMeter {} implements Meter
func Tick(m StaticMeter) int64 { int64(1) }

func run() fails go.Error needs go.Foreign {
	let ctx = try fixture.ContextCreate(fixture.ContextOptions{NodeCapacity: 4, Strict: true})
	let node = try fixture.NodeCreate(ctx, 1)
	try fixture.NodeSetMeasure(ctx, node, func(w float32, wm uint32, h float32, hm uint32) [2]float32 {
		[2]float32{w, h}
	})
	with context { Meter: StaticMeter } {
		let _ = try fixture.ContextWrite(ctx, []byte{65, 66}, 4, func(data []byte) int64 needs Meter {
			let _ = Meter.Tick()
			int64(len(data))
		})
	}
}
`
	if ds := checkWithFixtureBinding(t, binding, validSrc); len(ds) != 0 {
		t.Fatalf("expected valid emitted callback usage to check clean, got: %v", ds)
	}
}
