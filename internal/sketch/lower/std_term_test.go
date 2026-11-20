//go:build linux || darwin

package lower

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"ergo.local/prototype/internal/lawledger"
	"ergo.local/prototype/internal/proclimit"
)

// ptyIoctl is an ioctl on fd with the kernel struct at arg (nil for none).
// arg points to a live Go value of the request's kernel type.
func ptyIoctl(fd uintptr, req uintptr, arg unsafe.Pointer) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(arg)); errno != 0 {
		return errno
	}
	return nil
}

// ptyState is the terminal state of f.
func ptyState(t *testing.T, f *os.File) syscall.Termios {
	t.Helper()
	var st syscall.Termios
	if err := ptyIoctl(f.Fd(), ptyGetState, unsafe.Pointer(&st)); err != nil {
		t.Fatal(err)
	}
	return st
}

// termModesProbe runs the ops of its arguments: raw and noecho open a
// scope (term.Raw, term.NoEcho) for the ops after them, until a leave;
// read prints "read" and then the key it reads; stop prints "wait" and
// reads a key that never comes under timeout.After, whose stop ends the
// read and whose Expired leaves every open scope by a failure.
const termModesProbe = `import io
import os
import term
import timeout

func keyName(k term.Key) string {
	match k {
	case term.Char as c:
		"char " + c.Text
	case term.Return:
		"return"
	case term.Backspace:
		"backspace"
	case term.Tab:
		"tab"
	case term.Escape:
		"escape"
	case term.Up:
		"up"
	case term.Down:
		"down"
	case term.Left:
		"left"
	case term.Right:
		"right"
	case term.Interrupt:
		"interrupt"
	case term.EndOfInput:
		"end"
	case term.Other as o:
		"other " + o.Bytes
	case term.Chord as c:
		"chord " + (if c.Mods.Shift { "s" } else { "" }) + (if c.Mods.Alt { "a" } else { "" }) + (if c.Mods.Ctrl { "c" } else { "" }) + " " + c.Name
	case term.Paste as p:
		"paste " + p.Text
	case _:
		match term.KeyName(k) {
		case Some[string] as n:
			"key " + n.Value
		case None:
			"unnamed"
		}
	}
}

func run(ops []string, at int) int fails term.NotTerminal | io.ReadFailed | timeout.Expired needs term.Terminal | Console | Scheduler {
	let mut i = at
	for _ in 0..len(ops) {
		if i >= len(ops) {
			return i
		}
		let op = ops[i]
		i = i + 1
		if op == "raw" {
			i = try term.Raw() { try run(ops, i) }
		} else if op == "noecho" {
			i = try term.NoEcho() { try run(ops, i) }
		} else if op == "leave" {
			return i
		} else if op == "read" {
			Console.Println("read")
			Console.Println(keyName(try term.Terminal.ReadKey()))
		} else {
			Console.Println("wait")
			let k = try timeout.After(50ms) { try term.Terminal.ReadKey() }
			Console.Println("late " + keyName(k))
		}
	}
	i
}

func bit(b bool) string { if b { "1" } else { "0" } }

func main() needs Ambient | Console {
	let ops = with context { os.Process: os.NewHost() } { os.Process.Args() }
	with context { io.Stdin: io.NewStdin(), io.Stdout: io.NewHost(), io.Stderr: io.NewErrors() } {
		Console.Println("terminals " + bit(io.Stdin.IsTerminal()) + bit(io.Stdout.IsTerminal()) + bit(io.Stderr.IsTerminal()))
	}
	with context { term.Terminal: term.NewHost(), Scheduler: NewLiveScheduler() } {
		let _ = run(ops, 0) catch {
		case timeout.Expired:
			Console.Println("stopped")
			return
		case term.NotTerminal:
			Console.Println("not a terminal")
			return
		case io.ReadFailed as f:
			Console.Println("read failed " + f.Reason)
			return
		}
		Console.Println("done")
	}
}
`

