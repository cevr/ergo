package bindc

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the goldens under testdata")

// pinned are the verbatim inputs from anomalyco/opentui at e43fdc10. The
// tests refuse a changed input, so a golden never follows an edited header
// by accident.
var pinned = map[string]string{
	"testdata/opentui/opentui.h":               "477bd524f4c89f58393fda4d253eb6f9de748e548dfbec9a3926065bdf72a8ca",
	"testdata/opentui/native-abi.generated.ts": "105bf0fc6e8c2630ac667dcde47d3e397af250656ade45f24ecf57d744d442f3",
	"testdata/opentui/LICENSE":                 "d9c397a5dc5ac77a2a08fc967ea06a65317a4b5b8de48f6f732ffcb21737e3d2",
}

// headers are the golden-tested headers: testdata/<name>/<name>.h, except
// OpenTUI's, which keeps its upstream name.
var headers = []struct{ dir, header string }{
	{"testdata/opentui", "opentui.h"},
	{"testdata/fixture", "fixture.h"},
}

// readInput reads a test input and checks its pin, if it has one.
func readInput(t *testing.T, path string) []byte {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want, ok := pinned[path]; ok {
		sum := sha256.Sum256(src)
		if got := hex.EncodeToString(sum[:]); got != want {
			t.Fatalf("%s: sha256 %s, want %s: the input is pinned; restore it from anomalyco/opentui e43fdc10", path, got, want)
		}
	}
	return src
}

// parseHeader parses one golden-tested header.
func parseHeader(t *testing.T, dir, header string) *Model {
	t.Helper()
	path := filepath.Join(dir, header)
	m, err := Parse(path, readInput(t, path))
	if err != nil {
		t.Fatalf("Parse %s:\n%v", path, err)
	}
	return m
}

// checkGolden compares got with the golden file, or rewrites it with -update.
func checkGolden(t *testing.T, path string, got []byte) {
	t.Helper()
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/bindc -run TestGolden -update)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from the generator output (run go test ./internal/bindc -run TestGolden -update and review the diff):\n%s", path, firstDiff(want, got))
	}
}

// firstDiff names the first line where want and got differ.
func firstDiff(want, got []byte) string {
	w, g := bytes.Split(want, []byte("\n")), bytes.Split(got, []byte("\n"))
	for i := 0; i < len(w) || i < len(g); i++ {
		var a, b []byte
		if i < len(w) {
			a = w[i]
		}
		if i < len(g) {
			b = g[i]
		}
		if !bytes.Equal(a, b) {
			return fmt.Sprintf("line %d:\n  golden: %s\n  output: %s", i+1, a, b)
		}
	}
	return "(equal lines, different bytes)"
}

func TestPinnedInputs(t *testing.T) {
	for path := range pinned {
		readInput(t, path)
	}
}

func TestGolden(t *testing.T) {
	for _, h := range headers {
		t.Run(h.header, func(t *testing.T) {
			m := parseHeader(t, h.dir, h.header)
			checkGolden(t, filepath.Join(h.dir, "model.json"), JSON(m))
			b := bindHeader(t, h.dir, h.header, m)
			checkGolden(t, filepath.Join(h.dir, "report.txt"), Report(b))
		})
	}
}

// bindHeader binds a golden-tested header with its contract,
// testdata/<name>/<name>.bind.json.
func bindHeader(t *testing.T, dir, header string, m *Model) *Binding {
	t.Helper()
	contract := filepath.Join(dir, strings.TrimSuffix(filepath.Base(dir), ".h")+".bind.json")
	b, err := Bind(m, readInput(t, filepath.Join(dir, header)), readInput(t, contract))
	if err != nil {
		t.Fatalf("Bind %s:\n%v", contract, err)
	}
	return b
}
