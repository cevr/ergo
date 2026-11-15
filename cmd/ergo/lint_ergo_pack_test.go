package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/examples/lintpack/policy"
	sketchlint "ergo.local/prototype/internal/sketch/lint"
	"ergo.local/prototype/internal/sketchfront"
	"ergo.local/prototype/lint"
)

// TestErgoPackAgreesWithGoPack is LINT4's acceptance: the policy pack
// written in Ergo (examples/lint-policy, std lint) and the Go pack
// (examples/lintpack) give byte-equal `ergo lint test` reports over the
// same generated programs and configurations. Each seed generates a
// program of providers, withs, failures, helpers and suppression
// directives, and a configuration of both rules (severities, the preset,
// options that name absent declarations); the Ergo pack runs as a process
// over the protocol, as `ergo lint` runs it, and the Go pack is evaluated
// in process. A failing seed prints the program, the configuration, both
// reports and the replay command. The Ergo manifest is the Go pack's
// manifest with the Ergo program's path, and it is the example's
// `manifest` case.
//
// It builds Go because the Ergo pack is a separate program: the protocol,
// the codec of the snapshot and the response frame are what it holds, and
// no in-process check crosses them. The pack runs under the runner's
// kernel memory bound (lint.Limits, RLIMIT_DATA).
//
// Mutants (cmd/ergo ledger): lint4-policy-allow-ignored,
// lint4-policy-inline-entry-related, lint4-policy-list-format,
// lint4-policy-recovered-reported. Each is a departure of the Ergo pack
// from the Go pack that no law of std lint sees.
func TestErgoPackAgreesWithGoPack(t *testing.T) {
	needsBuild(t)
	dir := physicalTempDir(t)
	binary := filepath.Join(dir, "bin", "lint-policy")
	if out, code := commandInProcess(t, "build", filepath.Join("..", "..", "examples", "lint-policy", "cmd", "lint-policy"), "-o", binary); code != 0 {
		t.Fatalf("ergo build lint-policy: exit %d\n%s", code, out)
	}
	ergoManifest, err := exec.Command(binary, "manifest").Output()
	if err != nil {
		t.Fatal(err)
	}
	goManifest, err := policy.Pack.Manifest(lint.Executable{Path: "bin/lint-policy"})
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(goManifest)
	if err != nil {
		t.Fatal(err)
	}
	golden, err := os.ReadFile(filepath.Join("..", "..", "examples", "lint-policy", "cases", "manifest", "expected.out"))
	if err != nil {
		t.Fatal(err)
	}
	if want = append(want, '\n'); !bytes.Equal(ergoManifest, want) || !bytes.Equal(golden, want) {
		t.Fatalf("the Ergo manifest is not the Go pack's:\nergo:   %s\ngo:     %s\ngolden: %s", ergoManifest, want, golden)
	}
	smokeWrite(t, dir, "ergo.json", string(ergoManifest))
	goJSON, err := json.Marshal(goManifest)
	if err != nil {
		t.Fatal(err)
	}
	smokeWrite(t, dir, "go.json", string(goJSON))

	seeds := []uint64{}
	if s := os.Getenv("ERGO_LINT4_SEED"); s != "" {
		seed, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = append(seeds, seed)
	} else {
		for seed := uint64(1); seed <= 120; seed++ {
			seeds = append(seeds, seed)
		}
	}
	// reached counts what the reports hold over all seeds, so a generator
	// that never reaches a finding, a related place, a failed rule or an
	// applied directive fails instead of agreeing vacuously.
	reached := map[string]int{}
	defer func() {
		if len(seeds) > 1 && !t.Failed() {
			for _, what := range []string{"finding", "related", "failed", "applied", "unused", "off"} {
				if reached[what] == 0 {
					t.Errorf("no seed reached a report with a %s: %v", what, reached)
				}
			}
		}
	}()
	for _, seed := range seeds {
		source, config := lintPackFixture(seed)
		in := sketchlint.InputOf(sketchfront.Check(source))
		if !in.Checked {
			t.Fatalf("seed %d: the generated program does not check:\n%s\nreplay: ERGO_LINT4_SEED=%d go test ./cmd/ergo -run TestErgoPackAgreesWithGoPack", seed, source, seed)
		}
		ergoReport := lintPackRun(t, in, config, "ergo.json", dir, nil)
		goReport := lintPackRun(t, in, config, "go.json", dir, func(_ context.Context, c *lint.Configuration, _ string, snapshot *lint.Snapshot, _ lint.RunOptions) (lint.Report, error) {
			return lint.Evaluate(policy.Pack, c, snapshot)
		})
		if diff := goReport.Diff(ergoReport); diff != "" {
			t.Fatalf("seed %d: the Ergo pack's report differs from the Go pack's\nprogram:\n%s\nconfiguration: %s\n%s\nreplay: ERGO_LINT4_SEED=%d go test ./cmd/ergo -run TestErgoPackAgreesWithGoPack", seed, source, config, strings.ReplaceAll(strings.ReplaceAll(diff, "want:", "go:"), "got:", "ergo:"), seed)
		}
		for _, f := range ergoReport.Findings {
			if f.Code == sketchlint.CodeRunner {
				reached["failed"]++
			} else {
				reached["finding"]++
			}
			if len(f.Related) > 0 {
				reached["related"]++
			}
		}
		for _, s := range ergoReport.Suppressions {
			reached[s.Status]++
		}
		for _, r := range ergoReport.Rules {
			reached[r.Status]++
		}
	}
}

