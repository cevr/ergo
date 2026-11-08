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
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"ergo.local/prototype/internal/lawledger"
)

// fsMemoryProgram reads a provider name, "host" or "memory", then one
// operation per line, and runs each on fs.Host (in its working directory)
// or on one fstest.Memory, through fs.Files and fs.Writes. It prints one line
// per operation: "ok" and the value, or the failure and its fields. A Law
// line runs one of fs's laws, restated, on the same providers (or, for a
// Memory law, on a new Memory) and prints held, broken or skip, where skip
// is a case the law holds vacuously.
const fsMemoryProgram = `import filepath
import fs
import fstest
import io
import strconv
import strings

func access(text string) fs.Access {
	match text {
	case "1":
		fs.Execute
	case "4":
		fs.ReadOnly
	case "5":
		fs.ReadExecute
	case "6":
		fs.ReadWrite
	case "7":
		fs.ReadWriteExecute
	case _:
		fs.NoAccess
	}
}

// modeOf is the Mode of three digits, owner first.
func modeOf(text string) fs.Mode {
	fs.Mode{Owner: access(strings.Slice(text, 0, 1)), Group: access(strings.Slice(text, 1, 2)), Other: access(strings.Slice(text, 2, 3))}
}

func q(s string) string { strconv.Quote(s) }

func arg(f []string, i int) string {
	strconv.Unquote(f[i]) catch {
	case strconv.Malformed:
		"?"
	}
}

func verdict(held bool) string { if held { "held" } else { "broken" } }

func step(line string) string needs fs.Files | fs.Writes {
	let f = strings.Split(line, "\t")
	if f[0] == "Law" {
		return runLaw(arg(f, 1), f)
	}
	let out = run(f[0], arg(f, 1), f) catch {
	case fs.NotFound as n:
		return "NotFound " + q(n.Path)
	case fs.Denied as d:
		return "Denied " + q(d.Path)
	case fs.Failed as x:
		return "Failed " + q(x.Path) + " " + q(x.Reason)
	}
	"ok " + out
}

func run(op string, p string, f []string) string fails fs.NotFound | fs.Denied | fs.Failed needs fs.Files | fs.Writes {
	match op {
	case "ReadFile":
		q(try fs.Files.ReadFile(p))
	case "ReadDir":
		strings.Join(try fs.Files.ReadDir(p), ",")
	case "Stat":
		let i = try fs.Files.Stat(p)
		let size = if i.Dir { "-" } else { strconv.Itoa(i.Size) }
		"\(q(i.Name)) \(size) \(i.Dir) \(strconv.FormatInt(int64(i.Perm), 8))"
	case "WriteFile":
		try fs.Writes.WriteFile(p, arg(f, 2))
		""
	case "WriteFileMode":
		try fs.Writes.WriteFileMode(p, arg(f, 2), modeOf(f[3]))
		""
	case "AppendFile":
		try fs.Writes.AppendFile(p, arg(f, 2))
		""
	case "MkdirAll":
		try fs.Writes.MkdirAll(p)
		""
	case "MkdirAllMode":
		try fs.Writes.MkdirAllMode(p, modeOf(f[2]))
		""
	case "Chmod":
		try fs.Writes.Chmod(p, modeOf(f[2]))
		""
	case "Rename":
		try fs.Writes.Rename(p, arg(f, 2))
		""
	case "Remove":
		try fs.Writes.Remove(p)
		""
	case _:
		"unknown " + op
	}
}

func runLaw(name string, f []string) string needs fs.Files | fs.Writes {
	match name {
	case "ReadIsRepeatable":
		readIsRepeatable(arg(f, 2))
	case "MkdirAllIsIdempotent":
		mkdirAllIsIdempotent(arg(f, 2))
	case "RemoveThenNotFound":
		removeThenNotFound(arg(f, 2))
	case "RenameMoves":
		renameMoves(arg(f, 2), arg(f, 3), arg(f, 4))
	case "WriteThenRead":
		writeThenRead(arg(f, 2), arg(f, 3))
	case "AppendConcatenates":
		appendConcatenates(arg(f, 2), arg(f, 3), arg(f, 4))
	case "ModeIsKept":
		modeIsKept(arg(f, 2), arg(f, 3), modeOf(f[4]), arg(f, 5))
	case "MemoryCopiesShare":
		memoryCopiesShare(arg(f, 2), arg(f, 3))
	case "MemoryAppendConcatenates":
		memoryAppendConcatenates(arg(f, 2), arg(f, 3), arg(f, 4))
	case "MemoryModeIsKept":
		memoryModeIsKept(arg(f, 2), arg(f, 3), arg(f, 4))
	case _:
		"unknown law " + name
	}
}

// Files.ReadIsRepeatable
func readIsRepeatable(path string) string needs fs.Files {
	let first = fs.Files.ReadFile(path) catch {
	case fs.NotFound, fs.Denied, fs.Failed:
		return "skip"
	}
	let second = fs.Files.ReadFile(path) catch {
	case fs.NotFound, fs.Denied, fs.Failed:
		return "broken"
	}
	verdict(second == first)
}

// Writes.MkdirAllIsIdempotent
func mkdirAllIsIdempotent(path string) string needs fs.Writes {
	fs.Writes.MkdirAll(path) catch {
	case fs.Denied, fs.Failed:
		return "skip"
	}
	fs.Writes.MkdirAll(path) catch {
	case fs.Denied, fs.Failed:
		return "broken"
	}
	"held"
}

// Writes.RemoveThenNotFound
func removeThenNotFound(path string) string needs fs.Writes {
	fs.Writes.Remove(path) catch {
	case fs.NotFound, fs.Denied, fs.Failed:
		return "skip"
	}
	fs.Writes.Remove(path) catch {
	case fs.NotFound:
		return "held"
	case fs.Denied, fs.Failed:
		return "broken"
	}
	"broken"
}

// Writes.RenameMoves
func renameMoves(from string, to string, text string) string needs fs.Writes {
	if from == to {
		return "skip"
	}
	fs.Writes.WriteFile(from, text) catch {
	case fs.NotFound, fs.Denied, fs.Failed:
		return "skip"
	}
	fs.Writes.Remove(to) catch {
	case fs.NotFound:
	case fs.Denied, fs.Failed:
		return "skip"
	}
	fs.Writes.Rename(from, to) catch {
	case fs.NotFound, fs.Denied, fs.Failed:
		return "skip"
	}
	fs.Writes.Rename(from, to) catch {
	case fs.NotFound:
		return "held"
	case fs.Denied, fs.Failed:
		return "broken"
	}
	"broken"
}

// Writes: after WriteFile(path, text) succeeds, ReadFile(path) is text.
func writeThenRead(path string, text string) string needs fs.Files | fs.Writes {
	fs.Writes.WriteFile(path, text) catch {
	case fs.NotFound, fs.Denied, fs.Failed:
		return "skip"
	}
	let got = fs.Files.ReadFile(path) catch {
	case fs.NotFound, fs.Denied, fs.Failed:
		return "broken"
	}
	verdict(got == text)
}

// Writes, AppendConcatenates.
func appendConcatenates(path string, a string, b string) string needs fs.Files | fs.Writes {
	fs.Writes.WriteFile(path, a) catch {
	case fs.NotFound, fs.Denied, fs.Failed:
		return "skip"
	}
	fs.Writes.AppendFile(path, b) catch {
	case fs.NotFound, fs.Denied, fs.Failed:
		return "broken"
	}
	let got = fs.Files.ReadFile(path) catch {
	case fs.NotFound, fs.Denied, fs.Failed:
		return "broken"
	}
	verdict(got == a + b)
}

// Writes, ModeIsKept.
func modeIsKept(path string, text string, mode fs.Mode, more string) string needs fs.Files | fs.Writes {
	fs.Writes.WriteFileMode(path, text, mode) catch {
	case fs.NotFound, fs.Denied, fs.Failed:
		return "skip"
	}
	let first = fs.Files.Stat(path) catch {
	case fs.NotFound, fs.Denied, fs.Failed:
		return "broken"
	}
	fs.Writes.WriteFile(path, more) catch {
	case fs.NotFound, fs.Denied, fs.Failed:
		return "broken"
	}
	fs.Writes.AppendFile(path, more) catch {
	case fs.NotFound, fs.Denied, fs.Failed:
		return "broken"
	}
	let info = fs.Files.Stat(path) catch {
	case fs.NotFound, fs.Denied, fs.Failed:
		return "broken"
	}
	verdict(first.Perm == fs.Perm(mode) && info.Perm == fs.Perm(mode))
}

// MemoryCopiesShare
func memoryCopiesShare(path string, text string) string {
	let m = fstest.NewMemory()
	let copy = m
	copy.MkdirAll(filepath.Dir(path)) catch {
	case fs.Denied, fs.Failed:
		return "skip"
	}
	copy.WriteFile(path, text) catch {
	case fs.NotFound, fs.Denied, fs.Failed:
		return "skip"
	}
	let got = m.ReadFile(path) catch {
	case fs.NotFound, fs.Denied, fs.Failed:
		return "broken"
	}
	verdict(got == text)
}

// MemoryAppendConcatenates
func memoryAppendConcatenates(path string, a string, b string) string {
	let m = fstest.NewMemory()
	m.MkdirAll(filepath.Dir(path)) catch {
	case fs.Denied, fs.Failed:
		return "skip"
	}
	m.WriteFile(path, a) catch {
	case fs.NotFound, fs.Denied, fs.Failed:
		return "skip"
	}
	m.AppendFile(path, b) catch {
	case fs.NotFound, fs.Denied, fs.Failed:
		return "broken"
	}
	let got = m.ReadFile(path) catch {
	case fs.NotFound, fs.Denied, fs.Failed:
		return "broken"
	}
	verdict(got == a + b)
}

// MemoryModeIsKept
func memoryModeIsKept(path string, text string, more string) string {
	let m = fstest.NewMemory()
	m.MkdirAll(filepath.Dir(path)) catch {
	case fs.Denied, fs.Failed:
		return "skip"
	}
	m.WriteFileMode(path, text, fs.Private()) catch {
	case fs.NotFound, fs.Denied, fs.Failed:
		return "skip"
	}
	m.WriteFile(path, more) catch {
	case fs.NotFound, fs.Denied, fs.Failed:
		return "broken"
	}
	m.AppendFile(path, more) catch {
	case fs.NotFound, fs.Denied, fs.Failed:
		return "broken"
	}
	let info = m.Stat(path) catch {
	case fs.NotFound, fs.Denied, fs.Failed:
		return "broken"
	}
	verdict(info.Perm == fs.Perm(fs.Private()))
}

func each() needs Console | io.Stdin | fs.Files | fs.Writes {
	for line in io.Stdin.Lines() {
		Console.Println(step(line))
	}
}

func main() needs Console | Ambient {
	let input = io.NewHost()
	let mut which = ""
	for line in input.Lines() {
		which = line
		break
	}
	if which == "host" {
		with context { fs.Files: fs.NewHost(), fs.Writes: fs.NewHost(), io.Stdin: input } { each() }
		return
	}
	let m = fstest.NewMemory()
	with context { fs.Files: m, fs.Writes: m, io.Stdin: input } { each() }
}
`

