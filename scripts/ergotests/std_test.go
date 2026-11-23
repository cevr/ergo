package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStdLawsRunInPackage: a std package's law is a killer (STD-LAW-RUN,
// Laws and proofs). It runs in-package, as std is checked: json's RoundTrips calls
// the package's private writer and its trusted runtime Go, which a folder
// check refuses (S108), so before this the law could not run at all. A std
// law check time leaves undecided is no killer: ergo test cannot reach a
// std folder, so it is not asked.
//
// Mutants (testdata/mutants.json): stdlaw-std-as-folder,
// stdlaw-undecided-std-asks-ergo.
func TestStdLawsRunInPackage(t *testing.T) {
	json := filepath.Join("..", "..", "internal", "sketch", "std", "json")
	var out strings.Builder
	if code := runKiller(&out, []string{json}, "law:RoundTrips", ""); code != 0 {
		t.Errorf("-killer law:RoundTrips %s: exit %d, want 0 (it holds in-package)\n%s", json, code, out.String())
	}
	log := filepath.Join(t.TempDir(), "ergo.log")
	ergo := filepath.Join(t.TempDir(), "ergo.sh")
	if err := os.WriteFile(ergo, []byte("#!/bin/sh\necho ran > "+log+"\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := runKiller(&out, []string{json}, "law:StreamRoundTrips", ergo); code != 1 {
		t.Errorf("-killer law:StreamRoundTrips (skipped at check time): exit %d, want 1\n%s", code, out.String())
	}
	if _, err := os.Stat(log); err == nil {
		t.Error("a std law undecided at check time asked ergo test, which cannot check a std folder")
	}
}
