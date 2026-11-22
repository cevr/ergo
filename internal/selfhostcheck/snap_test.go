package selfhostcheck

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"ergo.local/prototype/internal/proclimit"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/check/snapshot"
	"ergo.local/prototype/internal/testwait"
)

// snapAgreePath is the std packages whose types the Ergo checker gives as
// the Go checker's export (CHK1b) gives them. The list only grows: a
// package on it that stops agreeing, or is skipped, fails the test. It is
// generated (snapAgreeCommand writes it).
var snapAgreePath = filepath.Join("testdata", "snap_agree.json")

// snapAgreeCommand writes snapAgreePath from a run of TestStdSnapshotAgrees.
const snapAgreeCommand = "ERGO_WRITE_SNAP=1 go test -count=1 -run '^TestStdSnapshotAgrees$' ./internal/selfhostcheck"

type snapAgree struct {
	Generated string   `json:"generated"`
	Agree     []string `json:"agree"`
}

// TestStdSnapshotAgrees is S3-SNAP's agreement: the check snapshot of std
// the Ergo checker gives (compiler/check/types through cmd/typedump, its
// imports loaded from std source) against CHK1b's, the Go checker's export
// (snapshot.ExportStd) decoded back (snapshot.Decode), compared package by
// package as decoded content, never bytes. The fields compared, per
// package, are what an importer's IR build reads of it: the kinds of each
// generic type's and function's bracket parameters (typedump -kinds), each
// record's parameters and field types, each defined type's base, and each
// function's signature, the refined types' parse functions included
// (typedump), each as Type.String prints it (goKinds, goTypes). A
// package's EXT and FOREIGN tables (its prelude, universe and Go imports'
// types) come from the decoded package too, until LOAD-MODULE-DEPS.
//
// Each package is one of: agreeing, differing (its diff logged), or
// skipped (typedump fails on it, or its segment does not decode; the
// reason logged). The test fails when a package of snapAgreePath does not
// agree, so the list only grows; snapAgreeCommand rewrites it with the
// agreeing packages, and refuses to drop one.
//
// Mutant: snap-decode-skips-segment (Decode leaves out the first std
// segment: a listed package is neither compared nor skipped).
//
// It builds Go because typedump is the Ergo program under test: only its
// lowered, built form runs over std.
func TestStdSnapshotAgrees(t *testing.T) {
	needsBuild(t)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	data, err := snapshot.ExportStd()
	if err != nil {
		t.Fatal(err)
	}
	dec, err := snapshot.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	td := typesDump(t, ctx)
	tmp := t.TempDir()
	e, f := filepath.Join(tmp, "ext"), filepath.Join(tmp, "foreign")
	// compare is "" when the package in dir agrees, else why not.
	compare := func(dir string, pkg *check.Package) string {
		files := ergoFiles(t, dir)
		kinds, ext := goKinds(pkg)
		fields, foreign := goTypes(pkg)
		writeLines(t, e, ext)
		writeLines(t, f, foreign)
		gotKinds, err := tryDump(ctx, td, append([]string{"-std", stdDir(), "-kinds", e, f}, files...)...)
		if err != nil {
			return "skipped: typedump -kinds: " + err.Error()
		}
		gotTypes, err := tryDump(ctx, td, append([]string{"-std", stdDir(), e, f}, files...)...)
		if err != nil {
			return "skipped: typedump: " + err.Error()
		}
		d := setDiff(kinds, gotKinds) + setDiff(fields, dropCharged(gotTypes))
		if d != "" {
			return "differs (go -, ergo +):\n" + d
		}
		return ""
	}
	results := map[string]string{} // path: "" when it agrees, else why not
	type pkgAt struct {
		path string
		pkg  *check.Package
	}
	all := []pkgAt{{"prelude", dec.Prelude}}
	for i, p := range dec.Paths {
		all = append(all, pkgAt{p, dec.Packages[i]})
	}
	for _, p := range all {
		dir := stdDir()
		if p.path != "prelude" {
			dir = filepath.Join(stdDir(), filepath.FromSlash(p.path))
		}
		results[p.path] = compare(dir, p.pkg)
	}
	for path, why := range dec.Failed {
		results[path] = "skipped: " + why
	}
	var agree []string
	for _, path := range slices.Sorted(maps.Keys(results)) {
		if why := results[path]; why == "" {
			agree = append(agree, path)
		} else {
			t.Logf("%s: %s", path, firstLines(why, 12))
		}
	}
	t.Logf("%d of %d std packages agree", len(agree), len(results))
	var held snapAgree
	if b, err := os.ReadFile(snapAgreePath); err == nil {
		if err := json.Unmarshal(b, &held); err != nil {
			t.Fatalf("%s: %v", snapAgreePath, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	for _, path := range held.Agree {
		why, seen := results[path]
		switch {
		case !seen:
			t.Errorf("%s agreed and is no longer compared (its segment is not in the export)", path)
		case why != "":
			t.Errorf("%s agreed and no longer does: %s", path, firstLines(why, 40))
		}
	}
	if os.Getenv("ERGO_WRITE_SNAP") == "" {
		for _, path := range agree {
			if !slices.Contains(held.Agree, path) {
				t.Logf("%s agrees and is not listed; %s adds it", path, snapAgreeCommand)
			}
		}
		return
	}
	if t.Failed() {
		t.Fatalf("not written: the list only grows")
	}
	b, err := json.MarshalIndent(snapAgree{Generated: snapAgreeCommand, Agree: agree}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(snapAgreePath, append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s: %d packages", snapAgreePath, len(agree))
}

// firstLines is s cut to its first n lines.
func firstLines(s string, n int) string {
	ls := strings.SplitAfter(s, "\n")
	if len(ls) <= n {
		return s
	}
	return strings.Join(ls[:n], "") + "…\n"
}

// tryDump is runDump that returns a failed run's error, with the end of
// its standard error, instead of failing the test.
func tryDump(ctx context.Context, bin string, args ...string) ([]string, error) {
	limit, err := proclimit.Limit(os.Environ(), proclimit.DefaultLimit)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := proclimit.Run(cmd, limit); err != nil {
		msg := strings.TrimSpace(errOut.String())
		if len(msg) > 300 {
			msg = msg[len(msg)-300:]
		}
		return nil, errors.New(err.Error() + ": " + msg)
	}
	return lines(out.String()), nil
}