// fsOps generates one sequence of n operation lines for fsMemoryProgram.
// Paths are one to three of the names a, b and c, sometimes written
// "./p" or with a doubled separator, and now and then the empty path or
// ".". A mode keeps the owner's bits whole (rw for a file, rwx for a
// directory or a Chmod), so the host never refuses the owner, as Memory
// never does; the group's and others' bits are any.
func fsOps(r *rand.Rand, n int) []string {
	names := []string{"a", "b", "c"}
	path := func() string {
		switch r.IntN(20) {
		case 0:
			return ""
		case 1:
			return "."
		}
		parts := make([]string, 1+r.IntN(3))
		for i := range parts {
			parts[i] = names[r.IntN(len(names))]
		}
		p := strings.Join(parts, "/")
		if r.IntN(6) == 0 {
			p = "./" + p
		}
		return p
	}
	texts := []string{"", "x", "yz\n", "é", "a\tb"}
	text := func() string { return texts[r.IntN(len(texts))] + texts[r.IntN(len(texts))] }
	bits := "014567"
	other := func() string { return string(bits[r.IntN(len(bits))]) + string(bits[r.IntN(len(bits))]) }
	fileMode := func() string { return string("67"[r.IntN(2)]) + other() }
	dirMode := func() string { return "7" + other() }
	q := strconv.Quote
	var ops []string
	for range n {
		var line string
		switch r.IntN(17) {
		case 0:
			line = "ReadFile\t" + q(path())
		case 1:
			line = "ReadDir\t" + q(path())
		case 2:
			line = "Stat\t" + q(path())
		case 3, 4:
			line = "WriteFile\t" + q(path()) + "\t" + q(text())
		case 5:
			line = "WriteFileMode\t" + q(path()) + "\t" + q(text()) + "\t" + fileMode()
		case 6:
			line = "AppendFile\t" + q(path()) + "\t" + q(text())
		case 7, 8:
			line = "MkdirAll\t" + q(path())
		case 9:
			line = "MkdirAllMode\t" + q(path()) + "\t" + dirMode()
		case 10:
			line = "Chmod\t" + q(path()) + "\t" + dirMode()
		case 11:
			line = "Rename\t" + q(path()) + "\t" + q(path())
		case 12:
			line = "Remove\t" + q(path())
		default:
			line = fsLawLine(r, path, text, fileMode)
		}
		ops = append(ops, line)
	}
	return ops
}