// lintPackRun lints in under the configuration document config, whose
// packs entry names manifest in dir. With run nil the pack runs as its
// process.
func lintPackRun(t *testing.T, in sketchlint.Input, config, manifest, dir string, run func(context.Context, *lint.Configuration, string, *lint.Snapshot, lint.RunOptions) (lint.Report, error)) sketchlint.Expectation {
	t.Helper()
	doc, err := sketchlint.ParseConfig([]byte(strings.Replace(config, "MANIFEST", manifest, 1)))
	if err != nil {
		t.Fatalf("%v\n%s", err, config)
	}
	doc.Dir = dir
	packs, err := sketchlint.LoadPacks(&doc, nil)
	if err != nil {
		t.Fatal(err)
	}
	conf, err := sketchlint.Builtin().ConfigureWith(doc, nil, packs)
	if err != nil {
		t.Fatalf("%v\n%s", err, config)
	}
	if run != nil {
		conf.UsePackRunner(run)
	}
	return sketchlint.ExpectationOf(conf.Run(in))
}

// lintPackFixture is the program and the configuration of seed. The
// program declares a Store capability with two providers, Disk and
// Memory, a named context Live that binds Disk, two failures, and
// functions of random kinds: binders through Live, a local, a shadowing
// local, Memory or a passed capability; functions that fail, pass a
// failure on through a helper, or recover one; pure functions. Some carry
// suppression directives. The configuration names functions of the program
// and sometimes one it does not declare, and a provider or failure of
// either kind or an absent one.
func lintPackFixture(seed uint64) (string, string) {
	r := rand.New(rand.NewPCG(seed, 0x4c494e5434))
	var b strings.Builder
	b.WriteString(`type Denied { Reason string }

type Missing { Key string }

type Store {
	Read(key string) int fails Denied | Missing,
}

type Disk { N int }

func Read(d Disk, key string) int fails Denied | Missing {
	if key == "" {
		return Missing{Key: key}
	}
	if key == "secret" {
		return Denied{Reason: key}
	}
	d.N
}

type Memory { N int }

func Read(m Memory, key string) int fails Denied | Missing {
	if key == "" {
		return Missing{Key: key}
	}
	m.N
}

let Live = context {
	Store: Disk{N: 1},
}

func peek() int needs Store {
	Store.Read("k") catch {
	case Denied:
		0
	case Missing:
		0
	}
}

func fetch(key string) int fails Denied | Missing needs Store {
	try Store.Read(key)
}
`)
	names := []string{"main.peek", "main.fetch"}
	failing := []string{"fetch"}
	locals := []string{"store", "disk", "Disk", "s", "live", "Memory"}
	n := 2 + r.IntN(9)
	for i := range n {
		name := fmt.Sprintf("%s%d", []string{"run", "load", "serve", "compose", "check"}[r.IntN(5)], i)
		names = append(names, "main."+name)
		b.WriteString("\n")
		if r.IntN(4) == 0 {
			rule := []string{"policy/forbidden-failure", "policy/provider-boundary"}[r.IntN(2)]
			fmt.Fprintf(&b, "// ergo-lint-disable-next-line %s -- generated directive %d\n", rule, i)
		}
		local := locals[r.IntN(len(locals))]
		switch r.IntN(10) {
		case 0:
			fmt.Fprintf(&b, "func %s() int {\n\twith Live { peek() }\n}\n", name)
		case 1:
			fmt.Fprintf(&b, "func %s() int {\n\tlet %s = Disk{N: 2}\n\twith context { Store: %s } { peek() }\n}\n", name, local, local)
		case 2:
			fmt.Fprintf(&b, "func %s() int {\n\tlet %s = Memory{N: 3}\n\twith context { Store: %s } { peek() }\n}\n", name, local, local)
		case 3:
			fmt.Fprintf(&b, "func %s(s Store) int {\n\twith context { Store: s } { peek() }\n}\n", name)
		case 4:
			fmt.Fprintf(&b, "func %s() int {\n\tlet a = with context { Store: Memory{N: 4} } { peek() }\n\tlet b = with Live { peek() }\n\ta + b\n}\n", name)
		case 5:
			fmt.Fprintf(&b, "func %s(key string) int fails Denied | Missing needs Store {\n\ttry Store.Read(key)\n}\n", name)
			failing = append(failing, name)
		case 6:
			helper := failing[r.IntN(len(failing))]
			fmt.Fprintf(&b, "func %s(key string) int fails Denied | Missing needs Store {\n\tlet a = try %s(key)\n\tlet b = try %s(key + \"x\")\n\ta + b\n}\n", name, helper, helper)
			failing = append(failing, name)
		case 7:
			fmt.Fprintf(&b, "func %s(key string) int fails Missing needs Store {\n\ttry %s(key) catch {\n\tcase Denied:\n\t\t0\n\t}\n}\n", name, failing[r.IntN(len(failing))])
		case 8:
			fmt.Fprintf(&b, "func %s(key string) int fails Denied needs Store {\n\ttry %s(key) catch {\n\tcase Missing:\n\t\t0\n\t}\n}\n", name, failing[r.IntN(len(failing))])
		default:
			fmt.Fprintf(&b, "func %s() int {\n\tlet Denied = 1\n\tDenied + 1\n}\n", name)
		}
	}
	b.WriteString("\nfunc main() needs Console {\n\tConsole.Println(\"ok\")\n}\n")

	pick := func() []string {
		out := []string{}
		for _, name := range names {
			if r.IntN(3) == 0 {
				out = append(out, name)
			}
		}
		if r.IntN(8) == 0 {
			out = append(out, "main.nonesuch")
		}
		return out
	}
	setting := func(options map[string]any) any {
		switch r.IntN(6) {
		case 0:
			return "off"
		case 1:
			return map[string]any{"options": options}
		default:
			return map[string]any{"severity": []string{"error", "warning", "information", "hint"}[r.IntN(4)], "options": options}
		}
	}
	doc := map[string]any{
		"version": 1,
		"packs":   []any{map[string]any{"manifest": "MANIFEST"}},
		"rules": map[string]any{
			"policy/provider-boundary": setting(map[string]any{"provider": []string{"main.Disk", "main.Disk", "main.Memory", "main.Absent"}[r.IntN(4)], "allow": pick()}),
			"policy/forbidden-failure": setting(map[string]any{"failure": []string{"main.Denied", "main.Denied", "main.Missing", "main.Absent"}[r.IntN(4)], "functions": pick()}),
		},
	}
	if r.IntN(3) == 0 {
		doc["extends"] = []string{"policy/strict"}
	}
	config, err := json.Marshal(doc)
	if err != nil {
		panic(err)
	}
	return b.String(), string(config)
}
