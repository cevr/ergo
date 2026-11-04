package check_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// wantMarker is `// want CODE`, or `// want CODE CODE` for two diagnostics on
// one line.
var wantMarker = regexp.MustCompile(`// want (S\d{3}(?: S\d{3})*)\s*$`)

// expected lists the `// want CODE` markers of a source as CODE@LINE, one per
// code.
func expected(src string) []string {
	var out []string
	for i, line := range strings.Split(src, "\n") {
		if m := wantMarker.FindStringSubmatch(line); m != nil {
			for _, code := range strings.Fields(m[1]) {
				out = append(out, fmt.Sprintf("%s@%d", code, i+1))
			}
		}
	}
	sort.Strings(out)
	return out
}

func got(diags []check.Diagnostic) []string {
	var out []string
	for _, d := range diags {
		out = append(out, fmt.Sprintf("%s@%d", d.Code, d.Line))
	}
	sort.Strings(out)
	return out
}

func describe(diags []check.Diagnostic) string {
	var parts []string
	for _, d := range diags {
		parts = append(parts, d.String())
	}
	return strings.Join(parts, "\n\t")
}

// TestFixtures checks the mirror of the acceptance fixtures (negative,
// positive and the spec-example programs): the diagnostics are exactly the
// `// want` markers, one per marked line, and no cascade (spec D3).
func TestFixtures(t *testing.T) {
	for _, dir := range []string{"negative", "positive", "spec"} {
		files, _ := filepath.Glob(filepath.Join("testdata", "fixtures", dir, "*.ergo"))
		if len(files) == 0 {
			t.Fatalf("no fixtures in %s", dir)
		}
		for _, file := range files {
			name := dir + "/" + strings.TrimSuffix(filepath.Base(file), ".ergo")
			t.Run(name, func(t *testing.T) {
				data, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				_, diags := check.CheckSource(string(data))
				want, have := expected(string(data)), got(diags)
				if strings.Join(want, " ") != strings.Join(have, " ") {
					t.Errorf("want %v, got %v\n\t%s", want, have, describe(diags))
				}
				for _, d := range diags {
					if !check.Registered(d.Code) {
						t.Errorf("unregistered code %s", d.Code)
					}
					if d.Line == 0 || d.Length < 0 {
						t.Errorf("unlocated diagnostic %s", d)
					}
				}
			})
		}
	}
}

// TestParity checks the prototype's fixtures: each keeps its `// want` code
// (or stays clean), except the overrides the designed rules change (spec
// D11, mirrored in testdata/parity.overrides). It reads their decided
// translations in the shared corpus ../testdata (#196; the frozen
// prototypes/sketch9 copies no longer parse).
func TestParity(t *testing.T) {
	base := filepath.Join("..", "testdata")
	overrides := map[string]string{}
	data, err := os.ReadFile(filepath.Join("testdata", "parity.overrides"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		rel, code, _ := strings.Cut(line, " ")
		overrides[rel] = code
	}
	var files []string
	for _, pattern := range []string{"*.ergo", "probes/*.ergo", "reject/*.ergo"} {
		m, _ := filepath.Glob(filepath.Join(base, pattern))
		files = append(files, m...)
	}
	if len(files) < 100 {
		t.Fatalf("found %d prototype fixtures", len(files))
	}
	want := regexp.MustCompile(`^// want (S\d{3})`)
	for _, file := range files {
		rel, _ := filepath.Rel(base, file)
		rel = filepath.ToSlash(rel)
		t.Run(rel, func(t *testing.T) {
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			expect := "clean"
			if m := want.FindStringSubmatch(strings.SplitN(string(data), "\n", 2)[0]); m != nil {
				expect = m[1]
			}
			if o, ok := overrides[rel]; ok {
				expect = o
			}
			if expect == "skip" {
				t.Skip("restated as a fixture")
			}
			_, diags := check.CheckSource(string(data))
			if expect == "clean" {
				if len(diags) > 0 {
					t.Errorf("want no diagnostics, got\n\t%s", describe(diags))
				}
				return
			}
			for _, d := range diags {
				if d.Code == expect {
					return
				}
			}
			t.Errorf("want %s, got\n\t%s", expect, describe(diags))
		})
	}
}
