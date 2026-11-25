package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStopFingerprintingAndCounting(t *testing.T) {
	raw := strings.Join([]string{
		"S /workspaces/.work/ergo/branch-a/internal/sketch/std/a.ergo 100 signature: fails row / frame: SHAPES: unspelled success result",
		"S /workspaces/.work/ergo/branch-a/internal/sketch/std/a.ergo 100 signature: fails row / frame: SHAPES: unspelled success result",
		"S /workspaces/.work/ergo/branch-a/internal/sketch/std/b.ergo 200 signature: fails row / frame: SHAPES: unspelled success result",
		"S /workspaces/.work/ergo/branch-a/examples/foo/x.ergo 50 result: instance Pair / body: literal of no plain record",
		"S /workspaces/.work/ergo/branch-a/examples/bar/y.ergo 60 result: instance -.Decision / body: literal of no plain record",
		"S /workspaces/.work/ergo/branch-a/examples/bar/y.ergo 90 result: UNION: unproved imported generic layout for -.Option / body: name of foreign -.None",
		"S /workspaces/.work/ergo/branch-a/examples/bar/y.ergo 120 let of slice",
	}, "\n") + "\n"

	log, err := ParseStopLog(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	// 7 total events, 6 distinct functions (a.ergo:100 repeated twice), 4 distinct files.
	if log.TotalEvents != 7 || log.DistinctFunctions != 6 || log.DistinctFiles != 4 {
		t.Fatalf("got events=%d funcs=%d files=%d, want 7/6/4", log.TotalEvents, log.DistinctFunctions, log.DistinctFiles)
	}
	if len(log.Groups) != 4 {
		t.Fatalf("expected 4 normalized fingerprints (Pair and -.Decision collapse), got %d: %+v", len(log.Groups), log.Groups)
	}

	ranked := log.RankedGroups(0)
	// Top group: SHAPES (2 distinct functions, 3 events, 2 files) vs instance <T> (2 funcs, 2 events, 2 files).
	if ranked[0].Fingerprint.Subsystem != "shapes" || ranked[0].Functions != 2 || ranked[0].Events != 3 || ranked[0].Files != 2 {
		t.Errorf("rank 0 unexpected: %+v", ranked[0])
	}
	if ranked[1].Fingerprint.Primary != "result: instance <T>" || ranked[1].Functions != 2 || ranked[1].Events != 2 || ranked[1].Files != 2 {
		t.Errorf("rank 1 unexpected: %+v", ranked[1])
	}
	if ranked[2].Fingerprint.Subsystem != "let" || ranked[3].Fingerprint.Subsystem != "union" {
		t.Errorf("expected deterministic subsystem order let then union, got %+v and %+v", ranked[2].Fingerprint, ranked[3].Fingerprint)
	}
}

func TestDiffStopsReductionAndRegressionExit(t *testing.T) {
	dir := t.TempDir()
	baseFile := filepath.Join(dir, "base.stops")
	headReducedFile := filepath.Join(dir, "head-reduced.stops")
	headRegressedFile := filepath.Join(dir, "head-regressed.stops")
	headNewDiffFile := filepath.Join(dir, "head-newdiff.stops")

	baseContent := strings.Join([]string{
		"S /workspaces/.work/ergo/base/internal/sketch/std/a.ergo 10 let of slice",
		"S /workspaces/.work/ergo/base/internal/sketch/std/a.ergo 20 let of slice",
		"S /workspaces/.work/ergo/base/internal/sketch/std/b.ergo 30 untyped conversion",
	}, "\n") + "\n"

	headReducedContent := strings.Join([]string{
		"S /workspaces/.work/ergo/head/internal/sketch/std/a.ergo 10 let of slice",
	}, "\n") + "\n"

	headRegressedContent := strings.Join([]string{
		"S /workspaces/.work/ergo/head/internal/sketch/std/a.ergo 10 let of slice",
		"S /workspaces/.work/ergo/head/internal/sketch/std/a.ergo 20 let of slice",
		"S /workspaces/.work/ergo/head/internal/sketch/std/a.ergo 40 let of slice",
		"S /workspaces/.work/ergo/head/internal/sketch/std/b.ergo 30 untyped conversion",
	}, "\n") + "\n"

	headNewDiffContent := strings.Join([]string{
		"S /workspaces/.work/ergo/head/internal/sketch/std/a.ergo 10 let of slice",
		"D /workspaces/.work/ergo/head/internal/sketch/std/b.ergo 30 body mismatch",
	}, "\n") + "\n"

	for p, c := range map[string]string{
		baseFile:          baseContent,
		headReducedFile:   headReducedContent,
		headRegressedFile: headRegressedContent,
		headNewDiffFile:   headNewDiffContent,
	} {
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// 1. Reduction passes even with -fail-on-regression.
	var out, errBuf bytes.Buffer
	if code := run([]string{"-diff-stops", "-fail-on-regression", "-json", baseFile, headReducedFile}, &out, &errBuf); code != 0 {
		t.Fatalf("reduced diff exit %d, want 0\nstdout:\n%s\nstderr:\n%s", code, out.String(), errBuf.String())
	}
	var d StopDiff
	if err := json.Unmarshal(out.Bytes(), &d); err != nil {
		t.Fatalf("decode StopDiff: %v", err)
	}
	if len(d.Reduced) != 2 || len(d.Regressed) != 0 || len(d.NewDiffs) != 0 {
		t.Fatalf("unexpected reduced diff: %+v", d)
	}

	// 2. Grown stop count fails with -fail-on-regression (exit 1).
	out.Reset()
	errBuf.Reset()
	if code := run([]string{"-diff-stops", "-fail-on-regression", baseFile, headRegressedFile}, &out, &errBuf); code != 1 {
		t.Fatalf("regressed diff exit %d, want 1\nstdout:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "REGRESSED") || !strings.Contains(out.String(), "let of slice") {
		t.Errorf("expected REGRESSED naming let of slice, got:\n%s", out.String())
	}

	// 3. New D diff fails with -fail-on-regression even when stops decreased (exit 1).
	out.Reset()
	errBuf.Reset()
	if code := run([]string{"-diff-stops", "-fail-on-regression", baseFile, headNewDiffFile}, &out, &errBuf); code != 1 {
		t.Fatalf("new D diff exit %d, want 1\nstdout:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "NEW IR DIFFS") || !strings.Contains(out.String(), "body mismatch") {
		t.Errorf("expected NEW IR DIFFS naming body mismatch, got:\n%s", out.String())
	}

	// 4. -group-stops CLI with -top 1 and -json.
	out.Reset()
	errBuf.Reset()
	if code := run([]string{"-group-stops", baseFile, "-top", "1", "-json"}, &out, &errBuf); code != 0 {
		t.Fatalf("-group-stops exit %d, want 0\nstderr:\n%s", code, errBuf.String())
	}
	if !strings.Contains(out.String(), `"let of slice"`) || strings.Contains(out.String(), `"untyped conversion"`) {
		t.Errorf("expected top-1 JSON to contain only let of slice, got:\n%s", out.String())
	}
}
