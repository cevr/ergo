package lint_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lint"
	sdk "ergo.local/prototype/lint"
)

// packCase is one generated program, two rule packs' behaviour and a
// configuration: the inputs of the pack half of the runner
// (docs/specs/custom-lint.md, suppression states).
type packCase struct {
	lines      []string
	directives []packDirective
	checked    bool
	// outcome of each pack: 0 completes, 1 its process fails (a crash), 2
	// the runner cannot run it.
	outcome map[string]int
	// findings are the lines each rule (ns/rule) reports; fails the rules
	// whose check returns an error.
	findings map[string][]int
	fails    map[string]bool
	settings map[string]string // ns/rule -> "", or a severity word
	strict   bool
}

// packDirective kinds: what the generator wrote.
const (
	packDirRule       = iota // ns/rule of a selected pack
	packDirUnknown           // an unknown rule of a selected pack: L001
	packDirUnselected        // a namespace no selected pack has
	packDirReserved          // ergo/… or builtin/…: L001
	packDirKinds
)

type packDirective struct {
	line int
	kind int
	rule string
}

var (
	testPackNames = []string{"acme", "team"}
	testPackRules = []string{"one", "two"}
	packSeverity  = []string{"", "off", "hint", "warning", "error"}
)

func generatePackCase(r *rand.Rand) packCase {
	c := packCase{checked: r.IntN(5) != 0, strict: r.IntN(2) == 0, outcome: map[string]int{}, findings: map[string][]int{}, fails: map[string]bool{}, settings: map[string]string{}}
	n := 3 + r.IntN(8)
	for i := 1; i <= n; i++ {
		if r.IntN(3) == 0 {
			c.lines = append(c.lines, "")
			continue
		}
		kind := r.IntN(packDirKinds)
		rule := testPackNames[r.IntN(2)] + "/" + testPackRules[r.IntN(2)]
		switch kind {
		case packDirUnknown:
			rule = testPackNames[r.IntN(2)] + "/three"
		case packDirUnselected:
			rule = "other/one"
		case packDirReserved:
			rule = []string{"ergo/one", "builtin/one"}[r.IntN(2)]
		}
		c.lines = append(c.lines, "// ergo-lint-disable-next-line "+rule+" -- reason")
		c.directives = append(c.directives, packDirective{line: i, kind: kind, rule: rule})
	}
	total := n + 2
	for _, ns := range testPackNames {
		c.outcome[ns] = []int{0, 0, 0, 1, 2}[r.IntN(5)]
		for _, name := range testPackRules {
			id := ns + "/" + name
			c.settings[id] = packSeverity[r.IntN(len(packSeverity))]
			c.fails[id] = r.IntN(6) == 0
			for line := 1; line <= total; line++ {
				if r.IntN(3) == 0 {
					c.findings[id] = append(c.findings[id], line)
				}
			}
		}
	}
	return c
}

func (c packCase) source() string {
	src := strings.Join(c.lines, "\n") + "\nfunc main() {\n}\n"
	if !c.checked {
		src += "func bad() int {\n\t\"x\"\n}\n"
	}
	return src
}

// packs are the case's two Go packs: each rule reports on the lines the
// case says, at the line's start, or fails.
func (c packCase) packs(src string) map[string]*sdk.Pack {
	out := map[string]*sdk.Pack{}
	for _, ns := range testPackNames {
		pack := &sdk.Pack{Namespace: ns, Version: "1.0.0", FactVersions: []int{sdk.FactSchemaVersion}}
		for _, name := range testPackRules {
			id := ns + "/" + name
			pack.Rules = append(pack.Rules, &sdk.Rule{
				Name: name, Version: "1", Description: "a generated rule", DefaultSeverity: sdk.SeverityWarning,
				Requires: []sdk.Family{sdk.FamilyCallables},
				Check: func(pass *sdk.Pass) error {
					if c.fails[id] {
						return errors.New("the rule fails")
					}
					for _, line := range c.findings[id] {
						at := int(lineStart(src, line))
						pass.Report(sdk.Finding{Message: "finding of " + id, Span: sdk.Span{File: pass.Snapshot.Sources[0].File, Offset: at, Line: line, Column: 1}})
					}
					return nil
				},
			})
		}
		out[ns] = pack
	}
	return out
}

