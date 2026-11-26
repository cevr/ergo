package stdlib_test

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ergo.local/prototype/internal/ergobuild"
	"ergo.local/prototype/internal/lawledger"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
)

// TestStdArchiveLawsRun runs the laws of std archive at check time, in
// process, and holds every archive row of runners.txt to a pass.
func TestStdArchiveLawsRun(t *testing.T) {
	rows, err := lawledger.Read()
	if err != nil {
		t.Fatal(err)
	}
	pkg, err := check.Std.Import("archive")
	if err != nil {
		t.Fatalf("import archive: %v", err)
	}
	b := eval.DefaultBudget()
	b.Steps = 200_000_000
	b.Bytes = 1 << 30
	rep, err := eval.Run(pkg, b)
	if err != nil {
		t.Fatalf("evaluate archive: %v", err)
	}
	results := map[string]eval.Result{}
	for _, r := range rep.Results {
		results["archive "+strings.TrimPrefix(r.Name, "law ")] = r
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

const archiveOracleSrc = `import archive
import fs
import hex
import io
import os
import strconv
import strings

func unhex(s string) []byte {
	let raw = hex.Decode(s) catch {
	case hex.Malformed:
		return []byte{}
	}
	raw.Bytes()
}

func num(s string) int {
	strconv.Atoi(s) catch {
	case strconv.Malformed, strconv.OutOfRange:
		return 0
	}
}

func kindOf(c string) archive.Kind {
	if c == "d" {
		archive.Dir
	} else if c == "s" {
		archive.Symlink
	} else if c == "h" {
		archive.HardLink
	} else {
		archive.File
	}
}

func kindText(k archive.Kind) string {
	match k {
	case archive.File:
		"f"
	case archive.Dir:
		"d"
	case archive.Symlink:
		"s"
	case archive.HardLink:
		"h"
	}
}

func formatOf(name string, level int) archive.Format {
	if name == "tar" {
		archive.Tar
	} else if name == "tgz" {
		archive.TarGzip{Level: level}
	} else {
		archive.Zip{Level: level}
	}
}

func build(specs []string) archive.Archive fails archive.Unsafe | archive.UnsafeLink | archive.ThroughLink {
	let mut a = archive.New()
	for s in specs {
		let f = strings.Split(s, ",")
		try archive.Add(&a, archive.Header{Path: unhex(f[1]).Text(), Kind: kindOf(f[0]), Mode: num(f[2]), ModTime: num(f[3]), Link: unhex(f[4]).Text()}, unhex(f[5]))
	}
	a
}

func listing(a archive.Archive) string {
	let mut parts = []string{"OK"}
	for i, e in archive.Entries(a) {
		parts = parts.Append(kindText(e.Kind) + "," + hex.Encode(e.Path) + ",\(e.Mode),\(e.ModTime)," + hex.Encode(e.Link) + "," + hex.Encode(archive.Content(a, i).Text()))
	}
	strings.Join(parts, " ")
}

func read(data []byte) archive.Archive fails archive.Unsafe | archive.UnsafeLink | archive.ThroughLink | archive.Malformed | archive.Truncated | archive.Unsupported | archive.BadChecksum | archive.LimitExceeded {
	try archive.Read(data, 67108864)
}

func handle(line string) string needs fs.Files | fs.Writes {
	let parts = strings.Fields(line)
	if parts.Len() == 0 {
		return ""
	}
	let cmd = parts[0]
	if cmd == "write" {
		let a = build(parts[3:]) catch {
		case archive.Unsafe:
			return "ERR Unsafe"
		case archive.UnsafeLink:
			return "ERR UnsafeLink"
		case archive.ThroughLink:
			return "ERR ThroughLink"
		}
		let out = archive.Write(a, formatOf(parts[1], num(parts[2]))) catch {
		case archive.TooLarge:
			return "ERR TooLarge"
		case archive.Unsupported:
			return "ERR Unsupported"
		}
		return "OK " + hex.Encode(out.Text())
	}
	let data = if parts.Len() >= 2 { unhex(parts[parts.Len()-1]) } else { []byte{} }
	let a = read(data) catch {
	case archive.Unsafe:
		return "ERR Unsafe"
	case archive.UnsafeLink:
		return "ERR UnsafeLink"
	case archive.ThroughLink:
		return "ERR ThroughLink"
	case archive.Malformed:
		return "ERR Malformed"
	case archive.Truncated:
		return "ERR Truncated"
	case archive.Unsupported:
		return "ERR Unsupported"
	case archive.BadChecksum:
		return "ERR BadChecksum"
	case archive.LimitExceeded:
		return "ERR LimitExceeded"
	}
	if cmd == "read" {
		return listing(a)
	}
	if cmd == "extract" {
		let done = archive.Extract(a, unhex(parts[1]).Text()) catch {
		case archive.LinkOnDisk as l:
			return "ERR LinkOnDisk " + l.Path
		case fs.NotFound, fs.Denied, fs.Failed:
			return "ERR fs"
		}
		return "OK \(done.Dirs) \(done.Files) \(done.Links.Len())"
	}
	"ERR Unknown"
}

func main() needs os.Ambient {
	with context { fs.Files: fs.NewHost(), fs.Writes: fs.NewHost(), io.Stdout: io.NewHost() } {
		for line in io.NewStdin().Lines() {
			let out = handle(line)
			if out != "" {
				io.Stdout.Println(out)
			}
		}
	}
}
`

var archiveOracle struct {
	once sync.Once
	exe  string
	err  error
}

// buildArchiveOracle builds the oracle program with the ergo CLI under
// ERGO_FALLBACK=off.
//
// It builds Go because the agreement is with Go's archive/tar, archive/zip
// and compress/gzip on the bytes the lowered program reads and writes, and
// because every body of std archive must lower through the IR: the
// check-time evaluator shows neither.
func buildArchiveOracle(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("full tier: builds Go")
	}
	archiveOracle.once.Do(func() {
		root := moduleRootDir(t)
		dir, err := os.MkdirTemp("", "ergo-archive-oracle-")
		if err != nil {
			archiveOracle.err = err
			return
		}
		files := map[string]string{
			"mod/ergo.mod":             "module m\n",
			"mod/cmd/oracle/main.ergo": archiveOracleSrc,
		}
		for rel, data := range files {
			p := filepath.Join(dir, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				archiveOracle.err = err
				return
			}
			if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
				archiveOracle.err = err
				return
			}
		}
		ergo := filepath.Join(dir, "bin", "ergo")
		build := ergobuild.Command(ergo, "./cmd/ergo", "-buildvcs=false")
		build.Dir = root
		if out, err := runBound(build); err != nil {
			archiveOracle.err = fmt.Errorf("build ergo: %v\n%s", err, out)
			return
		}
		exe := filepath.Join(dir, "bin", "oracle")
		compile := exec.Command(ergo, "build", "./cmd/oracle", "-o", exe)
		compile.Dir = filepath.Join(dir, "mod")
		compile.Env = append(os.Environ(), "ERGO_FALLBACK=off")
		if out, err := runBound(compile); err != nil {
			archiveOracle.err = fmt.Errorf("ergo build archive oracle: %v\n%s", err, out)
			return
		}
		archiveOracle.exe = exe
	})
	if archiveOracle.err != nil {
		t.Fatal(archiveOracle.err)
	}
	return archiveOracle.exe
}

