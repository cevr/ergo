package check_test

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestV1Fixtures mirrors the check-v1 acceptance rules (one positive and
// one negative program per rule, copied into testdata/v1 as each stage
// lands) at the check.Check seam. A positive program checks with no
// diagnostic. A negative program gets exactly its `// want CODE` lines, one
// diagnostic per code on its line and no cascade. Every diagnostic carries
// a registered code and a span: a non-empty byte range inside the source
// that starts on the line and column it reports.
func TestV1Fixtures(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("testdata", "v1", "*.ergo"))
	if len(files) == 0 {
		t.Fatal("no fixtures in testdata/v1")
	}
	for _, file := range files {
		name := strings.TrimSuffix(filepath.Base(file), ".ergo")
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			src := string(data)
			_, diags := check.CheckSource(src)
			want, have := expected(src), got(diags)
			if strings.HasSuffix(name, ".pos") && len(want) > 0 {
				t.Fatalf("a positive fixture has want markers: %v", want)
			}
			if strings.HasSuffix(name, ".neg") && len(want) == 0 {
				t.Fatal("a negative fixture has no want marker")
			}
			if strings.Join(want, " ") != strings.Join(have, " ") {
				t.Errorf("want %v, got %v\n\t%s", want, have, describe(diags))
			}
			for _, d := range diags {
				if !check.Registered(d.Code) {
					t.Errorf("unregistered code %s", d.Code)
				}
				if d.Length <= 0 || d.Offset < 0 || d.Offset+d.Length > len(src) {
					t.Errorf("no span in the source: %s (offset %d, length %d)", d, d.Offset, d.Length)
					continue
				}
				line := strings.Count(src[:d.Offset], "\n") + 1
				col := d.Offset - (strings.LastIndex(src[:d.Offset], "\n") + 1) + 1
				if line != d.Line || col != d.Column {
					t.Errorf("span starts at %d:%d, the diagnostic reports %d:%d: %s", line, col, d.Line, d.Column, d)
				}
			}
		})
	}
}

// TestV1Manifest: each testdata/v1 fixture is listed in
// testdata/v1/accept-rules.sha256 with its content hash, and each listed
// fixture exists. The manifest names the acceptance tree the fixtures are
// copied from, which the repository cannot read: `sha256sum -c` run there
// detects a rule that changed after its copy (E2 round 2 m1).
func TestV1Manifest(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "v1", "accept-rules.sha256"))
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		hash, name, ok := strings.Cut(line, "  ")
		if !ok || len(hash) != 64 {
			t.Fatalf("a manifest line is not `sha256  name`: %q", line)
		}
		listed[name] = hash
	}
	files, _ := filepath.Glob(filepath.Join("testdata", "v1", "*.ergo"))
	for _, file := range files {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		name := filepath.Base(file)
		sum := fmt.Sprintf("%x", sha256.Sum256(src))
		switch hash, ok := listed[name]; {
		case !ok:
			t.Errorf("%s is not in the manifest: add its source and hash", name)
		case hash != sum:
			t.Errorf("%s differs from its manifest hash: the copy changed without its source (sha256 %s, manifest %s)", name, sum, hash)
		}
		delete(listed, name)
	}
	for name := range listed {
		t.Errorf("the manifest lists %s, which testdata/v1 lacks", name)
	}
}
