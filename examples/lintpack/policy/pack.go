// Package policy is an example Ergo rule pack written against the public
// lint SDK (docs/specs/custom-lint.md). Its two unrelated project policies
// read resolved facts only: the provider a with binds, a callable's checked
// failure row and the rows of its calls. Neither is a language
// restriction; the configuration names the boundaries.
package policy

import (
	"fmt"

	"ergo.local/prototype/lint"
)

// Pack is the example pack. Its manifest (policy.json) is Pack.Manifest of
// the built cmd/policy-lint.
var Pack = &lint.Pack{
	Namespace:    "policy",
	Version:      "0.2.0",
	Description:  "Example project policies over resolved Ergo facts.",
	FactVersions: []int{lint.FactSchemaVersion},
	Rules:        []*lint.Rule{ProviderBoundary, ForbiddenFailure},
	Presets: []lint.Preset{{
		Name:        "strict",
		Description: "Both policies as warnings.",
		Rules: map[string]lint.RuleConfig{
			"provider-boundary": {Severity: lint.SeverityWarning},
			"forbidden-failure": {Severity: lint.SeverityWarning},
		},
	}},
}

// functions are the identities of the functions the configuration names
// as "PKG.Name"; a name that declares no function is a configuration
// error, never an empty selection.
func functions(snapshot *lint.Snapshot, names []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, name := range names {
		found := snapshot.FunctionNamed(name)
		if len(found) == 0 {
			return nil, fmt.Errorf("configured function %q is not declared", name)
		}
		for _, f := range found {
			out[f.Identity] = true
		}
	}
	return out, nil
}

// nameOf is the declared name of a callable identity.
func nameOf(snapshot *lint.Snapshot, identity string) string {
	if c := snapshot.Callable(identity); c != nil {
		return c.Package + "." + c.Name
	}
	return identity
}