func askArchiveOracle(t *testing.T, exe string, cmds []string) []string {
	t.Helper()
	cmd := exec.Command(exe)
	cmd.Stdin = strings.NewReader(strings.Join(cmds, "\n") + "\n")
	out, err := runBound(cmd)
	if err != nil {
		t.Fatalf("archive oracle failed: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != len(cmds) {
		t.Fatalf("archive oracle: %d answers to %d commands:\n%s", len(lines), len(cmds), out)
	}
	return lines
}

type archEntry struct {
	kind    byte
	path    string
	mode    int64
	mtime   int64
	link    string
	content []byte
}

func (e archEntry) spec() string {
	return fmt.Sprintf("%c,%s,%d,%d,%s,%s", e.kind, hex.EncodeToString([]byte(e.path)), e.mode, e.mtime, hex.EncodeToString([]byte(e.link)), hex.EncodeToString(e.content))
}

func parseListing(t *testing.T, resp string) []archEntry {
	t.Helper()
	fields := strings.Fields(resp)
	if len(fields) == 0 || fields[0] != "OK" {
		t.Fatalf("archive oracle: %s", resp[:min(len(resp), 200)])
	}
	var out []archEntry
	for _, f := range fields[1:] {
		p := strings.Split(f, ",")
		path, _ := hex.DecodeString(p[1])
		link, _ := hex.DecodeString(p[4])
		content, _ := hex.DecodeString(p[5])
		mode, _ := strconv.ParseInt(p[2], 10, 64)
		mtime, _ := strconv.ParseInt(p[3], 10, 64)
		out = append(out, archEntry{kind: p[0][0], path: string(path), mode: mode, mtime: mtime, link: string(link), content: content})
	}
	return out
}

func sameEntries(t *testing.T, what string, got, want []archEntry) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: %d entries, want %d", what, len(got), len(want))
		return
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.kind != w.kind || g.path != w.path || g.mode != w.mode || g.mtime != w.mtime || g.link != w.link || !bytes.Equal(g.content, w.content) {
			t.Errorf("%s: entry %d is {%c %q %o %d %q %d bytes}, want {%c %q %o %d %q %d bytes}", what, i, g.kind, g.path, g.mode, g.mtime, g.link, len(g.content), w.kind, w.path, w.mode, w.mtime, w.link, len(w.content))
		}
	}
}

