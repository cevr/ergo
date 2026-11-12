package lint

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"runtime"
	"slices"
	"strings"
)

// Registry is the set of pack rules a configuration may name: the
// manifests of explicitly selected packs. It holds metadata only. Ergo's
// built-in rules have their own registry (internal/sketch/lint), which
// routes every namespace/rule name here.
type Registry struct {
	packs []Manifest
}

// NewRegistry validates the selected manifests together: a duplicate or
// reserved namespace is refused before any configuration is considered.
func NewRegistry(packs ...Manifest) (*Registry, error) {
	registry := &Registry{}
	namespaces := map[string]bool{}
	for _, manifest := range packs {
		if err := manifest.Validate(); err != nil {
			return nil, err
		}
		if namespaces[manifest.Namespace] {
			return nil, fmt.Errorf("duplicate rule pack namespace %s", manifest.Namespace)
		}
		namespaces[manifest.Namespace] = true
		registry.packs = append(registry.packs, manifest)
	}
	slices.SortFunc(registry.packs, func(a, b Manifest) int { return strings.Compare(a.Namespace, b.Namespace) })
	return registry, nil
}

// Manifests are the selected manifests, by namespace.
func (r *Registry) Manifests() []Manifest { return slices.Clone(r.packs) }

// Pack returns the selected manifest for namespace.
func (r *Registry) Pack(namespace string) (Manifest, bool) {
	for _, manifest := range r.packs {
		if manifest.Namespace == namespace {
			return manifest, true
		}
	}
	return Manifest{}, false
}

// ConfigVersion is the lint configuration format version.
const ConfigVersion = 1

// MaxConfigBytes bounds one configuration document.
const MaxConfigBytes = 1 << 20

// Config is a lint configuration document. Packs selects rule packs by
// manifest path; LoadConfig resolves those paths against the document's
// directory. Extends applies presets of selected packs, named
// namespace/preset, in order, and Rules applies last. Rule keys are
// built-in names or namespace/rule identities of selected packs.
type Config struct {
	Version int                   `json:"version"`
	Packs   []PackSelection       `json:"packs,omitempty"`
	Extends []string              `json:"extends,omitempty"`
	Rules   map[string]RuleConfig `json:"rules,omitempty"`
}

// PackSelection selects one rule pack by the path of its manifest. Env
// names the host variables the pack's process receives; it replaces any
// selection of the pack's extended presets, and the default is none.
// Namespace is the selected manifest's namespace, which LoadConfig sets
// once it has loaded the manifest.
type PackSelection struct {
	Manifest  string       `json:"manifest"`
	Env       EnvSelection `json:"env,omitzero"`
	Namespace string       `json:"-"`
}

// overlay applies a later setting over an earlier one: a severity or
// options the later setting states replaces the earlier value, and what it
// omits is kept.
func (c RuleConfig) overlay(later RuleConfig) RuleConfig {
	if later.Severity != "" {
		c.Severity = later.Severity
	}
	if later.Options != nil {
		c.Options = later.Options
	}
	return c
}

// RuleConfig selects a severity and options. In JSON, a bare severity
// string is shorthand for {"severity": ...}. An omitted severity selects the
// rule's default; an explicit null or empty severity is refused.
type RuleConfig struct {
	Severity Severity        `json:"severity,omitempty"`
	Options  json.RawMessage `json:"options,omitempty"`
}

func (c *RuleConfig) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		return fmt.Errorf("a rule setting must be a severity or an object")
	}
	// Both spellings decode into a fresh value that replaces the receiver
	// only once valid: nothing of a previously decoded setting survives.
	var fresh RuleConfig
	var severity string
	if err := json.Unmarshal(data, &severity); err == nil {
		if err := fresh.setSeverity(severity); err != nil {
			return err
		}
		*c = fresh
		return nil
	}
	var value struct {
		Severity json.RawMessage `json:"severity"`
		Options  json.RawMessage `json:"options"`
	}
	if err := decodeStrict(data, &value); err != nil {
		return err
	}
	fresh.Options = value.Options
	if value.Severity != nil {
		if isNull(value.Severity) || json.Unmarshal(value.Severity, &severity) != nil {
			return fmt.Errorf("a rule severity must be a string")
		}
		if err := fresh.setSeverity(severity); err != nil {
			return err
		}
	}
	*c = fresh
	return nil
}

func (c *RuleConfig) setSeverity(severity string) error {
	if severity == "" {
		return fmt.Errorf("a rule severity must not be empty; omit it to select the default")
	}
	c.Severity = Severity(severity)
	return nil
}

