package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"testing"

	"ergo.local/prototype/internal/proclimit"
)

// TestLzpackAgreesWithControl is the agreement property of examples/lzpack
// (corpus wave HP2): on seeded inputs, `lzpack pack` of the Ergo program
// and of the hand Go control (testdata/lzpack/control) write exactly the
// bytes a plain reference parse writes (lzpackRefPack: the greedy rule of
// lz/pack.ergo over a list of positions per hash, no chain), and the
// reference decoder (lzpackRefUnpack) reads them back to the input. On
// those streams and on corrupted ones (cut short, a byte changed, bytes
// after the end mark, a long block header, noise after the magic), `lzpack
// unpack` of both prints what the reference decoder prints: stdout, stderr
// and the exit status, byte for byte. The inputs reach the block edge
// (65,536), repeats farther back than the window, runs that overlap their
// copy, long literal and match lengths (the 255 extension), and the four
// kinds of the receipts. A failing seed prints its replay command.
//
// It builds Go because the programs under test are the linked binaries:
// the Ergo program maps its input (fs.MapFile) and writes standard output,
// and the control is Go; no in-process check runs either. Each run is
// bounded by proclimit, and the test waits for its exit.
//
// It kills the example's mutants that no law sees (examples/lzpack/
// testdata/mutants.json).
func TestLzpackAgreesWithControl(t *testing.T) {
	needsBuild(t)
	ergoBin, work := buildExample(t, "lzpack")
	control := buildExampleGo(t, work, "lzpack", "control")
	dir := t.TempDir()
	seeds := 60
	first := uint64(1)
	if s := os.Getenv("LZPACK_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("LZPACK_SEED=%q is not a seed", s)
		}
		first, seeds = n, 1
	}
	for seed := first; seed < first+uint64(seeds); seed++ {
		r := rand.New(rand.NewPCG(seed, 0x12a7))
		input := lzpackInput(r)
		in := filepath.Join(dir, "in")
		if err := os.WriteFile(in, input, 0o644); err != nil {
			t.Fatal(err)
		}
		want := lzpackRefPack(input)
		if back, c := lzpackRefUnpack(want); c != "" || !bytes.Equal(back, input) {
			t.Fatalf("seed %d: the reference does not read its own stream back (%s)", seed, c)
		}
		replay := fmt.Sprintf("replay: LZPACK_SEED=%d go test -count=1 -run '^TestLzpackAgreesWithControl$' ./cmd/ergo", seed)
		wantRun := fmt.Sprintf("stdout:\n%x\nstderr:\n\nstatus 0\n", want)
		for _, bin := range []string{ergoBin, control} {
			if got := runLzpack(t, bin, "pack", in); got != wantRun {
				t.Fatalf("seed %d (%d bytes): %s pack printed\n%s\nthe reference\n%s\n%s", seed, len(input), filepath.Base(bin), clip(got), clip(wantRun), replay)
			}
		}
		packed := filepath.Join(dir, "packed")
		for i, stream := range append([][]byte{want}, lzpackCorrupt(r, want)...) {
			if err := os.WriteFile(packed, stream, 0o644); err != nil {
				t.Fatal(err)
			}
			out, c := lzpackRefUnpack(stream)
			wantRun := fmt.Sprintf("stdout:\n%x\nstderr:\n\nstatus 0\n", out)
			if c != "" {
				wantRun = fmt.Sprintf("stdout:\n\nstderr:\nlzpack: %s: %s\n\nstatus 1\n", packed, c)
			}
			for _, bin := range []string{ergoBin, control} {
				if got := runLzpack(t, bin, "unpack", packed); got != wantRun {
					t.Fatalf("seed %d, stream %d (%d bytes): %s unpack printed\n%s\nthe reference\n%s\n%s", seed, i, len(stream), filepath.Base(bin), clip(got), clip(wantRun), replay)
				}
			}
		}
	}
}

// buildExample links examples/NAME/cmd/NAME with `ergo build` and returns
// the binary and the emitted module's directory.
func buildExample(t *testing.T, name string) (bin, work string) {
	t.Helper()
	program := filepath.Join(smokeWorkspace(t, "examples/"+name), "examples", name, "cmd", name)
	bin = filepath.Join(t.TempDir(), name)
	command := exec.Command(buildTestCLI(t), "build", "-work", "-o", bin, program)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := proclimit.Run(command, 4<<30); err != nil {
		t.Fatalf("ergo build: %v\n%s%s", err, stdout.Bytes(), stderr.Bytes())
	}
	return bin, workDir(t, stderr.String())
}