// archiveCorpus is a fixed archive with every kind and every path length
// class (ustar name, ustar prefix, pax), and n seeded random ones.
func archiveCorpus(seed uint64, n int) [][]archEntry {
	rng := rand.New(rand.NewPCG(seed, 2026))
	random := make([]byte, 5000)
	for i := range random {
		random[i] = byte(rng.Uint32())
	}
	fixed := []archEntry{
		{'f', "a.txt", 0o644, 1700000000, "", []byte("hello archive\n")},
		{'d', "dir", 0o755, 1700000001, "", nil},
		{'f', "dir/random.bin", 0o600, 1700000002, "", random},
		{'f', "dir/repeat.txt", 0o640, 1700000003, "", bytes.Repeat([]byte("ergo archive "), 400)},
		{'s', "dir/link", 0o777, 1700000004, "../a.txt", nil},
		{'f', "p/" + strings.Repeat("q", 140) + "/split.txt", 0o644, 0, "", []byte("split")},
		{'f', "p/" + strings.Repeat("r", 300) + "/pax.txt", 0o644, 1, "", []byte("pax")},
		{'f', "ünï/çödé.txt", 0o644, 1600000000, "", []byte("utf-8 name")},
		{'f', "empty", 0o644, 1700000005, "", []byte{}},
	}
	out := [][]archEntry{fixed}
	for k := 0; k < n; k++ {
		var es []archEntry
		count := 1 + rng.IntN(6)
		for i := 0; i < count; i++ {
			body := make([]byte, rng.IntN(300))
			for j := range body {
				body[j] = byte('a' + rng.IntN(4))
			}
			name := fmt.Sprintf("r%d/%s%d", rng.IntN(3), strings.Repeat("n", 1+rng.IntN(120)), i)
			mode := int64(0o400 | rng.IntN(0o400))
			mtime := int64(rng.IntN(2_000_000_000))
			switch rng.IntN(4) {
			case 0:
				es = append(es, archEntry{'d', name, mode | 0o700, mtime, "", nil})
			case 1:
				es = append(es, archEntry{'s', name, 0o777, mtime, "t" + strconv.Itoa(i), nil})
			default:
				es = append(es, archEntry{'f', name, mode, mtime, "", body})
			}
		}
		out = append(out, es)
	}
	return out
}