// ParseConfig strictly decodes a configuration document. Semantic
// validation against a registry is Registry.Configure.
func ParseConfig(data []byte) (Config, error) {
	if len(data) > MaxConfigBytes {
		return Config{}, fmt.Errorf("lint configuration exceeds %d bytes", MaxConfigBytes)
	}
	var config Config
	if err := decodeStrict(data, &config); err != nil {
		return Config{}, fmt.Errorf("invalid lint configuration: %w", err)
	}
	return config, nil
}

// Problem codes reported by Configure.
const (
	ProblemVersion          = "unsupported-version"
	ProblemUnknownNamespace = "unknown-namespace"
	ProblemUnknownRule      = "unknown-rule"
	ProblemInvalidSeverity  = "invalid-severity"
	ProblemInvalidOptions   = "invalid-options"
	ProblemUnknownPreset    = "unknown-preset"
	// ProblemInvalidEnvironment names the pack and variable name only,
	// never a host value.
	ProblemInvalidEnvironment = "invalid-environment"
)

// Problem is one configuration refusal. Any problem refuses the whole
// configuration: invalid configuration must never start a pack.
type Problem struct {
	Code    string `json:"code"`
	Rule    string `json:"rule,omitempty"`
	Message string `json:"message"`
}

// RuleSetting is the effective, validated setting of one rule. Configured
// is set when a preset or the configuration's own rules state the rule.
// Enforced is set for an enabled rule that declares targets and that the
// configuration's own rules state: under a target the rule does not
// support, it is skipped and the analysis is incomplete. A target-limited
// rule only its pack's default or a preset enables does not apply to such a
// target instead.
type RuleSetting struct {
	Rule            string   `json:"rule"`
	Pack            string   `json:"pack,omitempty"`
	DefaultSeverity Severity `json:"defaultSeverity"`
	Severity        Severity `json:"severity"`
	Configured      bool     `json:"configured,omitempty"`
	Enforced        bool     `json:"enforced,omitempty"`
	Options         Options  `json:"-"`
}

// Configuration is a registry plus validated settings for every rule and
// the host variables each pack selects.
type Configuration struct {
	registry    *Registry
	settings    []RuleSetting
	environment map[string][]string
}