// fsLawLine is a Law line for one of the laws, with generated arguments.
func fsLawLine(r *rand.Rand, path, text, fileMode func() string) string {
	q := strconv.Quote
	switch r.IntN(10) {
	case 0:
		return "Law\t" + q("ReadIsRepeatable") + "\t" + q(path())
	case 1:
		return "Law\t" + q("MkdirAllIsIdempotent") + "\t" + q(path())
	case 2:
		return "Law\t" + q("RemoveThenNotFound") + "\t" + q(path())
	case 3:
		return "Law\t" + q("RenameMoves") + "\t" + q(path()) + "\t" + q(path()) + "\t" + q(text())
	case 4:
		return "Law\t" + q("WriteThenRead") + "\t" + q(path()) + "\t" + q(text())
	case 5:
		return "Law\t" + q("AppendConcatenates") + "\t" + q(path()) + "\t" + q(text()) + "\t" + q(text())
	case 6:
		return "Law\t" + q("ModeIsKept") + "\t" + q(path()) + "\t" + q(text()) + "\t" + fileMode() + "\t" + q(text())
	case 7:
		return "Law\t" + q("MemoryCopiesShare") + "\t" + q(path()) + "\t" + q(text())
	case 8:
		return "Law\t" + q("MemoryAppendConcatenates") + "\t" + q(path()) + "\t" + q(text()) + "\t" + q(text())
	}
	return "Law\t" + q("MemoryModeIsKept") + "\t" + q(path()) + "\t" + q(text()) + "\t" + q(text())
}