func goReadTar(t *testing.T, data []byte, gz bool) []archEntry {
	t.Helper()
	var r io.Reader = bytes.NewReader(data)
	if gz {
		zr, err := gzip.NewReader(r)
		if err != nil {
			t.Fatalf("gzip.NewReader: %v", err)
		}
		r = zr
	}
	tr := tar.NewReader(r)
	var out []archEntry
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatalf("tar.Next: %v", err)
		}
		content, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("tar read: %v", err)
		}
		kind := map[byte]byte{tar.TypeReg: 'f', tar.TypeDir: 'd', tar.TypeSymlink: 's', tar.TypeLink: 'h'}[h.Typeflag]
		if kind != 'f' {
			content = nil
		}
		out = append(out, archEntry{kind, strings.TrimSuffix(h.Name, "/"), h.Mode & 0o777, h.ModTime.Unix(), h.Linkname, content})
	}
}

func goReadZip(t *testing.T, data []byte) []archEntry {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	var out []archEntry
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("zip open %s: %v", f.Name, err)
		}
		content, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("zip read %s: %v", f.Name, err)
		}
		e := archEntry{'f', strings.TrimSuffix(f.Name, "/"), int64(f.Mode().Perm()), f.Modified.Unix(), "", content}
		switch {
		case f.Mode()&fs.ModeSymlink != 0:
			e.kind, e.link, e.content = 's', string(content), nil
		case f.Mode().IsDir():
			e.kind, e.content = 'd', nil
		}
		out = append(out, e)
	}
	return out
}

