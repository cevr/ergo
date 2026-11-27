package sketchfront

import (
	"ergo.local/prototype/internal/sketch/check"
)

// InspectMappersResult reports all check-time content mapper invocations
// across a checked package and its local/asset dependencies, including
// synthesized Ergo code, source span mappings, cache state, and diagnostics.
type InspectMappersResult struct {
	Package string               `json:"package"`
	Mappers []check.MapperRecord `json:"mappers"`
}

// InspectMappers returns all recorded check-time content mapper executions
// for a checked single-file or directory Analysis.
func (a Analysis) InspectMappers() InspectMappersResult {
	out := InspectMappersResult{Mappers: []check.MapperRecord{}}
	if a.pkg != nil {
		out.Package = a.pkg.Path
	}
	seen := map[string]bool{}
	addPkg := func(p *check.Package) {
		if p == nil || p.Std() || p.Host() || p.Derive == nil {
			return
		}
		for _, r := range p.Derive.Records {
			key := p.Path + "\x00" + r.Kind + "\x00" + r.Target + "\x00" + r.AssetPath
			if seen[key] {
				continue
			}
			seen[key] = true
			out.Mappers = append(out.Mappers, r)
		}
	}

	if a.program != nil {
		for _, lp := range a.program.imports {
			if lp == nil {
				continue
			}
			if lp.pkg != nil {
				addPkg(lp.pkg)
			} else if lp.assetRecord != nil {
				key := lp.path + "\x00" + lp.assetRecord.Kind + "\x00" + lp.assetRecord.Target + "\x00" + lp.assetRecord.AssetPath
				if !seen[key] {
					seen[key] = true
					out.Mappers = append(out.Mappers, *lp.assetRecord)
				}
			}
		}
		if a.program.root != nil {
			if a.program.root.pkg != nil {
				out.Package = a.program.root.pkg.Path
				addPkg(a.program.root.pkg)
			} else if a.program.root.assetRecord != nil {
				r := *a.program.root.assetRecord
				out.Mappers = append(out.Mappers, r)
			}
		}
		return out
	}

	addPkg(a.pkg)
	return out
}
