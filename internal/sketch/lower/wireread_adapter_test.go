package lower

import (
	"strings"
	"testing"
)

// TestOwnReadAdapterIsEmitted: a decode of pair on the Reader interface
// asks whether the reader is wrap's operations adapter (wireread.go). The
// naming pass names an adapter for every implicit conversion the checker
// recorded (naming.go, Info.Implicit), and decode[pair](w) on a wrap
// records one, wrap to Reader; but the lowering reads that decode on the
// direct path (wirereader.go), which converts nothing, so no conversion
// emits the adapter. The decoder that names it must emit it, or the Go
// names a type that does not exist (JSON-RAW's std json law build failed
// so: "undefined: opsReaderAsReader", Raw's WireRead on json.Reader).
// Checked in process on the lowered Go (typecheckGo). Mutant:
// wr-adapter-unemitted.
func TestOwnReadAdapterIsEmitted(t *testing.T) {
	head := cellReaderSource[:strings.Index(cellReaderSource, "func said(")]
	src := head + `
func onReader(r Reader) int {
	let p = decode[pair](r) catch {
	case _:
		return -1
	}
	p.A
}

func onWrap(w wrap) int {
	let p = decode[pair](w) catch {
	case _:
		return -1
	}
	p.B
}

func main() needs Console {
	let j = json.Open("[1,2]", 0, 0) catch {
	case _:
		return
	}
	let k = json.Open("[3,4]", 0, 0) catch {
	case _:
		return
	}
	Console.Println("\(onReader(j)) \(onWrap(wrap{j: k}))")
}
`
	o := lowerSource("ownreadadapter", src, true)
	if o.err != nil || o.panicked != nil || len(o.diags) > 0 || len(o.refused) > 0 {
		t.Fatalf("lowering: err %v, panic %v, diags %v, refused %v", o.err, o.panicked, o.diags, o.refused)
	}
	goSrc := o.mainGo()
	if !strings.Contains(goSrc, ".(*opswrapAsReader)") {
		t.Fatalf("the decoder of pair on the Reader asks for no wrap adapter, so this case reaches nothing:\n%s", goSrc)
	}
	typecheckGo(t, []*lowered{o})
}
