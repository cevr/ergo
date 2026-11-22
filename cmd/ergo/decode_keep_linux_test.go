package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/proclimit"
)

// keepKeysSource keeps keepKeys decoded values, each from a text of its
// own (one member, then 8 KiB of white space), and prints how many keys
// it kept and their total length. Its argument names the decode: decode,
// json.Decode of a Map (the direct reader's Map decoder); reader,
// decode[T] of a Map on the prelude's Reader (the interface decoder);
// value, json.Decode of a json.Value (an Object member's key and a Num's digits); stream,
// json.NewElements of Maps from a JSON array on standard input. After a
// decode of its own texts it waits for the end of standard input, still
// keeping what it decoded.
const keepKeysSource = `import io
import os
import json

let keepCount = 10000

func text(i int) string { "{\"k\(i)\":1" + pad() + "}" }

func pad() string {
	let mut s = " "
	for _ in 0..13 {
		s = s + s
	}
	s
}

func take(r Reader) Map[string, int] fails WireWrongKind | WireMissingField | WireUnknownTag {
	decode[Map[string, int]](r)
}

func keys(ms []Map[string, int]) string {
	let mut n = 0
	let mut total = 0
	for m in ms {
		for k, _ in m {
			n = n + 1
			total = total + len(k)
		}
	}
	"\(n) \(total)"
}

func memberKeys(vs []json.Value) string {
	let mut n = 0
	let mut total = 0
	for v in vs {
		match v {
		case json.Object as o:
			for m in o.Members {
				n = n + 1
				total = total + len(m.Key)
			}
		case _:
		}
	}
	"\(n) \(total)"
}

func run(mode string) string fails json.Malformed | io.ReadFailed | WireWrongKind | WireMissingField | WireUnknownTag needs Ambient {
	if mode == "stream" {
		let mut kept = []Map[string, int]{}
		let e = json.NewElements[Map[string, int]](json.NewStream(io.NewStdin(), 0, 0))
		for {
			match try e.Next() {
			case Some[Map[string, int]] as s:
				kept = kept.Append(s.Value)
			case None:
				return keys(kept)
			}
		}
	}
	if mode == "value" {
		let mut vs = []json.Value{}
		for i in 0..keepCount {
			vs = vs.Append(try json.Decode(json.Codec[json.Value]{MaxBytes: 0, MaxDepth: 0}, text(i)))
		}
		return memberKeys(vs)
	}
	let mut kept = []Map[string, int]{}
	for i in 0..keepCount {
		if mode == "reader" {
			kept = kept.Append(try take(try json.Open(text(i), 0, 0)))
		} else {
			kept = kept.Append(try json.Decode(json.Codec[Map[string, int]]{MaxBytes: 0, MaxDepth: 0}, text(i)))
		}
	}
	keys(kept)
}

func main() fails json.Malformed | io.ReadFailed | WireWrongKind | WireMissingField | WireUnknownTag needs Ambient {
	let args = os.NewHost().Args()
	let mode = args[len(args) - 1]
	let got = try run(mode)
	io.NewHost().Print(got + "\n")
	if mode != "stream" {
		let _ = try io.NewStdin().ReadAll()
	}
}
`