// termKeys are the keys the model types, each with the name the probe
// prints for it: bytes as a terminal sends them, the two forms of an
// arrow, both bytes of Backspace and Return, a rune of two bytes, Ctrl-C
// and Ctrl-D as keys of a raw mode, the named keys of UC1 (keys.ergo:
// a page key, Delete, a Ctrl and a modified arrow, a bracketed paste),
// and a sequence with no name.
var termKeys = []struct{ bytes, name string }{
	{"a", "char a"}, {" ", "char  "}, {"~", "char ~"}, {"é", "char é"}, {"界", "char 界"},
	{"\r", "return"}, {"\n", "return"}, {"\x7f", "backspace"}, {"\x08", "backspace"},
	{"\t", "tab"}, {"\x1b", "escape"}, {"\x03", "interrupt"}, {"\x04", "end"},
	{"\x1b[A", "up"}, {"\x1b[B", "down"}, {"\x1b[C", "right"}, {"\x1b[D", "left"},
	{"\x1bOA", "up"}, {"\x1bOD", "left"}, {"\x1b[5~", "key PageUp"}, {"\x1b[3~", "key Delete"}, {"\x01", "chord c a"},
	{"\x1b[1;5A", "chord c ArrowUp"}, {"\x1b[200~hi there\x1b[201~", "paste hi there"}, {"\x1b[99X", "other \x1b[99X"},
}

// termOps is a generated op sequence: scopes that open and leave, reads
// inside them, and at the end a stop or nothing. A read with no raw scope
// open reads in canonical mode, so it is typed a line feed.
func termOps(r *rand.Rand) []string {
	var ops []string
	depth := 0
	for range 2 + r.IntN(9) {
		switch k := r.IntN(4); {
		case k == 0 || depth == 0 && k == 3:
			ops = append(ops, []string{"raw", "noecho"}[r.IntN(2)])
			depth++
		case k == 3:
			ops = append(ops, "leave")
			depth--
		default:
			ops = append(ops, "read")
		}
	}
	if r.IntN(2) == 0 {
		ops = append(ops, "stop")
	}
	return ops
}

// TestTermModesRestore is CU2's acceptance: a model of std term's host over
// a pseudo-terminal. For generated sequences of raw, noecho, read, leave
// and stop, at each read the terminal's canonical, echo and signal flags
// are the model's (canonical and signals off while a raw scope is open,
// echo off while any scope is open), each typed key reads as its name, and
// after the program the terminal's whole state is the state before it,
// whether the program ended its scopes, returned from them or was stopped
// in a read. Each stream answers IsTerminal for itself: standard input,
// the terminal, is one, and the pipes of output and error are not. A
// failing seed prints its ops; replay with
// ERGO_TERM_SEED=<seed>.
//
// It builds Go because the restore under a stop is the built program's
// frame scope, its timeout and the kernel's terminal; no in-process check
// observes a terminal's state.
func TestTermModesRestore(t *testing.T) {
	needsBuild(t)
	exes := buildAll(t, []*lowered{mustLower(t, "termmodes", termModesProbe)}, true)
	if exes == nil {
		return
	}
	seeds := make([]uint64, 24)
	for i := range seeds {
		seeds[i] = uint64(i + 1)
	}
	if s := os.Getenv("ERGO_TERM_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = []uint64{n}
	}
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(seed, 2))
		ops := termOps(r)
		if msg := runTermModes(t, exes["termmodes"], ops, r); msg != "" {
			t.Fatalf("seed %d, ops %q: %s\nreplay: ERGO_TERM_SEED=%d go test -run '^TestTermModesRestore$' ./internal/sketch/lower", seed, ops, msg, seed)
		}
	}
}

// runTermModes runs the probe on a new pseudo-terminal with ops, types
// the keys it reads, and returns what differs from the model, or "".
func runTermModes(t *testing.T, exe string, ops []string, r *rand.Rand) string {
	master, slave, err := openPTY()
	if err != nil {
		t.Skipf("no pseudo-terminal: %v", err)
	}
	defer master.Close()
	defer slave.Close()
	go io.Copy(io.Discard, master) // the echo of canonical reads
	before := ptyState(t, slave)

	probeRan(exe)
	cmd := exec.Command(exe, ops...)
	cmd.Stdin = slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	fastTierGuard(t, "running the program "+cmd.Path)
	limit, err := proclimit.Limit(os.Environ(), proclimit.DefaultLimit)
	if err != nil {
		t.Fatal(err)
	}
	if err := proclimit.Start(cmd, limit); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.ProcessState == nil {
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			cmd.Wait()
		}
	}()
	lines := bufio.NewScanner(out)
	next := func() string {
		if !lines.Scan() {
			return "<end of output>"
		}
		return lines.Text()
	}

	var stack []string // the open scopes of the model
	raw := func() bool {
		for _, m := range stack {
			if m == "raw" {
				return true
			}
		}
		return false
	}
	got := []string{next()}
	want := []string{"terminals 100"} // standard input is the terminal; output and error are pipes
