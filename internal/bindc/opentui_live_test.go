package bindc

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha512"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// openTUIVersion is the version of the npm @opentui/core-<os>-<cpu>
// packages that TestLiveOpenTUIHello drives.
const openTUIVersion = "0.8.2"

// openTUIIntegrity are the npm registry's sha512 integrity of the four
// prebuilt packages of 0.8.2 (the first two were also measured on the
// downloaded files). Never commit a tarball or the library it holds: the
// package is "MIT AND LGPL-2.1-or-later".
var openTUIIntegrity = map[string]string{
	"linux-x64":    "sha512-R6SUsvcQdeVuVSdGIOYcAFrCdGGeAxzT2pJ7KT9LK6Tg0pELszpnC+FmRDiofMUz88VSjXP95NygBZx+SAvnuA==",
	"darwin-arm64": "sha512-/8pFK289hi+jtoUgJYZJEABTreLoQ3tNVQuMFLF0xDBkREJaR9xvh2RHKzhVLut9bhKf9oY2bgD37pMgYNqwqA==",
	"linux-arm64":  "sha512-Inkhg/P5UBNPySqeh0+4K8NbqUIolJ3WNgvX9I25x2nT/DFA8kUN1Y7fbQAaX2CrHa7rWu54xOmJYbYfOPsX8A==",
	"darwin-x64":   "sha512-AN3QB5yIGQyKs4XKgGuA1vSLA++jonTB6NR2fNLbw4+wizgETlQ77ur30175yEVysaN2tQNO2dJasvHMx05WQg==",
}

// openTUITarget is the npm name of the host target: <os>-<cpu>.
func openTUITarget(t *testing.T) string {
	t.Helper()
	cpu := map[string]string{"amd64": "x64", "arm64": "arm64"}[runtime.GOARCH]
	target := runtime.GOOS + "-" + cpu
	if _, ok := openTUIIntegrity[target]; !ok || cpu == "" {
		t.Skipf("no prebuilt OpenTUI %s package for %s/%s", openTUIVersion, runtime.GOOS, runtime.GOARCH)
	}
	return target
}

// checkIntegrity returns an error unless data has the npm integrity want
// ("sha512-<base64>").
func checkIntegrity(data []byte, want string) error {
	sum := sha512.Sum512(data)
	if got := "sha512-" + base64.StdEncoding.EncodeToString(sum[:]); got != want {
		return fmt.Errorf("integrity %s, want %s", got, want)
	}
	return nil
}

// openTUITarball reads core-<target>-0.8.2.tgz from $ERGO_BINDC_OPENTUI_DIR
// when it is set, or else downloads it from the npm registry.
func openTUITarball(t *testing.T, target string) []byte {
	t.Helper()
	name := fmt.Sprintf("core-%s-%s.tgz", target, openTUIVersion)
	if dir := os.Getenv("ERGO_BINDC_OPENTUI_DIR"); dir != "" {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("ERGO_BINDC_OPENTUI_DIR is set: %v", err)
		}
		return data
	}
	url := fmt.Sprintf("https://registry.npmjs.org/@opentui/core-%s/-/%s", target, name)
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("download %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("download %s: %s", url, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		t.Fatalf("download %s: %v", url, err)
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return data
}

// extractLibrary writes package/<lib> of the gzipped tarball data into dir
// and returns its path.
func extractLibrary(data []byte, lib, dir string) (string, error) {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return "", fmt.Errorf("the tarball has no package/%s", lib)
		}
		if err != nil {
			return "", err
		}
		if h.Name != "package/"+lib || h.Typeflag != tar.TypeReg {
			continue
		}
		path := filepath.Join(dir, lib)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return "", err
		}
		_, err = io.Copy(f, io.LimitReader(tr, 256<<20))
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		return path, err
	}
}

