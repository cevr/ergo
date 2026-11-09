package lower

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

var callbackSeed = flag.Uint64("callback.seed", 0, "run TestCallbackTunnelModel on this seed alone")

// cbCase is one walk of TestCallbackTunnelModel: Go's walker (WalkDir or
// Walk) over root, whose callback acts at its k-th call by mode (0 fails
// the Ergo failure Hit, 1 fs.SkipDir, 2 fs.SkipAll, 3 an Ergo-built
// go.Error, 4 nothing) and returns the error Go hands it, if any.
type cbCase struct {
	walk    string
	root    string
	k, mode int
}

// cbHit is the model's Hit: the Ergo failure that crosses Go as a tunnel.
type cbHit struct {
	n int
	p string
}

func (h cbHit) Error() string { return "hit" }

// model is the outcome the Ergo program prints for c, computed by Go's own
// walker with a Go callback: the visited paths, each after the with-bound
// tag, or the typed failure, or Go's error.
func (c cbCase) model() string {
	n, out := 0, ""
	visit := func(p string, err error) error {
		n++
		if n == c.k {
			switch c.mode {
			case 0:
				return cbHit{n, p}
			case 1:
				return fs.SkipDir
			case 2:
				return fs.SkipAll
			case 3:
				return errors.New("boom")
			}
		}
		if err != nil {
			return err
		}
		out += "t:" + p + ";"
		return nil
	}
	var err error
	if c.walk == "WalkDir" {
		err = filepath.WalkDir(c.root, func(p string, _ fs.DirEntry, err error) error { return visit(p, err) })
	} else {
		err = filepath.Walk(c.root, func(p string, _ fs.FileInfo, err error) error { return visit(p, err) })
	}
	var h cbHit
	switch {
	case errors.As(err, &h):
		return fmt.Sprintf("hit %d %s", h.n, h.p)
	case err != nil:
		return fmt.Sprintf("goerr %s nex=%t", strconv.Quote(err.Error()), errors.Is(err, fs.ErrNotExist))
	}
	return fmt.Sprintf("ok %d %s", n, out)
}

