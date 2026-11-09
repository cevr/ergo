package lower

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// corpusRoot is the shared corpus of decided sources (D-S5-1): the
// translated fixtures and probes of the retired prototype, moved byte for
// byte.
const corpusRoot = "../testdata"

// corpusOracle is the output a corpus file must print, from its first line
// ("// out: a|b", "// out-quoted: "…"") or its sibling .out file. It
// reports false for a file with no oracle.
func corpusOracle(path string, src string) (string, bool, error) {
	head, _, _ := strings.Cut(src, "\n")
	switch {
	case strings.HasPrefix(head, "// out-quoted: "):
		// For output with bytes a header cannot spell, as a Go string literal.
		want, err := strconv.Unquote(strings.TrimPrefix(head, "// out-quoted: "))
		return want, true, err
	case head == "// out:" || strings.HasPrefix(head, "// out: "):
		// The formatter drops the trailing space of "// out: " (no output).
		want := strings.ReplaceAll(strings.TrimPrefix(strings.TrimPrefix(head, "// out:"), " "), "|", "\n")
		if want != "" {
			want += "\n"
		}
		return want, true, nil
	}
	data, err := os.ReadFile(strings.TrimSuffix(path, ".ergo") + ".out")
	if os.IsNotExist(err) {
		return "", false, nil
	}
	return string(data), err == nil, err
}

// parityOverrides reads the checker's parity.overrides: a corpus file whose
// verdict the designed rules changed, keyed by its path below the corpus.
func parityOverrides(t *testing.T) map[string]string {
	t.Helper()
	data, err := os.ReadFile("../check/testdata/parity.overrides")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		rel, code, _ := strings.Cut(line, " ")
		out[rel] = strings.TrimSpace(code)
	}
	return out
}

// TestCorpusProbesRun (stage 5, D10; it replaces the prototype's TestProbes
// and TestBuildAndRun): every corpus file with an output oracle runs on the
// one semantic path. The set is discovered: each file below the corpus with
// an out header or a sibling .out whose parity override is not a code (a
// file the designed rules turned into a diagnostic is TestParity's) is
// checked by the real checker, lowered, vetted, built and run, and its
// stdout must equal the oracle with exit 0. The corpus has 37 such files
// (the prototype's 36 and its ported TestBuildAndRun program): fewer means
// the corpus moved or lost a probe.
func TestCorpusProbesRun(t *testing.T) {
	needsBuild(t)
	overrides := parityOverrides(t)
	type probe struct{ rel, src, want string }
	var probes []probe
	err := filepath.WalkDir(corpusRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".ergo") {
			return err
		}
		rel, _ := filepath.Rel(corpusRoot, path)
		rel = filepath.ToSlash(rel)
		if code, ok := overrides[rel]; ok && code != "clean" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		want, ok, err := corpusOracle(path, string(data))
		if err != nil {
			t.Errorf("%s: oracle: %v", rel, err)
			return nil
		}
		if ok {
			probes = append(probes, probe{rel, string(data), want})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(probes, func(i, j int) bool { return probes[i].rel < probes[j].rel })
	if len(probes) < 37 {
		t.Fatalf("found %d output probes below %s, want at least 37", len(probes), corpusRoot)
	}
	// Each program is package p/NAME of one module: NAME is the path with
	// every byte outside [a-z0-9] written as _, so a failure names its file.
	var cases []behavior
	for _, p := range probes {
		name := strings.Map(func(r rune) rune {
			if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
				return r
			}
			return '_'
		}, strings.TrimSuffix(p.rel, ".ergo"))
		cases = append(cases, behavior{name: name, src: p.src, stdout: p.want})
	}
	t.Logf("%d output probes", len(probes))
	runBehaviors(t, cases)
}
