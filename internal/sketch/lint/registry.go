package lint

import (
	"fmt"
	"slices"
	"strings"
)

// Preset is a named set of rule settings. A built-in preset names built-in
// rules; a pack's preset (LINT3) is named namespace/preset and names its
// own rules.
type Preset struct {
	Name  string
	Doc   string
	Rules map[string]Setting
}

// Registry is the set of rules and presets a configuration may name. It
// holds metadata only; nothing runs until Configuration.Run. Base is the
// preset every configuration starts from.
type Registry struct {
	rules   []*Rule
	byName  map[string]*Rule
	presets map[string]Preset
	base    string
}

// NewRegistry validates rules and presets together: names are kebab-case
// identifiers, unique, never a reserved runner name; codes are unique and
// never the runner's; every preset names known rules; base is a preset.
func NewRegistry(rules []*Rule, presets []Preset, base string) (*Registry, error) {
	r := &Registry{byName: map[string]*Rule{}, presets: map[string]Preset{}, base: base}
	codes := map[string]bool{CodeRunner: true, CodeSuppression: true}
	for _, rule := range rules {
		switch {
		case !validName(rule.Name) || reservedName(rule.Name):
			return nil, fmt.Errorf("invalid rule name %q", rule.Name)
		case r.byName[rule.Name] != nil:
			return nil, fmt.Errorf("duplicate rule %s", rule.Name)
		case rule.Code == "" || codes[rule.Code]:
			return nil, fmt.Errorf("rule %s: missing or duplicate code %q", rule.Name, rule.Code)
		case rule.Default == Off || rule.Default > Error:
			return nil, fmt.Errorf("rule %s: invalid default severity", rule.Name)
		case rule.Check == nil:
			return nil, fmt.Errorf("rule %s has no check", rule.Name)
		}
		codes[rule.Code] = true
		r.byName[rule.Name] = rule
		r.rules = append(r.rules, rule)
	}
	slices.SortFunc(r.rules, func(a, b *Rule) int { return strings.Compare(a.Name, b.Name) })
	for _, p := range presets {
		if _, dup := r.presets[p.Name]; !validName(p.Name) || dup {
			return nil, fmt.Errorf("invalid or duplicate preset %q", p.Name)
		}
		for name, s := range p.Rules {
			if err := r.admit(name, s); err != nil {
				return nil, fmt.Errorf("preset %s: %w", p.Name, err)
			}
		}
		r.presets[p.Name] = p
	}
	if _, ok := r.presets[base]; !ok {
		return nil, fmt.Errorf("base preset %q is not registered", base)
	}
	return r, nil
}

// Rules are the registered rules, by name.
func (r *Registry) Rules() []*Rule { return slices.Clone(r.rules) }

// admit checks one setting against the registry.
func (r *Registry) admit(name string, s Setting) error {
	if reservedName(name) {
		return fmt.Errorf("%s is the runner's and has a fixed severity", name)
	}
	if r.byName[name] == nil {
		return fmt.Errorf("unknown rule %s", name)
	}
	if len(s.Options) > 0 {
		return fmt.Errorf("rule %s takes no options", name)
	}
	return nil
}

// Configuration is a registry with every rule's effective severity.
type Configuration struct {
	registry *Registry
	severity map[string]Severity
}

// Configure layers the settings: the base preset, then the document's
// extends in order, then presets (the command line's --preset) in order,
// then the document's rules. A layer overlays what it states: a stated
// severity replaces the earlier one, and a setting with no severity turns
// an off rule on at its default and keeps an enabled rule's severity.
// Every problem refuses the whole configuration.
func (r *Registry) Configure(doc Config, presets []string) (*Configuration, error) {
	if doc.Version != ConfigVersion {
		return nil, fmt.Errorf("lint configuration version %d is not supported; write \"version\": %d", doc.Version, ConfigVersion)
	}
	c := &Configuration{registry: r, severity: map[string]Severity{}}
	for _, rule := range r.rules {
		c.severity[rule.Name] = Off
	}
	apply := func(name string, s Setting) {
		switch {
		case s.HasSeverity:
			c.severity[name] = s.Severity
		case c.severity[name] == Off:
			c.severity[name] = r.byName[name].Default
		}
	}
	layers := append([]string{r.base}, doc.Extends...)
	layers = append(layers, presets...)
	seen := map[string]bool{}
	for _, name := range layers {
		p, ok := r.presets[name]
		if !ok {
			return nil, fmt.Errorf("unknown preset %s", name)
		}
		if seen[name] {
			return nil, fmt.Errorf("preset %s is applied twice", name)
		}
		seen[name] = true
		for _, rule := range r.rules {
			if s, ok := p.Rules[rule.Name]; ok {
				apply(rule.Name, s)
			}
		}
	}
	names := make([]string, 0, len(doc.Rules))
	for name := range doc.Rules {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if err := r.admit(name, doc.Rules[name]); err != nil {
			return nil, err
		}
		apply(name, doc.Rules[name])
	}
	return c, nil
}

// Severity is a rule's effective severity; Off for an unknown name.
func (c *Configuration) Severity(name string) Severity { return c.severity[name] }

// Statuses lists every rule with its effective severity, before any run.
func (c *Configuration) Statuses() []RuleStatus {
	var out []RuleStatus
	for _, rule := range c.registry.rules {
		out = append(out, RuleStatus{Rule: rule.Name, Code: rule.Code, Severity: c.severity[rule.Name].String()})
	}
	return out
}

// validName reports a kebab-case identifier: lower-case letters and digits
// in words joined by single hyphens, starting with a letter.
func validName(name string) bool {
	if len(name) == 0 || name[0] < 'a' || name[0] > 'z' || name[len(name)-1] == '-' {
		return false
	}
	for i := 0; i < len(name); i++ {
		b := name[i]
		switch {
		case b >= 'a' && b <= 'z', b >= '0' && b <= '9':
		case b == '-' && name[i-1] != '-':
		default:
			return false
		}
	}
	return true
}
