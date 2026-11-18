package lower

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// aloneSrc hands an Ergo function to Go twice: to strings.IndexFunc, a
// non-escaping callback that Go calls only during the call on its
// caller's goroutine (v1.GoSyncCallbacks), and to
// crypto/subtle.WithDataIndependentTiming, whose binding is escaping (no
// reviewed row says otherwise), as time.AfterFunc's is: Go may call such a
// callback later, from a goroutine of its own.
const aloneSrc = `import go strings
import go crypto/subtle

func sync(s string) int {
	strings.IndexFunc(s, func(r rune) bool { r == 'x' })
}

func later() needs go.Foreign {
	subtle.WithDataIndependentTiming(func() {})
}

func main() needs go.Foreign {
	if sync("axb") == 1 {
		later()
	}
}
`

// TestHandedCallbackEndsAlone: the program ends rt.Alone (v1.GoHanded)
// before it hands Go an escaping callback, on both lowerings (the text
// emitter's goCallbacks and the IR's hostArgs), and not before a
// non-escaping one. A Ref skips its lock while the program is alone, so
// an escaping callback with no GoHanded before it could run on Go's
// goroutine against an unlocked Ref (the mutants "gohanded-off" and
// "ir-gohanded-off"); a GoHanded before IndexFunc would only give up the
// unlocked Ref in a program that runs one task ("gohanded-sync").
// The form is the promise here: GoHanded is the one fact checked.
func TestHandedCallbackEndsAlone(t *testing.T) {
	o := lowerSource("alone", aloneSrc, true)
	if o.err != nil || len(o.diags) > 0 || o.refused != nil || o.panicked != nil {
		t.Fatalf("lowering failed: %v %v %v %v", o.err, o.diags, o.refused, o.panicked)
	}
	text := goText(o)
	if got := handedIn(text, "func sync("); got != 0 {
		t.Errorf("text: sync hands Go a non-escaping callback, want no GoHanded, got %d:\n%s", got, text)
	}
	if got := handedIn(text, "func later("); got != 1 {
		t.Errorf("text: later hands Go an escaping callback, want one GoHanded, got %d:\n%s", got, text)
	}
	u := Unit{Package: o.pkg, Files: []*syntax.File{o.file}, Sources: []Source{{Path: testSourcePath, Text: aloneSrc}}}
	files, rep, err := LowerIR(u)
	if err != nil {
		t.Fatal(err)
	}
	var ir string
	for _, f := range files {
		if f.Path == IRFileName {
			ir = string(f.Data)
		}
	}
	onIR := strings.Join(rep.IR, " ")
	if !strings.Contains(onIR, "later") || !strings.Contains(onIR, "sync") {
		t.Fatalf("want sync and later on the IR, got %v (fallback %v)", rep.IR, rep.Fallback)
	}
	if got := strings.Count(ir, `"GoHanded"`); got != 1 {
		t.Errorf("IR: want one GoHanded (later's), got %d", got)
	}
}

// handedIn counts the GoHanded calls in the Go function of text that
// starts at head, up to the next top-level func.
func handedIn(text, head string) int {
	i := strings.Index(text, head)
	if i < 0 {
		return -1
	}
	body := text[i+len(head):]
	if j := strings.Index(body, "\nfunc "); j >= 0 {
		body = body[:j]
	}
	return strings.Count(body, ".GoHanded()")
}
