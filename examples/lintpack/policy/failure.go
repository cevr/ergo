package policy

import (
	"fmt"
	"slices"

	"ergo.local/prototype/lint"
)

// ForbiddenFailure forbids a configured failure from escaping the selected
// functions. It reads each function's checked failure row: a failure a
// helper forwards escapes, one the body recovers does not, and the rows of
// timeout and Go interop operators count as declared. Each call whose row
// carries the failure is a related place: where it enters the body. A
// local, a field, a string or a comment spelled like the failure is no
// failure of any row.
var ForbiddenFailure = &lint.Rule{
	Name:            "forbidden-failure",
	Version:         "2",
	Description:     "The configured failure must not escape the selected functions.",
	DefaultSeverity: lint.SeverityError,
	Requires:        []lint.Family{lint.FamilyCallables, lint.FamilyCalls},
	Options: []lint.OptionSpec{
		{Name: "failure", Type: lint.OptionString, Required: true, Description: "The failure type, as PKG.Name."},
		{Name: "functions", Type: lint.OptionStringList, Required: true, Description: "The functions, as PKG.Name, it must not escape."},
	},
	Check: func(pass *lint.Pass) error {
		snapshot := pass.Snapshot
		name := pass.Options.String("failure")
		failure := lint.TypeIdentity(name)
		selected, err := functions(snapshot, pass.Options.StringList("functions"))
		if err != nil {
			return err
		}
		for _, callable := range snapshot.Callables {
			if !selected[callable.Identity] || !slices.Contains(callable.Fails, failure) {
				continue
			}
			finding := lint.Finding{Message: fmt.Sprintf("failure %s can escape %s.%s", name, callable.Package, callable.Name), Span: callable.Span}
			for _, call := range snapshot.Calls {
				if call.Caller == callable.Identity && slices.Contains(call.Fails, failure) && len(finding.Related) < lint.MaxRelatedLocations {
					finding.Related = append(finding.Related, lint.Related{Message: name + " enters the body here", Span: call.Span})
				}
			}
			pass.Report(finding)
		}
		return nil
	},
}
