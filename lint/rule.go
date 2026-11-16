package lint

import (
	"fmt"
	"regexp"
	"slices"
	"unicode/utf8"
)

// Severity is a public diagnostic severity. Off is valid only in
// configuration: it disables a rule rather than describing a finding.
type Severity string

const (
	SeverityOff         Severity = "off"
	SeverityError       Severity = "error"
	SeverityWarning     Severity = "warning"
	SeverityInformation Severity = "information"
	SeverityHint        Severity = "hint"
)

func (s Severity) valid() bool {
	return s == SeverityError || s == SeverityWarning || s == SeverityInformation || s == SeverityHint
}

// Limits on rule output. A rule exceeding them fails rather than having its
// findings silently truncated. MaxFindingsPerRule is enforced by the
// custom-rule runner, not by Apply.
const (
	MaxFindingsPerRule       = 1000
	MaxMessageBytes          = 4096
	MaxRelatedLocations      = 16
	MaxSuggestionsPerFinding = 8
	MaxEditsPerSuggestion    = 64
	MaxEditTextBytes         = 64 << 10
)

var identifierPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

const maxIdentifierBytes = 64

func validIdentifier(name string) bool {
	return len(name) <= maxIdentifierBytes && identifierPattern.MatchString(name)
}

// ReservedNamespaces cannot be claimed by a rule pack: built-in rules use
// unqualified names, and the compiler and runner own these prefixes.
var ReservedNamespaces = []string{"builtin", "compiler", "ergo", "lint", "runner"}

// Rule is one Go rule. Name is local to its pack; the public identity is
// namespace/name. Check reads pass.Snapshot and pass.Options and calls
// pass.Report; it must not retain the snapshot. A rule reads a private
// copy: what it writes to it no other rule sees.
type Rule struct {
	Name            string
	Version         string
	Description     string
	DefaultSeverity Severity
	Requires        []Family
	// Targets limits the rule to compilation targets; empty means all.
	Targets []string
	Options []OptionSpec
	Check   func(*Pass) error
}

// Pack is a namespaced, versioned set of rules distributed together.
type Pack struct {
	Namespace   string
	Version     string
	Description string
	// FactVersions lists the supported fact schema versions.
	FactVersions []int
	Rules        []*Rule
	// Presets are named rule settings a configuration can extend.
	Presets []Preset
}

// Rule returns the pack rule called name.
func (p *Pack) Rule(name string) *Rule {
	for _, rule := range p.Rules {
		if rule.Name == name {
			return rule
		}
	}
	return nil
}

// Related is a secondary location supporting a finding.
type Related struct {
	Message string `json:"message"`
	Span    Span   `json:"span"`
}

// Finding is one rule result. The runner assigns rule identity and
// severity; a rule cannot choose either.
type Finding struct {
	Message     string       `json:"message"`
	Span        Span         `json:"span"`
	Related     []Related    `json:"related,omitempty"`
	Suggestions []Suggestion `json:"suggestions,omitempty"`
}

// Suggestion proposes replacing source text. It is a preview bound to the
// snapshot revision the rule read: the runner validates it and never
// applies it, and it is no proof that the edit preserves meaning.
type Suggestion struct {
	Message string `json:"message"`
	Edits   []Edit `json:"edits"`
}

// Edit replaces the source text at Span with NewText; a zero-length span
// inserts. Edits of one suggestion must not overlap or share a start.
type Edit struct {
	Span    Span   `json:"span"`
	NewText string `json:"newText"`
}

// Pass is one rule execution over one snapshot.
type Pass struct {
	Snapshot *Snapshot
	Options  Options
	findings []Finding
}

// Report records a finding.
func (p *Pass) Report(finding Finding) { p.findings = append(p.findings, finding) }

// Reportf records a finding with a formatted message.
func (p *Pass) Reportf(span Span, format string, args ...any) {
	p.Report(Finding{Message: fmt.Sprintf(format, args...), Span: span})
}

