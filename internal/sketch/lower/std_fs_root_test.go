package lower

import (
	"bytes"
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"ergo.local/prototype/internal/lawledger"
	"ergo.local/prototype/internal/testwait"
)

// fsRootProgram reads a provider name, "host" or "tree", then the nodes
// of a tree, one per line ("File", "Dir" or "Link", then the quoted path
// and the text or target), up to a line "end", then one operation per
// line. "host" runs each operation on fs.OpenRoot("root") in its working
// directory and ignores the nodes; "tree" runs it on fstest.NewTree of the
// nodes. Both run through fs.Root. It prints one line per operation: "ok"
// and the value, or the failure and its fields. A Law line runs Root's
// law, restated, and prints held or broken.
const fsRootProgram = `import fs
import fstest
import io
import strconv
import strings

func q(s string) string { strconv.Quote(s) }

func arg(f []string, i int) string {
	strconv.Unquote(f[i]) catch {
	case strconv.Malformed:
		"?"
	}
}

func step(line string) string needs fs.Root {
	let f = strings.Split(line, "\t")
	if f[0] == "Law" {
		return if leavingEscapes(arg(f, 1)) { "held" } else { "broken" }
	}
	let out = run(f[0], arg(f, 1)) catch {
	case fs.NotFound as n:
		return "NotFound " + q(n.Path)
	case fs.Denied as d:
		return "Denied " + q(d.Path)
	case fs.Failed as x:
		return "Failed " + q(x.Path) + " " + q(x.Reason)
	case fs.Escapes as e:
		return "Escapes " + q(e.Path)
	}
	"ok " + out
}

func run(op string, p string) string fails fs.NotFound | fs.Denied | fs.Failed | fs.Escapes needs fs.Root {
	match op {
	case "ReadFile":
		q(try fs.Root.ReadFile(p))
	case "ReadDir":
		strings.Join(try fs.Root.ReadDir(p), ",")
	case "Stat":
		let i = try fs.Root.Stat(p)
		let size = if i.Dir { "-" } else { strconv.Itoa(i.Size) }
		"\(q(i.Name)) \(size) \(i.Dir) \(strconv.FormatInt(int64(i.Perm), 8))"
	case _:
		"unknown " + op
	}
}

// Root.LeavingEscapes, restated through the capability.
func leavingEscapes(path string) bool needs fs.Root {
	let _ = fs.Root.ReadFile("../" + path) catch {
	case fs.Escapes:
		let _ = fs.Root.ReadFile("/" + path) catch {
		case fs.Escapes:
			let _ = fs.Root.Stat("../" + path) catch {
			case fs.Escapes:
				return true
			case fs.NotFound, fs.Denied, fs.Failed:
				return false
			}
			return false
		case fs.NotFound, fs.Denied, fs.Failed:
			return false
		}
		return false
	case fs.NotFound, fs.Denied, fs.Failed:
		return false
	}
	false
}

func each() needs Console | io.Stdin | fs.Root {
	for line in io.Stdin.Lines() {
		Console.Println(step(line))
	}
}

func main() needs Console | Ambient {
	let input = io.NewHost()
	let mut which = ""
	let mut nodes = []fstest.Node{}
	for line in input.Lines() {
		if which == "" {
			which = line
			continue
		}
		if line == "end" {
			break
		}
		let f = strings.Split(line, "\t")
		match f[0] {
		case "File":
			nodes = nodes.Append(fstest.File{Path: arg(f, 1), Text: arg(f, 2)})
		case "Dir":
			nodes = nodes.Append(fstest.Dir{Path: arg(f, 1)})
		case _:
			nodes = nodes.Append(fstest.Link{Path: arg(f, 1), Target: arg(f, 2)})
		}
	}
	if which == "host" {
		let root = fs.OpenRoot("root") catch {
		case fs.NotFound, fs.Denied, fs.Failed:
			Console.Println("no root")
			return
		}
		with context { fs.Root: root, io.Stdin: input } { each() }
		return
	}
	with context { fs.Root: fstest.NewTree(nodes), io.Stdin: input } { each() }
}
`

// rootNode is one generated node: its kind, its path below the root, and
// its text (a file) or target (a link).
type rootNode struct{ kind, path, arg string }

// rootSecret is the text of the one file outside the root. No output of
// either provider may hold it.
const rootSecret = "SECRET-outside-the-root"

