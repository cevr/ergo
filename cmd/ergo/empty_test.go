package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketchfront"
)

// TestEmptyRunMatchesTheRunner holds emptyTestRun to its reference, the
// test binary of a package with nothing to test, over every combination of
// the options that reach the binary (--seed, --receipt, --law, --skip-law,
// --run): the same stdout, stderr, exit status and receipt bytes. It kills
// the empty-* mutants, which no other test reaches: the CLI tests see an
// empty package only in its plain run.
// It builds Go because the reference is the emitted runner's own behaviour,
// which only its binary shows.
func TestEmptyRunMatchesTheRunner(t *testing.T) {
	if testing.Short() {
		t.Skip("the runner reference builds a test binary in the full tier")
	}
	root := physicalTempDir(t)
	smokeWrite(t, root, "ergo.mod", "module empty\n")
	dir := smokeWrite(t, root, "pkg/pkg.ergo", "func One() int { 1 }\n")
	dir = filepath.Dir(dir)
	l, err := load(dir, report.Target)
	if err != nil || !l.result.Checked || !nothingToTest(l.analysis.Root().Package) {
		t.Fatalf("load: %v, checked %v", err, l.result.Checked)
	}
	binary := filepath.Join(t.TempDir(), "pkg.test")
	var stderr bytes.Buffer
	m, err := sketchfront.Build(l.analysis, sketchfront.BuildRequest{
		Mode: build.ModeTest, Source: l.source.Admitted(), Output: binary,
		Cache: build.Cache{Root: t.TempDir()}, Toolchain: build.HostToolchain(), Stderr: &stderr,
	})
	if err != nil {
		t.Fatalf("build: %v\n%s", err, stderr.String())
	}
	m.Release()
	receipts := t.TempDir()
	for combo := range 32 {
		opts := fileOptions{command: "test", file: dir}
		if combo&1 != 0 {
			opts.seed = "18446744073709551615"
		}
		if combo&2 != 0 {
			opts.receipt = filepath.Join(receipts, fmt.Sprintf("%d", combo))
		}
		if combo&4 != 0 {
			opts.law = "Absent"
		}
		if combo&8 != 0 {
			opts.skipLaw = "Other"
		}
		if combo&16 != 0 {
			opts.run = "None"
		}
		t.Run(fmt.Sprint(combo), func(t *testing.T) {
			args, err := testChildArgs(opts, l.result.Revision)
			if err != nil {
				t.Fatal(err)
			}
			wantOut, wantErr, wantCode := runSmokeCommand(t, exec.Command(binary, args...))
			wantReceipt := readIfAny(t, opts.receipt)
			if opts.receipt != "" {
				if err := os.Remove(opts.receipt); err != nil && !errors.Is(err, os.ErrNotExist) {
					t.Fatal(err)
				}
			}
			var out, errOut bytes.Buffer
			code := 0
			if err := emptyTestRun(opts, l.result.Revision, &out, &errOut); err != nil {
				var status exitStatusError
				if !errors.As(err, &status) {
					t.Fatal(err)
				}
				code = status.code
			}
			gotReceipt := readIfAny(t, opts.receipt)
			if out.String() != string(wantOut) || errOut.String() != string(wantErr) || code != wantCode || gotReceipt != wantReceipt {
				t.Fatalf("options %+v:\nCLI exit %d\nstdout %q\nstderr %q\nreceipt %q\nrunner exit %d\nstdout %q\nstderr %q\nreceipt %q",
					args, code, out.String(), errOut.String(), gotReceipt, wantCode, wantOut, wantErr, wantReceipt)
			}
		})
	}
}

// readIfAny is the file at path, or "" when path is "" or names nothing.
func readIfAny(t *testing.T, path string) string {
	t.Helper()
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
