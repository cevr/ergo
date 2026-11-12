package lint_test

import (
	"encoding/json"
	"flag"
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lint"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/sketchfront"
)

// seedFlag replays one seed of the lint properties.
var seedFlag = flag.Uint64("lint.seed", 0, "replay one seed of the lint properties")

// propertySeeds are the seeds a property runs: the one -lint.seed names,
// or 1..n.
func propertySeeds(n uint64) []uint64 {
	if *seedFlag != 0 {
		return []uint64{*seedFlag}
	}
	var out []uint64
	for s := uint64(1); s <= n; s++ {
		out = append(out, s)
	}
	return out
}

// lintInput checks one source text in process, as `ergo lint` loads it.
func lintInput(src string) lint.Input {
	a := sketchfront.Check(src)
	root := a.Root()
	unit := lint.Unit{Package: root.Package}
	for _, f := range root.Files {
		unit.Files = append(unit.Files, lint.File{Rel: f.Rel, Syntax: f.Syntax})
	}
	return lint.Input{Checked: len(a.Diagnostics) == 0 && root.Package != nil, Units: []lint.Unit{unit}}
}

// A directive kind of the model: what the generator wrote, so the model
// never reads the directive's text.
type directiveKind int

const (
	dirPlain     directiveKind = iota // a registered rule, well formed
	dirUnknown                        // an unregistered plain name
	dirQualified                      // namespace/rule of no selected pack
	dirMalformed                      // L001, no status
	dirNotOne                         // a comment that is no directive
	dirKindCount
)

type modelDirective struct {
	line int
	kind directiveKind
	rule string
}

// suppressionCase is one generated program, registry behaviour and
// configuration.
type suppressionCase struct {
	lines      []string
	directives []modelDirective
	findings   map[string][]int // rule -> lines it reports, ascending, distinct
	failGamma  int              // 0 completes, 1 panics, 2 reports outside its file
	checked    bool
	config     string
	settings   map[string]string // rule -> what config states: "", a severity, "{}"
	presets    []string
	strict     bool
}

var testRules = []struct {
	name    string
	code    string
	initial lint.Severity
}{{"alpha", "L901", lint.Warning}, {"beta", "L902", lint.Hint}, {"gamma", "L903", lint.Error}}

var severityWords = []string{"off", "hint", "information", "warning", "error"}

func generateSuppressionCase(r *rand.Rand) suppressionCase {
	c := suppressionCase{findings: map[string][]int{}, checked: r.IntN(5) != 0, strict: r.IntN(2) == 0, settings: map[string]string{}}
	n := 3 + r.IntN(10)
	for i := 1; i <= n; i++ {
		if r.IntN(3) == 0 {
			c.lines = append(c.lines, []string{"", "// a note", "// note ergo-lint-disable-next-line alpha -- not at the start"}[r.IntN(3)])
			continue
		}
		kind := directiveKind(r.IntN(int(dirKindCount)))
		rule := testRules[r.IntN(len(testRules))].name
		var text string
		switch kind {
		case dirPlain:
			text = fmt.Sprintf("// ergo-lint-disable-next-line %s -- reason %d", rule, i)
			if r.IntN(3) == 0 {
				text = fmt.Sprintf("//\tergo-lint-disable-next-line\t%s --\tfine", rule)
			}
		case dirUnknown:
			rule = []string{"delta", "lint-runner", "invalid-suppression"}[r.IntN(3)]
			text = "// ergo-lint-disable-next-line " + rule + " -- why"
		case dirQualified:
			rule = []string{"acme/thing", "team-x/no-globals"}[r.IntN(2)]
			text = "// ergo-lint-disable-next-line " + rule + " -- why"
		case dirMalformed:
			text = []string{
				"// ergo-lint-disable-next-line " + rule + " --",
				"// ergo-lint-disable-next-line " + rule + " --   ",
				"// ergo-lint-disable-next-line " + rule + " because",
				"// ergo-lint-disable-next-line " + rule + " beta -- two rules",
				"// ergo-lint-disable-next-line Alpha -- upper case",
				"// ergo-lint-disable-next-line acme/ -- empty rule",
				"// ergo-lint-disable-next-line acme/a/b -- two slashes",
				"// ergo-lint-disable-next-line ergo/thing -- reserved",
				"// ergo-lint-disable-next-line builtin/thing -- reserved",
				"// ergo-lint-disable-next-line",
				"// ergo-lint-disable-next-line " + rule + " --reason",
			}[r.IntN(11)]
			rule = ""
		case dirNotOne:
			text = []string{"// ergo-lint-disable-next-lineX " + rule + " -- glued", "/* ergo-lint-disable-next-line " + rule + " -- block */"}[r.IntN(2)]
			rule = ""
		}
		c.lines = append(c.lines, text)
		c.directives = append(c.directives, modelDirective{line: i, kind: kind, rule: rule})
	}
	total := n + 2 // the lines of func main
	for _, rule := range testRules {
		for line := 1; line <= total; line++ {
			if r.IntN(3) == 0 {
				c.findings[rule.name] = append(c.findings[rule.name], line)
			}
		}
	}
	c.failGamma = []int{0, 0, 0, 1, 2}[r.IntN(5)]
	rules := map[string]any{}
	for _, rule := range testRules {
		switch k := r.IntN(8); {
		case k < len(severityWords):
			rules[rule.name] = severityWords[k]
			c.settings[rule.name] = severityWords[k]
		case k == len(severityWords):
			rules[rule.name] = map[string]any{}
			c.settings[rule.name] = "{}"
		}
	}
	doc, _ := json.Marshal(map[string]any{"version": 1, "rules": rules})
	c.config = string(doc)
	if r.IntN(2) == 0 {
		c.presets = []string{"more"}
	}
	return c
}