// buildExampleGo links testdata/EXAMPLE/NAME in a module with the go line
// of the example's emitted module in work, so one toolchain compiles both.
func buildExampleGo(t *testing.T, work, example, name string) string {
	t.Helper()
	mod, err := os.ReadFile(filepath.Join(work, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	goLine := regexp.MustCompile(`(?m)^go [0-9.]+$`).Find(mod)
	if goLine == nil {
		t.Fatalf("the emitted go.mod has no go line:\n%s", mod)
	}
	src, err := os.ReadFile(filepath.Join("testdata", example, name, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "module "+name+"\n\n"+string(goLine)+"\n")
	writeFile(t, filepath.Join(dir, "main.go"), string(src))
	bin := filepath.Join(dir, name)
	command := exec.Command("go", "build", "-o", bin, ".")
	command.Dir = dir
	var out bytes.Buffer
	command.Stdout, command.Stderr = &out, &out
	if err := proclimit.Run(command, 4<<30); err != nil {
		t.Fatalf("go build of %s/%s: %v\n%s", example, name, err, out.Bytes())
	}
	return bin
}

// runLzpack runs `bin verb path` and is its stdout (in hex), stderr and
// exit status as one text.
func runLzpack(t *testing.T, bin, verb, path string) string {
	t.Helper()
	command := exec.Command(bin, verb, path)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := proclimit.Run(command, 1<<30)
	code := 0
	if err != nil {
		exit, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("%s: %v", bin, err)
		}
		code = exit.ExitCode()
	}
	return fmt.Sprintf("stdout:\n%x\nstderr:\n%s\nstatus %d\n", stdout.Bytes(), stderr.Bytes(), code)
}

// lzpackInput is a seeded input: pieces of text, records, zeros, noise,
// runs and copies of earlier pieces (some farther back than the window),
// up to a little past two blocks, or nothing.
func lzpackInput(r *rand.Rand) []byte {
	size := []int{0, 1, 3, 4, 5, 40, 300, 5000, 65535, 65536, 65537, 70000, 140000}[r.IntN(13)]
	var b []byte
	words := []string{"the", "window", "of", "a", "block", "stream", "copies", "and", "literal", "é", "\x00\x01"}
	for len(b) < size {
		switch r.IntN(7) {
		case 0:
			for range 1 + r.IntN(60) {
				b = append(b, words[r.IntN(len(words))]...)
				b = append(b, ' ')
			}
		case 1:
			for range 1 + r.IntN(30) {
				b = binary.LittleEndian.AppendUint32(b, uint32(len(b)/7))
				b = append(b, byte(r.IntN(4)), 0, 0, 0)
			}
		case 2:
			b = append(b, make([]byte, r.IntN(700))...)
		case 3:
			for range r.IntN(3000) {
				b = append(b, byte(r.Uint32()))
			}
		case 4:
			c := byte(r.Uint32())
			for range 1 + r.IntN(400) {
				b = append(b, c)
			}
		case 5, 6:
			if len(b) > 8 {
				from := r.IntN(len(b))
				n := 1 + r.IntN(min(len(b)-from, 600))
				b = append(b, b[from:from+n]...)
			}
		}
	}
	return b[:size]
}

// lzpackCorrupt is streams near a valid stream that the decoder must
// refuse or read: cut short, a byte changed, a byte added at the end, a
// long block header, noise after the magic, the wrong magic.
func lzpackCorrupt(r *rand.Rand, valid []byte) [][]byte {
	var out [][]byte
	for range 3 {
		out = append(out, valid[:r.IntN(len(valid))])
		changed := bytes.Clone(valid)
		changed[r.IntN(len(changed))] = byte(r.Uint32())
		out = append(out, changed)
	}
	out = append(out, append(bytes.Clone(valid), byte(r.Uint32())))
	headers := [][]byte{{0x81, 0x80, 0x04}, {0xff, 0xff, 0x7f}, {0x80, 0x80, 0x80, 0x01}, {0x80, 0x00}}
	out = append(out, append([]byte("LZPK"), headers[r.IntN(len(headers))]...))
	noise := []byte("LZPK")
	for range r.IntN(40) {
		noise = append(noise, byte(r.Uint32()))
	}
	out = append(out, noise, []byte("LZP"), []byte("LZPJ\x00"))
	return out
}

// lzpackRefHash is the chain head of the four bytes at p.
func lzpackRefHash(b []byte, p int) int {
	return int((binary.LittleEndian.Uint32(b[p:]) * 2654435761) >> 17)
}

// lzpackRefPack is the format's greedy parse written plainly: for each
// position, the candidates are the earlier positions with its hash whose
// four bytes are in the input, newest first, fewer than 65,536 back, at
// most 16 of them; the longest match of at least 4 bytes within the block
// wins, the newest of equal ones, and a match of the whole rest of the
// block stops the search.
func lzpackRefPack(b []byte) []byte {
	at := map[int][]int{}
	for q := 0; q+4 <= len(b); q++ {
		h := lzpackRefHash(b, q)
		at[h] = append(at[h], q)
	}
	out := []byte("LZPK")
	ext := func(n int) {
		for ; n >= 255; n -= 255 {
			out = append(out, 255)
		}
		out = append(out, byte(n))
	}
	for from := 0; from < len(b); from += 65536 {
		end := min(from+65536, len(b))
		out = binary.AppendUvarint(out, uint64(end-from))
		anchor, p := from, from
		for p+4 <= end {
			list := at[lzpackRefHash(b, p)]
			i := sort.SearchInts(list, p) - 1
			best, bestQ := 0, 0
			for tries := 0; i >= 0 && tries < 16 && p-list[i] < 65536; i, tries = i-1, tries+1 {
				q, n := list[i], 0
				for p+n < end && b[q+n] == b[p+n] {
					n++
				}
				if n > best {
					best, bestQ = n, q
					if n == end-p {
						break
					}
				}
			}
			if best < 4 {
				p++
				continue
			}
			l, m := p-anchor, best-4
			out = append(out, byte(min(l, 15)<<4|min(m, 15)))
			if l >= 15 {
				ext(l - 15)
			}
			out = append(out, b[anchor:p]...)
			out = binary.LittleEndian.AppendUint16(out, uint16(p-bestQ))
			if m >= 15 {
				ext(m - 15)
			}
			p += best
			anchor = p
		}
		if anchor < end {
			l := end - anchor
			out = append(out, byte(min(l, 15)<<4))
			if l >= 15 {
				ext(l - 15)
			}
			out = append(out, b[anchor:end]...)
		}
	}
	return append(out, 0)
}

// lzpackRefUnpack decodes a stream by the format's text, byte by byte, and
// is the output or the text of the refusal ("corrupt at byte N: why").
func lzpackRefUnpack(in []byte) ([]byte, string) {
	fail := func(at int, why string) ([]byte, string) {
		return nil, fmt.Sprintf("corrupt at byte %d: %s", at, why)
	}
	if !bytes.HasPrefix(in, []byte("LZPK")) {
		return fail(0, "the stream does not start with LZPK")
	}
	var out []byte
	i := 4
	get := func() (int, bool) {
		if i >= len(in) {
			return 0, false
		}
		i++
		return int(in[i-1]), true
	}
	length := func(v int) (int, bool) {
		for {
			x, ok := get()
			if !ok {
				return 0, false
			}
			v += x
			if x != 255 {
				return v, true
			}
		}
	}
	short := func() ([]byte, string) { return fail(len(in), "the stream ends early") }
	for {
		h, n := i, 0
		for k := 0; ; k++ {
			x, ok := get()
			if !ok {
				return short()
			}
			n += (x & 127) << (7 * k)
			if x < 128 {
				break
			}
			if k == 2 {
				return fail(h, "a block of more than 65536 bytes")
			}
		}
		if n == 0 {
			break
		}
		if n > 65536 {
			return fail(h, "a block of more than 65536 bytes")
		}
		pos := 0
		for pos < n {
			t := i
			tok, ok := get()
			if !ok {
				return short()
			}
			l := tok >> 4
			if l == 15 {
				if l, ok = length(15); !ok {
					return short()
				}
			}
			if l > n-pos {
				return fail(t, "a sequence runs past the end of its block")
			}
			if i+l > len(in) {
				return short()
			}
			out = append(out, in[i:i+l]...)
			i += l
			pos += l
			if pos == n {
				if tok&15 != 0 {
					return fail(t, "a match after the last literals of a block")
				}
				break
			}
			if i+2 > len(in) {
				return short()
			}
			off := int(in[i]) | int(in[i+1])<<8
			i += 2
			if off == 0 || off > len(out) {
				return fail(t, "a copy from before the start of the output")
			}
			m := tok & 15
			if m == 15 {
				if m, ok = length(15); !ok {
					return short()
				}
			}
			m += 4
			if m > n-pos {
				return fail(t, "a sequence runs past the end of its block")
			}
			for range m {
				out = append(out, out[len(out)-off])
			}
			pos += m
		}
	}
	if i < len(in) {
		return fail(i, "bytes after the end of the stream")
	}
	return out, ""
}
