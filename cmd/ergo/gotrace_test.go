package main

import (
	"slices"
	"strings"
	"testing"
)

// TestGoTraceTagsTheBuild: --go-trace on build, run and test links
// runtime/obs, by the ergogotrace tag on the action (RT-OBS), and other
// commands refuse it. Mutant: obs-flag-untagged (buildAction adds no tag).
func TestGoTraceTagsTheBuild(t *testing.T) {
	for _, command := range []string{"build", "run", "test"} {
		opts, err := parseFileOptions(command, []string{"--go-trace", "main.ergo"})
		if err != nil {
			t.Fatalf("%s --go-trace: %v", command, err)
		}
		action, err := buildAction(opts)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(action.Tags, "ergogotrace") {
			t.Fatalf("%s --go-trace: tags %v; want ergogotrace", command, action.Tags)
		}
	}
	plain, err := parseFileOptions("build", []string{"main.ergo"})
	if err != nil {
		t.Fatal(err)
	}
	if action, err := buildAction(plain); err != nil || len(action.Tags) != 0 {
		t.Fatalf("build without --go-trace: tags %v, %v; want none", action.Tags, err)
	}
	if _, err := parseFileOptions("check", []string{"--go-trace", "main.ergo"}); err == nil || !strings.Contains(err.Error(), "--go-trace is only supported by build, run and test") {
		t.Fatalf("check --go-trace: %v; want the refusal", err)
	}
}
