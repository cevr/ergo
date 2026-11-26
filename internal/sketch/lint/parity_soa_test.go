package lint_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lint"
	"ergo.local/prototype/internal/sketchfront"
	sdk "ergo.local/prototype/lint"
)

// TestLintCorpusParity is the agreement harness of a lint speed change: it
// lints every package under the folders ERGO_LINT_PARITY_DIRS names
// (colon-separated, relative to the repository root) with every built-in
// rule enabled, and writes one line per package (its folder, then the JSON
// of the run's Result and Facts) to ERGO_WRITE_LINT_PARITY, or compares
// those lines byte for byte with the file ERGO_LINT_PARITY_REF names. Run
// it at the base with the first and at the head with the second; without
// either it is skipped, since it reads the whole repository.
func TestLintCorpusParity(t *testing.T) {
	write, ref := os.Getenv("ERGO_WRITE_LINT_PARITY"), os.Getenv("ERGO_LINT_PARITY_REF")
	if write == "" && ref == "" {
		t.Skip("set ERGO_WRITE_LINT_PARITY or ERGO_LINT_PARITY_REF")
	}
	dirs := strings.Split(os.Getenv("ERGO_LINT_PARITY_DIRS"), ":")
	if dirs[0] == "" {
		dirs = []string{"compiler", "examples", "modules"}
	}
	conf := benchAllRulesConfig(t)
	families := sdk.Families()
	var got bytes.Buffer
	for _, dir := range dirs {
		targets, err := sketchfront.DiscoverPackages(filepath.Join("..", "..", "..", dir))
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		for _, target := range targets {
			a, err := sketchfront.CheckDir(target.Dir)
			if err != nil {
				t.Fatalf("%s: %v", target.Dir, err)
			}
			in := lint.InputOf(a)
			raw, err := json.Marshal(struct {
				Result lint.Result
				Facts  *sdk.Snapshot
			}{conf.Run(in), lint.Facts(in, families)})
			if err != nil {
				t.Fatal(err)
			}
			rel, _ := filepath.Rel(filepath.Join("..", "..", ".."), target.Dir)
			got.WriteString(filepath.ToSlash(rel))
			got.WriteByte('\t')
			got.Write(raw)
			got.WriteByte('\n')
		}
	}
	if write != "" {
		if err := os.WriteFile(write, got.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(ref)
	if err != nil {
		t.Fatal(err)
	}
	wantLines := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(want))
	sc.Buffer(nil, 1<<30)
	for sc.Scan() {
		name, body, _ := strings.Cut(sc.Text(), "\t")
		wantLines[name] = body
	}
	n := 0
	sc = bufio.NewScanner(bytes.NewReader(got.Bytes()))
	sc.Buffer(nil, 1<<30)
	for sc.Scan() {
		name, body, _ := strings.Cut(sc.Text(), "\t")
		n++
		w, ok := wantLines[name]
		switch {
		case !ok:
			t.Errorf("%s: not in the reference", name)
		case w != body:
			t.Errorf("%s differs:\ngot:  %s\nwant: %s", name, clip(body), clip(w))
		}
		delete(wantLines, name)
	}
	for name := range wantLines {
		t.Errorf("%s: in the reference only", name)
	}
	t.Logf("%d packages compared", n)
}

func clip(s string) string {
	if len(s) <= 600 {
		return s
	}
	return s[:600] + "..."
}
