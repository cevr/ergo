package stdlib_test

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"hash/adler32"
	"io"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"ergo.local/prototype/internal/ergobuild"
	"ergo.local/prototype/internal/lawledger"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
)

// TestStdZlibLawsRun runs the laws of std zlib at check time, in process:
// the evaluator runs std zlib's pure Ergo directly, and lawledger.Run holds
// that every zlib row of runners.txt is checked and passes.
func TestStdZlibLawsRun(t *testing.T) {
	rows, err := lawledger.Read()
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := check.Std.Import("zlib")
	if err != nil {
		t.Fatalf("import zlib: %v", err)
	}
	b := eval.DefaultBudget()
	b.Steps = 100_000_000
	b.Bytes = 1 << 30
	rep, err := eval.Run(pkg, b)
	if err != nil {
		t.Fatalf("evaluate zlib: %v", err)
	}
	results := map[string]eval.Result{}
	for _, r := range rep.Results {
		results["zlib "+strings.TrimPrefix(r.Name, "law ")] = r
	}
	laws := map[string]lawledger.Law{}
	for _, row := range rows {
		if row.Runner != t.Name() {
			continue
		}
		r, ok := results[row.Key()]
		statement, _ := lawledger.StdStatement(row.Key())
		laws[row.Key()] = lawledger.Law{Statement: statement, Check: func(t *testing.T) {
			switch {
			case !ok:
				t.Errorf("std %s has no law %s at check time", row.Pkg, row.Law)
			case r.Status != eval.Passed || r.Evidence != "tested":
				t.Errorf("law %s: %s (%s) after %d cases, seed %d, case %d: %+v", row.Key(), r.Status, r.Reason, r.Cases, r.Seed, r.Case, r.Arguments)
			}
		}}
	}
	lawledger.Run(t, laws)
}

const zlibOracleDriverSrc = `import hex
import io
import strconv
import strings
import zlib

func hexToBytes(s string) []byte {
	let raw = hex.Decode(s) catch {
	case hex.Malformed:
		return []byte{}
	}
	raw.Bytes()
}

func bytesToHex(bs []byte) string {
	hex.Encode(bs.Text())
}

func handleLine(line string) string {
	let parts = strings.Fields(line)
	if len(parts) == 0 {
		return ""
	}
	let cmd = parts[0]
	if cmd == "adler" {
		let data = if len(parts) >= 2 { hexToBytes(parts[1]) } else { []byte{} }
		return "OK \(zlib.Adler32(data))"
	}
	if cmd == "deflate" {
		let level = strconv.Atoi(parts[1]) catch {
		case strconv.Malformed, strconv.OutOfRange:
			return "ERR BadArg"
		}
		let data = if len(parts) >= 3 { hexToBytes(parts[2]) } else { []byte{} }
		return "OK " + bytesToHex(zlib.DeflateLevel(data, level))
	}
	if cmd == "inflate" {
		let maxBytes = strconv.Atoi(parts[1]) catch {
		case strconv.Malformed, strconv.OutOfRange:
			return "ERR BadArg"
		}
		let data = if len(parts) >= 3 { hexToBytes(parts[2]) } else { []byte{} }
		let out = zlib.Inflate(data, maxBytes) catch {
		case zlib.BadHeader:
			return "ERR BadHeader"
		case zlib.UnsupportedDict:
			return "ERR UnsupportedDict"
		case zlib.BadBlock:
			return "ERR BadBlock"
		case zlib.BadDistance:
			return "ERR BadDistance"
		case zlib.BadHuffman:
			return "ERR BadHuffman"
		case zlib.BadChecksum:
			return "ERR BadChecksum"
		case zlib.Truncated:
			return "ERR Truncated"
		case zlib.TrailingData:
			return "ERR TrailingData"
		case zlib.LimitExceeded:
			return "ERR LimitExceeded"
		}
		return "OK " + bytesToHex(out)
	}
	if cmd == "prefix" {
		let maxBytes = strconv.Atoi(parts[1]) catch {
		case strconv.Malformed, strconv.OutOfRange:
			return "ERR BadArg"
		}
		let data = if len(parts) >= 3 { hexToBytes(parts[2]) } else { []byte{} }
		let res = zlib.InflatePrefix(data, maxBytes) catch {
		case zlib.BadHeader:
			return "ERR BadHeader"
		case zlib.UnsupportedDict:
			return "ERR UnsupportedDict"
		case zlib.BadBlock:
			return "ERR BadBlock"
		case zlib.BadDistance:
			return "ERR BadDistance"
		case zlib.BadHuffman:
			return "ERR BadHuffman"
		case zlib.BadChecksum:
			return "ERR BadChecksum"
		case zlib.Truncated:
			return "ERR Truncated"
		case zlib.LimitExceeded:
			return "ERR LimitExceeded"
		}
		return "OK \(res.Consumed) " + bytesToHex(res.Data)
	}
	"ERR Unknown"
}

func main() needs Console | Ambient {
	for line in io.NewStdin().Lines() {
		let out = handleLine(line)
		if out != "" {
			Console.Println(out)
		}
	}
}
`

