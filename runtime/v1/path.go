package v1

import "path/filepath"

// The bodies of std filepath (docs/spec/stdlib.md, filepath). Each is a
// top-level function of basic and slice types with no hidden argument, so
// BodyOf binds it as written, and each calls the function of the same name
// in Go's path/filepath. Go's targets for Ergo are Linux and darwin, where
// the separator is '/' and no path has a volume name.
//
// Each body is marked //ergo:pure (docs/spec/stdlib.md, Pure bodies): it
// computes on its arguments only. path/filepath is in PureImports, but its
// members that read the host (Abs reads the working directory; EvalSymlinks,
// Glob, Walk and WalkDir read the file system) are in PureImportsExcept, so
// BodyOf refuses a marked body that names one of them.

// FilepathJoin is filepath.Join of parts: the non-empty parts with a
// separator between each pair, cleaned; "" when every part is empty.
//
//ergo:pure
//ergo:wait none
func FilepathJoin(parts []string) string { return filepath.Join(parts...) }

// FilepathExt is filepath.Ext.
//
//ergo:pure
//ergo:wait none
func FilepathExt(path string) string { return filepath.Ext(path) }

// NotRelative is filepath.NotRelative, the typed failure of Rel: target
// cannot be made relative to base by lexical processing alone. It is a
// record of a v1 package, so the lowering binds filepath.NotRelative to
// this Go type (docs/spec/stdlib.md, Types 4).
type NotRelative struct{ Base, Target string }

// Error is the failure's text: its Ergo name and fields, as the lowering
// writes a record's.
func (e NotRelative) Error() string {
	return "filepath.NotRelative{Base:" + e.Base + " Target:" + e.Target + "}"
}

// FilepathRel is filepath.Rel; any error is NotRelative{base, target}.
//
//ergo:pure
//ergo:wait none
func FilepathRel(base, target string) (string, error) {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return "", NotRelative{Base: base, Target: target}
	}
	return rel, nil
}

// BadPattern is filepath.BadPattern: a pattern Match cannot read.
type BadPattern struct{ Pattern string }

// Error is the failure's text: its Ergo name and fields.
func (e BadPattern) Error() string { return "filepath.BadPattern{Pattern:" + e.Pattern + "}" }

// FilepathMatch is the body of filepath.Match: Go's filepath.Match.
//
//ergo:pure
//ergo:wait none
func FilepathMatch(pattern, name string) (bool, error) {
	ok, err := filepath.Match(pattern, name)
	if err != nil {
		return false, BadPattern{Pattern: pattern}
	}
	return ok, nil
}
