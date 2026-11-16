package lint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"
)

// ManifestVersion is the static rule-pack manifest format version.
const ManifestVersion = 1

const (
	MaxManifestBytes = 1 << 20
	maxManifestRules = 256
	maxExecutableArg = 64
	maxShortText     = 256
)

// Manifest is the static description of a rule pack. Reading, validating
// and inspecting it never executes pack code. Executable is the explicit
// program and argument array a later runner may start; it is never
// interpreted by a shell.
type Manifest struct {
	ManifestVersion int            `json:"manifestVersion"`
	Namespace       string         `json:"namespace"`
	Version         string         `json:"version"`
	Description     string         `json:"description,omitempty"`
	FactSchema      ManifestSchema `json:"factSchema"`
	Executable      Executable     `json:"executable"`
	Rules           []ManifestRule `json:"rules"`
	Presets         []Preset       `json:"presets,omitempty"`
}

// Preset is a named set of settings for rules of its own pack, keyed by
// local rule name. A configuration applies it with extends; it cannot
// configure built-in rules or another pack.
type Preset struct {
	Name        string                `json:"name"`
	Description string                `json:"description,omitempty"`
	Rules       map[string]RuleConfig `json:"rules,omitempty"`
	// Env states the host variables the pack needs, such as PATH for an
	// interpreter that looks up helpers; a later layer can replace it.
	Env EnvSelection `json:"env,omitzero"`
}

const maxPresets = 64

// ManifestSchema names the fact schema versions a pack accepts.
type ManifestSchema struct {
	Name     string `json:"name"`
	Versions []int  `json:"versions"`
}

// Executable is a program path and literal argument array.
type Executable struct {
	Path string   `json:"path"`
	Args []string `json:"args,omitempty"`
}

// ManifestRule is the static metadata of one rule.
type ManifestRule struct {
	Name            string       `json:"name"`
	Version         string       `json:"version"`
	Description     string       `json:"description"`
	DefaultSeverity Severity     `json:"defaultSeverity"`
	Requires        []Family     `json:"requires"`
	Targets         []string     `json:"targets,omitempty"`
	Options         []OptionSpec `json:"options,omitempty"`
}

