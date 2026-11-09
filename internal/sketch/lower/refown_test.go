package lower

import (
	"regexp"
	"strings"
	"testing"
)

// refOwnSource writes files to an fstest.Memory and prints to an
// io.Recorder, so it reaches each changing operation of both (V6).
const refOwnSource = `import fs
import fstest
import io

func FileWrites(n int) int {
	let m = fstest.NewMemory()
	m.MkdirAll("d") catch {
	case fs.Denied, fs.Failed:
		return -1
	}
	for i in 0..n {
		m.WriteFile("d/f\(i % 1024)", "x") catch {
		case fs.NotFound, fs.Denied, fs.Failed:
			return -1
		}
	}
	m.Rename("d/f0", "d/g") catch {
	case fs.NotFound, fs.Denied, fs.Failed:
		return -1
	}
	m.Remove("d/g") catch {
	case fs.NotFound, fs.Denied, fs.Failed:
		return -1
	}
	m.Chmod("d/f1", fs.Private()) catch {
	case fs.NotFound, fs.Denied, fs.Failed:
		return -1
	}
	let names = m.ReadDir("d") catch {
	case fs.NotFound, fs.Denied, fs.Failed:
		return -1
	}
	len(names)
}

func Prints(n int) int {
	let r = io.NewRecorder()
	for _ in 0..n {
		r.Print("ab")
	}
	len(r.Text())
}

func main() needs Console {
	Console.Println("\(FileWrites(3)) \(Prints(3))")
}
`

// TestRefOwnElides: each changing operation of fstest.Memory and
// io.Recorder lowers to an owned Ref call whose literal is proved to
// return unshared values (fresh, true), and the owned helpers they call
// copy nothing, so a write costs O(the path) and a Print O(1) amortized.
// It reads the emitted Go only: TestValueRuleAgrees holds the owned calls
// to the copying lowering, which passes it with every copy in place, so
// only the Go text tells the two apart (the mutants "never own", the
// copying lowering, and "never fresh", a site proved shared). The
// copying lowering "copy" emits no owned call.
func TestRefOwnElides(t *testing.T) {
	code := lowerValueMode(t, "refown", refOwnSource, true).mainGo()
	owned := regexp.MustCompile(`ergo\.Ref(Update|Modify)Owned\[`).FindAllStringIndex(code, -1)
	// MkdirAll, WriteFile, Rename, Remove, Chmod, ReadDir, the look of
	// ReadFile and Stat, Print and Text.
	if len(owned) < 9 {
		t.Errorf("%d owned Ref calls, want one per changing operation and read (9):\n%s", len(owned), code)
	}
	if n := strings.Count(code, "ergo.RefModify[") + strings.Count(code, ").Update(func"); n > 0 {
		t.Errorf("%d plain Ref calls with a literal remain", n)
	}
	for _, loc := range owned {
		call := code[loc[0]:]
		// The call ends at the copy function, then the proof.
		end := regexp.MustCompile(`\{ return (maps|slices)\.Clone\([a-z_0-9]+\) \}, (true|false)\)`).FindStringSubmatch(call)
		if end == nil || end[2] != "true" {
			t.Errorf("an owned call is not proved fresh:\n%s", firstLines(call, 40))
		}
	}
	for _, fn := range []string{"writeOwned", "mkdirAllOwned", "moveOwned"} {
		if body := goFunc(t, code, fn); strings.Contains(body, "Clone") {
			t.Errorf("%s copies the tree:\n%s", fn, body)
		}
	}
	if print := goFuncBody(t, code, "Print"); strings.Contains(print, "Clip(") {
		t.Errorf("Print copies the writes:\n%s", print)
	}
	if ref := lowerValueMode(t, "refowncopy", refOwnSource, true, "copy").mainGo(); strings.Contains(ref, "Owned") {
		t.Error("the copying lowering emits an owned call")
	}
}

// BenchmarkRefOwn measures fstest.Memory's WriteFile and io.Recorder's
// Print against hand Go: a map[string][]byte under a sync.Mutex that
// cleans the path and looks up its parent directory, as Memory does, and
// a []string under a sync.Mutex. Each Ergo iteration is a whole run of
// FileWrites(4096) (4096 writes over 1024 files) or Prints(4096).
// go test -run '^$' -bench '^BenchmarkRefOwn$' -benchmem ./internal/sketch/lower
func BenchmarkRefOwn(b *testing.B) {
	emittedBench(b, "refownbench", refOwnSource, func(mainGo string) string {
		return `package main

import (
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

var sink int

func BenchmarkWritesErgo(b *testing.B) {
	for range b.N {
		sink = ` + emittedCall(mainGo, "FileWrites", "4096") + `
	}
}

type goFile struct {
	dir  bool
	text []byte
	perm int
}

func BenchmarkWritesGo(b *testing.B) {
	for range b.N {
		var mu sync.Mutex
		tree := map[string]goFile{".": {dir: true}, "/": {dir: true}, "d": {dir: true}}
		for i := range 4096 {
			p := filepath.Clean("d/f" + strconv.Itoa(i%1024))
			mu.Lock()
			if parent, ok := tree[filepath.Dir(p)]; ok && parent.dir {
				tree[p] = goFile{text: []byte("x"), perm: 0o644}
			}
			mu.Unlock()
		}
		sink = len(tree)
	}
}

func BenchmarkPrintsErgo(b *testing.B) {
	for range b.N {
		sink = ` + emittedCall(mainGo, "Prints", "4096") + `
	}
}

func BenchmarkPrintsGo(b *testing.B) {
	for range b.N {
		var mu sync.Mutex
		writes := []string{}
		for range 4096 {
			mu.Lock()
			writes = append(writes, "ab")
			mu.Unlock()
		}
		mu.Lock()
		sink = len(strings.Join(writes, ""))
		mu.Unlock()
	}
}
`
	}, "WritesErgo", "WritesGo", "PrintsErgo", "PrintsGo")
}
