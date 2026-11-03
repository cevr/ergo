package provenance

import (
	"fmt"
	"os"
	"slices"
	"strings"
)

// Manifest is one package's provenance contract. It lives in one plain text
// file beside the package (internal/sketch/check/testdata/provenance-manifest.txt
// for the checker), read by this analyzer through its -manifest flag and by
// the package's own tests, so it has exactly one copy.
//
// One directive per line, fields separated by spaces; a line that starts
// with # is a comment:
//
//	package <path>                     the package the contract binds
//	syntax <path>                      the package that declares syntax.Node
//	made <name>                        the helper that registers a fresh node
//	clone <name>                       the helper that copies a node through made
//	import <path>...                   the reviewed import allow-list
//	callee <syntax name> <site>...     a node-yielding syntax callee and its sites
//	entry <name> <site>...             a caller-text entry and the sites that may reference it
//	site <site> <kind> <fixture>       a construction site, its node kind and the
//	                                   name of a clean program in the package's
//	                                   tests that reaches it
//
// Sites are SSA functions named relative to the package, closures as Go's
// SSA names them: CheckSource$1, prelude$1, (*checker).f$1$2.
type Manifest struct {
	Package, Syntax string
	// Made registers a fresh node, its second argument, with its origin;
	// Clone copies a node through Made. Both are trusted: their bodies make
	// and copy nodes.
	Made, Clone string
	// Imports is the reviewed import allow-list.
	Imports []string
	// Callees lists the syntax functions, methods and function-valued
	// variables that yield a node (by name relative to the syntax package,
	// "Parse", "(*Ident).Copy"), each with the sites that may call it.
	Callees map[string][]string
	// Entries lists the package's own functions that parse caller text, each
	// with the sites that may reference it.
	Entries map[string][]string
	// Sites lists every construction site, each a made call on a fresh node
	// or a clone call, in file order.
	Sites []Site
}

// Site is one construction site: an SSA function, the node kind it makes,
// and the name of a clean program in the package's tests that reaches it
// (for the package's own sweep; the analyzer does not read it).
type Site struct{ Fn, Kind, Fixture string }

// key is a site without its fixture.
func (s Site) key() Site { return Site{Fn: s.Fn, Kind: s.Kind} }

// ReadManifest reads and parses a manifest file.
func ReadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	m, err := ParseManifest(string(data))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return m, nil
}

// ParseManifest parses a manifest's text.
func ParseManifest(text string) (*Manifest, error) {
	m := &Manifest{Callees: map[string][]string{}, Entries: map[string][]string{}}
	for i, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		fail := func(format string, args ...any) error {
			return fmt.Errorf("line %d: %s", i+1, fmt.Sprintf(format, args...))
		}
		one := func(dst *string) error {
			if len(fields) != 2 {
				return fail("%s takes one field", fields[0])
			}
			if *dst != "" {
				return fail("%s given twice", fields[0])
			}
			*dst = fields[1]
			return nil
		}
		var err error
		switch fields[0] {
		case "package":
			err = one(&m.Package)
		case "syntax":
			err = one(&m.Syntax)
		case "made":
			err = one(&m.Made)
		case "clone":
			err = one(&m.Clone)
		case "import":
			m.Imports = append(m.Imports, fields[1:]...)
		case "callee", "entry":
			dst := m.Callees
			if fields[0] == "entry" {
				dst = m.Entries
			}
			if len(fields) < 2 {
				return nil, fail("%s needs a name", fields[0])
			}
			if _, dup := dst[fields[1]]; dup {
				return nil, fail("%s %s given twice", fields[0], fields[1])
			}
			dst[fields[1]] = append([]string(nil), fields[2:]...)
		case "site":
			if len(fields) != 4 {
				return nil, fail("site takes a function, a kind and a fixture")
			}
			site := Site{Fn: fields[1], Kind: fields[2], Fixture: fields[3]}
			if slices.ContainsFunc(m.Sites, func(s Site) bool { return s.key() == site.key() }) {
				return nil, fail("site %s %s given twice", site.Fn, site.Kind)
			}
			m.Sites = append(m.Sites, site)
		default:
			return nil, fail("unknown directive %q", fields[0])
		}
		if err != nil {
			return nil, err
		}
	}
	for name, v := range map[string]string{"package": m.Package, "syntax": m.Syntax, "made": m.Made, "clone": m.Clone} {
		if v == "" {
			return nil, fmt.Errorf("the manifest names no %s", name)
		}
	}
	return m, nil
}
