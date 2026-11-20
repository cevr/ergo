package snapshot

import (
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/fingerprint"
	"ergo.local/prototype/internal/sketch/syntax"
)

// A node path names a syntax node of a user package from another user
// package's export segment (compile-speed/cs5b.md §3, Node; CS5b-4), as an
// object path names an object: by the node's file, its top-level
// declaration and its index in that declaration's canonical walk
// (syntax.Objects). The declaration is named by its key, the digest of its
// token text and its walk's shape, and its ordinal among the declarations
// of the file with that key. So a node path resolves in a fresh check of
// the package to the node at the same place of the same declaration, after
// any edit outside that declaration (a comment, another declaration, a new
// file), and to nothing after an edit of the declaration itself: the
// importer is then not taken from the cache, never bound to a wrong node.
//
// Decided: the key holds the declaration's token text, not only its shape,
// by correctness: two declarations of one shape (two functions `n * 2` and
// `n * 3`) must not trade places when one is inserted before the other.
// Reverse to a name-based key if a body edit of a declaration an importer
// names costs too many misses.

// nodePath is a node path's encoding: file, key, ordinal and index,
// separated by NUL.
type nodePath string

// declWalk is one top-level declaration's key and walk.
type declWalk struct {
	key  string
	objs []any
}

// declWalks are the keys and walks of f's declarations; a declaration with
// no source span has no key and is never named.
func declWalks(f *syntax.File) []declWalk {
	out := make([]declWalk, len(f.Decls))
	for i, d := range f.Decls {
		objs, head := syntax.Objects(d)
		out[i].objs = objs
		pos, end := d.Pos(), d.End()
		if pos.IsValid() && end >= pos && int(end) <= len(f.Src) {
			out[i].key = fingerprint.Of(syntax.TokenText(f.Src[pos:end]), string(head))
		}
	}
	return out
}

// nodePaths are the node paths of pkg's syntax nodes that a top-level
// declaration with a key reaches. A node two declarations reach belongs to
// the first, in file and declaration order.
func nodePaths(pkg *check.Package) map[any]nodePath {
	out := map[any]nodePath{}
	for _, f := range pkg.Files() {
		seen := map[string]int{}
		for _, w := range declWalks(f) {
			if w.key == "" {
				continue
			}
			nth := seen[w.key]
			seen[w.key]++
			prefix := f.Name + "\x00" + w.key + "\x00" + strconv.Itoa(nth) + "\x00"
			for i, x := range w.objs {
				if _, ok := out[x]; !ok {
					out[x] = nodePath(prefix + strconv.Itoa(i))
				}
			}
		}
	}
	return out
}

// ResolveNodePath returns the syntax node the node path p (a CrossRef's
// Path) names in pkg.
func ResolveNodePath(pkg *check.Package, p string) (any, bool) {
	var r nodeResolver
	return r.resolve(pkg, nodePath(p))
}

// nodeResolver resolves node paths in packages, walking each file once.
type nodeResolver struct {
	files map[*syntax.File][]declWalk
}

// resolve returns the node p names in pkg.
func (r *nodeResolver) resolve(pkg *check.Package, p nodePath) (any, bool) {
	parts := strings.Split(string(p), "\x00")
	if len(parts) != 4 {
		return nil, false
	}
	nth, err1 := strconv.Atoi(parts[2])
	index, err2 := strconv.Atoi(parts[3])
	if err1 != nil || err2 != nil {
		return nil, false
	}
	for _, f := range pkg.Files() {
		if f.Name != parts[0] {
			continue
		}
		if r.files == nil {
			r.files = map[*syntax.File][]declWalk{}
		}
		walks, ok := r.files[f]
		if !ok {
			walks = declWalks(f)
			r.files[f] = walks
		}
		for _, w := range walks {
			if w.key != parts[1] {
				continue
			}
			if nth > 0 {
				nth--
				continue
			}
			if index < 0 || index >= len(w.objs) {
				return nil, false
			}
			return w.objs[index], true
		}
	}
	return nil, false
}