// Configure validates config against the registry. Every rule receives an
// effective setting: the extended presets in order, then config's own
// rules, each overlaying what it states. Unconfigured rules use their
// default severity and default options. A target-limited rule that
// config's own rules state and enable is enforced (RuleSetting.Enforced).
// Each pack's environment selection is the last one stated by its extended
// presets and then its Config.Packs entry; none by default. The registry already holds the selected
// manifests: a Config.Packs entry only contributes its environment, which
// needs the namespace LoadConfig resolved. A non-empty problem list means
// no Configuration.
func (r *Registry) Configure(config Config) (*Configuration, []Problem) {
	var problems []Problem
	if config.Version != ConfigVersion {
		problems = append(problems, Problem{Code: ProblemVersion, Message: fmt.Sprintf("lint configuration version must be %d", ConfigVersion)})
	}
	configured, environment, extendProblems := r.extend(config.Extends)
	problems = append(problems, extendProblems...)
	for _, selected := range config.Packs {
		if !selected.Env.Set {
			continue
		}
		if _, ok := r.Pack(selected.Namespace); !ok {
			problems = append(problems, Problem{Code: ProblemInvalidEnvironment, Message: "the environment of pack selection " + selected.Manifest + " applies to no loaded rule pack"})
			continue
		}
		if err := selected.Env.validate(); err != nil {
			problems = append(problems, Problem{Code: ProblemInvalidEnvironment, Message: "rule pack " + selected.Namespace + ": " + err.Error()})
			continue
		}
		environment[selected.Namespace] = selected.Env
	}
	keys := make([]string, 0, len(config.Rules))
	for key := range config.Rules {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	explicit := map[string]bool{}
	for _, key := range keys {
		if problem, ok := r.admitRuleKey(key); !ok {
			problems = append(problems, problem)
			continue
		}
		configured[key] = configured[key].overlay(config.Rules[key])
		explicit[key] = true
	}
	configuration := &Configuration{registry: r, environment: map[string][]string{}}
	for namespace, selection := range environment {
		names := slices.Clone(selection.Names)
		slices.SortFunc(names, func(a, b string) int {
			return strings.Compare(canonicalName(runtime.GOOS, a), canonicalName(runtime.GOOS, b))
		})
		configuration.environment[namespace] = names
	}
	settle := func(setting RuleSetting, specs []OptionSpec, targeted bool) {
		options := json.RawMessage(nil)
		if rule, ok := configured[setting.Rule]; ok {
			setting.Configured = true
			options = rule.Options
			if rule.Severity != "" {
				if rule.Severity != SeverityOff && !rule.Severity.valid() {
					problems = append(problems, Problem{Code: ProblemInvalidSeverity, Rule: setting.Rule, Message: fmt.Sprintf("invalid severity %q for %s; use off, error, warning, information or hint", rule.Severity, setting.Rule)})
				} else {
					setting.Severity = rule.Severity
				}
			}
		}
		setting.Enforced = targeted && explicit[setting.Rule] && setting.Severity != SeverityOff
		// A disabled rule without options needs no required values; options
		// given to a disabled rule are still validated.
		if setting.Severity == SeverityOff && len(options) == 0 {
			configuration.settings = append(configuration.settings, setting)
			return
		}
		values, err := ValidateOptions(specs, options)
		if err != nil {
			problems = append(problems, Problem{Code: ProblemInvalidOptions, Rule: setting.Rule, Message: setting.Rule + ": " + err.Error()})
		}
		setting.Options = values
		configuration.settings = append(configuration.settings, setting)
	}
	for _, manifest := range r.packs {
		for _, rule := range manifest.Rules {
			id := manifest.Namespace + "/" + rule.Name
			settle(RuleSetting{Rule: id, Pack: manifest.Namespace, DefaultSeverity: rule.DefaultSeverity, Severity: rule.DefaultSeverity}, rule.Options, len(rule.Targets) > 0)
		}
	}
	if len(problems) > 0 {
		return nil, problems
	}
	slices.SortFunc(configuration.settings, func(a, b RuleSetting) int { return strings.Compare(a.Rule, b.Rule) })
	return configuration, nil
}

// extend merges the named presets in order. A preset's rules and
// environment belong to its own pack, which manifest validation has already
// checked.
func (r *Registry) extend(presets []string) (map[string]RuleConfig, map[string]EnvSelection, []Problem) {
	merged := map[string]RuleConfig{}
	environment := map[string]EnvSelection{}
	var problems []Problem
	for i, reference := range presets {
		namespace, name, qualified := strings.Cut(reference, "/")
		manifest, selected := r.Pack(namespace)
		preset, found := manifest.Preset(name)
		switch {
		case !qualified || !selected:
			problems = append(problems, Problem{Code: ProblemUnknownNamespace, Rule: reference, Message: "preset " + reference + " does not name a preset of a selected rule pack"})
			continue
		case !found:
			problems = append(problems, Problem{Code: ProblemUnknownPreset, Rule: reference, Message: "rule pack " + namespace + " has no preset " + name})
			continue
		case slices.Contains(presets[:i], reference):
			problems = append(problems, Problem{Code: ProblemUnknownPreset, Rule: reference, Message: "preset " + reference + " is extended twice"})
			continue
		}
		if preset.Env.Set {
			environment[namespace] = preset.Env
		}
		rules := make([]string, 0, len(preset.Rules))
		for rule := range preset.Rules {
			rules = append(rules, rule)
		}
		slices.Sort(rules)
		for _, rule := range rules {
			key := namespace + "/" + rule
			merged[key] = merged[key].overlay(preset.Rules[rule])
		}
	}
	return merged, environment, problems
}

func (r *Registry) admitRuleKey(key string) (Problem, bool) {
	namespace, name, qualified := strings.Cut(key, "/")
	if !qualified {
		return Problem{Code: ProblemUnknownRule, Rule: key, Message: "lint rule " + key + " is not a namespace/rule of a rule pack"}, false
	}
	manifest, ok := r.Pack(namespace)
	if !ok {
		return Problem{Code: ProblemUnknownNamespace, Rule: key, Message: "lint rule namespace " + namespace + " is not a selected rule pack"}, false
	}
	if _, ok := manifest.Rule(name); !ok {
		return Problem{Code: ProblemUnknownRule, Rule: key, Message: "rule pack " + namespace + " has no rule " + name}, false
	}
	return Problem{}, true
}

// Settings lists every effective rule setting ordered by rule identity.
func (c *Configuration) Settings() []RuleSetting { return slices.Clone(c.settings) }

// Setting returns the effective setting for a rule identity.
func (c *Configuration) Setting(rule string) (RuleSetting, bool) {
	index := slices.IndexFunc(c.settings, func(setting RuleSetting) bool { return setting.Rule == rule })
	if index < 0 {
		return RuleSetting{}, false
	}
	return c.settings[index], true
}

// Environment lists the host variable names a pack selects, in canonical
// order; empty when it selects none.
func (c *Configuration) Environment(namespace string) []string {
	return slices.Clone(c.environment[namespace])
}

// Identity digests the effective settings: severities and options after
// defaults, whether a target-limited rule is enforced, and each pack's
// selected variable names (never values). It is independent of pack
// identity, so the same configuration text over a changed pack keeps its
// configuration identity.
func (c *Configuration) Identity() string {
	type entry struct {
		Rule     string          `json:"rule"`
		Severity Severity        `json:"severity"`
		Options  json.RawMessage `json:"options"`
		Enforced bool            `json:"enforced,omitempty"`
	}
	entries := make([]entry, 0, len(c.settings))
	for _, setting := range c.settings {
		entries = append(entries, entry{setting.Rule, setting.Severity, setting.Options.canonical(), setting.Enforced})
	}
	identity := map[string]any{"configVersion": ConfigVersion, "rules": entries}
	environment := map[string][]string{}
	for namespace, names := range c.environment {
		if len(names) > 0 {
			environment[namespace] = names
		}
	}
	if len(environment) > 0 {
		identity["environment"] = environment
	}
	return digest(identity)
}

// RuleInfo describes one rule for inspection (`ergo lint rules`). Enforced
// is the effective setting's (RuleSetting.Enforced). Producing it reads
// metadata only; no pack code runs. Builtin and Code describe a built-in
// rule, which the host lists beside the pack rules.
type RuleInfo struct {
	Rule            string       `json:"rule"`
	Builtin         bool         `json:"builtin,omitempty"`
	Code            string       `json:"code,omitempty"`
	Pack            string       `json:"pack,omitempty"`
	PackVersion     string       `json:"packVersion,omitempty"`
	PackIdentity    string       `json:"packIdentity,omitempty"`
	Version         string       `json:"version,omitempty"`
	Description     string       `json:"description"`
	DefaultSeverity Severity     `json:"defaultSeverity"`
	Severity        Severity     `json:"severity"`
	Requires        []Family     `json:"requires,omitempty"`
	Targets         []string     `json:"targets,omitempty"`
	Enforced        bool         `json:"enforced,omitempty"`
	FactVersions    []int        `json:"factVersions,omitempty"`
	Options         []OptionSpec `json:"options,omitempty"`
	// Environment names the host variables the rule's pack receives.
	Environment []string `json:"environment,omitempty"`
}

// Inspect lists every rule with its effective severity.
func (c *Configuration) Inspect() []RuleInfo {
	var rules []RuleInfo
	for _, manifest := range c.registry.packs {
		identity := manifest.Identity()
		for _, rule := range manifest.Rules {
			id := manifest.Namespace + "/" + rule.Name
			setting, _ := c.Setting(id)
			rules = append(rules, RuleInfo{Rule: id, Pack: manifest.Namespace, PackVersion: manifest.Version, PackIdentity: identity, Version: rule.Version, Description: rule.Description, DefaultSeverity: rule.DefaultSeverity, Severity: setting.Severity, Requires: slices.Clone(rule.Requires), Targets: slices.Clone(rule.Targets), Enforced: setting.Enforced, FactVersions: slices.Clone(manifest.FactSchema.Versions), Options: slices.Clone(rule.Options), Environment: c.Environment(manifest.Namespace)})
		}
	}
	slices.SortFunc(rules, func(a, b RuleInfo) int { return strings.Compare(a.Rule, b.Rule) })
	return rules
}

// AnalysisIdentity qualifies one lint analysis by every input a pack
// receives. It combines the semantic snapshot and producer with the fact
// schema, every selected pack identity and the configuration identity, the
// digest of the snapshot exactly as a pack receives it, plus, for one
// pack's report, the request that pack was sent and, when a pack process
// produced the report, that process's execution identity. ReuseScope is
// the semantic snapshot's scope, or "none" when the snapshot or the
// execution is unqualified.
type AnalysisIdentity struct {
	Digest        string   `json:"digest"`
	ReuseScope    string   `json:"reuseScope"`
	Semantic      Semantic `json:"semantic"`
	FactSchema    Schema   `json:"factSchema"`
	Packs         []string `json:"packs"`
	Configuration string   `json:"configuration"`
	// Snapshot digests the snapshot's encoded form: the document URI and byte
	// count and every fact family, never the host-only source text.
	Snapshot string `json:"snapshot"`
	// Request digests the request of a pack's report without its id: the
	// pack namespace, version and manifest identity it names, the admitted
	// rules with their effective options, and the snapshot. Two packs
	// sharing one executable are two analyses. Configuration.Analysis,
	// which evaluates no single pack, has none.
	Request   string             `json:"request,omitempty"`
	Execution *ExecutionIdentity `json:"execution,omitempty"`
	InProcess bool               `json:"inProcess,omitempty"`
}

// ExecutionIdentity qualifies the process behind a report. Program is the
// absolute path the runner started, which the process sees as its first
// argument, and Dir its working directory. Both are display text: JSON
// replaces bytes that are not UTF-8, which a Unix path may hold, so Paths
// digests their exact bytes, length-framed. Executable is the content
// digest of that program file, read just before the runner started it.
// Complete is false when that content cannot stand
// for the pack's implementation: a script (a file starting with #!) runs an
// interpreter over code outside it, and manifest arguments can name further
// code. The libraries a native executable loads are outside it either way.
// Environment digests the exact process environment under the environment
// policy and platform: every variable's name and value bytes, an empty
// value distinct from an absent one. Variables lists those names; Ergo
// reports names and the digest, never values.
type ExecutionIdentity struct {
	Program     string   `json:"program"`
	Dir         string   `json:"dir"`
	Paths       string   `json:"paths"`
	Executable  string   `json:"executable"`
	Complete    bool     `json:"complete"`
	Environment string   `json:"environment"`
	Variables   []string `json:"variables"`
}

// requested qualifies the analysis with the request of one pack's report,
// whose id is not yet bound.
func (a AnalysisIdentity) requested(request Request) AnalysisIdentity {
	request.ID = ""
	a.Request = digest(request)
	a.Digest = ""
	a.Digest = digest(a)
	return a
}

// executed qualifies the analysis with the execution that produced it.
func (a AnalysisIdentity) executed(execution ExecutionIdentity) AnalysisIdentity {
	a.Execution = &execution
	if !execution.Complete {
		a.ReuseScope = "none"
	}
	a.Digest = ""
	a.Digest = digest(a)
	return a
}

// inProcess marks an analysis of the in-process authoring harness: no
// process, executable or environment qualifies it, so it is never reusable.
func (a AnalysisIdentity) inProcess() AnalysisIdentity {
	a.InProcess, a.ReuseScope = true, "none"
	a.Digest = ""
	a.Digest = digest(a)
	return a
}

// Analysis computes the analysis identity of evaluating this configuration
// over snapshot.
func (c *Configuration) Analysis(snapshot *Snapshot) AnalysisIdentity {
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		panic(err)
	}
	return c.analysis(snapshot, encoded)
}

