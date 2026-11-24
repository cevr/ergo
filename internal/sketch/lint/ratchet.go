package lint

import (
	"context"
	"encoding/json"
	"fmt"

	"ergo.local/prototype/internal/linked/graph"
	"ergo.local/prototype/internal/sketch/graphrows"
)

// LayoutSupportEntry designates a test-support package for layout comparison.
type LayoutSupportEntry struct {
	Package string `json:"package"`
	Seam    string `json:"seam"`
}

// LayoutSeamSpec declares a logical seam for layout comparison.
type LayoutSeamSpec struct {
	Root               string   `json:"root"`
	Packages           []string `json:"packages"`
	Entries            []string `json:"entries"`
	TestSupport        []string `json:"testSupport"`
	TestThroughEntries bool     `json:"testThroughEntries"`
}

// LayoutPolicy configures all eight organization layout rules for a typed
// base/head comparison (0 or empty disables numeric/configured policies).
type LayoutPolicy struct {
	MaxSurfaceList      int                  `json:"maxSurfaceList"`
	MaxFileLines        int                  `json:"maxFileLines"`
	MaxDirectoryMembers int                  `json:"maxDirectoryMembers"`
	MaxTangleMembers    int                  `json:"maxTangleMembers"`
	Support             []LayoutSupportEntry `json:"support"`
	Seams               []LayoutSeamSpec     `json:"seams"`
}

// LayoutEvidenceItem is one stable, monotone evidence row from a single
// checked graph snapshot (keyed by module-qualified handles and normalized
// paths, never dense node IDs or line numbers).
type LayoutEvidenceItem struct {
	Rule    string `json:"rule"`
	Subject string `json:"subject"`
	Key     string `json:"key"`
	Count   int    `json:"count"`
	Detail  string `json:"detail"`
}

// LayoutEvidenceDelta records one added or grown evidence item between base
// and head.
type LayoutEvidenceDelta struct {
	Rule      string `json:"rule"`
	Subject   string `json:"subject"`
	Key       string `json:"key"`
	BaseCount int    `json:"baseCount"`
	HeadCount int    `json:"headCount"`
	Detail    string `json:"detail"`
}

// LayoutComparison is the typed advisory result of comparing base and head
// checked graph rows under one LayoutPolicy.
type LayoutComparison struct {
	Advisory  bool                  `json:"advisory"`
	Regressed bool                  `json:"regressed"`
	Added     []LayoutEvidenceDelta `json:"added"`
	Grown     []LayoutEvidenceDelta `json:"grown"`
	Unchanged []LayoutEvidenceItem  `json:"unchanged"`
	Removed   []LayoutEvidenceItem  `json:"removed"`
}

// CompareLayout runs std codegraph's CompareLayout through the linked
// `ergo graph compare` query over base and head checked graph tables.
func CompareLayout(base, head *graphrows.Table, modules []string, policy LayoutPolicy) (LayoutComparison, error) {
	if base == nil || head == nil {
		return LayoutComparison{}, fmt.Errorf("compare layout: base and head tables are required")
	}
	norm := LayoutPolicy{
		MaxSurfaceList:      policy.MaxSurfaceList,
		MaxFileLines:        policy.MaxFileLines,
		MaxDirectoryMembers: policy.MaxDirectoryMembers,
		MaxTangleMembers:    policy.MaxTangleMembers,
		Support:             policy.Support,
		Seams:               make([]LayoutSeamSpec, len(policy.Seams)),
	}
	if norm.Support == nil {
		norm.Support = []LayoutSupportEntry{}
	}
	for i, s := range policy.Seams {
		norm.Seams[i] = LayoutSeamSpec{
			Root:               s.Root,
			Packages:           nonNilStrings(s.Packages),
			Entries:            nonNilStrings(s.Entries),
			TestSupport:        nonNilStrings(s.TestSupport),
			TestThroughEntries: s.TestThroughEntries,
		}
	}
	polJSON, err := json.Marshal(norm)
	if err != nil {
		return LayoutComparison{}, err
	}
	args := []string{"compare", "base", "--policy", string(polJSON), "--json"}
	for _, m := range modules {
		args = append(args, "--module", m)
	}
	strs, ints := head.AppendColumns(nil, nil)
	strs, ints = base.AppendColumns(strs, ints)
	out, err := graph.RunColumns(context.Background(), args, strs, ints)
	if err != nil {
		return LayoutComparison{}, err
	}
	if len(out) != 3 || out[0] != "0" {
		return LayoutComparison{}, fmt.Errorf("ergo graph compare: %v", out)
	}
	var cmp LayoutComparison
	if err := json.Unmarshal([]byte(out[1]), &cmp); err != nil {
		return LayoutComparison{}, err
	}
	return cmp, nil
}