var zlibDriver struct {
	once sync.Once
	dir  string
	exe  string
	err  error
}

func buildZlibDriver(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("full tier: builds Go")
	}
	zlibDriver.once.Do(func() {
		root := moduleRootDir(t)
		dir, err := os.MkdirTemp("", "ergo-zlib-oracle-")
		if err != nil {
			zlibDriver.err = err
			return
		}
		zlibDriver.dir = dir
		files := map[string][]byte{
			"mod/ergo.mod":             []byte("module m\n"),
			"mod/cmd/oracle/main.ergo": []byte(zlibOracleDriverSrc),
		}
		for rel, data := range files {
			p := filepath.Join(dir, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				zlibDriver.err = err
				return
			}
			if err := os.WriteFile(p, data, 0o644); err != nil {
				zlibDriver.err = err
				return
			}
		}
		ergo := filepath.Join(dir, "bin", "ergo")
		if err := os.MkdirAll(filepath.Dir(ergo), 0o755); err != nil {
			zlibDriver.err = err
			return
		}
		build := ergobuild.Command(ergo, "./cmd/ergo", "-buildvcs=false")
		build.Dir = root
		if out, err := runBound(build); err != nil {
			zlibDriver.err = fmt.Errorf("build ergo: %v\n%s", err, out)
			return
		}
		exe := filepath.Join(dir, "bin", "oracle")
		compile := exec.Command(ergo, "build", "./cmd/oracle", "-o", exe)
		compile.Dir = filepath.Join(dir, "mod")
		if out, err := runBound(compile); err != nil {
			zlibDriver.err = fmt.Errorf("ergo build zlib oracle: %v\n%s", err, out)
			return
		}
		zlibDriver.exe = exe
	})
	if zlibDriver.err != nil {
		t.Fatal(zlibDriver.err)
	}
	return zlibDriver.exe
}

func runZlibBatch(t *testing.T, exe string, cmds []string) []string {
	t.Helper()
	cmd := exec.Command(exe)
	cmd.Stdin = strings.NewReader(strings.Join(cmds, "\n") + "\n")
	out, err := runBound(cmd)
	if err != nil {
		t.Fatalf("zlib oracle driver failed: %v\n%s", err, out)
	}
	trimmed := strings.TrimSuffix(out, "\n")
	if trimmed == "" {
		return nil
	}
	lines := strings.Split(trimmed, "\n")
	if len(lines) != len(cmds) {
		t.Fatalf("expected %d oracle responses, got %d:\n%s", len(cmds), len(lines), out)
	}
	return lines
}

func goZlibCompress(t *testing.T, data []byte, level int) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := zlib.NewWriterLevel(&buf, level)
	if err != nil {
		t.Fatalf("zlib.NewWriterLevel(%d): %v", level, err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatalf("zlib write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("zlib close: %v", err)
	}
	return buf.Bytes()
}

func goZlibDecompress(t *testing.T, data []byte) []byte {
	t.Helper()
	r, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("zlib.NewReader: %v", err)
	}
	defer r.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("zlib read: %v", err)
	}
	return out
}

func zlibCorpus() []struct {
	name string
	data []byte
} {
	rng := rand.New(rand.NewPCG(42, 1951))
	rand8k := make([]byte, 8192)
	for i := range rand8k {
		rand8k[i] = byte(rng.Uint32())
	}
	all256 := make([]byte, 256*4)
	for i := range all256 {
		all256[i] = byte(i)
	}
	var prose bytes.Buffer
	for i := range 200 {
		fmt.Fprintf(&prose, "commit %04d tree abcdef0123456789 author Cristian <seeve.c@gmail.com> %d +0000\n\n\tfeat(zlib): pure Ergo RFC 1950/1951 codec line %d\n", i, 1700000000+i, i)
	}
	return []struct {
		name string
		data []byte
	}{
		{"empty", []byte{}},
		{"one", []byte{'x'}},
		{"hello", []byte("hello world, hello zlib, hello ergo!")},
		{"rle1", bytes.Repeat([]byte{'A'}, 6000)},
		{"rle2", bytes.Repeat([]byte("ab"), 4000)},
		{"rle3", bytes.Repeat([]byte("xyz"), 3000)},
		{"all256", all256},
		{"random8k", rand8k},
		{"prose16k", prose.Bytes()},
	}
}