// ParseManifest strictly decodes and validates a manifest: unknown fields,
// duplicate keys, trailing data and oversized input are refused.
func ParseManifest(data []byte) (Manifest, error) {
	if len(data) > MaxManifestBytes {
		return Manifest{}, fmt.Errorf("manifest exceeds %d bytes", MaxManifestBytes)
	}
	var manifest Manifest
	if err := decodeStrict(data, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("invalid manifest: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// Validate checks the manifest's identity, schema and rule metadata.
func (m Manifest) Validate() error {
	fail := func(format string, args ...any) error {
		return fmt.Errorf("invalid manifest %s: %s", m.Namespace, fmt.Sprintf(format, args...))
	}
	if m.ManifestVersion != ManifestVersion {
		return fail("unsupported manifestVersion %d", m.ManifestVersion)
	}
	if !validIdentifier(m.Namespace) {
		return fail("namespace must be a lowercase kebab-case identifier")
	}
	if slices.Contains(ReservedNamespaces, m.Namespace) {
		return fail("namespace %q is reserved", m.Namespace)
	}
	if !validText(m.Version, maxShortText) || m.Version == "" {
		return fail("version must be non-empty text of at most %d bytes", maxShortText)
	}
	if !validText(m.Description, maxDescriptionLen) {
		return fail("description exceeds %d bytes", maxDescriptionLen)
	}
	if m.FactSchema.Name != FactSchemaName || len(m.FactSchema.Versions) == 0 {
		return fail("factSchema must name %s with at least one version", FactSchemaName)
	}
	for _, version := range m.FactSchema.Versions {
		if version < 1 {
			return fail("fact schema version %d is invalid", version)
		}
	}
	if err := m.Executable.validate(); err != nil {
		return fail("%v", err)
	}
	if len(m.Rules) == 0 || len(m.Rules) > maxManifestRules {
		return fail("must declare between 1 and %d rules", maxManifestRules)
	}
	seen := map[string]bool{}
	for _, rule := range m.Rules {
		if !validIdentifier(rule.Name) {
			return fail("rule name %q must be a lowercase kebab-case identifier", rule.Name)
		}
		if seen[rule.Name] {
			return fail("duplicate rule %s/%s", m.Namespace, rule.Name)
		}
		seen[rule.Name] = true
		if err := rule.validate(); err != nil {
			return fail("rule %s: %v", rule.Name, err)
		}
	}
	if len(m.Presets) > maxPresets {
		return fail("declares %d presets; the limit is %d", len(m.Presets), maxPresets)
	}
	for i, preset := range m.Presets {
		if !validIdentifier(preset.Name) {
			return fail("preset name %q must be a lowercase kebab-case identifier", preset.Name)
		}
		if slices.ContainsFunc(m.Presets[:i], func(other Preset) bool { return other.Name == preset.Name }) {
			return fail("duplicate preset %s/%s", m.Namespace, preset.Name)
		}
		if err := m.validatePreset(preset); err != nil {
			return fail("preset %s: %v", preset.Name, err)
		}
	}
	return nil
}

// validatePreset checks a preset as configuration of its own pack: every
// key names a pack rule, severities are valid, and options satisfy the
// rule's schema.
func (m Manifest) validatePreset(preset Preset) error {
	if !validText(preset.Description, maxDescriptionLen) {
		return fmt.Errorf("description exceeds %d bytes", maxDescriptionLen)
	}
	if len(preset.Rules) == 0 && !preset.Env.Set {
		return fmt.Errorf("must configure at least one rule or an environment")
	}
	if err := preset.Env.validate(); err != nil {
		return err
	}
	names := make([]string, 0, len(preset.Rules))
	for name := range preset.Rules {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		rule, ok := m.Rule(name)
		if !ok {
			return fmt.Errorf("pack has no rule %q", name)
		}
		setting := preset.Rules[name]
		if setting.Severity != "" && setting.Severity != SeverityOff && !setting.Severity.valid() {
			return fmt.Errorf("invalid severity %q for %s", setting.Severity, name)
		}
		if setting.Options != nil {
			if _, err := ValidateOptions(rule.Options, setting.Options); err != nil {
				return fmt.Errorf("%s: %v", name, err)
			}
		}
	}
	return nil
}

// Preset returns the manifest preset called name.
func (m Manifest) Preset(name string) (Preset, bool) {
	for _, preset := range m.Presets {
		if preset.Name == name {
			return preset, true
		}
	}
	return Preset{}, false
}

func (r ManifestRule) validate() error {
	if r.Version == "" || !validText(r.Version, maxShortText) {
		return fmt.Errorf("version must be non-empty text of at most %d bytes", maxShortText)
	}
	if r.Description == "" || !validText(r.Description, maxDescriptionLen) {
		return fmt.Errorf("description must be non-empty text of at most %d bytes", maxDescriptionLen)
	}
	if !r.DefaultSeverity.valid() {
		return fmt.Errorf("invalid default severity %q", r.DefaultSeverity)
	}
	if len(r.Requires) == 0 {
		return fmt.Errorf("must require at least one fact family")
	}
	for i, family := range r.Requires {
		if !knownFamily(family) {
			return fmt.Errorf("unknown fact family %q", family)
		}
		if slices.Contains(r.Requires[:i], family) {
			return fmt.Errorf("duplicate fact family %q", family)
		}
	}
	// Go is the only compilation target.
	for _, target := range r.Targets {
		if target != "go" {
			return fmt.Errorf("unsupported target %q", target)
		}
	}
	return validateOptionSpecs(r.Options)
}

func (e Executable) validate() error {
	if e.Path == "" || !validText(e.Path, 4096) || strings.ContainsRune(e.Path, 0) {
		return fmt.Errorf("executable path must be non-empty text without NUL")
	}
	if len(e.Args) > maxExecutableArg {
		return fmt.Errorf("executable declares %d arguments; the limit is %d", len(e.Args), maxExecutableArg)
	}
	for _, arg := range e.Args {
		if !validText(arg, 4096) || strings.ContainsRune(arg, 0) {
			return fmt.Errorf("executable arguments must be text without NUL")
		}
	}
	return nil
}

func validText(text string, limit int) bool {
	return len(text) <= limit && utf8.ValidString(text)
}

// Identity is the content identity of the validated manifest. It changes
// with any metadata, option schema or executable declaration change.
func (m Manifest) Identity() string {
	data, _ := json.Marshal(m)
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Rule returns the manifest rule called name.
func (m Manifest) Rule(name string) (ManifestRule, bool) {
	for _, rule := range m.Rules {
		if rule.Name == name {
			return rule, true
		}
	}
	return ManifestRule{}, false
}

// Manifest derives the static manifest of a Go pack, so a pack author has
// one source of metadata. The result is validated.
func (p *Pack) Manifest(executable Executable) (Manifest, error) {
	manifest := Manifest{
		ManifestVersion: ManifestVersion,
		Namespace:       p.Namespace,
		Version:         p.Version,
		Description:     p.Description,
		FactSchema:      ManifestSchema{Name: FactSchemaName, Versions: slices.Clone(p.FactVersions)},
		Executable:      Executable{Path: executable.Path, Args: slices.Clone(executable.Args)},
	}
	for _, preset := range p.Presets {
		preset.Rules = maps.Clone(preset.Rules)
		preset.Env.Names = slices.Clone(preset.Env.Names)
		manifest.Presets = append(manifest.Presets, preset)
	}
	for _, rule := range p.Rules {
		if rule.Check == nil {
			return Manifest{}, fmt.Errorf("rule %s/%s has no check", p.Namespace, rule.Name)
		}
		manifest.Rules = append(manifest.Rules, ManifestRule{
			Name: rule.Name, Version: rule.Version, Description: rule.Description,
			DefaultSeverity: rule.DefaultSeverity, Requires: slices.Clone(rule.Requires),
			Targets: slices.Clone(rule.Targets), Options: slices.Clone(rule.Options),
		})
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}