// screen applies terminal output to a grid of runes: CSI H moves the
// cursor (1-based row;col), other CSI sequences change no cell, and each
// other rune is written at the cursor, which moves one column right.
// OpenTUI paints the first frame whole and later frames as the changed
// cells only, so the text of a later frame is on the screen, not always in
// its bytes ("Ready" after "Hello" is "R", then "ady").
type screen struct {
	cells    [][]rune
	row, col int
}

func newScreen(width, height int) *screen {
	s := &screen{cells: make([][]rune, height)}
	for i := range s.cells {
		s.cells[i] = slices.Repeat([]rune{' '}, width)
	}
	return s
}

func (s *screen) apply(b []byte) error {
	for len(b) > 0 {
		if b[0] == 0x1b {
			if len(b) < 2 || b[1] != '[' {
				return fmt.Errorf("an escape that is not CSI: %q", b[:min(len(b), 8)])
			}
			end := 2
			for end < len(b) && (b[end] < 0x40 || b[end] > 0x7e) {
				end++
			}
			if end == len(b) {
				return fmt.Errorf("an unterminated CSI: %q", b)
			}
			if b[end] == 'H' {
				row, col := 1, 1
				if p := string(b[2:end]); p != "" {
					if _, err := fmt.Sscanf(p, "%d;%d", &row, &col); err != nil {
						return fmt.Errorf("CSI %sH: %v", p, err)
					}
				}
				s.row, s.col = row-1, col-1
			}
			b = b[end+1:]
			continue
		}
		r, n := utf8.DecodeRune(b)
		if r == utf8.RuneError {
			return fmt.Errorf("invalid UTF-8 at %q", b[:min(len(b), 8)])
		}
		if s.row < 0 || s.row >= len(s.cells) || s.col < 0 || s.col >= len(s.cells[s.row]) {
			return fmt.Errorf("%q written outside the screen at row %d, column %d", r, s.row+1, s.col+1)
		}
		s.cells[s.row][s.col] = r
		s.col++
		b = b[n:]
	}
	return nil
}

func (s *screen) String() string {
	var rows []string
	for _, r := range s.cells {
		rows = append(rows, string(r))
	}
	return strings.Join(rows, "\n")
}