// Apply runs rule over snapshot with already validated options. It is the
// single execution path for built-in and pack rules: it recovers a panic as
// a failure and validates every finding against the snapshot's source.
// Missing families or targets are the caller's admission decision, and so
// are output-count limits: a custom-rule runner bounds pack output, while
// built-in rules report every finding to suppression and lint policy.
func Apply(rule *Rule, snapshot *Snapshot, options Options) (findings []Finding, err error) {
	if rule == nil || rule.Check == nil {
		return nil, fmt.Errorf("rule has no check")
	}
	// The admitted source bounds output. It is captured before the rule
	// runs: the rule can write to its snapshot, never to this bound.
	source := newSourceBounds(snapshot.Sources)
	pass := &Pass{Snapshot: snapshot, Options: options}
	defer func() {
		if recovered := recover(); recovered != nil {
			findings, err = nil, fmt.Errorf("rule panicked: %v", recovered)
		}
	}()
	if err := rule.Check(pass); err != nil {
		return nil, err
	}
	for _, finding := range pass.findings {
		if err := validateFinding(finding, source); err != nil {
			return nil, err
		}
	}
	return slices.Clone(pass.findings), nil
}

func validateFinding(finding Finding, source *sourceBounds) error {
	if err := validateMessage(finding.Message); err != nil {
		return err
	}
	if err := source.check(finding.Span); err != nil {
		return fmt.Errorf("finding %q: %w", finding.Message, err)
	}
	if len(finding.Related) > MaxRelatedLocations {
		return fmt.Errorf("finding %q has %d related locations; the limit is %d", finding.Message, len(finding.Related), MaxRelatedLocations)
	}
	for _, related := range finding.Related {
		if err := validateMessage(related.Message); err != nil {
			return err
		}
		if err := source.check(related.Span); err != nil {
			return fmt.Errorf("related location %q: %w", related.Message, err)
		}
	}
	if len(finding.Suggestions) > MaxSuggestionsPerFinding {
		return fmt.Errorf("finding %q has %d suggestions; the limit is %d", finding.Message, len(finding.Suggestions), MaxSuggestionsPerFinding)
	}
	for _, suggestion := range finding.Suggestions {
		if err := validateSuggestion(suggestion, source); err != nil {
			return fmt.Errorf("finding %q: %w", finding.Message, err)
		}
	}
	return nil
}

// validateSuggestion checks that every edit lies inside the source and that
// no two edits overlap or start at the same offset, so applying them in
// any order yields the same text.
func validateSuggestion(suggestion Suggestion, source *sourceBounds) error {
	if err := validateMessage(suggestion.Message); err != nil {
		return fmt.Errorf("suggestion: %w", err)
	}
	if len(suggestion.Edits) == 0 || len(suggestion.Edits) > MaxEditsPerSuggestion {
		return fmt.Errorf("suggestion %q must have between 1 and %d edits", suggestion.Message, MaxEditsPerSuggestion)
	}
	edits := slices.Clone(suggestion.Edits)
	for _, edit := range edits {
		if err := source.check(edit.Span); err != nil {
			return fmt.Errorf("suggestion %q edit: %w", suggestion.Message, err)
		}
		if len(edit.NewText) > MaxEditTextBytes || !utf8.ValidString(edit.NewText) {
			return fmt.Errorf("suggestion %q edit text must be UTF-8 of at most %d bytes", suggestion.Message, MaxEditTextBytes)
		}
	}
	slices.SortFunc(edits, func(a, b Edit) int { return a.Span.Offset - b.Span.Offset })
	for i := 1; i < len(edits); i++ {
		previous, next := edits[i-1].Span, edits[i].Span
		if previous.Offset == next.Offset || previous.Offset+previous.Length > next.Offset {
			return fmt.Errorf("suggestion %q has overlapping edits at bytes %d and %d", suggestion.Message, previous.Offset, next.Offset)
		}
	}
	return nil
}

func validateMessage(message string) error {
	if message == "" || len(message) > MaxMessageBytes || !utf8.ValidString(message) {
		return fmt.Errorf("finding message must be non-empty UTF-8 of at most %d bytes", MaxMessageBytes)
	}
	return nil
}