ops:
	for _, op := range ops {
		switch op {
		case "raw", "noecho":
			stack = append(stack, op)
		case "leave":
			stack = stack[:len(stack)-1]
		case "read":
			want = append(want, "read")
			line := next()
			got = append(got, line)
			if line != "read" {
				break ops
			}
			st := ptyState(t, slave)
			canon, echo, isig := st.Lflag&syscall.ICANON != 0, st.Lflag&syscall.ECHO != 0, st.Lflag&syscall.ISIG != 0
			if canon == raw() || isig == raw() || echo != (len(stack) == 0) {
				return fmt.Sprintf("at read %d with scopes %v: canonical %v, signals %v, echo %v", len(want), stack, canon, isig, echo)
			}
			key := termKeys[5+r.IntN(2)] // a line ends a canonical read
			if raw() {
				key = termKeys[r.IntN(len(termKeys))]
			}
			if _, err := master.WriteString(key.bytes); err != nil {
				t.Fatal(err)
			}
			want = append(want, key.name)
			got = append(got, next())
		case "stop":
			want = append(want, "wait", "stopped")
			got = append(got, next(), next())
			break ops
		}
	}
	if len(want) == 0 || want[len(want)-1] != "stopped" {
		want = append(want, "done")
		got = append(got, next())
	}
	io.Copy(io.Discard, out)
	if err := cmd.Wait(); err != nil {
		return fmt.Sprintf("exit: %v\nstderr: %s", err, stderr.String())
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		return fmt.Sprintf("printed\n%q\nmodel\n%q\nstderr: %s", got, want, stderr.String())
	}
	if after := ptyState(t, slave); after != before {
		return fmt.Sprintf("the terminal's state after the program\n%+v\nis not the state before it\n%+v", after, before)
	}
	return ""
}

