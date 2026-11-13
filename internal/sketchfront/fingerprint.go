package sketchfront

import (
	"fmt"
	"slices"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Fingerprints are the fingerprints of the program's packages, in import
// dependency order, the root package last (check.FingerprintOf; CS5a,
// docs/spec/build.md, Interface fingerprint). Each package's environment
// is the module, the platform, go.mod and go.sum, the module bindings the
// check read, and the assets the package embeds: an edit of any of them
// changes the package's source fingerprint. A program of one source text
// has none.
func (a Analysis) Fingerprints() []check.Fingerprint {
	p := a.program
	if p == nil || p.root == nil {
		return nil
	}
	var env strings.Builder
	fmt.Fprintf(&env, "module %q\nplatform %s/%s\n", p.module, p.platform.GOOS, p.platform.GOARCH)
	fmt.Fprintf(&env, "go.mod %q\ngo.sum %q\n", p.goModule.GoModFile(), p.goModule.Sum)
	bindings := make([]string, 0, len(p.bindings))
	for path := range p.bindings {
		bindings = append(bindings, path)
	}
	slices.Sort(bindings)
	for _, path := range bindings {
		fmt.Fprintf(&env, "binding %q %q\n", path, p.bindings[path])
	}
	byPath := map[string]check.Fingerprint{}
	var out []check.Fingerprint
	for _, q := range append(slices.Clone(p.imports), p.root) {
		files := make([]*syntax.File, len(q.files))
		for i, f := range q.files {
			files[i] = f.syntax
		}
		var imports []check.Fingerprint
		for _, imp := range q.imports {
			imports = append(imports, byPath[imp.path])
		}
		checkPath := q.path
		if q == p.root {
			checkPath = "main"
		}
		pkgEnv := env.String()
		assets := make([]string, 0, len(q.assets))
		for name := range q.assets {
			assets = append(assets, name)
		}
		slices.Sort(assets)
		for _, name := range assets {
			pkgEnv += fmt.Sprintf("asset %q %q\n", name, q.assets[name])
		}
		fp := check.FingerprintOf(q.path, checkPath, files, q.pkg, q.pkg != nil && !q.broken, pkgEnv, imports)
		byPath[q.path] = fp
		out = append(out, fp)
	}
	return out
}
