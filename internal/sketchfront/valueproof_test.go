package sketchfront

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestBuildRefusesReachableRewrite builds Go because the admission boundary
// consumes a native proof receipt and must not turn refutation into the
// "optimization unavailable" fallback. Lowering alone cannot observe that.
func TestBuildRefusesReachableRewrite(t *testing.T) {
	if testing.Short() {
		t.Skip("native proof admission")
	}
	const source = `
type Counter { Inc(), Twice(), Read() bool,
	law Two() { Inc(); Inc() } == { Twice() },
}
type Box { at bool }
func NewBox() Box { Box{at: false} }
func Inc(p *Box) { p.at = !p.at }
func Twice(p *Box) { p.at = false }
func Read(p Box) bool { p.at }
func main() {
	let mut p = NewBox()
	p.Inc()
	p.Inc()
	let _ = p.Read()
}
`
	analysis := Check(source)
	if len(analysis.Diagnostics) != 0 {
		t.Fatal(analysis.Diagnostics)
	}
	dir := t.TempDir()
	var stderr bytes.Buffer
	_, err := Build(analysis, buildRequest(filepath.Join(dir, "cache"), "/ergo-test/refuted.ergo", filepath.Join(dir, "out"), &stderr))
	var refuted *lower.RefutedRewriteError
	if !errors.As(err, &refuted) {
		t.Fatalf("reachable false law: %v\n%s", err, stderr.String())
	}
}