// termLawsProbe restates std term's laws (stdlib.md, Std laws) as tests.
const termLawsProbe = `import io
import term

type failed {}

func failWhen(fail bool) int fails failed {
	if fail {
		return failed
	}
	0
}

func names(modes []term.Mode) string {
	let mut out = ""
	for m in modes {
		if out != "" {
			out = out + ","
		}
		match m {
		case term.RawMode:
			out = out + "raw"
		case term.NoEchoMode:
			out = out + "noecho"
		}
	}
	out
}

test RawRestores(n int) {
	let fail = n % 2 != 0
	let s = term.NewScript([]term.Key{}, term.Window{Cols: 80, Rows: 24})
	let mut inside = 0
	let got = with context { term.Terminal: s } {
		term.Raw() {
			inside = len(s.Modes())
			try failWhen(fail)
		} catch {
		case failed:
			-1
		case term.NotTerminal:
			-2
		}
	}
	assert inside == 1 && len(s.Modes()) == 0 && got == (if fail { -1 } else { 0 })
}

test NestedRestoresInOrder() {
	let s = term.NewScript([]term.Key{}, term.Window{Cols: 80, Rows: 24})
	let mut seen = ""
	with context { term.Terminal: s } {
		term.Raw() {
			try term.NoEcho() { seen = seen + names(s.Modes()) }
			seen = seen + "|" + names(s.Modes())
		} catch {
		case term.NotTerminal:
			seen = "not a terminal"
		}
	}
	assert seen == "raw,noecho|raw" && len(s.Modes()) == 0
}

test ScriptPlaysKeys(texts []string) {
	let mut keys []term.Key = []term.Key{}
	for t in texts {
		keys = keys.Append(term.Char{Text: t})
	}
	let s = term.NewScript(keys, term.Window{Cols: 1, Rows: 1})
	let mut ok = true
	with context { term.Terminal: s } {
		for t in texts {
			match term.Terminal.ReadKey() catch { case io.ReadFailed: term.EndOfInput } {
			case term.Char as c:
				ok = ok && c.Text == t
			case _:
				ok = false
			}
		}
		match term.Terminal.ReadKey() catch { case io.ReadFailed: term.Return } {
		case term.EndOfInput:
			ok = ok
		case _:
			ok = false
		}
	}
	assert ok
}

test DecodeNamesControls(token string) {
	let ok = match term.Decode(token) {
	case term.Char as c:
		c.Text == token && token >= " " && token != "\x7f"
	case _:
		token < " " || token == "\x7f" || token == ""
	}
	assert ok
}

// UC1: the key laws of keys.ergo and term.ergo restated.
// recorded is one key's bytes as a terminal sends them and its key. (a
// law's helper)
type recorded { bytes string, key term.Key }

// mods is a term.Chord's modifiers: Shift, Alt, Ctrl. (a law's helper)
func mods(shift bool, alt bool, ctl bool) term.Mods { term.Mods{Shift: shift, Ctrl: ctl, Alt: alt, Meta: false} }

// keyTable is the recorded keys. (a law's helper)
let keyTable = []recorded{
	recorded{bytes: "a", key: term.Char{Text: "a"}},
	recorded{bytes: "~", key: term.Char{Text: "~"}},
	recorded{bytes: "é", key: term.Char{Text: "é"}},
	recorded{bytes: "界", key: term.Char{Text: "界"}},
	recorded{bytes: "🙂", key: term.Char{Text: "🙂"}},
	recorded{bytes: "\r", key: term.Return},
	recorded{bytes: "\n", key: term.Return},
	recorded{bytes: "\x7f", key: term.Backspace},
	recorded{bytes: "\x08", key: term.Backspace},
	recorded{bytes: "\t", key: term.Tab},
	recorded{bytes: "\x1b", key: term.Escape},
	recorded{bytes: "\x03", key: term.Interrupt},
	recorded{bytes: "\x04", key: term.EndOfInput},
	recorded{bytes: "\x01", key: term.Chord{Mods: mods(false, false, true), Name: "a"}},
	recorded{bytes: "\x1a", key: term.Chord{Mods: mods(false, false, true), Name: "z"}},
	recorded{bytes: "\x00", key: term.Chord{Mods: mods(false, false, true), Name: " "}},
	recorded{bytes: "\x1b[A", key: term.Up},
	recorded{bytes: "\x1b[B", key: term.Down},
	recorded{bytes: "\x1b[C", key: term.Right},
	recorded{bytes: "\x1b[D", key: term.Left},
	recorded{bytes: "\x1bOA", key: term.Up},
	recorded{bytes: "\x1bOD", key: term.Left},
	recorded{bytes: "\x1b[H", key: term.Home},
	recorded{bytes: "\x1b[F", key: term.End},
	recorded{bytes: "\x1bOH", key: term.Home},
	recorded{bytes: "\x1bOF", key: term.End},
	recorded{bytes: "\x1b[1~", key: term.Home},
	recorded{bytes: "\x1b[4~", key: term.End},
	recorded{bytes: "\x1b[2~", key: term.Insert},
	recorded{bytes: "\x1b[3~", key: term.Delete},
	recorded{bytes: "\x1b[5~", key: term.PageUp},
	recorded{bytes: "\x1b[6~", key: term.PageDown},
	recorded{bytes: "\x1bOP", key: term.F{N: 1}},
	recorded{bytes: "\x1bOS", key: term.F{N: 4}},
	recorded{bytes: "\x1b[15~", key: term.F{N: 5}},
	recorded{bytes: "\x1b[17~", key: term.F{N: 6}},
	recorded{bytes: "\x1b[21~", key: term.F{N: 10}},
	recorded{bytes: "\x1b[24~", key: term.F{N: 12}},
	recorded{bytes: "\x1b[1;5A", key: term.Chord{Mods: mods(false, false, true), Name: "ArrowUp"}},
	recorded{bytes: "\x1b[1;2D", key: term.Chord{Mods: mods(true, false, false), Name: "ArrowLeft"}},
	recorded{bytes: "\x1b[1;3C", key: term.Chord{Mods: mods(false, true, false), Name: "ArrowRight"}},
	recorded{bytes: "\x1b[1;6H", key: term.Chord{Mods: mods(true, false, true), Name: "Home"}},
	recorded{bytes: "\x1b[3;5~", key: term.Chord{Mods: mods(false, false, true), Name: "Delete"}},
	recorded{bytes: "\x1b[5;2~", key: term.Chord{Mods: mods(true, false, false), Name: "PageUp"}},
	recorded{bytes: "\x1b[1;2P", key: term.Chord{Mods: mods(true, false, false), Name: "F1"}},
	recorded{bytes: "\x1b[15;5~", key: term.Chord{Mods: mods(false, false, true), Name: "F5"}},
	recorded{bytes: "\x1b[Z", key: term.Chord{Mods: mods(true, false, false), Name: "Tab"}},
	recorded{bytes: "\x1bx", key: term.Chord{Mods: mods(false, true, false), Name: "x"}},
	recorded{bytes: "\x1b\x7f", key: term.Chord{Mods: mods(false, true, false), Name: "Backspace"}},
	recorded{bytes: "\x1b\x01", key: term.Chord{Mods: mods(false, true, true), Name: "a"}},
	recorded{bytes: "\x1b\x1b[A", key: term.Chord{Mods: mods(false, true, false), Name: "ArrowUp"}},
	recorded{bytes: "\x1b[97;5u", key: term.Chord{Mods: mods(false, false, true), Name: "a"}},
	recorded{bytes: "\x1b[13;2u", key: term.Chord{Mods: mods(true, false, false), Name: "Enter"}},
	recorded{bytes: "\x1b[27u", key: term.Escape},
	recorded{bytes: "\x1b[127;3u", key: term.Chord{Mods: mods(false, true, false), Name: "Backspace"}},
	recorded{bytes: "\x1b[1089::99;5u", key: term.Chord{Mods: mods(false, false, true), Name: "с"}},
	recorded{bytes: "\x1b[200~hello\r\nworld\x1b[201~", key: term.Paste{Text: "hello\r\nworld"}},
	recorded{bytes: "\x1b[200~\x1b[201~", key: term.Paste{Text: ""}},
	recorded{bytes: "\x1b[<64;10;5M", key: term.Wheel{Up: true, Col: 10, Row: 5}},
	recorded{bytes: "\x1b[<65;1;24M", key: term.Wheel{Up: false, Col: 1, Row: 24}},
	recorded{bytes: "\x1b[<80;3;3M", key: term.Wheel{Up: true, Col: 3, Row: 3}},
	recorded{bytes: "\x1b[<0;3;3M", key: term.Other{Bytes: "\x1b[<0;3;3M"}},
	recorded{bytes: "\x1b[99X", key: term.Other{Bytes: "\x1b[99X"}},
	recorded{bytes: "\x1c", key: term.Other{Bytes: "\x1c"}},
}

// DecodeIsTheTable: over drawn runs of recorded keys sent in drawn
// chunks, term.Feed and then term.Flush give the run's keys in order, and each
// recorded key's bytes are one key (term.Cut) that term.Decode names. A chunk never
// ends right after an ESC with more of its key to come, and always ends
// after an term.Escape key alone, since a terminal writes each key in one
// write: that is the one rule that tells term.Escape and then a key from Alt
// and the key.
func lawDecodeIsTheTable(seed int, length int) bool {
	let mut r = seeded(seed)
	let n = (length%24+24)%24 + 1
	let mut want = []term.Key{}
	let mut chunks = []string{}
	let mut chunk = ""
	for _ in 0..n {
		let e = keyTable[draw(&r, len(keyTable))]
		if term.Cut(e.bytes, false) != len(e.bytes) || term.Decode(e.bytes) != e.key {
			return false
		}
		want = want.Append(e.key)
		for i in 0..len(e.bytes) {
			chunk = chunk + e.bytes[i:i+1]
			let startsLonger = e.bytes[i] == 0x1b && i+1 < len(e.bytes)
			if !startsLonger && draw(&r, 3) == 0 {
				chunks = chunks.Append(chunk)
				chunk = ""
			}
		}
		if e.bytes == "\x1b" && chunk != "" {
			chunks = chunks.Append(chunk)
			chunk = ""
		}
	}
	chunks = chunks.Append(chunk)
	let mut d = term.NewDecoder()
	let mut got = []term.Key{}
	for c in chunks {
		for k in term.Feed(&d, c) {
			got = got.Append(k)
		}
	}
	for k in term.Flush(&d) {
		got = got.Append(k)
	}
	got == want
}

// CutTakesOneKey: over drawn bytes, term.Cut at the end of the input takes
// between one byte and all of them from a nonempty b, and term.Cut before the
// end takes no more than at the end, or waits. (a property against no
// table: a cut never runs past its bytes or loops on none)
func lawCutTakesOneKey(seed int, size int) bool {
	let mut r = seeded(seed)
	let mut b = ""
	let alphabet = "\x1b[O;<0123456789~uAHPMZ\x01\x7fa\xc3\xa9\xe7\x95\x8c"
	for _ in 0..(size%12+12)%12 {
		let i = draw(&r, len(alphabet))
		b = b + alphabet[i:i+1]
	}
	let atEnd = term.Cut(b, true)
	let before = term.Cut(b, false)
	if len(b) == 0 {
		return atEnd == 0 && before == -1
	}
	atEnd >= 1 && atEnd <= len(b) && (before == -1 || (before >= 1 && before <= atEnd))
}

// ScriptResizes: a Script's Size is its window until it plays a term.Resized,
// and that term.Resized's window after.
func lawScriptResizes(cols int, rows int) bool {
	let w = term.Window{Cols: (cols%300+300)%300 + 1, Rows: (rows%100+100)%100 + 1}
	let s = term.NewScript([]term.Key{term.Char{Text: "a"}, term.Resized{Window: w}, term.Char{Text: "b"}}, term.Window{Cols: 80, Rows: 24})
	let mut sizes = []term.Window{}
	with context { term.Terminal: s } {
		for _ in 0..3 {
			let _ = term.Terminal.ReadKey() catch {
			case io.ReadFailed:
				term.EndOfInput
			}
			sizes = sizes.Append(term.Terminal.Size() catch {
			case term.NotTerminal:
				term.Window{Cols: 0, Rows: 0}
			})
		}
	}
	sizes == []term.Window{term.Window{Cols: 80, Rows: 24}, w, w}
}

// rng is a law's seeded generator. (a law's helper)
type rng { x int }

// draw is a number in [0, n) from r. (a law's helper)
func draw(r mut rng, n int) int {
	r.x = (r.x*1103515245 + 12345) % 2147483648
	(r.x / 65536) % n
}

// seeded is the generator of seed. (a law's helper)
func seeded(seed int) rng { rng{x: (seed%2147483648 + 2147483648) % 2147483648} }

test DecodeIsTheTable(seed int, length int) {
	assert lawDecodeIsTheTable(seed, length)
}

test CutTakesOneKey(seed int, size int) {
	assert lawCutTakesOneKey(seed, size)
}

test ScriptResizes(cols int, rows int) {
	assert lawScriptResizes(cols, rows)
}
`