// analysis is Analysis over the snapshot's encoded form, which the runner has
// already encoded for the request.
func (c *Configuration) analysis(snapshot *Snapshot, encoded []byte) AnalysisIdentity {
	sum := sha256.Sum256(encoded)
	identity := AnalysisIdentity{Semantic: snapshot.Semantic, FactSchema: snapshot.Schema, Packs: []string{}, Configuration: c.Identity(), Snapshot: "sha256:" + hex.EncodeToString(sum[:])}
	for _, manifest := range c.registry.packs {
		identity.Packs = append(identity.Packs, manifest.Namespace+"@"+manifest.Identity())
	}
	identity.ReuseScope = snapshot.Semantic.ReuseScope
	if snapshot.Semantic.Producer == "" || identity.ReuseScope == "" {
		identity.ReuseScope = "none"
	}
	identity.Digest = digest(identity)
	return identity
}

func digest(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// framedDigest commits to the exact bytes of each field. Length-prefixed
// framing keeps it byte-exact where JSON text cannot: no encoding can merge
// two values, and an empty field differs from an absent one.
func framedDigest(fields ...string) string {
	hash := sha256.New()
	for _, field := range fields {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(field)))
		hash.Write(length[:])
		hash.Write([]byte(field))
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

// Rule statuses. Only completed establishes that a rule found nothing.
const (
	StatusCompleted = "completed"
	StatusSkipped   = "skipped"
	StatusFailed    = "failed"
	StatusOff       = "off"
)

// RuleStatus is the execution status of one selected rule. Inapplicable
// marks a rule skipped under a target it does not support that is not
// enforced (RuleSetting.Enforced): it does not apply to this analysis, so
// its skip leaves the report complete.
type RuleStatus struct {
	Rule         string `json:"rule"`
	Status       string `json:"status"`
	Reason       string `json:"reason,omitempty"`
	Inapplicable bool   `json:"inapplicable,omitempty"`
	Findings     int    `json:"findings"`
}

// ReportedFinding is a finding projected with its rule identity and
// configured severity. Suggestions are previews bound to the report's
// semantic revision.
type ReportedFinding struct {
	Rule        string       `json:"rule"`
	Severity    Severity     `json:"severity"`
	Message     string       `json:"message"`
	Span        Span         `json:"span"`
	Related     []Related    `json:"related,omitempty"`
	Suggestions []Suggestion `json:"suggestions,omitempty"`
}

// Report is the evaluation result of one pack. Complete is false when any
// enabled rule failed or was skipped, unless the skip is inapplicable.
// Failure is set when the pack as a whole failed (its process crashed, hung
// or answered invalidly); every rule it was asked to run is then failed and
// none of its findings is reported.
type Report struct {
	Analysis AnalysisIdentity  `json:"analysis"`
	Complete bool              `json:"complete"`
	Failure  *ExecutionFailure `json:"failure,omitempty"`
	Rules    []RuleStatus      `json:"rules"`
	Findings []ReportedFinding `json:"findings"`
}

// Evaluate runs a Go pack in-process over snapshot under configuration,
// which must have been built from a registry containing the pack's
// manifest. It is the authoring/test harness for Run: the same admission,
// request, rule execution and response validation under the default
// limits, including the size of the response a pack process would write,
// without a process or framing. Each rule reads its own copy decoded from
// the snapshot's encoded form, exactly as in a pack process.
func Evaluate(pack *Pack, configuration *Configuration, snapshot *Snapshot) (Report, error) {
	work, err := prepare(configuration, pack.Namespace, snapshot)
	if err != nil {
		return Report{}, err
	}
	work.bind(nil)
	if len(work.request.Rules) > 0 {
		response := respond(pack, work.request)
		// The size Serve would frame: the same encoding of the same value.
		encoded, err := json.Marshal(response)
		if err != nil {
			return Report{}, fmt.Errorf("encode response: %w", err)
		}
		if failure := work.accept(response, len(encoded), DefaultLimits()); failure != nil {
			work.fail(failure)
		}
	}
	return work.finish(), nil
}

// admit returns the status refusing a rule over snapshot, or a zero status
// when the rule may run. An incompatible fact schema is a failure: the pack
// cannot read the request at all. A target the rule does not support or an
// unavailable family is a skip. The target skip of a rule the setting does
// not enforce is inapplicable: only a pack default or preset enabled the
// rule, and it does not apply to this target.
func admit(manifest Manifest, rule ManifestRule, setting RuleSetting, snapshot *Snapshot) RuleStatus {
	if snapshot.Schema.Name != FactSchemaName || !slices.Contains(manifest.FactSchema.Versions, snapshot.Schema.Version) {
		return RuleStatus{Status: StatusFailed, Reason: fmt.Sprintf("unsupported fact schema %s/%d", snapshot.Schema.Name, snapshot.Schema.Version)}
	}
	if len(rule.Targets) > 0 && !slices.Contains(rule.Targets, snapshot.Semantic.Target) {
		return RuleStatus{Status: StatusSkipped, Reason: "target-unsupported: " + snapshot.Semantic.Target, Inapplicable: !setting.Enforced}
	}
	for _, family := range rule.Requires {
		if reason := snapshot.UnavailableReason(family); reason != "" {
			return RuleStatus{Status: StatusSkipped, Reason: "facts-unavailable: " + string(family) + " (" + reason + ")"}
		}
	}
	return RuleStatus{}
}

func compareFindings(a, b ReportedFinding) int {
	if order := strings.Compare(a.Span.File, b.Span.File); order != 0 {
		return order
	}
	if a.Span.Offset != b.Span.Offset {
		return a.Span.Offset - b.Span.Offset
	}
	if a.Span.Length != b.Span.Length {
		return a.Span.Length - b.Span.Length
	}
	if order := strings.Compare(a.Rule, b.Rule); order != 0 {
		return order
	}
	return strings.Compare(a.Message, b.Message)
}