// TestDecodedKeysKeepNoText is the guard of stdlib.md json 2's key rule:
// a Reader's Key is a window of the text, which a record's decode only
// compares, and each decode that keeps a key (a Map's, both the direct
// and the interface decoder, and a json.Value's Object member) keeps its
// own copy, from a whole text and from a Stream alike. A program keeps
// 10000 short keys, each read from a text or chunk of its own of 8 KiB,
// and its peak resident memory stays under the bound; were each key a
// window, the kept keys would hold 10000 texts (about 80 MiB) or windows
// (about 40 MiB). It builds Go because what a key keeps alive is the heap
// of the running program, which no in-process check of the lowered Go
// shows; it runs bounded by proclimit and waits for the program's exit.
// The peak is VmHWM, read while the program holds every key, never a
// time, as TestStreamValuesKeepNoWindow reads it.
//
// Mutants (lower ledger): map-key-not-cloned-text (wireDecodeBody's Map
// keeps Key as it is) puts decode and stream over the bound;
// map-key-not-cloned-ir (ir_wire_read.go's Map) puts reader over it;
// member-key-not-cloned (wireMembersDecode) and num-not-owned (json
// ParseNum keeps its text) put value over it.
func TestDecodedKeysKeepNoText(t *testing.T) {
	needsBuild(t)
	const keepKeys, pad, keepBound = 10000, 8192, 32 << 20
	dir := t.TempDir()
	src := filepath.Join(dir, "keepkeys.ergo")
	writeFile(t, src, keepKeysSource)
	bin := filepath.Join(dir, "keepkeys")
	build := exec.Command(buildTestCLI(t), "build", "-o", bin, src)
	var out bytes.Buffer
	build.Stdout, build.Stderr = &out, &out
	if err := proclimit.Run(build, 4<<30); err != nil {
		t.Fatalf("ergo build: %v\n%s", err, out.Bytes())
	}
	own := 0 // the total length of the keys "k0" to "k9999"
	for i := range keepKeys {
		own += 1 + len(strconv.Itoa(i))
	}
	for _, mode := range []string{"decode", "reader", "value", "stream"} {
		t.Run(mode, func(t *testing.T) {
			peak, got := keepKeysRun(t, bin, mode, keepKeys, pad)
			want := fmt.Sprintf("%d %d", keepKeys, own)
			if mode == "stream" {
				want = fmt.Sprintf("%d %d", keepKeys, keepKeys*6)
			}
			if got != want {
				t.Fatalf("keepkeys %s printed %q, want %q", mode, got, want)
			}
			t.Logf("peak resident memory keeping %d keys: %d KiB", keepKeys, peak>>10)
			if peak > keepBound {
				t.Errorf("keeping %d keys, each read from its own 8 KiB, peaked at %d KiB; want at most %d KiB: a kept key keeps its text", keepKeys, peak>>10, keepBound>>10)
			}
		})
	}
}

// keepKeysRun runs bin in mode and gives its peak resident memory, read
// while it keeps every key, and the line it printed. In stream mode it
// writes the array of keepKeys objects, each padded, and reads the peak
// before the array's last byte; in the others it reads the peak once the
// program has printed, then ends its standard input.
func keepKeysRun(t *testing.T, bin, mode string, keepKeys, pad int) (int64, string) {
	t.Helper()
	in, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer outR.Close()
	run := exec.Command(bin, mode)
	// GOGC=off with a memory limit, as TestStreamValuesKeepNoWindow runs:
	// the heap stays near the limit, so the peak is what the kept keys hold.
	run.Env = append(os.Environ(), "GOMAXPROCS=1", "GOGC=off", "GOMEMLIMIT=16MiB")
	run.Stdin = in
	run.Stdout = outW
	var errs bytes.Buffer
	run.Stderr = &errs
	watch := proclimit.Watch(run)
	err = proclimit.Start(run, 1<<30)
	in.Close()
	outW.Close()
	if err != nil {
		t.Fatalf("keepkeys: %v\n%s", err, errs.Bytes())
	}
	lines := bufio.NewReader(outR)
	var peak int64
	var perr, werr error
	if mode == "stream" {
		spaces := strings.Repeat(" ", pad)
		_, werr = io.WriteString(w, "[")
		for i := 0; i < keepKeys && werr == nil; i++ {
			sep := ","
			if i == keepKeys-1 {
				sep = ""
			}
			_, werr = fmt.Fprintf(w, "{%q:1}%s%s", fmt.Sprintf("k%05d", i), spaces, sep)
		}
		peak, perr = residentPeak(run.Process.Pid)
		if werr == nil {
			_, werr = io.WriteString(w, "]")
		}
		w.Close()
	}
	line, lerr := lines.ReadString('\n')
	if mode != "stream" {
		peak, perr = residentPeak(run.Process.Pid)
		w.Close()
	}
	if err := proclimit.Check(run.Path, 1<<30, run.Wait(), watch.Stderr()); err != nil {
		t.Fatalf("keepkeys: %v\n%s%s", err, line, errs.Bytes())
	}
	if werr != nil {
		t.Fatalf("writing keepkeys' input: %v", werr)
	}
	if lerr != nil {
		t.Fatalf("reading keepkeys' output: %v\n%s", lerr, errs.Bytes())
	}
	if perr != nil {
		t.Fatal(perr)
	}
	return peak, strings.TrimSuffix(line, "\n")
}