// termLawStatements are the ledger statements of term's laws.
var termLawStatements = map[string]string{
	"term RawRestores":           `RawRestores(fail bool) { let s = NewScript([]Key{}, Window{Cols: 80, Rows: 24}) let mut inside = 0 let got = with context { Terminal: s } { Raw() { inside = len(s.Modes()) try failWhen(fail) } catch { case failed: -1 case NotTerminal: -2 } } inside == 1 && len(s.Modes()) == 0 && got == (if fail { -1 } else { 0 }) }`,
	"term NestedRestoresInOrder": `NestedRestoresInOrder() { let s = NewScript([]Key{}, Window{Cols: 80, Rows: 24}) let mut seen = "" with context { Terminal: s } { Raw() { try NoEcho() { seen = seen + names(s.Modes()) } seen = seen + "|" + names(s.Modes()) } catch { case NotTerminal: seen = "not a terminal" } } seen == "raw,noecho|raw" && len(s.Modes()) == 0 }`,
	"term ScriptPlaysKeys":       `ScriptPlaysKeys(texts []string) { let mut keys []Key = []Key{} for t in texts { keys = keys.Append(Char{Text: t}) } let s = NewScript(keys, Window{Cols: 1, Rows: 1}) let mut ok = true with context { Terminal: s } { for t in texts { match Terminal.ReadKey() catch { case io.ReadFailed: EndOfInput } { case Char as c: ok = ok && c.Text == t case _: ok = false } } match Terminal.ReadKey() catch { case io.ReadFailed: Return } { case EndOfInput: ok = ok case _: ok = false } } ok }`,
	"term DecodeNamesControls":   `DecodeNamesControls(token string) { match Decode(token) { case Char as c: c.Text == token && token >= " " && token != "\x7f" case _: token < " " || token == "\x7f" || token == "" } }`,
	"term DecodeIsTheTable":      `DecodeIsTheTable(seed int, length int) { let mut r = seeded(seed) let n = (length%24+24)%24 + 1 let mut want = []Key{} let mut chunks = []string{} let mut chunk = "" for _ in 0..n { let e = keyTable[draw(&r, len(keyTable))] if Cut(e.bytes, false) != len(e.bytes) || Decode(e.bytes) != e.key { return false } want = want.Append(e.key) for i in 0..len(e.bytes) { chunk = chunk + e.bytes[i:i+1] let startsLonger = e.bytes[i] == 0x1b && i+1 < len(e.bytes) if !startsLonger && draw(&r, 3) == 0 { chunks = chunks.Append(chunk) chunk = "" } } if e.bytes == "\x1b" && chunk != "" { chunks = chunks.Append(chunk) chunk = "" } } chunks = chunks.Append(chunk) let mut d = NewDecoder() let mut got = []Key{} for c in chunks { for k in Feed(&d, c) { got = got.Append(k) } } for k in Flush(&d) { got = got.Append(k) } got == want }`,
	"term CutTakesOneKey":        `CutTakesOneKey(seed int, size int) { let mut r = seeded(seed) let mut b = "" let alphabet = "\x1b[O;<0123456789~uAHPMZ\x01\x7fa\xc3\xa9\xe7\x95\x8c" for _ in 0..(size%12+12)%12 { let i = draw(&r, len(alphabet)) b = b + alphabet[i:i+1] } let atEnd = Cut(b, true) let before = Cut(b, false) if len(b) == 0 { return atEnd == 0 && before == -1 } atEnd >= 1 && atEnd <= len(b) && (before == -1 || (before >= 1 && before <= atEnd)) }`,
	"term ScriptResizes":         `ScriptResizes(cols int, rows int) { let w = Window{Cols: (cols%300+300)%300 + 1, Rows: (rows%100+100)%100 + 1} let s = NewScript([]Key{Char{Text: "a"}, Resized{Window: w}, Char{Text: "b"}}, Window{Cols: 80, Rows: 24}) let mut sizes = []Window{} with context { Terminal: s } { for _ in 0..3 { let _ = Terminal.ReadKey() catch { case io.ReadFailed: EndOfInput } sizes = sizes.Append(Terminal.Size() catch { case NotTerminal: Window{Cols: 0, Rows: 0} }) } } sizes == []Window{Window{Cols: 80, Rows: 24}, w, w} }`,
}

// TestTermLaws runs std term's laws: the scope laws over a Script kill a
// Raw that restores only on success and a Restore that drops the oldest
// mode, which the pty model reaches only on the host.
//
// It builds Go because the laws run in the built test binary (ergo test),
// the only place a law runs.
func TestTermLaws(t *testing.T) {
	needsBuild(t)
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "termlaws", termLawsProbe)}, true)
	if exes == nil {
		return
	}
	out, errout, code := runExeArgs(t, exes["termlaws"], "--seed", "1")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errout)
	}
	lawledger.Run(t, lawsPassed(t, termLawStatements, func(key string) string {
		return strings.TrimPrefix(key, "term ")
	}, termLawsProbe, out))
}
