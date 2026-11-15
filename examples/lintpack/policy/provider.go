package policy

import (
	"fmt"

	"ergo.local/prototype/lint"
)

// ProviderBoundary restricts a configured provider to the configured
// composition functions. It reads the provider each with binds, resolved by
// the checker: a local of another name holding the provider, or a named
// context that binds it, matches; a local spelled like the provider that
// holds another value, or a capability value passed on, does not.
var ProviderBoundary = &lint.Rule{
	Name:            "provider-boundary",
	Version:         "2",
	Description:     "Only the configured composition functions may bind the configured provider.",
	DefaultSeverity: lint.SeverityError,
	Requires:        []lint.Family{lint.FamilyDeclarations, lint.FamilyCallables, lint.FamilyWiths},
	Options: []lint.OptionSpec{
		{Name: "provider", Type: lint.OptionString, Required: true, Description: "The provider type, as PKG.Name."},
		{Name: "allow", Type: lint.OptionStringList, Required: true, Description: "The functions, as PKG.Name, that may bind it."},
	},
	Check: func(pass *lint.Pass) error {
		snapshot := pass.Snapshot
		name := pass.Options.String("provider")
		provider := snapshot.TypeNamed(name)
		if provider == nil {
			return fmt.Errorf("configured provider %q is not declared", name)
		}
		allowed, err := functions(snapshot, pass.Options.StringList("allow"))
		if err != nil {
			return err
		}
		for _, with := range snapshot.Withs {
			if allowed[with.Callable] {
				continue
			}
			for _, entry := range with.Entries {
				if entry.Provider != provider.Identity {
					continue
				}
				finding := lint.Finding{
					Message: fmt.Sprintf("%s binds %s; only %v may bind it", nameOf(snapshot, with.Callable), name, pass.Options.StringList("allow")),
					Span:    with.Span,
				}
				if entry.Span != nil && !inside(*entry.Span, with.Span) {
					finding.Related = append(finding.Related, lint.Related{Message: "the context entry that binds it", Span: *entry.Span})
				}
				pass.Report(finding)
				break
			}
		}
		return nil
	},
}

// inside reports whether span a lies within span b of the same file.
func inside(a, b lint.Span) bool {
	return a.File == b.File && a.Offset >= b.Offset && a.Offset+a.Length <= b.Offset+b.Length
}