func goWriteTar(t *testing.T, es []archEntry, format tar.Format, gz bool) []byte {
	t.Helper()
	var buf bytes.Buffer
	var w io.Writer = &buf
	var zw *gzip.Writer
	if gz {
		zw = gzip.NewWriter(&buf)
		w = zw
	}
	tw := tar.NewWriter(w)
	for _, e := range es {
		h := &tar.Header{Name: e.path, Mode: e.mode, ModTime: time.Unix(e.mtime, 0), Linkname: e.link, Format: format}
		switch e.kind {
		case 'f':
			h.Typeflag, h.Size = tar.TypeReg, int64(len(e.content))
		case 'd':
			h.Typeflag, h.Name = tar.TypeDir, e.path+"/"
		case 's':
			h.Typeflag = tar.TypeSymlink
		case 'h':
			h.Typeflag = tar.TypeLink
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatalf("tar header %q: %v", e.path, err)
		}
		if _, err := tw.Write(e.content); err != nil {
			t.Fatalf("tar write: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if zw != nil {
		if err := zw.Close(); err != nil {
			t.Fatalf("gzip close: %v", err)
		}
	}
	return buf.Bytes()
}

func goWriteZip(t *testing.T, es []archEntry, method uint16) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range es {
		h := &zip.FileHeader{Name: e.path, Method: method, Modified: time.Unix(e.mtime, 0).UTC()}
		body := e.content
		switch e.kind {
		case 'f':
			h.SetMode(fs.FileMode(e.mode))
		case 'd':
			h.Name, h.Method = e.path+"/", zip.Store
			h.SetMode(fs.ModeDir | fs.FileMode(e.mode))
		case 's':
			h.SetMode(fs.ModeSymlink | fs.FileMode(e.mode))
			body = []byte(e.link)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatalf("zip header %q: %v", e.path, err)
		}
		if _, err := w.Write(body); err != nil {
			t.Fatalf("zip write: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

func withoutHardLinks(es []archEntry) []archEntry {
	var out []archEntry
	for _, e := range es {
		if e.kind != 'h' {
			out = append(out, e)
		}
	}
	return out
}

// TestStdArchiveAgreesWithGo checks std archive against Go's archive/tar,
// archive/zip and compress/gzip in both directions on a fixed corpus and
// seeded random archives (seed printed on a failure): what the Ergo program
// writes, Go reads back entry for entry; what Go writes (ustar, pax, GNU,
// gzip, zip stored and deflated with data descriptors), the Ergo program
// reads entry for entry. Hostile archives Go writes are refused with their
// typed failure, and Extract on the disk does not write through a link that
// is already there.
func TestStdArchiveAgreesWithGo(t *testing.T) {
	exe := buildArchiveOracle(t)
	const seed = 7
	corpus := archiveCorpus(seed, 20)
	corpus[0] = append(corpus[0], archEntry{'h', "hard", 0o644, 1700000006, "a.txt", nil})

	type ask struct {
		cmd    string
		answer func(resp string)
	}
	var asks []ask
	for ci, es := range corpus {
		specs := make([]string, 0, len(es))
		zipSpecs := make([]string, 0, len(es))
		for _, e := range es {
			specs = append(specs, e.spec())
			if e.kind != 'h' {
				zipSpecs = append(zipSpecs, e.spec())
			}
		}
		for _, f := range []struct {
			name  string
			level int
		}{{"tar", 0}, {"tgz", 6}, {"zip", 0}, {"zip", 6}} {
			want, list := es, specs
			if f.name == "zip" {
				want, list = withoutHardLinks(es), zipSpecs
			}
			what := fmt.Sprintf("seed %d archive %d: Ergo %s level %d, read by Go", seed, ci, f.name, f.level)
			asks = append(asks, ask{fmt.Sprintf("write %s %d %s", f.name, f.level, strings.Join(list, " ")), func(resp string) {
				raw, ok := strings.CutPrefix(resp, "OK ")
				if !ok {
					t.Errorf("%s: %s", what, resp)
					return
				}
				data, _ := hex.DecodeString(raw)
				var got []archEntry
				if f.name == "zip" {
					got = goReadZip(t, data)
				} else {
					got = goReadTar(t, data, f.name == "tgz")
				}
				sameEntries(t, what, got, want)
			}})
		}
		for _, g := range []struct {
			name string
			data []byte
			want []archEntry
		}{
			{"tar pax", goWriteTar(t, es, tar.FormatPAX, false), es},
			{"tar gnu", goWriteTar(t, es, tar.FormatGNU, false), es},
			{"tar any, gzip", goWriteTar(t, es, tar.FormatUnknown, true), es},
			{"zip deflate", goWriteZip(t, withoutHardLinks(es), zip.Deflate), withoutHardLinks(es)},
			{"zip store", goWriteZip(t, withoutHardLinks(es), zip.Store), withoutHardLinks(es)},
		} {
			what := fmt.Sprintf("seed %d archive %d: Go %s, read by Ergo", seed, ci, g.name)
			want := g.want
			asks = append(asks, ask{"read " + hex.EncodeToString(g.data), func(resp string) {
				sameEntries(t, what, parseListing(t, resp), want)
			}})
		}
	}

	type refusal struct {
		name string
		data []byte
		want string
	}
	var device bytes.Buffer
	dw := tar.NewWriter(&device)
	if err := dw.WriteHeader(&tar.Header{Name: "dev", Typeflag: tar.TypeChar, Mode: 0o644, Format: tar.FormatPAX}); err != nil {
		t.Fatal(err)
	}
	if err := dw.Close(); err != nil {
		t.Fatal(err)
	}
	good := goWriteZip(t, []archEntry{{'f', "f", 0o644, 0, "", []byte("checksummed content")}}, zip.Store)
	flipped := append([]byte(nil), good...)
	flipped[bytes.Index(flipped, []byte("checksummed"))] ^= 1
	gz := goWriteTar(t, corpus[0], tar.FormatPAX, true)
	hostile := []refusal{
		{"tar device", device.Bytes(), "ERR Unsupported"},
		{"zip crc", flipped, "ERR BadChecksum"},
		{"tgz cut", gz[:len(gz)-3], "ERR Truncated"},
		{"tar ../", goWriteTar(t, []archEntry{{'f', "../evil", 0o644, 0, "", []byte("x")}}, tar.FormatPAX, false), "ERR Unsafe"},
		{"tar absolute", goWriteTar(t, []archEntry{{'f', "/etc/evil", 0o644, 0, "", []byte("x")}}, tar.FormatPAX, false), "ERR Unsafe"},
		{"tar link out", goWriteTar(t, []archEntry{{'s', "l", 0o777, 0, "../../x", nil}}, tar.FormatPAX, false), "ERR UnsafeLink"},
		{"tar through link", goWriteTar(t, []archEntry{{'s', "l", 0o777, 0, "sub", nil}, {'f', "l/f", 0o644, 0, "", []byte("x")}}, tar.FormatPAX, false), "ERR ThroughLink"},
		{"zip ../", goWriteZip(t, []archEntry{{'f', "a/../../evil", 0o644, 0, "", []byte("x")}}, zip.Store), "ERR Unsafe"},
		{"zip backslash", goWriteZip(t, []archEntry{{'f', "a\\evil", 0o644, 0, "", []byte("x")}}, zip.Store), "ERR Unsafe"},
	}
	for _, h := range hostile {
		h := h
		asks = append(asks, ask{"read " + hex.EncodeToString(h.data), func(resp string) {
			if resp != h.want {
				t.Errorf("hostile %s: %q, want %q", h.name, resp[:min(len(resp), 80)], h.want)
			}
		}})
	}

	out := filepath.Join(t.TempDir(), "out")
	outside := t.TempDir()
	victim := filepath.Join(outside, "victim")
	for _, err := range []error{
		os.Mkdir(out, 0o755),
		os.WriteFile(victim, []byte("kept"), 0o644),
		os.Symlink(outside, filepath.Join(out, "evil")),
		os.Symlink(victim, filepath.Join(out, "g")),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	through := goWriteTar(t, []archEntry{{'f', "evil/f", 0o644, 0, "", []byte("x")}}, tar.FormatPAX, false)
	replace := goWriteTar(t, []archEntry{{'d', "d", 0o755, 0, "", nil}, {'f', "d/f", 0o640, 0, "", []byte("in d")}, {'f', "g", 0o644, 0, "", []byte("new")}, {'s', "s", 0o777, 0, "d/f", nil}}, tar.FormatPAX, false)
	outHex := hex.EncodeToString([]byte(out))
	asks = append(asks,
		ask{"extract " + outHex + " " + hex.EncodeToString(through), func(resp string) {
			if resp != "ERR LinkOnDisk evil" {
				t.Errorf("extract through a link on disk: %q", resp)
			}
		}},
		ask{"extract " + outHex + " " + hex.EncodeToString(replace), func(resp string) {
			if resp != "OK 1 2 1" {
				t.Errorf("extract: %q, want OK 1 2 1", resp)
			}
		}},
	)

	resps := askArchiveOracle(t, exe, func() []string {
		cmds := make([]string, len(asks))
		for i, a := range asks {
			cmds[i] = a.cmd
		}
		return cmds
	}())
	for i, a := range asks {
		a.answer(resps[i])
	}

	if b, err := os.ReadFile(victim); err != nil || string(b) != "kept" {
		t.Errorf("file outside the target: %q, %v; want it kept", b, err)
	}
	if _, err := os.Stat(filepath.Join(outside, "f")); err == nil {
		t.Errorf("Extract wrote through the link evil")
	}
	if fi, err := os.Lstat(filepath.Join(out, "g")); err != nil || !fi.Mode().IsRegular() {
		t.Errorf("g after Extract: %v, %v; want a regular file", fi, err)
	}
	got := map[string]string{}
	for _, p := range []string{"d/f", "g"} {
		b, err := os.ReadFile(filepath.Join(out, p))
		if err != nil {
			t.Errorf("read %s: %v", p, err)
		}
		got[p] = string(b)
	}
	if got["d/f"] != "in d" || got["g"] != "new" {
		t.Errorf("extracted contents %v", got)
	}
	keys := make([]string, 0)
	entries, _ := os.ReadDir(out)
	for _, e := range entries {
		keys = append(keys, e.Name())
	}
	sort.Strings(keys)
	if strings.Join(keys, ",") != "d,evil,g" {
		t.Errorf("target holds %v, want d, evil, g", keys)
	}
}