// anyOn reports whether a rule of pack ns is enabled.
func (c packCase) anyOn(ns string) bool {
	for _, name := range testPackRules {
		if c.settings[ns+"/"+name] != "off" {
			return true
		}
	}
	return false
}

func (c packCase) config() string {
	rules := map[string]any{}
	for id, s := range c.settings {
		if s != "" {
			rules[id] = s
		}
	}
	doc, _ := json.Marshal(map[string]any{"version": 1, "rules": rules})
	return string(doc)
}

// model is the reference of custom-lint.md's states: each pack rule's
// status, the findings left after suppression (pack findings, L000 and
// L001), each directive's status, completeness and policy.
func (c packCase) model() modelOutcome {
	out := modelOutcome{Complete: c.checked}
	type finding struct {
		code string
		sev  string
		line int
	}
	var findings []finding
	status := map[string]string{}
	severity := map[string]string{}
	for _, ns := range testPackNames {
		// A pack with no enabled rule starts no process, so it cannot
		// crash; a runner that cannot run it fails it all the same.
		packFailed := c.checked && (c.outcome[ns] == 2 || c.outcome[ns] == 1 && c.anyOn(ns))
		if packFailed {
			out.Complete = false
			findings = append(findings, finding{code: "L000", sev: "error"})
		}
		for _, name := range testPackRules {
			id := ns + "/" + name
			sev := c.settings[id]
			if sev == "" {
				sev = "warning"
			}
			severity[id] = sev
			st := "completed"
			switch {
			case sev == "off":
				st = "off"
			case !c.checked:
				st = "not-evaluated"
			case packFailed:
				st = "failed"
			case c.fails[id]:
				st = "failed"
				out.Complete = false
				findings = append(findings, finding{code: "L000", sev: "error"})
			default:
				for _, line := range c.findings[id] {
					findings = append(findings, finding{code: id, sev: sev, line: line})
				}
			}
			status[id] = st
			out.Rules = append(out.Rules, fmt.Sprintf("%s %s %s", id, st, sev))
		}
	}
	removed := map[int]bool{}
	for _, d := range c.directives {
		covered := d.line + 1
		var state string
		switch d.kind {
		case packDirUnknown, packDirReserved:
			findings = append(findings, finding{code: "L001", sev: "error", line: d.line})
			continue
		case packDirUnselected:
			state = "not-evaluated pack-not-selected"
		default:
			switch {
			case !c.checked:
				state = "not-evaluated unchecked-source"
			case status[d.rule] == "off":
				state = "not-evaluated rule-off"
			case status[d.rule] == "failed":
				state = "not-evaluated pack-failed"
			default:
				state = "unused"
				for i, f := range findings {
					if f.code == d.rule && f.line == covered {
						removed[i] = true
						state = "applied"
					}
				}
				if state == "unused" {
					findings = append(findings, finding{code: "L001", sev: "error", line: d.line})
				}
			}
		}
		out.Suppressions = append(out.Suppressions, fmt.Sprintf("%s %s %d %d", d.rule, state, d.line, covered))
	}
	out.Policy = c.checked && out.Complete
	for i, f := range findings {
		if removed[i] {
			continue
		}
		rule := ""
		if strings.Contains(f.code, "/") {
			rule = f.code
		}
		out.Findings = append(out.Findings, fmt.Sprintf("%d %s %s %s", f.line, f.code, rule, f.sev))
		if f.sev == "error" || (c.strict && f.sev == "warning") {
			out.Policy = false
		}
	}
	slices.Sort(out.Findings)
	slices.Sort(out.Rules)
	return out
}