// rootTree generates one tree: six top names, each a file, a directory or
// a link, and up to three entries in each directory, each a file or a
// link. Link targets are names inside, a ".." past the root and back in,
// the file outside, ".", "..", a target of a target, the link itself (a
// loop) and an absolute path.
func rootTree(r *rand.Rand) []rootNode {
	targets := []string{"n0", "n1", "n2/m0", "n3/", "./n4", ".", "..", "../outside/secret", "../outside", "../root/n0",
		"/nonexistent/loop-probe-x", "m0", "m1", "../n1", "../../outside/secret", "n5", "n2/../n0"}
	var nodes []rootNode
	pick := func(path string, dir bool) {
		k := r.IntN(10)
		switch {
		case dir && k < 3:
			nodes = append(nodes, rootNode{"Dir", path, ""})
			for j := range r.IntN(4) {
				child := fmt.Sprintf("%s/m%d", path, j)
				if r.IntN(2) == 0 {
					nodes = append(nodes, rootNode{"File", child, fmt.Sprintf("text of %s", child)})
				} else {
					nodes = append(nodes, rootNode{"Link", child, targets[r.IntN(len(targets))]})
				}
			}
		case k < 6:
			nodes = append(nodes, rootNode{"File", path, fmt.Sprintf("text of %s", path)})
		case k < 7:
			nodes = append(nodes, rootNode{"Link", path, path[strings.LastIndexByte(path, '/')+1:]}) // a loop
		default:
			nodes = append(nodes, rootNode{"Link", path, targets[r.IntN(len(targets))]})
		}
	}
	for i := range 6 {
		pick(fmt.Sprintf("n%d", i), true)
	}
	return nodes
}

// rootPath generates one request path: one to four elements of names,
// "." and "..", sometimes with a leading or a final "/", a doubled "/",
// and now and then the empty path.
func rootPath(r *rand.Rand) string {
	if r.IntN(25) == 0 {
		return ""
	}
	elems := []string{"n0", "n1", "n2", "n3", "n4", "n5", "m0", "m1", "m2", "..", ".", "outside", "secret", "root"}
	var b strings.Builder
	if r.IntN(10) == 0 {
		b.WriteString("/")
	}
	for i := range 1 + r.IntN(4) {
		if i > 0 {
			b.WriteString("/")
			if r.IntN(12) == 0 {
				b.WriteString("/")
			}
		}
		b.WriteString(elems[r.IntN(len(elems))])
	}
	if r.IntN(8) == 0 {
		b.WriteString("/")
	}
	return b.String()
}

// makeRootWorld writes the tree below dir/root, and dir/outside/secret.
func makeRootWorld(t *testing.T, dir string, nodes []rootNode) {
	t.Helper()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(dir, "root"), 0o755))
	must(os.MkdirAll(filepath.Join(dir, "outside"), 0o755))
	must(os.WriteFile(filepath.Join(dir, "outside", "secret"), []byte(rootSecret), 0o644))
	for _, n := range nodes {
		p := filepath.Join(dir, "root", filepath.FromSlash(n.path))
		must(os.MkdirAll(filepath.Dir(p), 0o755))
		switch n.kind {
		case "File":
			must(os.WriteFile(p, []byte(n.arg), 0o644))
		case "Dir":
			must(os.Mkdir(p, 0o755))
		case "Link":
			must(os.Symlink(n.arg, p))
		}
	}
}

// runRootProgram runs the built fsRootProgram in dir with the provider,
// the nodes and the operations on its standard input.
func runRootProgram(t *testing.T, exe, dir, provider string, nodes []rootNode, ops []string) []string {
	t.Helper()
	probeRan(exe)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	var in strings.Builder
	in.WriteString(provider + "\n")
	for _, n := range nodes {
		fmt.Fprintf(&in, "%s\t%s\t%s\n", n.kind, strconv.Quote(n.path), strconv.Quote(n.arg))
	}
	in.WriteString("end\n" + strings.Join(ops, "\n") + "\n")
	cmd := exec.CommandContext(ctx, exe)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.Stdin = strings.NewReader(in.String())
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := runProgram(t, cmd) // under the test kit's memory limit (proclimit)
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if err != nil || stderr.Len() > 0 {
		t.Fatalf("%s: %v\n%s", provider, err, firstLines(stderr.String(), 20))
	}
	return strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
}

// rootOutcomesReached are the outcomes the generated trees and paths must
// reach, by operation, so a weaker generator fails. "link Escapes" is an
// Escapes of a path with no leading "/" and no ".." element: only a link
// leads it out.
var rootOutcomesReached = []string{
	`ReadFile ok`, `ReadFile NotFound`, `ReadFile Escapes`, `ReadFile link Escapes`,
	`ReadFile Failed "is a directory"`, `ReadFile Failed "not a directory"`,
	`ReadFile Failed "too many levels of symbolic links"`, `ReadFile Failed "empty path"`,
	`ReadDir ok`, `ReadDir NotFound`, `ReadDir Escapes`, `ReadDir link Escapes`, `ReadDir Failed "not a directory"`,
	`Stat ok`, `Stat NotFound`, `Stat Escapes`, `Stat link Escapes`, `Stat Failed "not a directory"`,
}