// runFSProgram runs the built fsMemoryProgram in dir with the provider
// name and the operation lines on its standard input, in a process group
// of its own that is killed when it ends or after a minute, under the test
// kit's memory limit (runProgram, internal/proclimit).
func runFSProgram(t *testing.T, exe, dir, provider string, ops []string) []string {
	t.Helper()
	probeRan(exe)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.Stdin = strings.NewReader(provider + "\n" + strings.Join(ops, "\n") + "\n")
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

// fsOutcomeKind is the kind of a result line of fsMemoryProgram: "ok",
// "NotFound", or "Failed" and its reason.
func fsOutcomeKind(line string) string {
	kind, rest, _ := strings.Cut(line, " ")
	if kind == "Failed" {
		if i := strings.Index(rest, "\" \""); i >= 0 {
			return kind + " " + rest[i+2:]
		}
	}
	return kind
}

// fsOutcomesReached are the outcomes, by operation, that the sequences of
// TestFSMemoryAgreesWithHost reach.
var fsOutcomesReached = []string{
	`AppendFile Failed "is a directory"`, `AppendFile Failed "not a directory"`, `AppendFile NotFound`, `AppendFile ok`,
	`Chmod Failed "not a directory"`, `Chmod NotFound`, `Chmod ok`,
	`MkdirAll Failed "no such file or directory"`, `MkdirAll Failed "not a directory"`, `MkdirAll ok`,
	`MkdirAllMode Failed "no such file or directory"`, `MkdirAllMode Failed "not a directory"`, `MkdirAllMode ok`,
	`ReadDir Failed "not a directory"`, `ReadDir NotFound`, `ReadDir ok`,
	`ReadFile Failed "is a directory"`, `ReadFile Failed "not a directory"`, `ReadFile NotFound`, `ReadFile ok`,
	`Remove Failed "directory not empty"`, `Remove Failed "invalid argument"`, `Remove Failed "not a directory"`, `Remove NotFound`, `Remove ok`,
	`Rename Failed "device or resource busy"`, `Rename Failed "file exists"`, `Rename Failed "invalid argument"`, `Rename Failed "not a directory"`, `Rename NotFound`, `Rename ok`,
	`Stat Failed "not a directory"`, `Stat NotFound`, `Stat ok`,
	`WriteFile Failed "is a directory"`, `WriteFile Failed "not a directory"`, `WriteFile NotFound`, `WriteFile ok`,
	`WriteFileMode Failed "is a directory"`, `WriteFileMode Failed "not a directory"`, `WriteFileMode NotFound`, `WriteFileMode ok`,
}

// fsLawStatements are fs's laws as the ledger states them, by key.
var fsLawStatements = map[string]string{
	"fstest CopiesShare":             `CopiesShare(path string, text string) { let m = NewMemory() let copy = m copy.MkdirAll(filepath.Dir(path)) catch { case fs.Denied, fs.Failed: return true } copy.WriteFile(path, text) catch { case fs.NotFound, fs.Denied, fs.Failed: return true } try m.ReadFile(path) == text }`,
	"fstest AppendConcatenates":      `AppendConcatenates(path string, a string, b string) { let m = NewMemory() m.MkdirAll(filepath.Dir(path)) catch { case fs.Denied, fs.Failed: return true } m.WriteFile(path, a) catch { case fs.NotFound, fs.Denied, fs.Failed: return true } try m.AppendFile(path, b) try m.ReadFile(path) == a+b }`,
	"fstest ModeIsKept":              `ModeIsKept(path string, text string, more string) { let m = NewMemory() m.MkdirAll(filepath.Dir(path)) catch { case fs.Denied, fs.Failed: return true } m.WriteFileMode(path, text, fs.Private()) catch { case fs.NotFound, fs.Denied, fs.Failed: return true } try m.WriteFile(path, more) try m.AppendFile(path, more) let info = try m.Stat(path) info.Perm == fs.Perm(fs.Private()) }`,
	"fs Files.ReadIsRepeatable":      `ReadIsRepeatable(path string) { let first = try ReadFile(path) try ReadFile(path) == first }`,
	"fs Writes.MkdirAllIsIdempotent": `MkdirAllIsIdempotent(path string) { MkdirAll(path) catch { case Denied, Failed: return true } try MkdirAll(path) true }`,
	"fs Writes.RemoveThenNotFound":   `RemoveThenNotFound(path string) { Remove(path) catch { case NotFound, Denied, Failed: return true } Remove(path) catch { case NotFound: return true case Denied, Failed: return false } false }`,
	"fs Writes.RenameMoves":          `RenameMoves(from string, to string, text string) { if from == to { return true } WriteFile(from, text) catch { case NotFound, Denied, Failed: return true } Remove(to) catch { case NotFound: case Denied, Failed: return true } Rename(from, to) catch { case NotFound, Denied, Failed: return true } Rename(from, to) catch { case NotFound: return true case Denied, Failed: return false } false }`,
}

// TestFSMemoryAgreesWithHost (U1 std-fs-memory; stdlib.md, fs 17 to 19)
// is a model-based differential test: 150 seeded sequences of 60
// operations, every operation of fs.Files and fs.Writes, run on fs.Host in
// a new directory under umask 022 and on fstest.NewMemory(). On Linux every
// line agrees: each value, each failure kind, its path and its reason.
// fs's laws, and the Writes contracts no member law can state
// (WriteThenRead, AppendConcatenates, ModeIsKept), run inside the
// sequences on both providers, and the Memory laws on a new Memory; none
// is broken, and each holds in some case that is not vacuous. On another
// host only the laws are held, since its failure reasons and the order of
// rename's checks are its own. A failing sequence prints its seed, its
// operations and both outputs; ERGO_FS_SEED=n replays only sequence n.
// It builds and runs the program because the reference is the host itself
// (fs.Host's Go bodies on a real directory) and Memory is Ergo source with
// no interpreter to run it in process; one build serves every sequence.
func TestFSMemoryAgreesWithHost(t *testing.T) {
	p := mustLower(t, "fsMemory", fsMemoryProgram)
	exes := buildAll(t, []*lowered{p}, true)
	if exes == nil {
		t.Fatal("the lowered program does not build")
	}
	exe := exes["fsMemory"]
	compare := runtime.GOOS == "linux"
	// No test of this package that runs at the same time makes a file: the
	// parallel tests wait until the sequential ones end.
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)
	held := map[string]int{}
	broken := map[string][]string{}
	reached := map[string]int{}
	seqs := 150
	first := 0
	if s := os.Getenv("ERGO_FS_SEED"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			t.Fatal(err)
		}
		first, seqs = n, n+1
	}
	for seed := first; seed < seqs; seed++ {
		ops := fsOps(rand.New(rand.NewPCG(uint64(seed), 7)), 60)
		dir := filepath.Join(t.TempDir(), "root")
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		host := runFSProgram(t, exe, dir, "host", ops)
		mem := runFSProgram(t, exe, t.TempDir(), "memory", ops)
		if len(host) != len(ops) || len(mem) != len(ops) {
			t.Fatalf("seed %d: %d operations, %d host lines, %d memory lines", seed, len(ops), len(host), len(mem))
		}
		for i, op := range ops {
			if compare && host[i] != mem[i] {
				var b strings.Builder
				for j := range i + 1 {
					fmt.Fprintf(&b, "  %s\n      host   %s\n      memory %s\n", ops[j], host[j], mem[j])
				}
				t.Fatalf("seed %d, operation %d disagrees (ERGO_FS_SEED=%d replays it):\n%s", seed, i, seed, b.String())
			}
			if !strings.HasPrefix(op, "Law\t") {
				name, _, _ := strings.Cut(op, "\t")
				reached[name+" "+fsOutcomeKind(mem[i])]++
				continue
			}
			name, _ := strconv.Unquote(strings.Split(op, "\t")[1])
			for side, out := range map[string]string{"host": host[i], "memory": mem[i]} {
				switch out {
				case "held":
					held[name]++
				case "skip":
				default:
					broken[name] = append(broken[name], fmt.Sprintf("seed %d op %d on %s: %s gave %s", seed, i, side, op, out))
				}
			}
		}
	}
	if first == 0 {
		var kinds []string
		for k, n := range reached {
			kinds = append(kinds, fmt.Sprintf("%s: %d", k, n))
		}
		slices.Sort(kinds)
		t.Logf("outcomes reached:\n%s", strings.Join(kinds, "\n"))
		t.Logf("laws held: %v", held)
		// The sequences reach every outcome of every operation that the
		// host gives here, so a weaker generator fails.
		for _, k := range fsOutcomesReached {
			if reached[k] == 0 {
				t.Errorf("no operation gave %s", k)
			}
		}
	}
	lawCheck := func(name string) func(t *testing.T) {
		return func(t *testing.T) {
			for _, b := range broken[name] {
				t.Error(b)
			}
			if first == 0 && held[name] < 20 {
				t.Errorf("%s held in only %d cases that are not vacuous", name, held[name])
			}
		}
	}
	for _, name := range []string{"WriteThenRead", "AppendConcatenates", "ModeIsKept"} {
		t.Run("Writes."+name, lawCheck(name))
	}
	laws := map[string]lawledger.Law{}
	for key, statement := range fsLawStatements {
		// "fs Writes.RenameMoves" is the program's RenameMoves, and
		// "fstest CopiesShare" its MemoryCopiesShare.
		pkg, name, _ := strings.Cut(key, " ")
		if i := strings.IndexByte(name, '.'); i >= 0 {
			name = name[i+1:]
		}
		if pkg == "fstest" {
			name = "Memory" + name
		}
		laws[key] = lawledger.Law{Statement: statement, Check: lawCheck(name)}
	}
	lawledger.Run(t, laws)
}
