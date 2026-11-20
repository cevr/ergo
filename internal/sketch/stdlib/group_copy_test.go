package stdlib_test

import (
	"strings"
	"testing"
)

// groupCaller is a program whose view makes two component calls (head
// and card, each through the frame's group), and whose head calls a
// primitive of the frame's own package (ui.Text, a plain call).
const groupCaller = `import ui

type Item { n int, on bool }

func head(f mut ui.Frame[int]) {
	ui.Text(f, "head")
}

func card(f mut ui.Frame[int], it Item) {
	ui.Text(f, "n \(it.n)")
}

func view(f mut ui.Frame[int], it Item) {
	head(f)
	card(f, it)
}

func main() {
	let mut f = ui.NewFrame[int]()
	let _ = ui.Render(&f, view, Item{n: 1, on: true})
}
`

// TestGroupProtocolCopyAgrees is the group protocol's parity (SP9; owner,
// 2026-11-16: std has no privileges over user code): a user library that
// declares a frame, a group of the group shape and a law that reaches it
// (here std ui copied into the user package copy/ui) gets the component
// calls std ui gets, the same Go; the same library without the law gets
// plain calls, since the compiler sends a call through a group only on
// the law the skip rests on (lower/uigroup.go, groupFrame). A call of the
// frame package's own function (ui.Text) is never a group.
//
// Only view's Go is compared: the inlined bodies of ui's primitives differ
// between std and a copy by the flat-union lowering (TestStdCopyAgrees
// holds ui's copy the same).
func TestGroupProtocolCopyAgrees(t *testing.T) {
	const law = "law GroupSkipsEqualArguments("
	const group = "group_#0[" // copyCanonicalNames
	pair := checkCopy(t, "ui")
	if len(pair.diags) > 0 {
		t.Fatalf("the copy of ui does not check: %v", pair.diags[0])
	}
	text := copyLower(t, pair, groupCaller, false)
	view := goFunc(t, text, "view")
	if n := strings.Count(view, group); n != 2 {
		t.Fatalf("std ui: view's Go has %d group calls, want 2 (head's and card's):\n%s", n, view)
	}
	if h := goFunc(t, text, "head"); strings.Contains(h, group) {
		t.Errorf("std ui: head's call of ui.Text, a function of the frame's own package, is a group:\n%s", h)
	}
	if copied := goFunc(t, copyLower(t, pair, groupCaller, true), "view"); copied != view {
		x, y := firstDiff(view, copied)
		t.Errorf("a user library with the group protocol gets other Go than std ui for a component call:\nstd:  %s\ncopy: %s", x, y)
	}
	unlawed := checkCopyEdited(t, "ui", func(name, text string) string {
		at := strings.Index(text, law)
		if at < 0 {
			return text
		}
		end := strings.Index(text[at:], "\n}\n")
		return text[:at] + text[at+end+3:]
	})
	if len(unlawed.diags) > 0 {
		t.Fatalf("the copy of ui without %s) does not check: %v", law, unlawed.diags[0])
	}
	if v := goFunc(t, copyLower(t, unlawed, groupCaller, true), "view"); strings.Contains(v, group) {
		t.Errorf("a library whose laws do not reach its group gets component calls through it:\n%s", v)
	}
}

// goFunc is the normalised Go text of the function name in the lowered
// files text.
func goFunc(t *testing.T, text, name string) string {
	t.Helper()
	at := strings.Index(text, "\nfunc "+name+"_#0(") // copyCanonicalNames
	if at < 0 {
		t.Fatalf("no func %s in the lowered Go:\n%s", name, text)
	}
	end := strings.Index(text[at+1:], "\n}\n")
	if end < 0 {
		t.Fatalf("func %s has no end in the lowered Go", name)
	}
	return text[at+1 : at+1+end+2]
}