// crashed is the report of a pack whose process failed, as lint.Run gives
// it (lint/run_test.go holds Run to this shape over real processes): every
// requested rule failed, off rules off, no finding.
func crashed(conf *sdk.Configuration, pack *sdk.Pack) sdk.Report {
	report := sdk.Report{Failure: &sdk.ExecutionFailure{Pack: pack.Namespace, Code: sdk.FailureCrashed, Message: "pack exited unsuccessfully"}}
	for _, rule := range pack.Rules {
		id := pack.Namespace + "/" + rule.Name
		setting, _ := conf.Setting(id)
		st := sdk.RuleStatus{Rule: id, Status: sdk.StatusFailed, Reason: "pack-failed: " + sdk.FailureCrashed}
		if setting.Severity == sdk.SeverityOff {
			st = sdk.RuleStatus{Rule: id, Status: sdk.StatusOff}
		}
		report.Rules = append(report.Rules, st)
	}
	return report
}

// TestPackSuppressionAgreesWithModel runs the runner over generated
// programs, two Go packs evaluated in process (lint.Evaluate) and
// configurations, and compares every pack rule's status, the findings left
// (pack findings under ns/rule and their severity, L000 for a failed pack
// or rule, L001 for an unknown, reserved or unused directive), each
// directive's status and reason, completeness and policy with the model of
// custom-lint.md's states. facts-unavailable and target-unsupported have no
// generator: the checked program gives every family, and a manifest names
// only the go target. Mutants: lint3-runner-crash-not-l000,
// lint3-runner-off-rule-pack-failed, lint3-runner-unselected-diagnosed,
// lint3-runner-pack-rule-failed-unused, lint3-runner-pack-severity-default.
func TestPackSuppressionAgreesWithModel(t *testing.T) {
	registry, err := lint.NewRegistry(nil, []lint.Preset{{Name: "base"}}, "base")
	if err != nil {
		t.Fatal(err)
	}
	for _, seed := range propertySeeds(250) {
		c := generatePackCase(rand.New(rand.NewPCG(seed, 0x9ac5)))
		src := c.source()
		packs := c.packs(src)
		var selected []sdk.SelectedPack
		for _, ns := range testPackNames {
			manifest, err := packs[ns].Manifest(sdk.Executable{Path: ns + "-lint"})
			if err != nil {
				t.Fatal(err)
			}
			selected = append(selected, sdk.SelectedPack{Manifest: manifest})
		}
		doc, err := lint.ParseConfig([]byte(c.config()))
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		conf, err := registry.ConfigureWith(doc, nil, selected)
		if err != nil {
			t.Fatalf("seed %d: configure %s: %v", seed, c.config(), err)
		}
		conf.UsePackRunner(func(_ context.Context, sc *sdk.Configuration, ns string, snapshot *sdk.Snapshot, _ sdk.RunOptions) (sdk.Report, error) {
			switch {
			case c.outcome[ns] == 1 && c.anyOn(ns):
				return crashed(sc, packs[ns]), nil
			case c.outcome[ns] == 2:
				return sdk.Report{}, errors.New("the runner cannot run it")
			}
			return sdk.Evaluate(packs[ns], sc, snapshot)
		})
		in := lintInput(src)
		if in.Checked != c.checked {
			t.Fatalf("seed %d: the generated source checked=%v, want %v:\n%s", seed, in.Checked, c.checked, src)
		}
		got := outcome(conf.Run(in), c.strict)
		slices.Sort(got.Rules)
		if want := c.model(); !reflect.DeepEqual(got, want) {
			gj, _ := json.MarshalIndent(got, "", "  ")
			wj, _ := json.MarshalIndent(want, "", "  ")
			t.Fatalf("seed %d: runner and model differ\nsource:\n%s\nconfig %s outcomes %v fails %v findings %v strict %v\nrunner: %s\nmodel: %s\nreplay: go test ./internal/sketch/lint -run TestPackSuppressionAgreesWithModel -lint.seed=%d",
				seed, src, c.config(), c.outcome, c.fails, c.findings, c.strict, gj, wj, seed)
		}
	}
}