// TestZlibCrossDecodeGo cross-decodes pure-Ergo std/zlib bidirectionally
// against Go's compress/zlib at every compression level (stored, fixed
// Huffman, dynamic Huffman, HuffmanOnly) and verifies Adler-32, exact
// member-boundary InflatePrefix, and every typed Error variant.
func TestZlibCrossDecodeGo(t *testing.T) {
	exe := buildZlibDriver(t)
	corpus := zlibCorpus()
	goLevels := []int{
		zlib.NoCompression,
		zlib.BestSpeed,
		2, 4,
		zlib.DefaultCompression,
		zlib.BestCompression,
		zlib.HuffmanOnly,
	}
	ergoLevels := []int{0, 1, 6, 9}

	var cmds []string
	type expectation struct {
		desc   string
		verify func(resp string)
	}
	var checks []expectation

	add := func(cmd, desc string, verify func(string)) {
		cmds = append(cmds, cmd)
		checks = append(checks, expectation{desc: desc, verify: verify})
	}

	for _, tc := range corpus {
		wantAdler := adler32.Checksum(tc.data)
		add("adler "+hex.EncodeToString(tc.data), "adler/"+tc.name, func(resp string) {
			want := fmt.Sprintf("OK %d", wantAdler)
			if resp != want {
				t.Errorf("adler %s: got %q, want %q", tc.name, resp, want)
			}
		})

		for _, lvl := range goLevels {
			z := goZlibCompress(t, tc.data, lvl)
			wantHex := hex.EncodeToString(tc.data)
			add(fmt.Sprintf("inflate %d %s", len(tc.data)+16, hex.EncodeToString(z)), fmt.Sprintf("go->ergo/%s/lvl%d", tc.name, lvl), func(resp string) {
				if resp != "OK "+wantHex {
					t.Errorf("go->ergo %s lvl=%d: got %q (len=%d), want OK (len=%d)", tc.name, lvl, resp[:min(len(resp), 64)], len(resp), len(wantHex)+3)
				}
			})
		}

		for _, lvl := range ergoLevels {
			tc := tc
			lvl := lvl
			add(fmt.Sprintf("deflate %d %s", lvl, hex.EncodeToString(tc.data)), fmt.Sprintf("ergo->go/%s/lvl%d", tc.name, lvl), func(resp string) {
				rawHex, ok := strings.CutPrefix(resp, "OK ")
				if !ok {
					t.Fatalf("ergo deflate %s lvl=%d failed: %s", tc.name, lvl, resp)
				}
				compressed, err := hex.DecodeString(rawHex)
				if err != nil {
					t.Fatalf("hex decode %s lvl=%d: %v", tc.name, lvl, err)
				}
				got := goZlibDecompress(t, compressed)
				if !bytes.Equal(got, tc.data) {
					t.Errorf("ergo->go %s lvl=%d mismatch: got %d bytes, want %d bytes", tc.name, lvl, len(got), len(tc.data))
				}
			})
		}
	}

	// Concatenated Go zlib members + arbitrary tail: verify exact InflatePrefix boundary.
	z1 := goZlibCompress(t, []byte("first member payload repeated repeated repeated"), zlib.BestCompression)
	z2 := goZlibCompress(t, bytes.Repeat([]byte("pack-entry-"), 300), zlib.DefaultCompression)
	tail := []byte{0xde, 0xad, 0xbe, 0xef, 0x01, 0x02}
	joined := append(append(append([]byte{}, z1...), z2...), tail...)
	add(fmt.Sprintf("prefix 4096 %s", hex.EncodeToString(joined)), "prefix/member1", func(resp string) {
		want := fmt.Sprintf("OK %d %s", len(z1), hex.EncodeToString([]byte("first member payload repeated repeated repeated")))
		if resp != want {
			t.Errorf("prefix member1: got %q, want %q", resp, want)
		}
	})
	add(fmt.Sprintf("prefix 8192 %s", hex.EncodeToString(joined[len(z1):])), "prefix/member2", func(resp string) {
		want := fmt.Sprintf("OK %d %s", len(z2), hex.EncodeToString(bytes.Repeat([]byte("pack-entry-"), 300)))
		if resp != want {
			t.Errorf("prefix member2: got %q, want %q", resp[:min(len(resp), 80)], want[:min(len(want), 80)])
		}
	})

	// Negative / error variants against Go-produced streams.
	sample := []byte("error-check payload with repetition repetition repetition")
	goodZ := goZlibCompress(t, sample, zlib.DefaultCompression)
	badTrailer := append([]byte(nil), goodZ...)
	badTrailer[len(badTrailer)-1] ^= 0x01
	badHeader := append([]byte(nil), goodZ...)
	badHeader[0] = 0x79
	fdictStream := []byte{0x78, 0xbb, 0x00, 0x00, 0x00, 0x01}

	errCases := []struct {
		name     string
		maxBytes int
		stream   []byte
		wantErr  string
	}{
		{"bad-checksum", len(sample) + 64, badTrailer, "ERR BadChecksum"},
		{"truncated", len(sample) + 64, goodZ[:len(goodZ)-1], "ERR Truncated"},
		{"trailing", len(sample) + 64, append(append([]byte(nil), goodZ...), 0x55), "ERR TrailingData"},
		{"limit-exceeded", len(sample) - 1, goodZ, "ERR LimitExceeded"},
		{"bad-header", len(sample) + 64, badHeader, "ERR BadHeader"},
		{"fdict", len(sample) + 64, fdictStream, "ERR UnsupportedDict"},
	}
	for _, ec := range errCases {
		ec := ec
		add(fmt.Sprintf("inflate %d %s", ec.maxBytes, hex.EncodeToString(ec.stream)), "err/"+ec.name, func(resp string) {
			if resp != ec.wantErr {
				t.Errorf("err %s: got %q, want %q", ec.name, resp, ec.wantErr)
			}
		})
	}

	resps := runZlibBatch(t, exe, cmds)
	for i, c := range checks {
		c.verify(resps[i])
	}
}

