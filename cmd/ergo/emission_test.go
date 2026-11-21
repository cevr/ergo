package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/build"
)

// TestEmittedDigestNamesTheLinkedModule: the digest `ergo build --emitted`
// and `ergo test --emitted` print is the digest of the Go module that
// `ergo build` and `ergo test` of the same folder write and link, read
// from the module folder -work names. examples/counter's main has a rewrite
// law in its ledger, so the build digest covers the ledger's evidence too.
// The examples runner keys a program on these digests, and the compiler's
// lowering only through them (early cutoff): a digest of another lowering
// than the linked one (the text lowering, where builds link the IR one)
// let an edit of lower/ir*.go replay every program's old verdict
// (gate-speed GS32c). It catches gs32c-ir-edit-replays,
// gs32c-test-ir-edit-replays and gs32c-ledger-unread.
//
// It builds Go because the module a build links is only known by building
// it: the linked folder is the reference the digest must name.
func TestEmittedDigestNamesTheLinkedModule(t *testing.T) {
	needsBuild(t)
	binary := buildTestCLI(t)
	root, err := filepath.Abs(filepath.Join("..", "..", "examples", "counter"))
	if err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	run := func(args ...string) (string, string) {
		t.Helper()
		command := exec.Command(binary, args...)
		command.Dir = root
		command.Env = append(os.Environ(), "ERGO_CACHE="+cache, "ERGO_LAWCACHE=off")
		stdout, stderr, code := runSmokeCommand(t, command)
		if code != 0 {
			t.Fatalf("ergo %s: exit %d\n%s\n%s", strings.Join(args, " "), code, stdout, stderr)
		}
		return string(stdout), string(stderr)
	}
	for _, c := range []struct {
		mode   build.Mode
		folder string
		link   []string
	}{
		{build.ModeBuild, "cmd/counter", []string{"build", "cmd/counter", "-o", filepath.Join(t.TempDir(), "counter"), "-work"}},
		{build.ModeTest, "wheel", []string{"test", "wheel", "-work"}},
	} {
		emitted, _ := run(string(c.mode), "--emitted", c.folder)
		fields := strings.Fields(emitted)
		if len(fields) != 3 || fields[0] != "emitted" {
			t.Fatalf("%s --emitted %s printed %q", c.mode, c.folder, emitted)
		}
		_, stderr := run(c.link...)
		if linked := moduleDigest(t, workDir(t, stderr), c.mode); fields[1] != linked {
			t.Errorf("%s --emitted %s prints %s; the module %s links has %s", c.mode, c.folder, fields[1], c.mode, linked)
		}
	}
}

// moduleDigest is sketchfront.EmittedDigest read back from a module
// folder: the mode, then each file but the cache's manifest by path order,
// as length-prefixed path and bytes.
func moduleDigest(t *testing.T, dir string, mode build.Mode) string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err == nil && rel != build.ManifestName {
			paths = append(paths, filepath.ToSlash(rel))
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(paths)
	h := sha256.New()
	fmt.Fprintf(h, "mode %s\n", mode)
	for _, p := range paths {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(h, "%d:%s%d:", len(p), p, len(data))
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil))
}