// genTree writes a tree of directories and files under root.
func genTree(t *testing.T, r *rand.Rand, root string, depth int) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := range 1 + r.IntN(3) {
		p := filepath.Join(root, fmt.Sprintf("%c%d", 'a'+rune(r.IntN(4)), i))
		if depth < 3 && r.IntN(2) == 0 {
			genTree(t, r, p, depth+1)
			continue
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestCallbackTunnelModel is the acceptance of import go callbacks (IG6):
// an Ergo function literal passed to filepath.WalkDir and filepath.Walk,
// whose bindings give the synchronous callback the call's rows. Each seed
// writes a tree in a temp dir and draws walks: the callback fails Hit (an
// Ergo failure, tunnelled through Go and typed again at the call) at its
// k-th call, or returns fs.SkipDir, fs.SkipAll or an Ergo-built go.Error
// (Go's own errors, which Go acts on), and returns the go.Error Go hands
// it (a missing root's *fs.PathError). Every visit reads Tag, a
// capability a with provides, so the callback runs with its caller's
// capabilities. The outcome (the visits, the typed Hit, or the go.Error)
// equals the model: Go's walker with a Go callback of the same rules. The
// program is lowered lazily and with the eager go.Error reference; both
// print the model. -callback.seed=N replays one seed.
//
// It builds Go because the adapter and the tunnel are emitted Go: which
// error Go's walker returns, and what it does with SkipDir.
//
// Mutants: the tunnel loses Hit (GoUntunnel returns GoErrorOf); the
// callback's failure dropped (the adapter returns nil); a go.Error failure
// tunnelled rather than passed as Go's error (SkipDir does not skip); the
// environment not the caller's (build: Tag unbound).
func TestCallbackTunnelModel(t *testing.T) {
	seeds := []uint64{1, 2, 3}
	if *callbackSeed != 0 {
		seeds = []uint64{*callbackSeed}
	}
	dir := t.TempDir()
	var src strings.Builder
	src.WriteString(`import go path/filepath
import go io/fs

type Hit { N int, P string }

type Tag {
	Get() string,
}

type Named { Name string }

func Get(n Named) string { n.Name }

func act(n int, k int, mode int, err go.Error) fails Hit | go.Error {
	if n == k {
		if mode == 0 {
			return Hit{N: n, P: ""}
		}
		if mode == 1 {
			return fs.SkipDir
		}
		if mode == 2 {
			return fs.SkipAll
		}
		if mode == 3 {
			return go.Error{Text: "boom"}
		}
	}
	if err.Text != "" {
		return err
	}
}

func walkDir(root string, k int, mode int) string fails Hit | go.Error needs Tag | go.Foreign {
	let mut n = 0
	let mut out = ""
	try filepath.WalkDir(root, func(p string, d fs.DirEntry, err go.Error) fails Hit | go.Error {
		n = n + 1
		act(n, k, mode, err) catch {
			case Hit: return Hit{N: n, P: p}
			case go.Error as e: return e
		}
		out = out + Tag.Get() + p + ";"
	})
	"ok \(n) \(out)"
}

func walk(root string, k int, mode int) string fails Hit | go.Error needs Tag | go.Foreign {
	let mut n = 0
	let mut out = ""
	try filepath.Walk(root, func(p string, info fs.FileInfo, err go.Error) fails Hit | go.Error {
		n = n + 1
		act(n, k, mode, err) catch {
			case Hit: return Hit{N: n, P: p}
			case go.Error as e: return e
		}
		out = out + Tag.Get() + p + ";"
	})
	"ok \(n) \(out)"
}

func show(r string) needs Console {
	Console.Println(r)
}

func failed(h Hit) string { "hit \(h.N) \(h.P)" }

func goFailed(e go.Error) string { "goerr \(strconv.Quote(e.Text)) nex=\(go.Is(e, fs.ErrNotExist))" }

func outcome(w string, root string, k int, mode int) string needs Tag | go.Foreign {
	if w == "WalkDir" {
		return walkDir(root, k, mode) catch {
			case Hit as h: failed(h)
			case go.Error as e: goFailed(e)
		}
	}
	walk(root, k, mode) catch {
		case Hit as h: failed(h)
		case go.Error as e: goFailed(e)
	}
}
`)
	var want []string
	var calls []string
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(seed, 0x1c6b))
		root := filepath.Join(dir, fmt.Sprintf("s%d", seed))
		genTree(t, r, root, 0)
		for range 8 {
			c := cbCase{walk: []string{"WalkDir", "Walk"}[r.IntN(2)], root: root, k: 1 + r.IntN(12), mode: r.IntN(5)}
			if r.IntN(6) == 0 {
				c.root = "/nonexistent/loop-probe-x/ig6"
			}
			calls = append(calls, fmt.Sprintf("\t\tConsole.Println(outcome(%q, %q, %d, %d))\n", c.walk, c.root, c.k, c.mode))
			want = append(want, c.model())
		}
	}
	src.WriteString("\nfunc main() needs Console | go.Foreign {\n\twith context { Tag: Named{Name: \"t:\"} } {\n")
	for _, c := range calls {
		src.WriteString(c)
	}
	src.WriteString("\t}\n}\n")
	text := strings.Replace(src.String(), "import go io/fs\n", "import go io/fs\nimport go strconv\n", 1)
	lazy := mustLower(t, "callback_lazy", text)
	restore := SetGoErrorEagerForTest(true)
	eager := mustLower(t, "callback_eager", text)
	restore()
	exes := buildAll(t, []*lowered{lazy, eager}, true)
	if exes == nil {
		return
	}
	for _, prog := range []string{"callback_lazy", "callback_eager"} {
		stdout, stderr, code := runExe(t, exes[prog])
		if code != 0 {
			t.Fatalf("%s: exit %d, stderr %s", prog, code, firstLines(stderr, 8))
		}
		got := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
		if len(got) != len(want) {
			t.Fatalf("%s: %d lines for %d walks:\n%s\nsource:\n%s", prog, len(got), len(want), stdout, text)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%s walk %d (replay with -callback.seed=N):\nergo %s\ngo   %s", prog, i, got[i], want[i])
			}
		}
	}
	if t.Failed() {
		t.Logf("source:\n%s", text)
	}
}
