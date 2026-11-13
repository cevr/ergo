// Package fingerprint holds a package's fingerprints and the reuse
// decision of the incremental check (compile-speed plan §5.3, CS5a;
// docs/spec/build.md, Interface fingerprint). The checker computes them
// (check.FingerprintOf); this package only holds and compares them, so the
// checker's export codec, which covers every type the checker declares,
// does not carry them.
package fingerprint

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// Package is one package's fingerprints. Source is "" when a file does not
// parse, Interface "" when the package was not checked or has diagnostics,
// and Implementation "" when Source is.
type Package struct {
	Path           string
	Imports        []string // the local import paths, sorted
	Source         string
	Interface      string
	Implementation string
}

// Clean reports a package that was checked with no diagnostic.
func (p Package) Clean() bool { return p.Interface != "" }

// Of is the hash of parts, each length-prefixed.
func Of(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		fmt.Fprintf(h, "%d:%s\x00", len(p), p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Reusable is the paths of cur, in its order, whose check is the check of
// prev: the package was clean in prev, its source is the same, and each
// of its imports has a clean, unchanged interface. Each such package's
// own interface is then unchanged too, so the decision runs bottom up.
func Reusable(prev, cur []Package) []string {
	before := map[string]Package{}
	for _, p := range prev {
		before[p.Path] = p
	}
	now := map[string]Package{}
	for _, c := range cur {
		now[c.Path] = c
	}
	var out []string
next:
	for _, c := range cur {
		p, ok := before[c.Path]
		if !ok || !p.Clean() || c.Source == "" || c.Source != p.Source {
			continue
		}
		for _, imp := range c.Imports {
			q, pq := now[imp], before[imp]
			if !q.Clean() || q.Interface != pq.Interface {
				continue next
			}
		}
		out = append(out, c.Path)
	}
	return out
}