// TestLiveOpenTUIHello (A8, ERGO_BINDC_LIVE=1) runs OpenTUI's hello example
// in Go (testdata/opentui/hello/main.go) on the published library: the npm
// @opentui/core-<os>-<cpu> 0.8.2 tarball, checked against its pinned sha512
// integrity (a changed byte fails the check), and its libopentui extracted
// in Go. Load passes the ABI check (2); the first frame's output holds
// "Hello" and box-drawing border characters, and the screen holds "Ready"
// after the second; a measure function on each of two leaves runs during a
// frame; a measure function that panics makes ScenePaint panic again with
// a *CallbackPanic of ot_scene_measure_callback after C returned, and the
// next frame paints, commits and drains ("Again" on the screen); after
// ContextDestroy, LiveCallbacks is 0; Owner.Close returns nil. The
// program's timings of a typed call and a writer callback go to
// hello-<goos>-<goarch>.txt in $ERGO_BINDC_RECEIPTS (for information).
func TestLiveOpenTUIHello(t *testing.T) {
	e := newLiveEnv(t)
	target := openTUITarget(t)
	data := openTUITarball(t, target)
	want := openTUIIntegrity[target]
	if err := checkIntegrity(data, want); err != nil {
		t.Fatalf("core-%s-%s.tgz: %v", target, openTUIVersion, err)
	}
	changed := slices.Clone(data)
	changed[len(changed)/2] ^= 1
	if checkIntegrity(changed, want) == nil {
		t.Fatal("the integrity check passed a tarball with a changed byte")
	}
	libName := "libopentui.so"
	if runtime.GOOS == "darwin" {
		libName = "libopentui.dylib"
	}
	lib, err := extractLibrary(data, libName, t.TempDir())
	if err != nil {
		t.Fatalf("core-%s-%s.tgz: %v", target, openTUIVersion, err)
	}

	for name, src := range goldenGoFiles(t, "testdata/opentui") {
		e.write(filepath.Join(e.mod, "opentui", name), src)
	}
	e.write(filepath.Join(e.mod, "hello", "main.go"), readInput(t, "testdata/opentui/hello/main.go"))
	bin := filepath.Join(e.root, "hello-bin")
	e.run(e.mod, "", nil, e.goTool, "build", "-o", bin, "./hello")
	frames := t.TempDir()
	out, err := e.try(e.root, "", nil, bin, lib, frames)
	checks := []string{
		"Load passed the ABI check (2)",
		"two frames drained through the Go writer",
		"a measure function runs with its leaf's key during a frame",
		"a panicking measure function makes ScenePaint panic with a *CallbackPanic after C returned",
		"the next frame paints, commits and drains (C state intact)",
		"session close begun and context destroyed",
		"LiveCallbacks is 0 after ContextDestroy",
		"Owner.Close returned nil",
	}
	if got := okLines(out); err != nil || !slices.Equal(got, checks) {
		t.Fatalf("the hello program: %v\n%s\nwant the checks:\n%s", err, out, strings.Join(checks, "\n"))
	}

	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join(frames, name))
		if err != nil {
			t.Fatal(err)
		}
		if len(b) == 0 {
			t.Fatalf("%s is empty: the frame drained no output", name)
		}
		return b
	}
	hello := read("hello.ansi")
	if !bytes.Contains(hello, []byte("Hello")) || !bytes.ContainsFunc(hello, func(r rune) bool { return r >= 0x2500 && r <= 0x257f }) {
		t.Fatalf("the first frame has no \"Hello\" or no box-drawing character: %q", hello)
	}
	sc := newScreen(12, 3)
	for _, f := range []struct{ file, text string }{{"hello.ansi", "Hello"}, {"ready.ansi", "Ready"}, {"again.ansi", "Again"}} {
		if err := sc.apply(read(f.file)); err != nil {
			t.Fatalf("%s: %v", f.file, err)
		}
		if !strings.Contains(sc.String(), f.text) || !strings.ContainsRune(sc.String(), '│') {
			t.Fatalf("the screen after %s does not hold %q in its box:\n%s", f.file, f.text, sc)
		}
	}
	t.Logf("the screen after the last frame:\n%s", sc)

	var costs []string
	for _, line := range strings.Split(out, "\n") {
		if c, ok := strings.CutPrefix(line, "cost: "); ok {
			costs = append(costs, c)
		}
	}
	if len(costs) != 2 {
		t.Fatalf("the hello program printed %d cost lines, want 2:\n%s", len(costs), out)
	}
	receipt := fmt.Sprintf("OpenTUI hello timings (information only; ns/op), %s/%s, %s, load %s\n%s\n",
		runtime.GOOS, runtime.GOARCH, runtime.Version(), loadAverage(), strings.Join(costs, "\n"))
	t.Log(receipt)
	writeReceipt(t, fmt.Sprintf("hello-%s-%s.txt", runtime.GOOS, runtime.GOARCH), receipt)
}

// loadAverage is the host's load average (1, 5 and 15 minutes), or "-".
func loadAverage() string {
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		if f := strings.Fields(string(b)); len(f) >= 3 {
			return strings.Join(f[:3], " ")
		}
	}
	if runtime.GOOS == "darwin" {
		if b, err := exec.Command("sysctl", "-n", "vm.loadavg").Output(); err == nil {
			return strings.Trim(strings.TrimSpace(string(b)), "{} ")
		}
	}
	return "-"
}

// writeReceipt writes name into $ERGO_BINDC_RECEIPTS when it is set.
func writeReceipt(t *testing.T, name, content string) {
	t.Helper()
	dir := os.Getenv("ERGO_BINDC_RECEIPTS")
	if dir == "" {
		t.Logf("ERGO_BINDC_RECEIPTS is not set: %s is not written", name)
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
