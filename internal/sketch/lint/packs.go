package lint

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	sdk "ergo.local/prototype/lint"
)

// packSet is the rule packs a configuration selects (docs/specs/custom-lint.md):
// their manifests and directories, by namespace, the SDK's configuration of
// their rules, and how one pack runs.
type packSet struct {
	selected []sdk.SelectedPack
	config   *sdk.Configuration
	run      PackRunner
}

// PackRunner runs one selected pack over a snapshot: sdk.Run, a separate
// process. A test substitutes sdk.Evaluate over a Go pack.
type PackRunner func(ctx context.Context, conf *sdk.Configuration, namespace string, snapshot *sdk.Snapshot, options sdk.RunOptions) (sdk.Report, error)

// More not-evaluated reasons, for a pack rule (custom-lint.md, suppression
// states).
const (
	ReasonPackFailed        = "pack-failed"
	ReasonTargetUnsupported = "target-unsupported"
)

// LoadPacks reads the manifest of each pack doc selects, resolved against
// doc.Dir, and of each path in extra (the command line's --rules), resolved
// against the working directory, and names each selection's namespace.
// Loading reads files only: no pack starts. Any unreadable or invalid
// manifest refuses the whole configuration.
func LoadPacks(doc *Config, extra []string) ([]sdk.SelectedPack, error) {
	for _, path := range extra {
		if len(path) == 0 {
			return nil, fmt.Errorf("--rules needs a manifest path")
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		doc.Packs = append(doc.Packs, sdk.PackSelection{Manifest: absolute})
	}
	var packs []sdk.SelectedPack
	for i, selection := range doc.Packs {
		path := selection.Manifest
		if !filepath.IsAbs(path) {
			path = filepath.Join(doc.Dir, path)
		}
		pack, err := sdk.LoadManifest(path)
		if err != nil {
			return nil, err
		}
		doc.Packs[i].Namespace = pack.Manifest.Namespace
		packs = append(packs, pack)
	}
	return packs, nil
}

// configurePacks configures the rules of packs from doc's namespace/name
// presets and rules, then presets'. With no pack, a namespace/name preset
// or rule is refused.
func configurePacks(doc Config, presets []string, packs []sdk.SelectedPack) (*packSet, error) {
	sdkDoc := sdk.Config{Version: sdk.ConfigVersion, Packs: doc.Packs, Rules: map[string]sdk.RuleConfig{}}
	for _, p := range append(slices.Clone(doc.Extends), presets...) {
		if qualified(p) {
			sdkDoc.Extends = append(sdkDoc.Extends, p)
		}
	}
	for name, s := range doc.Rules {
		if !qualified(name) {
			continue
		}
		rc := sdk.RuleConfig{Options: s.Options}
		if s.HasSeverity {
			rc.Severity = sdk.Severity(s.Severity.String())
		}
		sdkDoc.Rules[name] = rc
	}
	if len(packs) == 0 {
		for _, p := range sdkDoc.Extends {
			return nil, fmt.Errorf("preset %s names no selected rule pack", p)
		}
		for name := range sdkDoc.Rules {
			return nil, fmt.Errorf("lint rule %s names no selected rule pack", name)
		}
		return nil, nil
	}
	manifests := make([]sdk.Manifest, 0, len(packs))
	for _, p := range packs {
		manifests = append(manifests, p.Manifest)
	}
	registry, err := sdk.NewRegistry(manifests...)
	if err != nil {
		return nil, err
	}
	conf, problems := registry.Configure(sdkDoc)
	if len(problems) > 0 {
		var messages []string
		for _, p := range problems {
			messages = append(messages, p.Message)
		}
		return nil, fmt.Errorf("%s", strings.Join(messages, "; "))
	}
	selected := slices.Clone(packs)
	slices.SortFunc(selected, func(a, b sdk.SelectedPack) int { return strings.Compare(a.Manifest.Namespace, b.Manifest.Namespace) })
	return &packSet{selected: selected, config: conf, run: sdk.Run}, nil
}

// severityOf is the internal severity of an SDK severity word.
func severityOf(s sdk.Severity) Severity {
	severity, _ := parseSeverity(string(s))
	return severity
}

// packStatuses are the pack rules' statuses before any run.
func (p *packSet) statuses() []RuleStatus {
	var out []RuleStatus
	for _, setting := range p.config.Settings() {
		out = append(out, RuleStatus{Rule: setting.Rule, Code: setting.Rule, Severity: severityOf(setting.Severity).String()})
	}
	return out
}

// selects reports whether namespace is a selected pack's.
func (p *packSet) selects(namespace string) bool {
	if p == nil {
		return false
	}
	_, ok := p.manifest(namespace)
	return ok
}

// manifest is the selected manifest of namespace.
func (p *packSet) manifest(namespace string) (sdk.Manifest, bool) {
	for _, s := range p.selected {
		if s.Manifest.Namespace == namespace {
			return s.Manifest, true
		}
	}
	return sdk.Manifest{}, false
}

// runPacks runs every selected pack over the program, in parallel over one
// fact snapshot that holds the families their enabled rules require, and
// projects the reports: each pack finding under its namespace/rule and
// configured severity, each rule's status, and one L000 error for a pack
// that failed and for each rule that failed or did not run where it
// applies. On unchecked source no pack starts and every enabled pack rule
// is not-evaluated: unchecked-source.
func (c *Configuration) runPacks(in Input) (findings []Finding, statuses []RuleStatus, complete bool) {
	complete = true
	p := c.packs
	if p == nil {
		return nil, nil, true
	}
	settings := map[string]sdk.RuleSetting{}
	for _, s := range p.config.Settings() {
		settings[s.Rule] = s
	}
	if !in.Checked {
		for _, s := range p.config.Settings() {
			st := RuleStatus{Rule: s.Rule, Code: s.Rule, Severity: severityOf(s.Severity).String(), Status: StatusOff}
			if s.Severity != sdk.SeverityOff {
				st.Status, st.Reason = StatusNotEvaluated, ReasonUnchecked
			}
			statuses = append(statuses, st)
		}
		return nil, statuses, true
	}
	var families []sdk.Family
	for _, pack := range p.selected {
		for _, rule := range pack.Manifest.Rules {
			if settings[pack.Manifest.Namespace+"/"+rule.Name].Severity == sdk.SeverityOff {
				continue
			}
			for _, f := range rule.Requires {
				if !slices.Contains(families, f) {
					families = append(families, f)
				}
			}
		}
	}
	snapshot := Facts(in, families)
	host := sdk.CaptureHost()
	reports := make([]sdk.Report, len(p.selected))
	errs := make([]error, len(p.selected))
	var wg sync.WaitGroup
	for i, pack := range p.selected {
		ns := pack.Manifest.Namespace
		options := sdk.RunOptions{Dir: pack.Dir, Env: host.Select(p.config.Environment(ns))}
		wg.Add(1)
		go func() {
			defer wg.Done()
			reports[i], errs[i] = p.run(context.Background(), p.config, ns, snapshot, options)
		}()
	}
	wg.Wait()
	runner := func(message string) {
		complete = false
		findings = append(findings, Finding{Code: CodeRunner, Severity: Error, Message: "lint-runner: " + message})
	}
	for i, pack := range p.selected {
		ns, identity := pack.Manifest.Namespace, pack.Manifest.Identity()
		report := reports[i]
		if errs[i] != nil {
			runner(fmt.Sprintf("rule pack %s (manifest %s) did not run: %v", ns, identity, errs[i]))
			for _, rule := range pack.Manifest.Rules {
				id := ns + "/" + rule.Name
				st := RuleStatus{Rule: id, Code: id, Severity: severityOf(settings[id].Severity).String(), Status: StatusFailed, Reason: ReasonPackFailed}
				if settings[id].Severity == sdk.SeverityOff {
					st.Status, st.Reason = StatusOff, ""
				}
				statuses = append(statuses, st)
			}
			continue
		}
		if f := report.Failure; f != nil {
			message := fmt.Sprintf("rule pack %s (manifest %s) failed: %s: %s", ns, identity, f.Code, f.Message)
			if len(f.Exit) > 0 {
				message += " (" + f.Exit + ")"
			}
			runner(message)
		}
		for _, st := range report.Rules {
			out := RuleStatus{Rule: st.Rule, Code: st.Rule, Severity: severityOf(settings[st.Rule].Severity).String(), Reason: st.Reason}
			switch st.Status {
			case sdk.StatusCompleted:
				out.Status, out.Reason = StatusCompleted, ""
			case sdk.StatusOff:
				out.Status = StatusOff
			case sdk.StatusFailed:
				out.Status = StatusFailed
				if report.Failure == nil {
					runner(fmt.Sprintf("rule %s of pack %s (manifest %s) failed: %s", st.Rule, ns, identity, st.Reason))
				}
			case sdk.StatusSkipped:
				out.Status = StatusNotEvaluated
				if !st.Inapplicable {
					runner(fmt.Sprintf("rule %s of pack %s (manifest %s) did not run: %s", st.Rule, ns, identity, st.Reason))
				}
			}
			statuses = append(statuses, out)
		}
		for _, f := range report.Findings {
			findings = append(findings, Finding{
				Code: f.Rule, Rule: f.Rule, Severity: severityOf(f.Severity), Message: f.Message,
				File: f.Span.File, Offset: f.Span.Offset, Length: f.Span.Length, Line: f.Span.Line, Column: f.Span.Column,
				Related: f.Related, Suggestions: f.Suggestions,
			})
		}
		complete = complete && report.Complete
	}
	return findings, statuses, complete
}

// notEvaluatedReason is the suppression reason of a pack rule that did not
// complete: its status's reason without the detail after the colon.
func notEvaluatedReason(st *RuleStatus) string {
	if st.Status == StatusFailed {
		return ReasonPackFailed
	}
	reason, _, _ := strings.Cut(st.Reason, ":")
	return reason
}

// Inspect lists every rule with its effective severity: the built-in
// rules, then each selected pack's rules with their metadata. It reads
// metadata only; no pack starts.
func (c *Configuration) Inspect() []sdk.RuleInfo {
	var out []sdk.RuleInfo
	for _, rule := range c.registry.rules {
		out = append(out, sdk.RuleInfo{
			Rule: rule.Name, Builtin: true, Code: rule.Code, Description: rule.Doc,
			DefaultSeverity: sdk.Severity(rule.Default.String()), Severity: sdk.Severity(c.severity[rule.Name].String()),
		})
	}
	if c.packs != nil {
		out = append(out, c.packs.config.Inspect()...)
	}
	return out
}

// PacksSelected reports whether the configuration selects a rule pack.
func (c *Configuration) PacksSelected() bool { return c.packs != nil }

// UsePackRunner replaces how the configuration's packs run (sdk.Run, a
// process each). A test runs Go packs in process through sdk.Evaluate.
func (c *Configuration) UsePackRunner(run PackRunner) {
	if c.packs != nil {
		c.packs.run = run
	}
}