// rootOutcome is the kind of a result line of fsRootProgram for the
// operation op on path.
func rootOutcome(op, path, line string) string {
	kind, rest, _ := strings.Cut(line, " ")
	switch kind {
	case "Failed":
		_, reason, _ := strings.Cut(rest, `" "`)
		return op + ` Failed "` + reason
	case "Escapes":
		if !strings.HasPrefix(path, "/") && !strings.Contains("/"+path+"/", "/../") {
			return op + " link Escapes"
		}
	}
	return op + " " + kind
}

var fsRootLawStatement = `LeavingEscapes(path string) { let _ = ReadFile("../" + path) catch { case Escapes: let _ = ReadFile("/" + path) catch { case Escapes: let _ = Stat("../" + path) catch { case Escapes: return true case NotFound, Denied, Failed: return false } return false case NotFound, Denied, Failed: return false } return false case NotFound, Denied, Failed: return false } false }`

// TestFSRootTreeAgreesWithHost (G1f; stdlib.md, fs 20 to 23) is a
// model-based differential test of confinement: 120 seeded trees of
// files, directories and symbolic links (links inside, links out by a
// relative or an absolute target, links past the root and back in,
// chains and loops), each with 40 operations of fs.Root on generated
// paths, run on fs.OpenRoot over the tree written to disk beside a file
// outside the root, and on fstest.NewTree of the same nodes. On Linux
// every line agrees: each value, each failure kind, its path and its
// reason. On every host no line of either provider holds the outside
// file's text, and Root's law LeavingEscapes holds on both. A failing
// tree prints its seed, its nodes, its operations and both outputs;
// ERGO_FS_SEED=n replays only tree n.
//
// It builds Go because the reference is the host itself (os.Root below a
// real directory with real links) and fstest.Tree is Ergo source with no
// interpreter to run it in process; one build serves every tree.
func TestFSRootTreeAgreesWithHost(t *testing.T) {
	needsBuild(t)
	exe := tableExe(t, "fsRoot")
	compare := runtime.GOOS == "linux"
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)
	reached := map[string]int{}
	held, broken := 0, []string{}
	seeds, first := 120, 0
	if s := os.Getenv("ERGO_FS_SEED"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			t.Fatal(err)
		}
		first, seeds = n, n+1
	}
	for seed := first; seed < seeds; seed++ {
		r := rand.New(rand.NewPCG(uint64(seed), 11))
		nodes := rootTree(r)
		var ops, paths []string
		for range 40 {
			p := rootPath(r)
			op := []string{"ReadFile", "ReadFile", "ReadDir", "Stat", "Law"}[r.IntN(5)]
			ops, paths = append(ops, op+"\t"+strconv.Quote(p)), append(paths, p)
		}
		dir := t.TempDir()
		makeRootWorld(t, dir, nodes)
		host := runRootProgram(t, exe, dir, "host", nodes, ops)
		tree := runRootProgram(t, exe, t.TempDir(), "tree", nodes, ops)
		if len(host) != len(ops) || len(tree) != len(ops) {
			t.Fatalf("seed %d: %d operations, %d host lines, %d tree lines\nhost %q\ntree %q", seed, len(ops), len(host), len(tree), host, tree)
		}
		report := func(i int) string {
			var b strings.Builder
			for _, n := range nodes {
				fmt.Fprintf(&b, "  %s %q %q\n", n.kind, n.path, n.arg)
			}
			for j := range i + 1 {
				fmt.Fprintf(&b, "  %s\n      host %s\n      tree %s\n", ops[j], host[j], tree[j])
			}
			return b.String()
		}
		for i, op := range ops {
			if strings.Contains(host[i], rootSecret) || strings.Contains(tree[i], rootSecret) {
				t.Fatalf("seed %d, operation %d read outside the root (ERGO_FS_SEED=%d replays it):\n%s", seed, i, seed, report(i))
			}
			if compare && host[i] != tree[i] {
				t.Fatalf("seed %d, operation %d disagrees (ERGO_FS_SEED=%d replays it):\n%s", seed, i, seed, report(i))
			}
			name, _, _ := strings.Cut(op, "\t")
			if name != "Law" {
				reached[rootOutcome(name, paths[i], tree[i])]++
				continue
			}
			for side, out := range map[string]string{"host": host[i], "tree": tree[i]} {
				if out == "held" {
					held++
				} else {
					broken = append(broken, fmt.Sprintf("seed %d op %d on %s: %s gave %s", seed, i, side, op, out))
				}
			}
		}
	}
	if first == 0 {
		for _, k := range rootOutcomesReached {
			if reached[k] == 0 {
				t.Errorf("no operation gave %s; reached %v", k, reached)
			}
		}
	}
	lawledger.Run(t, map[string]lawledger.Law{"fs Root.LeavingEscapes": {Statement: fsRootLawStatement, Check: func(t *testing.T) {
		for _, b := range broken {
			t.Error(b)
		}
		if first == 0 && held < 20 {
			t.Errorf("LeavingEscapes held in only %d cases", held)
		}
	}}})
}
