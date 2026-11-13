package eval

import (
	"errors"
	"io/fs"
	"math/rand/v2"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// TestRootResolvesAsGo holds the simulated file system's links
// (links.go) to Go and Linux on a real directory: a seeded tree of
// directories, files and symbolic links (relative, absolute, dangling,
// looping and chained) is made both in a temporary directory and in a
// simulated host, and seeded paths (with ".", "..", empty elements and a
// final slash) are read through Go's os.Root and through an opened
// HostRoot, and through os.ReadFile and the host's resolve. Each read's
// outcome, the bytes or Escapes, NotFound or a Failed reason, must be
// the same. A host read whose real resolution leaves the temporary
// directory is not compared: what lies there is the machine's. A failing
// seed prints the tree, the path and both outcomes, and the replay
// command. It reads and writes files only under t.TempDir; it builds and
// runs no Go.
//
// Mutants only it kills (testdata/mutants.json): oracle-o28-root-link-not-spliced,
// oracle-o28-root-link-limit, oracle-o28-host-dotdot-lexical.
func TestRootResolvesAsGo(t *testing.T) {
	n := 200
	if testing.Short() {
		n = 60
	}
	first := uint64(0)
	if s := os.Getenv("ERGO_EVAL_LINK_SEED"); s != "" {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		first, n = v, 1
	}
	names := []string{"a", "b", "c", "f", "g", "l", "m"}
	targets := []string{"a", "f", "c", "a/c", "../f", "../a/c", "../../f", "l", "m", "a/../f", "/nonexistent/loop-probe-x", "c/./g", "nothing", "a/", "f/x"}
	for seed := first; seed < first+uint64(n); seed++ {
		r := rand.New(rand.NewPCG(seed, 0x11))
		top := t.TempDir()
		real := filepath.Join(top, "w")
		var files []File
		var tree []string
		var make func(rel string, depth int)
		make = func(rel string, depth int) {
			for _, name := range names {
				if r.IntN(3) != 0 {
					continue
				}
				p := path.Join(rel, name)
				switch k := r.IntN(4); {
				case k == 0 && depth < 2:
					files = append(files, File{Path: p, Dir: true, Perm: 0o755})
					tree = append(tree, p+"/")
					make(p, depth+1)
				case k <= 1:
					data := "data of " + p
					files = append(files, File{Path: p, Data: data, Perm: 0o644})
					tree = append(tree, p)
				default:
					to := targets[r.IntN(len(targets))]
					files = append(files, File{Path: p, Link: to})
					tree = append(tree, p+" -> "+to)
				}
			}
		}
		make("", 0)
		// A chain of links to a file, around os.Root's limit of 8.
		chain := r.IntN(12)
		files = append(files, File{Path: "end", Data: "the end", Perm: 0o644})
		for k := range chain {
			to := "end"
			if k+1 < chain {
				to = "k" + strconv.Itoa(k+1)
			}
			files = append(files, File{Path: "k" + strconv.Itoa(k), Link: to})
		}
		tree = append(tree, "chain of "+strconv.Itoa(chain))
		if err := os.Mkdir(real, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			p := filepath.Join(real, f.Path)
			var err error
			switch {
			case f.Dir:
				err = os.Mkdir(p, 0o755)
			case f.Link != "":
				err = os.Symlink(f.Link, p)
			default:
				err = os.WriteFile(p, []byte(f.Data), 0o644)
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		h := newHost(Process{Dir: "/w", Files: files})
		root, err := os.OpenRoot(real)
		if err != nil {
			t.Fatal(err)
		}
		for range 30 {
			var elems []string
			for range 1 + r.IntN(4) {
				switch k := r.IntN(10); {
				case k == 0:
					elems = append(elems, "..")
				case k == 1:
					elems = append(elems, ".")
				case k == 2:
					elems = append(elems, "")
				default:
					elems = append(elems, names[r.IntN(len(names))])
				}
			}
			q := strings.Join(elems, "/")
			if chain > 0 && r.IntN(4) == 0 {
				q = "k0"
			}
			if r.IntN(6) == 0 {
				q += "/"
			}
			// Through the root.
			data, err := root.ReadFile(q)
			want := goOutcome(data, err)
			p, f, kind, reason := h.rootResolve("/w", q)
			got := simOutcome(p, f, kind, reason)
			if got != want {
				root.Close()
				t.Fatalf("seed %d: tree %v\nroot read %q: the evaluator gives %s, Go %s\nreplay: ERGO_EVAL_LINK_SEED=%d go test -run TestRootResolvesAsGo ./internal/sketch/eval", seed, tree, q, got, want, seed)
			}
			// Through the host, Linux's way.
			if q == "" || q[0] == '/' {
				continue
			}
			if resolved, err := filepath.EvalSymlinks(real + "/" + q); err != nil || !strings.HasPrefix(resolved+"/", top+"/") {
				if err == nil || !errors.Is(err, syscall.ELOOP) {
					continue
				}
			}
			data, err = os.ReadFile(real + "/" + q)
			want = goOutcome(data, err)
			p, f, kind, reason = h.resolve(q)
			got = simOutcome(p, f, kind, reason)
			if got != want {
				root.Close()
				t.Fatalf("seed %d: tree %v\nhost read %q: the evaluator gives %s, Go %s\nreplay: ERGO_EVAL_LINK_SEED=%d go test -run TestRootResolvesAsGo ./internal/sketch/eval", seed, tree, q, got, want, seed)
			}
		}
		root.Close()
	}
}

// goOutcome is a Go read's outcome as std fs names it.
func goOutcome(data []byte, err error) string {
	var pe *fs.PathError
	switch {
	case err == nil:
		return "read " + strconv.Quote(string(data))
	case errors.Is(err, fs.ErrNotExist):
		return "NotFound"
	case errors.As(err, &pe) && pe.Err.Error() == "path escapes from parent":
		return "Escapes"
	case errors.As(err, &pe):
		return "Failed " + pe.Err.Error()
	}
	return "Failed " + err.Error()
}

// simOutcome is the simulated read's outcome, as HostRoot.ReadFile and
// fs.Host's ReadFile give it.
func simOutcome(_ string, f *hostFile, kind, reason string) string {
	switch {
	case kind == "Failed":
		return "Failed " + reason
	case kind != "":
		return kind
	case f.dir:
		return "Failed is a directory"
	}
	return "read " + strconv.Quote(f.data)
}