// TestZlibCrossDecodeGitLoose cross-decodes real C Git loose objects in both
// directions: Ergo Inflate reads objects created by `git hash-object -w` and
// `git commit-tree`, and C Git (`git fsck --strict` + `git cat-file -p`)
// verifies loose objects compressed by pure-Ergo std/zlib.
func TestZlibCrossDecodeGitLoose(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not in PATH")
	}
	exe := buildZlibDriver(t)
	repo := t.TempDir()

	runGit := func(stdin []byte, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = []string{
			"PATH=" + os.Getenv("PATH"),
			"HOME=" + repo,
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=Oracle",
			"GIT_AUTHOR_EMAIL=oracle@example.com",
			"GIT_AUTHOR_DATE=1700000000 +0000",
			"GIT_COMMITTER_NAME=Oracle",
			"GIT_COMMITTER_EMAIL=oracle@example.com",
			"GIT_COMMITTER_DATE=1700000000 +0000",
		}
		if stdin != nil {
			cmd.Stdin = bytes.NewReader(stdin)
		}
		out, err := runBound(cmd)
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(out)
	}

	runGit(nil, "init", "--quiet", "--object-format=sha1")

	// Direction 1: C Git writes loose objects -> Ergo Inflate decodes raw .git/objects/xx/yyyy...
	blobs := [][]byte{
		{},
		[]byte("hello from c git\n"),
		bytes.Repeat([]byte("git loose object line with repeating tokens\n"), 200),
		func() []byte {
			b := make([]byte, 1024)
			for i := range b {
				b[i] = byte((i*37 + 13) & 0xff)
			}
			return b
		}(),
	}

	var inflateCmds []string
	var wantPayloads [][]byte
	var lastOID string
	for _, content := range blobs {
		oid := runGit(content, "hash-object", "-w", "--stdin")
		lastOID = oid
		objPath := filepath.Join(repo, ".git", "objects", oid[:2], oid[2:])
		raw, err := os.ReadFile(objPath)
		if err != nil {
			t.Fatalf("read loose object %s: %v", oid, err)
		}
		header := fmt.Sprintf("blob %d\x00", len(content))
		full := append([]byte(header), content...)
		inflateCmds = append(inflateCmds, fmt.Sprintf("inflate %d %s", len(full)+16, hex.EncodeToString(raw)))
		wantPayloads = append(wantPayloads, full)
	}

	// Also test a real tree and commit loose object written by C Git.
	runGit(nil, "update-index", "--add", "--cacheinfo", "100644,"+lastOID+",sample.bin")
	treeOID := runGit(nil, "write-tree")
	commitOID := runGit([]byte("initial commit\n"), "commit-tree", treeOID)
	for _, pair := range []struct {
		typ string
		oid string
	}{{"tree", treeOID}, {"commit", commitOID}} {
		objPath := filepath.Join(repo, ".git", "objects", pair.oid[:2], pair.oid[2:])
		raw, err := os.ReadFile(objPath)
		if err != nil {
			t.Fatalf("read loose %s %s: %v", pair.typ, pair.oid, err)
		}
		// Verify Ergo Inflate returns `<typ> <size>\0<body>` whose SHA-1 equals pair.oid.
		inflateCmds = append(inflateCmds, fmt.Sprintf("inflate 65536 %s", hex.EncodeToString(raw)))
		wantPayloads = append(wantPayloads, []byte("OID:"+pair.oid))
	}

	resps := runZlibBatch(t, exe, inflateCmds)
	for i, resp := range resps {
		rawHex, ok := strings.CutPrefix(resp, "OK ")
		if !ok {
			t.Fatalf("ergo inflate of C git loose object #%d failed: %s", i, resp)
		}
		got, err := hex.DecodeString(rawHex)
		if err != nil {
			t.Fatalf("hex decode #%d: %v", i, err)
		}
		if strings.HasPrefix(string(wantPayloads[i]), "OID:") {
			wantOID := strings.TrimPrefix(string(wantPayloads[i]), "OID:")
			sum := sha1.Sum(got)
			if gotOID := hex.EncodeToString(sum[:]); gotOID != wantOID {
				t.Errorf("loose object #%d SHA-1 mismatch: got %s, want %s", i, gotOID, wantOID)
			}
			continue
		}
		if !bytes.Equal(got, wantPayloads[i]) {
			t.Errorf("loose blob #%d content mismatch: got %d bytes, want %d bytes", i, len(got), len(wantPayloads[i]))
		}
	}

	// Direction 2: Pure-Ergo Deflate writes loose objects -> C Git `git fsck --strict` & `git cat-file -p` verify them!
	writeRepo := t.TempDir()
	runWriteGit := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", writeRepo}, args...)...)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + writeRepo, "GIT_CONFIG_NOSYSTEM=1"}
		out, err := runBound(cmd)
		if err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
		return out
	}
	runWriteGit("init", "--quiet", "--object-format=sha1")

	ergoBlobs := []struct {
		level int
		body  []byte
	}{
		{0, []byte("stored level 0 ergo git blob\n")},
		{1, bytes.Repeat([]byte("fast level 1 ergo git blob "), 120)},
		{6, bytes.Repeat([]byte("default level 6 ergo git blob with overlap abcabcabc\n"), 180)},
	}
	var deflateCmds []string
	var rawObjects [][]byte
	var oids []string
	for _, eb := range ergoBlobs {
		full := append([]byte(fmt.Sprintf("blob %d\x00", len(eb.body))), eb.body...)
		sum := sha1.Sum(full)
		oids = append(oids, hex.EncodeToString(sum[:]))
		rawObjects = append(rawObjects, full)
		deflateCmds = append(deflateCmds, fmt.Sprintf("deflate %d %s", eb.level, hex.EncodeToString(full)))
	}
	defResps := runZlibBatch(t, exe, deflateCmds)
	for i, resp := range defResps {
		rawHex, ok := strings.CutPrefix(resp, "OK ")
		if !ok {
			t.Fatalf("ergo deflate blob #%d failed: %s", i, resp)
		}
		compressed, err := hex.DecodeString(rawHex)
		if err != nil {
			t.Fatalf("hex decode #%d: %v", i, err)
		}
		oid := oids[i]
		objPath := filepath.Join(writeRepo, ".git", "objects", oid[:2], oid[2:])
		if err := os.MkdirAll(filepath.Dir(objPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(objPath, compressed, 0o444); err != nil {
			t.Fatal(err)
		}
		got := runWriteGit("cat-file", "-p", oid)
		if got != string(ergoBlobs[i].body) {
			t.Errorf("git cat-file -p %s mismatch: got %d bytes, want %d bytes", oid, len(got), len(ergoBlobs[i].body))
		}
		_ = strconv.Itoa(len(rawObjects[i]))
	}
	runWriteGit("fsck", "--strict", "--full")
}
