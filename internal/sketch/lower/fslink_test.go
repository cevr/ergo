package lower

import (
	"fmt"
	"strings"
	"testing"
)

// hardlinkLinker is dray's hardlink Linker (github.com/cevr/dray,
// hardlink/hardlink.ergo) on std alone (G6): Place links the store's entry
// at a new name beside out and renames it over out; Same is the no-op
// check, one file by identity. It used the module's trusted Go (gohost
// Link, SameFile) before std fs had Link and SameFile.
const hardlinkLinker = `import fs

type NotSameVolume { From string, To string }

type LinkFailed { Path string, Why string }

type Linker { host fs.Host }

func place(l Linker, entry string, out string) fails NotSameVolume | LinkFailed {
	let tmp = out + ".dray-tmp"
	fs.Remove(l.host, tmp) catch {
	case fs.NotFound:
	case fs.Denied as e:
		return LinkFailed{Path: e.Path, Why: "denied"}
	case fs.Failed as e:
		return LinkFailed{Path: e.Path, Why: e.Reason}
	}
	fs.Link(l.host, entry, tmp) catch {
	case fs.CrossDevice as e:
		return NotSameVolume{From: e.From, To: e.To}
	case fs.NotFound as e:
		return LinkFailed{Path: e.Path, Why: "missing"}
	case fs.Denied as e:
		return LinkFailed{Path: e.Path, Why: "denied"}
	case fs.Failed as e:
		return LinkFailed{Path: e.Path, Why: e.Reason}
	}
	fs.Rename(l.host, tmp, out) catch {
	case fs.NotFound as e:
		return LinkFailed{Path: e.Path, Why: "missing"}
	case fs.Denied as e:
		return LinkFailed{Path: e.Path, Why: "denied"}
	case fs.Failed as e:
		return LinkFailed{Path: e.Path, Why: e.Reason}
	case fs.CrossDevice as e:
		return NotSameVolume{From: e.From, To: e.To}
	}
}

func same(l Linker, entry string, out string) bool {
	fs.SameFile(l.host, entry, out) catch {
	case fs.NotFound, fs.Denied, fs.Failed:
		return false
	}
}

func main() needs Console | Ambient {
	let l = Linker{host: fs.NewHost()}
	place(l, "/nonexistent/loop-probe-x", "/nonexistent/loop-probe-y") catch {
	case NotSameVolume:
		Console.Println("xdev")
	case LinkFailed as e:
		Console.Println(e.Why)
	}
	Console.Println("\(same(l, "/nonexistent/loop-probe-x", "/nonexistent/loop-probe-y"))")
}
`

// TestHardlinkLinkerOnStd: dray's hardlink Linker and its no-op check
// check with std fs alone and their Go type-checks, calling Link and
// SameFile's bodies, FSLink and FSSameFile. Mutants (stdlib
// mutants.json): g6-link-unbound, g6-crossdevice-unlisted.
func TestHardlinkLinkerOnStd(t *testing.T) {
	p := lowerSource("hardlinklinker", hardlinkLinker, true)
	if p.err != nil || len(p.diags) != 0 || len(p.refused) != 0 || p.panicked != nil {
		t.Fatalf("err=%v diagnostics=%s refusals=%v panic=%v\n%s", p.err, fmt.Sprint(p.diags), p.refused, p.panicked, hardlinkLinker)
	}
	typecheckGo(t, []*lowered{p})
	goSrc := p.mainGo()
	for _, body := range []string{".FSLink(", ".FSSameFile("} {
		if !strings.Contains(goSrc, body) {
			t.Errorf("the linker does not call %s:\n%s", body, goSrc)
		}
	}
}