func (c suppressionCase) source() string {
	src := strings.Join(c.lines, "\n") + "\nfunc main() {\n}\n"
	if !c.checked {
		src += "func bad() int {\n\t\"x\"\n}\n"
	}
	return src
}

// registry is the test registry: three rules that report where the case
// says, gamma failing as the case says; base turns alpha and beta on, more
// turns gamma on at error.
func (c suppressionCase) registry(t *testing.T) *lint.Registry {
	var rules []*lint.Rule
	for _, tr := range testRules {
		name := tr.name
		rules = append(rules, &lint.Rule{Name: name, Code: tr.code, Default: tr.initial, Check: func(p *lint.Pass) {
			if name == "gamma" && c.failGamma == 1 {
				panic("gamma fails")
			}
			if name == "gamma" && c.failGamma == 2 {
				p.Report(syntax.Pos(len(p.File.Src)+1), syntax.Pos(len(p.File.Src)+2), "outside")
			}
			for _, line := range c.findings[name] {
				at := lineStart(p.File.Src, line)
				p.Report(at, at, "finding of "+name)
			}
		}})
	}
	reg, err := lint.NewRegistry(rules, []lint.Preset{
		{Name: "base", Rules: map[string]lint.Setting{"alpha": {}, "beta": {}}},
		{Name: "more", Rules: map[string]lint.Setting{"gamma": {Severity: lint.Error, HasSeverity: true}, "beta": {}}},
	}, "base")
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func lineStart(src string, line int) syntax.Pos {
	at := 0
	for l := 1; l < line; l++ {
		i := strings.IndexByte(src[at:], '\n')
		if i < 0 {
			return syntax.Pos(len(src))
		}
		at += i + 1
	}
	return syntax.Pos(at)
}

// modelOutcome is what the model and the runner are compared on.
type modelOutcome struct {
	Findings     []string
	Suppressions []string
	Rules        []string
	Complete     bool
	Policy       bool
}

// model is the reference: severities by layer, rule statuses, then each
// directive by the kind the generator wrote.
func (c suppressionCase) model() modelOutcome {
	severity := map[string]lint.Severity{"alpha": lint.Off, "beta": lint.Off, "gamma": lint.Off}
	defaults := map[string]lint.Severity{}
	for _, tr := range testRules {
		defaults[tr.name] = tr.initial
	}
	on := func(rule string) {
		if severity[rule] == lint.Off {
			severity[rule] = defaults[rule]
		}
	}
	on("alpha")
	on("beta")
	if len(c.presets) > 0 {
		severity["gamma"] = lint.Error
		on("beta")
	}
	for _, tr := range testRules {
		switch s := c.settings[tr.name]; s {
		case "":
		case "{}":
			on(tr.name)
		default:
			severity[tr.name] = lint.Severity(slices.Index(severityWords, s))
		}
	}
	var out modelOutcome
	out.Complete = c.checked
	status := map[string]string{}
	type finding struct {
		code, rule string
		sev        lint.Severity
		line       int
	}
	var findings []finding
	for _, tr := range testRules {
		st := "completed"
		switch {
		case severity[tr.name] == lint.Off:
			st = "off"
		case !c.checked:
			st = "not-evaluated"
		case tr.name == "gamma" && c.failGamma != 0:
			st = "failed"
			out.Complete = false
			findings = append(findings, finding{code: "L000", sev: lint.Error})
		default:
			for _, line := range c.findings[tr.name] {
				findings = append(findings, finding{code: tr.code, rule: tr.name, sev: severity[tr.name], line: line})
			}
		}
		status[tr.name] = st
		out.Rules = append(out.Rules, fmt.Sprintf("%s %s %s", tr.name, st, severity[tr.name]))
	}
	removed := map[int]bool{}
	for _, d := range c.directives {
		covered := d.line + 1
		switch d.kind {
		case dirNotOne:
			continue
		case dirMalformed, dirUnknown:
			findings = append(findings, finding{code: "L001", sev: lint.Error, line: d.line})
			continue
		case dirQualified:
			out.Suppressions = append(out.Suppressions, fmt.Sprintf("%s not-evaluated pack-not-selected %d %d", d.rule, d.line, covered))
			continue
		}
		var state string
		switch {
		case !c.checked:
			state = "not-evaluated unchecked-source"
		case status[d.rule] == "off":
			state = "not-evaluated rule-off"
		case status[d.rule] == "failed":
			state = "not-evaluated facts-unavailable"
		default:
			state = "unused"
			for i, f := range findings {
				if f.rule == d.rule && f.line == covered {
					removed[i] = true
					state = "applied"
				}
			}
			if state == "unused" {
				findings = append(findings, finding{code: "L001", sev: lint.Error, line: d.line})
			}
		}
		out.Suppressions = append(out.Suppressions, fmt.Sprintf("%s %s %d %d", d.rule, state, d.line, covered))
	}
	out.Policy = c.checked && out.Complete
	for i, f := range findings {
		if removed[i] {
			continue
		}
		out.Findings = append(out.Findings, fmt.Sprintf("%d %s %s %s", f.line, f.code, f.rule, f.sev))
		if f.sev == lint.Error || (c.strict && f.sev == lint.Warning) {
			out.Policy = false
		}
	}
	slices.Sort(out.Findings)
	return out
}

func outcome(res lint.Result, strict bool) modelOutcome {
	out := modelOutcome{Complete: res.Complete, Policy: res.PolicyPassed(strict)}
	for _, f := range res.Findings {
		out.Findings = append(out.Findings, fmt.Sprintf("%d %s %s %s", f.Line, f.Code, f.Rule, f.Severity))
	}
	slices.Sort(out.Findings)
	for _, s := range res.Suppressions {
		state := s.Status
		if s.Reason != "" {
			state += " " + s.Reason
		}
		out.Suppressions = append(out.Suppressions, fmt.Sprintf("%s %s %d %d", s.Rule, state, s.Line, s.Covered))
	}
	for _, st := range res.Rules {
		out.Rules = append(out.Rules, fmt.Sprintf("%s %s %s", st.Rule, st.Status, st.Severity))
	}
	return out
}

// TestSuppressionAgreesWithModel runs the runner over generated programs,
// rule behaviour and configurations, and compares its findings, rule
// statuses, suppression statuses, completeness and policy with a model
// that knows each directive by the kind the generator wrote.
func TestSuppressionAgreesWithModel(t *testing.T) {
	for _, seed := range propertySeeds(250) {
		c := generateSuppressionCase(rand.New(rand.NewPCG(seed, 0x11a7)))
		doc, err := lint.ParseConfig([]byte(c.config))
		if err != nil {
			t.Fatalf("seed %d: config %s: %v", seed, c.config, err)
		}
		conf, err := c.registry(t).Configure(doc, c.presets)
		if err != nil {
			t.Fatalf("seed %d: configure: %v", seed, err)
		}
		src := c.source()
		in := lintInput(src)
		if in.Checked != c.checked {
			t.Fatalf("seed %d: the generated source checked=%v, want %v:\n%s", seed, in.Checked, c.checked, src)
		}
		got, want := outcome(conf.Run(in), c.strict), c.model()
		if !reflect.DeepEqual(got, want) {
			gj, _ := json.MarshalIndent(got, "", "  ")
			wj, _ := json.MarshalIndent(want, "", "  ")
			t.Fatalf("seed %d: runner and model differ\nsource:\n%s\nconfig %s presets %v strict %v gamma-fails %d findings %v\nrunner: %s\nmodel: %s\nreplay: go test ./internal/sketch/lint -run TestSuppressionAgreesWithModel -lint.seed=%d",
				seed, src, c.config, c.presets, c.strict, c.failGamma, c.findings, gj, wj, seed)
		}
	}
}
