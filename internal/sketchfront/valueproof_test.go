package sketchfront

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestLedgerRefusesReachableRewrite builds Go because ergo test's ledger
// writer consumes a native proof run and must not turn a refutation into
// an entry or a silent "no rewrite": it fails, and writes nothing.
// Lowering alone cannot observe that.
func TestLedgerRefusesReachableRewrite(t *testing.T) {
	if testing.Short() {
		t.Skip("native proof admission")
	}
	const source = `
type Counter { Inc(), Twice(), Read() bool,
	law Two() { Inc(); Inc() } == { Twice() },
}
type Box { at bool } implements Counter
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
	req := buildRequest(filepath.Join(dir, "cache"), "/ergo-test/refuted.ergo", filepath.Join(dir, "out"), &stderr)
	req.Ledger = filepath.Join(dir, lower.LedgerFile)
	_, err := UpdateLedger(analysis, req)
	var refuted *lower.RefutedRewriteError
	if !errors.As(err, &refuted) {
		t.Fatalf("reachable false law: %v\n%s", err, stderr.String())
	}
	if _, statErr := os.Stat(req.Ledger); statErr == nil {
		t.Fatal("a refuted law wrote the ledger")
	}
}
