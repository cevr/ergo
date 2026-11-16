package policy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	sketchlint "ergo.local/prototype/internal/sketch/lint"
	"ergo.local/prototype/internal/sketchfront"
	"ergo.local/prototype/lint"
)

// TestManifestIsThePack: the committed manifest is the pack's own metadata
// for its built program, so `ergo lint rules` and the configuration see
// what the program serves. Mutant: lint3-policy-manifest-drift.
func TestManifestIsThePack(t *testing.T) {
	manifest, err := Pack.Manifest(lint.Executable{Path: "bin/policy-lint"})
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	got, err := os.ReadFile("../policy.json")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("policy.json is not Pack.Manifest; want:\n%s", want)
	}
}

// fixtures are the example's source fixtures, each with the configuration
// it runs under.
var fixtures = []struct{ source, config string }{
	{"provider_boundary.ergo", "provider.lint.json"},
	{"forbidden_failure.ergo", "failure.lint.json"},
}

// TestFixturesInProcess runs both policies over the real checked fixtures
// through the runner ergo lint uses, with the pack evaluated in process
// (lint.Evaluate), and compares each with its expectation: the same
// expectation `ergo lint test` checks over the built pack process. Mutants:
// lint3-policy-allow-ignored, lint3-policy-reads-needs,
// lint3-facts-with-provider-is-capability, lint3-facts-fails-written-only.
func TestFixturesInProcess(t *testing.T) {
	for _, f := range fixtures {
		t.Run(f.source, func(t *testing.T) {
			got := runInProcess(t, filepath.Join("..", "testdata", f.source), filepath.Join("..", f.config))
			data, err := os.ReadFile(filepath.Join("..", "testdata", f.source[:len(f.source)-len(".ergo")]+".lint.json"))
			if err != nil {
				t.Fatal(err)
			}
			want, err := sketchlint.ParseExpectation(data)
			if err != nil {
				t.Fatal(err)
			}
			if diff := want.Diff(got); diff != "" {
				t.Fatal(diff)
			}
		})
	}
}

// TestForbiddenFailureReadsOperatorRows: the failures timeout.After and a
// Go binding contribute are found through their declared rows, and a
// caller that recovers them is clean: std and Go binding types keep their
// package in their identity. Mutant: lint3-facts-std-type-unqualified.
func TestForbiddenFailureReadsOperatorRows(t *testing.T) {
	for _, run := range []struct{ failure, functions, want string }{
		{"timeout.Expired", `["main.timed", "main.Timed"]`, "failure timeout.Expired can escape main.timed"},
		{"go.Error", `["main.main", "main.foreign"]`, "failure go.Error can escape main.foreign"},
	} {
		config := `{"version":1,"packs":[{"manifest":"policy.json"}],"rules":{"policy/provider-boundary":"off","policy/forbidden-failure":{"options":{"failure":"` + run.failure + `","functions":` + run.functions + `}}}}`
		got := runConfig(t, filepath.Join("..", "testdata", "forbidden_failure.ergo"), []byte(config), "..")
		if !got.Complete || len(got.Findings) != 1 || got.Findings[0].Message != run.want {
			t.Fatalf("%s: %s", run.failure, got.Encode())
		}
	}
}

// runInProcess lints source under the configuration at config, with every
// selected pack evaluated in process.
func runInProcess(t *testing.T, source, config string) sketchlint.Expectation {
	t.Helper()
	data, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	return runConfig(t, source, data, filepath.Dir(config))
}

// runConfig lints source under the configuration document data, whose
// directory is dir.
func runConfig(t *testing.T, source string, data []byte, dir string) sketchlint.Expectation {
	t.Helper()
	text, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := sketchlint.ParseConfig(data)
	if err != nil {
		t.Fatal(err)
	}
	doc.Dir = dir
	packs, err := sketchlint.LoadPacks(&doc, nil)
	if err != nil {
		t.Fatal(err)
	}
	conf, err := sketchlint.Builtin().ConfigureWith(doc, nil, packs)
	if err != nil {
		t.Fatal(err)
	}
	conf.UsePackRunner(func(_ context.Context, c *lint.Configuration, _ string, snapshot *lint.Snapshot, _ lint.RunOptions) (lint.Report, error) {
		return lint.Evaluate(Pack, c, snapshot)
	})
	in := sketchlint.InputOf(sketchfront.Check(string(text)))
	if !in.Checked {
		t.Fatalf("%s does not check", source)
	}
	return sketchlint.ExpectationOf(conf.Run(in))
}
