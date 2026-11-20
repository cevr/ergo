package lower

import (
	"strings"
	"testing"
)

// TestOwnReadAdapterIsEmitted: a decode of pair on the Reader interface
// asks whether the reader is wrap's operations adapter (wireread.go). The
// naming pass names that adapter for every wrap-to-Reader conversion of
// the package, but a conversion only emits it when its function is
// lowered; here the one conversion is in a function nothing calls, so
// the decoder is the adapter's only user and must emit it, or the Go
// names a type that does not exist (the std json law build of JSON-RAW
// failed so: "undefined: opsReaderAsReader"). Checked in process on the
// lowered Go (typecheckGo). Mutant: wr-adapter-unemitted.
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

func unused(w wrap) Reader { w }

func main() needs Console {
	let j = json.Open("[1,2]", 0, 0) catch {
	case _:
		return
	}
	Console.Println("\(onReader(j))")
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
